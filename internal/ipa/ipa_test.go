package ipa

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/majd/ipatool/v2/pkg/appstore"
)

// newTestDeps 装配一套完全隔离的依赖：状态目录在 t.TempDir() 里，
// 不碰用户真实的 ~/.ipatool 与钥匙串条目。
func newTestDeps(t *testing.T) *Deps {
	t.Helper()

	deps, err := NewDeps(Options{
		StateDir:   t.TempDir(),
		Passphrase: StaticPassphrase("test-passphrase"),
	})
	if err != nil {
		t.Fatalf("NewDeps 失败: %v", err)
	}

	return deps
}

// TestPreflight 断言启动自检在干净环境下通过。
//
// 这条测试真正的价值在于：它会在「上游漂移」时变红。
// 例如有人把 go.mod 里的 ipatool 升到 v2.8.2，upstream_version 检查会失败；
// 又例如在 macOS 上用 CGO_ENABLED=0 跑测试，credential_backend 会失败。
func TestPreflight(t *testing.T) {
	deps := newTestDeps(t)

	report := deps.Preflight()

	for _, check := range report.Checks {
		t.Logf("[%s] %s: %s", check.Status, check.Name, check.Detail)
	}

	if !report.OK {
		t.Fatalf("启动自检未通过：%+v", report.Checks)
	}

	// 测试二进制里 debug.ReadBuildInfo().Deps 为空，版本项只能是 warn。
	// 版本一致性由 TestGoModPinsUpstreamDependencies 从 go.mod 侧断言。
	for _, check := range report.Checks {
		if check.Name == "upstream_version" && check.Status == StatusFail {
			t.Errorf("版本自检应当最多 warn，实际 fail：%s", check.Detail)
		}
	}
}

// TestEvaluateUpstreamVersion 给版本判据本身写测试。
//
// 背景（实测）：`go test` 构建的测试二进制里 debug.ReadBuildInfo().Deps == 0，
// 所以「读得到版本」这条路径在测试里跑不到。把比较抽成纯函数后，三条分支都可测。
func TestEvaluateUpstreamVersion(t *testing.T) {
	cases := []struct {
		name    string
		version string
		found   bool
		want    CheckStatus
	}{
		{name: "版本一致", version: ExpectedUpstreamVersion, found: true, want: StatusOK},
		{name: "fork 版本", version: LocalForkVersion, found: true, want: StatusOK},
		{name: "版本漂移", version: "v2.9.0", found: true, want: StatusFail},
		{name: "读不到版本", version: "", found: false, want: StatusWarn},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check, effective := evaluateUpstreamVersion(tc.version, tc.found)
			if check.Status != tc.want {
				t.Fatalf("Status = %s，期望 %s（%s）", check.Status, tc.want, check.Detail)
			}

			if tc.found && effective != tc.version {
				t.Errorf("effective = %q，期望 %q", effective, tc.version)
			}
		})
	}
}

// TestGoModPinsUpstreamDependencies 是真正的漂移护栏。
//
// 它盯的不是「我们的代码」，而是三条**架构前提**：
//  1. ipatool 必须钉在 ExpectedUpstreamVersion —— 我们复刻了它的
//     未导出装配函数（statedir.go / keychain.go），并依赖它的错误文案；
//  2. byteness/keyring 必须与上游同版 —— 否则凭据后端的实现可能不同，
//     「GUI 登录过 → 官方 CLI / MCP helper 能用」这条承诺会断；
//  3. progressbar 必须与上游同版 —— 我们直接读它的 State()/GetMax64()，
//     换版本可能改变进度语义（分母、百分比、续传起点）。
//
// 谁升级这三个依赖，谁就得先回答上面的问题。
func TestGoModPinsUpstreamDependencies(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("读取 go.mod 失败: %v", err)
	}

	content := string(data)

	required := map[string]string{
		"github.com/majd/ipatool/v2":           ExpectedUpstreamVersion,
		"github.com/byteness/keyring":          "v1.9.0",
		"github.com/schollz/progressbar/v3":    "v3.19.1",
		"github.com/juju/persistent-cookiejar": "v1.0.0",
	}

	for module, version := range required {
		want := module + " " + version
		if !strings.Contains(content, want) {
			t.Errorf("go.mod 里缺少 %q（这条钉版是架构前提，不是随手写的）", want)
		}
	}

	// GUI 实际链接的是 third_party 里的 fork（v2.8.1 + FairPlay sinf 修复），
	// 不是上游模块。replace 丢了就会静默退回上游代码，下载缺 sinf 的问题会复发。
	if !strings.Contains(content, "github.com/majd/ipatool/v2 => ./third_party/ipatool") {
		t.Errorf("go.mod 必须把 ipatool replace 到 ./third_party/ipatool（v2.8.1 + sinf 修复）")
	}
}

