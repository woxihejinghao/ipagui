// Package services 是 Wails 前端与 internal/ipa、internal/store 之间的绑定层。
//
// 边界（见 docs/architecture.md §2）：
//   - 只有本包、main.go、app.go 允许 import wails；
//   - 本包只做「编排 + 事件发送 + 类型转换」，业务逻辑一律留在 internal/ipa；
//   - 前端永远不碰文件系统，所有落盘动作都从这里发起。
package services

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/majd/ipatool/v2/pkg/appstore"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/milan/ipagui/internal/ipa"
	"github.com/milan/ipagui/internal/store"
)

// DatabaseFileName 是账本文件名（放在应用配置目录下，与 ipatool 的状态目录分开）。
const DatabaseFileName = "library.db"

// Runtime 持有整个应用的进程级状态，并把上面那些服务串起来。
//
// 注意构造函数**不返回 error**：初始化失败（例如 Windows 上文件后端缺口令）
// 时必须仍然能启动界面，让用户看到「环境不可用」页 —— 启动即崩在 GUI 上
// 等于用户既看不到原因、也没法自救。
type Runtime struct {
	version string

	deps *ipa.Deps
	svc  *ipa.Services
	lib  *store.Store

	// initErr 记录初始化失败的原因；为 nil 时上面的字段可用。
	initErr error

	ctxMu sync.RWMutex
	ctx   context.Context

	settingsMu sync.RWMutex
	settings   ipa.Settings
}

// NewRuntime 组装运行期依赖，并打开本地账本。
func NewRuntime(version string) *Runtime {
	instance := &Runtime{version: version, settings: ipa.DefaultSettings()}

	settings, err := ipa.LoadSettings()
	if err == nil {
		instance.settings = settings
	}
	// 设置读不出来不算致命：用默认值起步，用户在设置页里改一次即可。

	deps, err := ipa.NewDeps(ipa.Options{Passphrase: ipa.PassphraseChain()})
	if err != nil {
		instance.initErr = err

		return instance
	}

	instance.deps = deps
	instance.svc = ipa.NewServices(deps)

	// 凭据过期时 ipa 层会自己退掉登录态；这里负责把「为什么变成未登录」
	// 广播给界面（ipa 层不能 import wails，所以事件只能在上层发）。
	//
	// auth:state 让顶部栏/账号卡片立刻变成未登录，purchases:invalidated
	// 让前端的已购缓存跟着丢掉（后端缓存 ipa 层已自己作废）。
	instance.svc.SetSessionExpiredHandler(func() {
		instance.Emit("auth:state", map[string]any{
			"loggedIn": false,
			"phase":    "expired",
			"expired":  true,
			"message":  "登录凭据已过期，请重新登录",
		})
		instance.Emit("purchases:invalidated", map[string]any{"reason": "expired"})
	})

	// 启动时按钥匙串里的当前账号剪一次 cookie：升级前换号留在 jar 里的
	// 旧账号 mz_at* 会被带进每个商店请求，表现为「明明登录了新账号，下载还是
	// 走旧账号」。失败不影响启动（最坏退回旧行为）。
	instance.svc.PruneStaleAccountCookies()

	configDir, err := ipa.ConfigDir()
	if err != nil {
		instance.initErr = err

		return instance
	}

	lib, err := store.Open(filepath.Join(configDir, DatabaseFileName))
	if err != nil {
		instance.initErr = err

		return instance
	}

	instance.lib = lib

	// 上次退出时还在下载的任务不可能还在跑，标成可重试的失败态。
	if _, err := lib.RecoverInterruptedTasks(context.Background()); err != nil {
		instance.initErr = fmt.Errorf("恢复中断任务失败: %w", err)
	}

	return instance
}

// Startup 由 Wails 在窗口就绪时调用。
func (r *Runtime) Startup(ctx context.Context) {
	r.ctxMu.Lock()
	r.ctx = ctx
	r.ctxMu.Unlock()
}

