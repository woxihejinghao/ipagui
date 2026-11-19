package ipa

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/majd/ipatool/v2/pkg/appstore"
)

// ExpectedUpstreamVersion 是 go.mod 里钉住的 ipatool 版本。
//
// 为什么要有这个自检：我们依赖的是上游的**未导出装配函数**（复刻）与一批
// pkg/ 内部符号。上游一个 patch 版本如果改了状态目录布局、错误文案或
// 进度条用法，症状会是「登录态读不到」「错误提示变成一句英文」这类
// 静默故障。把版本比对做成启动自检，就能把静默故障变成一条明确的失败。
const ExpectedUpstreamVersion = "v2.8.0"

const (
	// LocalForkVersion 是 v2.8.0 基线 + FairPlay sinf 修复的 fork 标记。
	//
	// go.mod 通过本地 replace 指向 third_party/ipatool；Go 构建信息里只能
	// 读到替换路径、读不到版本号，所以这里统一映射成这个固定值，避免把
	// “按预期使用 fork”误判成“上游版本漂移”。
	LocalForkVersion = "v2.8.0+sinf-fix"

	// localForkPathFragment 用来确认本地 replace 指向仓库内的 fork 副本，
	// 而不是任意目录。
	localForkPathFragment = "third_party/ipatool"
)

// CheckStatus 是自检项的结论。
type CheckStatus string

const (
	StatusOK   CheckStatus = "ok"
	StatusWarn CheckStatus = "warn"
	StatusFail CheckStatus = "fail"
)

// Check 是单条自检结论。
type Check struct {
	Name   string      `json:"name"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail"`
}

// Report 是启动自检报告，会原样透传给界面（诊断页/首次运行页）。
type Report struct {
	OK              bool      `json:"ok"`
	Checks          []Check   `json:"checks"`
	StateDir        string    `json:"stateDir"`
	HomeDir         string    `json:"homeDir"`
	GOOS            string    `json:"goos"`
	GOARCH          string    `json:"goarch"`
	CGOEnabled      bool      `json:"cgoEnabled"`
	UpstreamVersion string    `json:"upstreamVersion"`
	Backend         string    `json:"backend"`
	GeneratedAt     time.Time `json:"generatedAt"`
}

// goos 是 runtime.GOOS 的间接引用，方便单测替换（不要在别处直接用 runtime.GOOS）。
func goos() string { return runtime.GOOS }

// Preflight 跑一遍启动自检。任何 StatusFail 都会让 Report.OK = false，
// 调用方（main/wails 启动流程）应当在有失败项时拒绝进入主界面并展示报告。
func (d *Deps) Preflight() Report {
	report := Report{
		OK:         true,
		StateDir:   d.StateDir,
		HomeDir:    d.HomeDir,
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		CGOEnabled: cgoEnabled(),
		Backend:    backendName(),
	}

	add := func(name string, status CheckStatus, format string, args ...any) {
		report.Checks = append(report.Checks, Check{
			Name:   name,
			Status: status,
			Detail: fmt.Sprintf(format, args...),
		})

		if status == StatusFail {
			report.OK = false
		}
	}

	// 1) 状态目录可写。登录态、cookie jar、文件后端的密文都落在这里。
	if err := assertDirWritable(d.StateDir); err != nil {
		add("state_dir_writable", StatusFail, "状态目录不可写：%v（%s）", err, d.StateDir)
	} else {
		add("state_dir_writable", StatusOK, "%s 可写", d.StateDir)
	}

	// 2) 凭据后端。macOS 上必须是钥匙串：一旦用 CGO_ENABLED=0 构建，
	//    byteness/keyring 的 keychain 后端会静默消失并降级成文件后端。
	expected := ExpectedBackend()
	actual, ok := PickedBackend()
	switch {
	case !ok:
		add("credential_backend", StatusFail, "没有任何可用的凭据后端（期望 %s）", expected)
	case actual != expected:
		add("credential_backend", StatusFail,
			"凭据后端降级：期望 %s，实际 %s（本机：%s）。macOS 上用 CGO_ENABLED=0 构建会丢掉钥匙串后端",
			expected, actual, backendName())
	default:
		add("credential_backend", StatusOK, "%s", actual)
	}

	// 3) 错误哨兵可用性：MapError 全部依赖 errors.Is 链，这里做一次语义断言。
	sentinels := []struct {
		name string
		err  error
	}{
		{"ErrAuthCodeRequired", appstore.ErrAuthCodeRequired},
		{"ErrPasswordTokenExpired", appstore.ErrPasswordTokenExpired},
		{"ErrLicenseRequired", appstore.ErrLicenseRequired},
		{"ErrLicenseAlreadyExists", appstore.ErrLicenseAlreadyExists},
		{"ErrTemporarilyUnavailable", appstore.ErrTemporarilyUnavailable},
		{"ErrAppNotFound", appstore.ErrAppNotFound},
	}

	var broken []string

	for _, sentinel := range sentinels {
		if sentinel.err == nil {
			broken = append(broken, sentinel.name+"=nil")

			continue
		}

		// 模拟上游的包装方式，确认错误链仍可穿透。
		wrapped := fmt.Errorf("failed to do thing: %w", sentinel.err)
		if !errors.Is(wrapped, sentinel.err) {
			broken = append(broken, sentinel.name+"=unwrappable")
		}
	}

	if len(broken) > 0 {
		add("error_sentinels", StatusFail, "错误哨兵异常：%v", broken)
	} else {
		add("error_sentinels", StatusOK, "%d 个哨兵错误均可穿透包装", len(sentinels))
	}

	// 4) 进度语义：新进度条的 max 必须是 1（上游在拿到响应头前不设分母），
	//    我们据此判定「进度不可知」。若上游改了初始 max，这个前提就塌了。
	bar := NewProgressBar()
	snapshot := Snapshot("selftest", "selftest", bar)
	if !snapshot.Indeterminate || bar.GetMax64() > 1 {
		add("progress_semantics", StatusWarn,
			"新进度条初始 max=%d（期望 1）：分母未知的判据可能已失效", bar.GetMax64())
	} else {
		add("progress_semantics", StatusOK, "初始 max=1，分母未知判据成立")
	}

	// 5) 上游版本。
	versionCheck, effective := evaluateUpstreamVersion(upstreamVersionFromBuildInfo())
	report.UpstreamVersion = effective
	report.Checks = append(report.Checks, versionCheck)

	if versionCheck.Status == StatusFail {
		report.OK = false
	}

	report.GeneratedAt = time.Now()

	return report
}