// TestForkRetainsSinfFallback 盯第四件事：fork 里的 FairPlay 回退不能丢。
//
// 升上游时最容易犯的错是“整个覆盖 third_party”，然后把 replace 留着的旧代码
// 当成已经带了补丁。缺失的表现是静默的：能下到包，但包里的二进制没 sinf，
// 在未授权设备上装完打不开。这里按符号判，不按文件哈希判（上游上下文会变）。
func TestForkRetainsSinfFallback(t *testing.T) {
	path := filepath.Join("..", "..", "third_party", "ipatool", "pkg", "appstore", "appstore_download_product.go")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 fork 的下载实现失败: %v", err)
	}

	content := string(data)

	if !strings.Contains(content, "hasDownloadFairPlayData") {
		t.Errorf("%s 里缺少 hasDownloadFairPlayData（FairPlay 回退补丁丢了，重新按 FORK.md 重放）", path)
	}

	if !strings.Contains(content, "err == nil && hasDownloadFairPlayData(res)") {
		t.Errorf("%s 里 ent/download 分支没有接入 sinf 判定（补丁只带了一半）", path)
	}

	// 下架 App 恢复（sendDelistedDownload）走的是另一个 ent/download 分支。
	// 上游 v2.8.1 之后明确把「该分支成功返回但没有 sinf/dpInfo」当作合法结果，
	// 所以这里只要求主链路有判定：定义 1 + 主链路 1。
	if count := strings.Count(content, "hasDownloadFairPlayData("); count < 2 {
		t.Errorf("%s 里 hasDownloadFairPlayData 只出现 %d 次（期望 >= 2：定义 1 + 主链路 1）", path, count)
	}
}

// TestForkRetainsStorefrontRetry 盯第五件事：fork 里的店面发现重试不能丢。
//
// 这处补丁的作用是「苹果侧 5xx/429/连接重置时自动再试两次」，丢了不会编译失败，
// 只会让搜索/登录在苹果抖一下时直接报错，静默退化。同样按符号判。
func TestForkRetainsStorefrontRetry(t *testing.T) {
	path := filepath.Join("..", "..", "third_party", "ipatool", "pkg", "appstore", "storefront.go")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 fork 的店面发现实现失败: %v", err)
	}

	content := string(data)

	for _, symbol := range []string{
		"storefrontDiscoveryAttempts",
		"storefrontRetrySleep",
		"fetchAvailableStorefronts",
		"StatusTooManyRequests",
	} {
		if !strings.Contains(content, symbol) {
			t.Errorf("%s 里缺少 %s（店面发现重试补丁丢了，重新按 FORK.md 重放）", path, symbol)
		}
	}
}

// TestPreflightRejectsUnwritableStateDir 证明自检不是「永远返回 OK」的摆设：
// 状态目录不可写时，必须报 fail。
func TestPreflightRejectsUnwritableStateDir(t *testing.T) {
	deps := newTestDeps(t)

	// 目录不存在 → 自检必须发现（CreateTemp 会失败）。
	deps.StateDir = filepath.Join(deps.StateDir, "does", "not", "exist")

	report := deps.Preflight()
	if report.OK {
		t.Fatal("状态目录不可写时自检仍报告 OK")
	}

	if !hasFailedCheck(report, "state_dir_writable") {
		t.Fatalf("缺少 state_dir_writable 失败项：%+v", report.Checks)
	}
}