// Shutdown 由 Wails 在退出前调用。
func (r *Runtime) Shutdown(_ context.Context) {
	r.ctxMu.Lock()
	r.ctx = nil
	r.ctxMu.Unlock()

	if r.lib != nil {
		_ = r.lib.Close()
	}
}

// Context 返回 Wails 的启动上下文（可能为 nil，调用方要判空）。
func (r *Runtime) Context() context.Context {
	r.ctxMu.RLock()
	defer r.ctxMu.RUnlock()

	return r.ctx
}

// CallContext 给绑定方法一个稳定的 ctx。
//
// 为什么不用 Wails 的启动上下文：它可能为 nil（窗口还没就绪），直接传给上游会
// panic。这里做一次收口，所有服务方法共用。
func (r *Runtime) CallContext() context.Context {
	if ctx := r.Context(); ctx != nil {
		return ctx
	}

	return context.Background()
}

// Emit 向所有窗口广播一个事件。没有上下文时静默跳过（启动早期/退出之后）。
func (r *Runtime) Emit(name string, payload any) {
	ctx := r.Context()
	if ctx == nil {
		return
	}

	wailsruntime.EventsEmit(ctx, name, payload)
}

// Version 返回应用版本。
func (r *Runtime) Version() string { return r.version }

// InitError 返回初始化失败原因（nil 表示一切正常）。
func (r *Runtime) InitError() error { return r.initErr }

// Dependencies 返回 ipa 层依赖；初始化失败时为 nil。
func (r *Runtime) Dependencies() *ipa.Deps { return r.deps }

// Catalog 返回 ipa 层的目录/会话服务；初始化失败时为 nil。
func (r *Runtime) Catalog() *ipa.Services { return r.svc }

// Library 返回账本；初始化失败时为 nil。
func (r *Runtime) Library() *store.Store { return r.lib }

// AppStore 返回上游客户端；初始化失败时为 nil。
func (r *Runtime) AppStore() appstore.AppStore {
	if r.deps == nil {
		return nil
	}

	return r.deps.Store
}

// Settings 返回当前设置副本。
func (r *Runtime) Settings() ipa.Settings {
	r.settingsMu.RLock()
	defer r.settingsMu.RUnlock()

	return r.settings
}

// SaveSettings 校验并持久化设置，成功后更新内存副本。
func (r *Runtime) SaveSettings(settings ipa.Settings) error {
	if err := ipa.SaveSettings(settings); err != nil {
		return err
	}

	r.settingsMu.Lock()
	r.settings = settings
	r.settingsMu.Unlock()

	return nil
}

// EnsureReady 在初始化失败时返回统一的错误，避免每个方法重复判空。
func (r *Runtime) EnsureReady() error {
	if r.initErr != nil {
		return fmt.Errorf("运行环境不可用: %w", r.initErr)
	}

	if r.deps == nil || r.svc == nil || r.lib == nil {
		return errors.New("运行环境不可用：依赖未初始化")
	}

	return nil
}

// revealInFileManager 在系统文件管理器里高亮一个文件。
//
// 为什么不用 Wails 自带的 OpenInBrowserURL：那会在浏览器里打开，而这里的意图是
// 「帮我在访达/资源管理器里定位到这个文件」，两者体验完全不同。
func revealInFileManager(path string) error {
	if path == "" {
		return errors.New("路径为空")
	}

	var command *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", "-R", path)
	case "windows":
		// explorer 的 /select 参数要求路径紧跟在逗号后面，不能有空格。
		command = exec.Command("explorer", "/select,"+path)
	default:
		command = exec.Command("xdg-open", filepath.Dir(path))
	}

	if err := command.Start(); err != nil {
		return fmt.Errorf("打开文件管理器失败: %w", err)
	}

	// 刻意不 Wait：这些命令有的会立刻返回、有的会一直挂着（explorer 就是），
	// 我们只需要把窗口唤起来，不需要等它退出。
	return nil
}