// evaluateUpstreamVersion 把「从构建信息里读到的版本」翻译成一条结论。
//
// 为什么单独抽成纯函数：实测 `go test` 构建出的测试二进制里
// debug.ReadBuildInfo().Deps 是**空的**（deps=0），所以在测试环境里
// 这个判据永远读不到版本。只有把「比较」这一步抽出来，才能给它写测试
// （见 TestEvaluateUpstreamVersion / TestGoModPinsUpstreamDependencies）。
func evaluateUpstreamVersion(version string, found bool) (Check, string) {
	if !found {
		return Check{
			Name:   "upstream_version",
			Status: StatusWarn,
			Detail: "构建信息里找不到 ipatool 版本（测试二进制 / go run 环境下 Deps 为空，属预期）",
		}, ""
	}

	if version != ExpectedUpstreamVersion && version != LocalForkVersion {
		return Check{
			Name:   "upstream_version",
			Status: StatusFail,
			Detail: fmt.Sprintf(
				"上游版本漂移：实际 %s，预期 %s（或 fork 的 %s）。请复核 statedir.go / keychain.go 的复刻逻辑与错误文案",
				version, ExpectedUpstreamVersion, LocalForkVersion),
		}, version
	}

	detail := fmt.Sprintf("%s（与预期一致）", version)
	if version == LocalForkVersion {
		detail = fmt.Sprintf("%s（v2.8.0 基线 + FairPlay sinf 修复）", version)
	}

	return Check{
		Name:   "upstream_version",
		Status: StatusOK,
		Detail: detail,
	}, version
}

func assertDirWritable(dir string) error {
	if dir == "" {
		return errors.New("状态目录为空")
	}

	file, err := os.CreateTemp(dir, ".ipagui-selftest-*")
	if err != nil {
		return err
	}

	name := file.Name()
	_ = file.Close()

	return os.Remove(name)
}

// cgoEnabled 从构建信息里读 CGO_ENABLED，比编译期常量更贴近真实构建。
func cgoEnabled() bool {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}

	for _, setting := range info.Settings {
		if setting.Key == "CGO_ENABLED" {
			return setting.Value == "1"
		}
	}

	return false
}

func backendName() string {
	backend, ok := PickedBackend()
	if !ok {
		return "(none)"
	}

	return string(backend)
}

// upstreamVersionFromBuildInfo 在依赖列表里找 ipatool 的版本。
func upstreamVersionFromBuildInfo() (string, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}

	if info.Main.Path == "github.com/majd/ipatool/v2" {
		return info.Main.Version, true
	}

	for _, dep := range info.Deps {
		if dep.Path != "github.com/majd/ipatool/v2" {
			continue
		}

		if dep.Replace != nil {
			// 本地文件系统 replace 的 Version 是空串或 "(devel)"，路径才是可靠信号；
			// 先按路径确认它指向仓库内的 fork 副本。
			if strings.Contains(filepath.ToSlash(dep.Replace.Path), localForkPathFragment) {
				return LocalForkVersion, true
			}

			// 远程 replace 带真实版本号。
			if dep.Replace.Version != "" && dep.Replace.Version != "(devel)" {
				return dep.Replace.Version, true
			}

			// 非预期的本地替换：返回路径，让版本检查明确失败。
			return dep.Replace.Path, true
		}

		return dep.Version, true
	}

	return "", false
}