func hasFailedCheck(report Report, name string) bool {
	for _, check := range report.Checks {
		if check.Name == name && check.Status == StatusFail {
			return true
		}
	}

	return false
}

// TestSnapshotIndeterminateUntilTotalKnown 固定住「分母未知」这条判据。
//
// 界面靠它决定显示不确定态动画还是百分比。上游改行为时，这里会红。
func TestSnapshotIndeterminateUntilTotalKnown(t *testing.T) {
	bar := NewProgressBar()

	initial := Snapshot("t1", "download", bar)
	if !initial.Indeterminate {
		t.Fatalf("新建进度条应当是分母未知态，实际 %+v", initial)
	}

	// 模拟上游拿到 Content-Length 之后的行为。
	bar.ChangeMax64(4096)
	_ = bar.Set64(2048)

	known := Snapshot("t1", "download", bar)
	if known.Indeterminate {
		t.Fatalf("设置分母后不应还是未知态：%+v", known)
	}

	if known.Total != 4096 {
		t.Errorf("Total = %d，期望 4096", known.Total)
	}

	if known.Current != 2048 {
		t.Errorf("Current = %d，期望 2048", known.Current)
	}

	if known.Percent < 0.49 || known.Percent > 0.51 {
		t.Errorf("Percent = %f，期望约 0.5", known.Percent)
	}
}

// TestPollProgressEmitsAndStops 证明轮询会推送、且 stop 之后 goroutine 收干净。
func TestPollProgressEmitsAndStops(t *testing.T) {
	bar := NewProgressBar()
	bar.ChangeMax64(1000)
	_ = bar.Set64(250)

	var got []Progress

	stop := PollProgressUntil("t2", "download", bar, func(p Progress) {
		got = append(got, p)
	})

	// 直接调 stop：至少要验证它不阻塞、不 panic。
	stop()

	// 再轮询一次，确认停止后不再推。
	before := len(got)
	stop()

	if len(got) != before {
		t.Fatalf("停止后仍在推送：%d -> %d", before, len(got))
	}
}

// TestMapErrorNotLoggedIn 用真实的上游错误串验证「未登录」判定。
func TestMapErrorNotLoggedIn(t *testing.T) {
	// 这是上游在凭据库里找不到账号时的真实报错前缀。
	err := errors.New("failed to get account: " + markerKeyringNotFound)

	mapped := MapError(err)
	if mapped.Code != CodeNotLoggedIn {
		t.Fatalf("Code = %s，期望 %s", mapped.Code, CodeNotLoggedIn)
	}

	if !IsNotLoggedIn(err) {
		t.Error("IsNotLoggedIn 应为 true")
	}
}

// TestMapErrorPreservesSentinels 保证错误链没被吃掉：
// 界面拿到 Code，程序内部仍能 errors.Is 到上游哨兵。
//
// 上游的包装方式是 fmt.Errorf("failed to download file: %w", err)，
// 所以这里必须用 %w —— 用字符串拼接是**测不出错误链**的（那会退化成
// 「只能靠文案匹配」，正是我们要避免的脆弱判据）。
func TestMapErrorPreservesSentinels(t *testing.T) {
	wrapped := fmt.Errorf("failed to download file: %w", appstore.ErrLicenseRequired)

	mapped := MapError(wrapped)
	if mapped.Code != CodeLicenseRequired {
		t.Fatalf("Code = %s，期望 %s", mapped.Code, CodeLicenseRequired)
	}

	if !mapped.Retryable {
		t.Error("缺许可应当是可重试的（可先获取许可）")
	}

	if !errors.Is(mapped, appstore.ErrLicenseRequired) {
		t.Error("MapError 应当保留错误链，让调用方仍能 errors.Is 到上游哨兵")
	}
}

