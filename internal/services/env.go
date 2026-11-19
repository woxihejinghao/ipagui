package services

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/milan/ipagui/internal/ipa"
)

// EnvService 暴露运行环境与自检结果，供前端的「环境不可用」页使用。
type EnvService struct {
	runtime *Runtime
}

// NewEnvService 构造环境服务。
func NewEnvService(runtimeInstance *Runtime) *EnvService {
	return &EnvService{runtime: runtimeInstance}
}

// EnvironmentInfo 是前端的启动信息。
type EnvironmentInfo struct {
	Version      string `json:"version"`
	Initialized  bool   `json:"initialized"`
	InitError    string `json:"initError,omitempty"`
	StateDir     string `json:"stateDir,omitempty"`
	DatabasePath string `json:"databasePath,omitempty"`
	CacheDir     string `json:"cacheDir,omitempty"`
	OutputDir    string `json:"outputDir"`
	Platform     string `json:"platform"`
}

// Info 返回不含自检细节的启动信息（界面首屏用，必须很快）。
func (s *EnvService) Info() EnvironmentInfo {
	info := EnvironmentInfo{
		Version:     s.runtime.Version(),
		Initialized: s.runtime.InitError() == nil,
		OutputDir:   s.runtime.Settings().OutputDir,
		Platform:    s.runtime.Settings().Platform,
	}

	if cacheDir, err := os.UserCacheDir(); err == nil {
		// 上游把首次登录要下载的 Apple 资产与 Unicorn 运行时缓存在这里
		// （<cache>/ipatool/sap/apple-assets-v2、<cache>/ipatool/unicorn/2.1.4）。
		// 界面把它显示出来，「首次准备」这件事就变成用户能自己核对的事实。
		info.CacheDir = cacheDir
	}

	if err := s.runtime.InitError(); err != nil {
		info.InitError = err.Error()
	}

	if deps := s.runtime.Dependencies(); deps != nil {
		info.StateDir = deps.StateDir
	}

	if library := s.runtime.Library(); library != nil {
		info.DatabasePath = library.Path()
	}

	return info
}

// Confirm 弹一个系统原生确认框，返回用户是否点了「确定」。
//
// 为什么不用前端的 window.confirm：Wails v2 在 macOS 的 WKWebView 没有实现
// WKUIDelegate 的 runJavaScriptConfirmPanel / runJavaScriptAlertPanel，WebKit
// 遇到这种情况按「用户点了取消」处理 —— 于是 window.confirm 在 macOS 上恒为
// false，「移除记录」这类二次确认就成了点了没反应。原生对话框由 Wails 在
// macOS / Windows / Linux 上各自实现，行为一致。
//
// 注意：这是模态对话框，调用期间会阻塞界面；前端应当只在用户手动的破坏性操作前调它。
func (s *EnvService) Confirm(title, message string) (bool, error) {
	ctx := s.runtime.Context()
	if ctx == nil {
		return false, errors.New("窗口还没准备好，稍后再试")
	}

	if title == "" {
		title = "确认操作"
	}

	buttons := []string{"确定", "取消"}

	selection, err := wailsruntime.MessageDialog(ctx, wailsruntime.MessageDialogOptions{
		Type:          wailsruntime.QuestionDialog,
		Title:         title,
		Message:       message,
		Buttons:       buttons,
		DefaultButton: buttons[0],
		CancelButton:  buttons[1],
	})
	if err != nil {
		return false, err
	}

	return selection == buttons[0], nil
}

// ClientReady 由前端在挂载成功后调用，用来回答一个排障问题：
// 「窗口起来了，但界面到底跑没跑起来？」
//
// WebView 空白（前端脚本报错、资源没打进包里）时不会有任何报错传到 Go 侧 ——
// 用户只会看到一个白窗口。所以这里让前端主动留一条痕迹：有了这一行，就能区分
// 「界面没渲染」和「界面渲染了但后端有问题」。
func (s *EnvService) ClientReady() error {
	directory, err := ipa.ConfigDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}

	line := time.Now().Format(time.RFC3339) + " ui-ready version=" + s.runtime.Version() + "\n"

	file, err := os.OpenFile(filepath.Join(directory, "ui-ready.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}

	defer func() { _ = file.Close() }()

	_, err = file.WriteString(line)

	return err
}