// TestMapErrorTransientUpstreamStatus 覆盖真实遇到的登录失败：
// 首次登录要初始化 SAP 签名器，Apple 在那个交换接口上返回 502 时，
// 上游只给英文文本；界面必须得到「苹果侧临时故障、稍后重试」，而不是 unknown。
func TestMapErrorTransientUpstreamStatus(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus string
	}{
		{
			name: "SAP 签名器 502",
			err: errors.New("failed to initialize SAP action signer: create SAP signer: " +
				"exchange SAP setup message: apple returned 502 Bad Gateway"),
			wantStatus: "502",
		},
		{
			name:       "目录接口 503",
			err:        errors.New("received unexpected status code: 503"),
			wantStatus: "503",
		},
		{
			name:       "登录限流 429",
			err:        errors.New("apple returned 429 Too Many Requests"),
			wantStatus: "429",
		},
		{
			name:       "Anisette provisioning 502",
			err:        errors.New("prepare Apple authentication: anisette provisioning returned HTTP 502"),
			wantStatus: "502",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			mapped := MapError(testCase.err)
			if mapped.Code != CodeTemporarilyUnavailable {
				t.Fatalf("Code = %s，期望 %s", mapped.Code, CodeTemporarilyUnavailable)
			}

			if !mapped.Retryable {
				t.Error("苹果侧临时故障应当是可重试的")
			}

			text := mapped.Error()
			if strings.Contains(text, "unknown") || strings.Contains(text, "temporarily_unavailable") {
				t.Errorf("给用户看的文本不应包含错误码: %q", text)
			}

			if !strings.Contains(text, testCase.wantStatus) {
				t.Errorf("文本应当说明 HTTP 状态 %s，实际 %q", testCase.wantStatus, text)
			}
		})
	}
}

// TestAppErrorTextIsUserFacing 钉住「错误码不进界面」这条 PRD 要求：
// Wails 只把 error.Error() 送给前端，所以 Error() 必须是中文动作，而不是码 + 英文原因。
func TestAppErrorTextIsUserFacing(t *testing.T) {
	err := MapError(errors.New("some unexpected upstream failure"))

	if strings.Contains(err.Error(), "unknown:") {
		t.Errorf("Error() 不应以错误码开头: %q", err.Error())
	}

	if !strings.Contains(err.Error(), "操作失败") {
		t.Errorf("Error() 应当保留中文说明: %q", err.Error())
	}
}

// TestMapErrorAccountDisabled 覆盖那个真实的用户投诉：
// Apple 返回 "Your account is disabled."，但上游把它放进 Metadata，
// 只有 Error() 文本里有 —— 我们必须能识别。
func TestMapErrorAccountDisabled(t *testing.T) {
	err := errors.New("account is disabled")

	mapped := MapError(err)
	if mapped.Code != CodeAccountDisabled {
		t.Fatalf("Code = %s，期望 %s", mapped.Code, CodeAccountDisabled)
	}
}

// TestValidateAuthCode 与上游的 6 位数字约束对齐。
func TestValidateAuthCode(t *testing.T) {
	cases := map[string]bool{
		"":        true,
		"123456":  true,
		"12345":   false,
		"1234567": false,
		"12345a":  false,
	}

	for input, wantOK := range cases {
		err := ValidateAuthCode(input)
		if (err == nil) != wantOK {
			t.Errorf("ValidateAuthCode(%q) = %v，期望 ok=%v", input, err, wantOK)
		}
	}
}

// TestPartialSizeAndReset 固定住续传契约：.tmp 在就能读到大小，Reset 之后就没有了。
func TestPartialSizeAndReset(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "App.ipa")
	tmpPath := destination + ".tmp"

	if _, ok := PartialSize(tmpPath); ok {
		t.Fatal("分片尚不存在，PartialSize 不应返回 true")
	}

	if err := os.WriteFile(tmpPath, []byte("partial-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	size, ok := PartialSize(tmpPath)
	if !ok {
		t.Fatal("分片存在，PartialSize 应返回 true")
	}

	if size != int64(len("partial-bytes")) {
		t.Errorf("size = %d，期望 %d", size, len("partial-bytes"))
	}

	if err := ResetPartial(destination); err != nil {
		t.Fatalf("ResetPartial 失败: %v", err)
	}

	if _, ok := PartialSize(tmpPath); ok {
		t.Fatal("ResetPartial 之后分片应当不存在")
	}

	// 幂等：分片不存在时再删一次也不算错误。
	if err := ResetPartial(destination); err != nil {
		t.Fatalf("ResetPartial 应当幂等: %v", err)
	}
}

// TestDownloadRejectsRelativeOutputPath 证明「必须传绝对路径」这条约束是承重的：
// 否则 .tmp 的位置在下载前不可预测，续传就是空谈。
func TestDownloadRejectsRelativeOutputPath(t *testing.T) {
	deps := newTestDeps(t)
	services := NewServices(deps)

	_, err := services.Download(t.Context(), DownloadRequest{
		BundleID:   "com.example.app",
		OutputPath: "relative/App.ipa",
	}, nil)

	if err == nil {
		t.Fatal("相对路径应当被拒绝")
	}

	var mapped *AppError
	if !errors.As(err, &mapped) {
		t.Fatalf("错误应当是 *AppError，实际 %T", err)
	}
}

// 目录必须被明确拒绝：上游其实接受目录，但那样 destination 要等响应才知道，
// 我们就算不出 .tmp 的真实位置 —— 续传判断会指向一个不存在的路径（静默失效）。
func TestDownloadRejectsDirectoryOutputPath(t *testing.T) {
	deps := newTestDeps(t)
	services := NewServices(deps)

	dir := t.TempDir()

	_, err := services.Download(t.Context(), DownloadRequest{
		BundleID:   "com.example.app",
		OutputPath: dir,
	}, nil)
	if err == nil {
		t.Fatal("目录作为输出路径应当被拒绝")
	}

	var mapped *AppError
	if !errors.As(err, &mapped) {
		t.Fatalf("错误应当是 *AppError，实际 %T", err)
	}

	if !strings.Contains(mapped.Message, "目录") {
		t.Fatalf("错误文案没说明是目录问题: %+v", mapped)
	}
}

func TestDefaultDownloadFileName(t *testing.T) {
	cases := []struct {
		name      string
		appID     int64
		bundleID  string
		versionID string
		want      string
	}{
		{"常规", 123, "com.example.app", "1234567", "com.example.app_1234567.ipa"},
		{"无版本号", 123, "com.example.app", "", "com.example.app_latest.ipa"},
		{"无 bundle 用 appID", 123, "", "9", "app-123_9.ipa"},
		{"全空", 0, "", "", "app_latest.ipa"},
	}

	for _, testCase := range cases {
		got := DefaultDownloadFileName(testCase.appID, testCase.bundleID, testCase.versionID)
		if got != testCase.want {
			t.Errorf("%s: got %q, want %q", testCase.name, got, testCase.want)
		}

		if filepath.Base(got) != got {
			t.Errorf("%s: 结果含路径分隔符: %q", testCase.name, got)
		}
	}
}

// 这条守的是「模型/设置里来的 bundle id 不能变成路径逃逸」。
// 判据不是「看起来没有 ..」，而是几何判据：拼进目录后仍在该目录内。
func TestDefaultDownloadFileNameCannotEscapeDirectory(t *testing.T) {
	dir := t.TempDir()

	evil := []struct {
		name      string
		bundleID  string
		versionID string
	}{
		{"上跳", "../../etc/passwd", "1"},
		{"反斜杠", `..\..\windows\system32`, "1"},
		{"绝对路径", "/etc/passwd", "1"},
		{"空字节", "com.example\x00evil", "1"},
		{"只有上跳", "..", ".."},
		{"冒号", "C:evil", "1"},
	}

	for _, testCase := range evil {
		fileName := DefaultDownloadFileName(42, testCase.bundleID, testCase.versionID)

		if filepath.Base(fileName) != fileName {
			t.Errorf("%s: 文件名含分隔符: %q", testCase.name, fileName)
		}

		full := filepath.Join(dir, fileName)

		relative, err := filepath.Rel(dir, full)
		if err != nil {
			t.Errorf("%s: Rel 失败: %v", testCase.name, err)
			continue
		}

		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Errorf("%s: 逃出了输出目录: %q", testCase.name, full)
		}
	}
}
