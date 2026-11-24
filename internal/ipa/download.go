package ipa

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/majd/ipatool/v2/pkg/appstore"
)

// DownloadRequest 是一次下载请求。
type DownloadRequest struct {
	AppID    int64  `json:"appId"`
	BundleID string `json:"bundleId"`
	Platform string `json:"platform"`
	// ExternalVersionID 为空表示下载最新版；对应 CLI 的 --external-version-id。
	ExternalVersionID string `json:"externalVersionId,omitempty"`
	// OutputPath 必须是**绝对路径的完整文件名**（不是目录）。
	//
	// 为什么强制：上游的 resolveDestinationPath 在 OutputPath 为空时会用
	// `workdir/<名字>`，而那个名字依赖下载响应里的 bundleShortVersionString，
	// 只有拿到响应才知道 —— 于是「续传用的 .tmp 在哪」在下载前无法确定。
	// 传完整绝对路径就能让 destination 完全可预测，从而支持跨进程续传。
	OutputPath string `json:"outputPath"`
	// AcquireLicense 对应 CLI 的 --purchase：缺许可时先调 Purchase（仅免费应用）。
	AcquireLicense bool `json:"acquireLicense"`
}

// DownloadResult 是一次下载的结论。
type DownloadResult struct {
	DestinationPath string `json:"destinationPath"`
	TmpPath         string `json:"tmpPath"`
	// Resumed 表示本次是从已存在的 .tmp 分片继续的。
	Resumed bool `json:"resumed"`
	// BytesAtStart 是续传起点（0 表示全新下载）。
	BytesAtStart int64 `json:"bytesAtStart"`
	// AcquiredLicense 表示本次下载前成功获取了许可。
	AcquiredLicense bool `json:"acquiredLicense"`
	// SinfCount 是下载响应里 Apple 单独下发的 sinf 数量。
	// Download 会在返回前把它们写回包内（ReplicateSinf），所以 >0 表示已注入；
	// 0 表示服务端没有下发 sinf（例如 macOS 包），包内自然也就没有。
	SinfCount int `json:"sinfCount"`
}

// maxDownloadAttempts 与 CLI 一致（retry.Attempts(3)）。
const maxDownloadAttempts = 3

// DownloadFileName 给出默认的下载文件名（不含目录）：`<App 名称>_<版本>_<账号>.ipa`。
//
// 主名用 App 名称（用户明确要求）：以前是 bundle id（com.tencent.mqq_9.3.75.ipa），
// 在访达里根本看不出这是哪个 App，中文应用尤其明显。名称取不到时才退回 bundle id，
// 再退回 app-<id>。
//
// 版本优先级：Apple 版本标识（download --external-version-id 那串数字）>
// 显示版本号（8.0.50）> latest —— 文件名自己就能回答「这是哪一版」，
// 避免 _latest 被下一次下载惄惄覆盖后什么也说不清。
//
// 账号段（account）拼在最后：同一个 App 换号后可能都要下一份（已购/许可按账号算），
// 只有文件名里带上账号，才能一眼分清哪份是谁下的、也才不会互相覆盖。
// 调用方传 email（唯一、能对应到 Apple ID）；没有 email 时传姓名；两者都没有就传空，
// 这一段会被省略 —— 旧格式 `<App 名称>_<版本>.ipa` 仍然成立。
//
// 名字里出现的所有外部输入（App 名称、bundle id、版本号、账号）都过 sanitizeSegment，
// 避免它们被构造成 `../../x` 这类路径逃逸，也顺手把 Windows 不允许的字符收敛掉。
func DownloadFileName(appID int64, appName, bundleID, versionID, displayVersion, account string) string {
	base := sanitizeSegment(appName)
	if base == "" {
		base = sanitizeSegment(bundleID)
	}

	if base == "" && appID > 0 {
		base = "app-" + strconv.FormatInt(appID, 10)
	}

	if base == "" {
		base = "app"
	}

	version := sanitizeSegment(versionID)
	if version == "" {
		version = sanitizeSegment(displayVersion)
	}

	if version == "" {
		version = "latest"
	}

	name := base + "_" + version

	// 账号段单独限长：email 可能很长，不能把整个文件名的额度吃光。
	if label := sanitizeSegment(truncateRunes(account, maxAccountSegment)); label != "" {
		name += "_" + label
	}

	return name + ".ipa"
}

// maxAccountSegment 是账号段（email / 姓名）的字符上限。
const maxAccountSegment = 60

// DefaultDownloadFileName 是「不知道 App 名称与账号」时的入口（测试与旧的调用点）。
func DefaultDownloadFileName(appID int64, bundleID, versionID string) string {
	return DownloadFileName(appID, "", bundleID, versionID, "", "")
}

// sanitizeSegment 把一段外部输入收敛成安全的文件名片段。
func sanitizeSegment(value string) string {
	value = strings.TrimSpace(value)

	var builder strings.Builder

	for _, char := range value {
		switch {
		case char == '/' || char == '\\' || char == ':' || char == 0,
			// Windows 文件名不允许这些字符：在 macOS 上能存，但同一个下载目录
			// 搬到 Windows 版（项目同时构建 windows-amd64）就会直接失败。
			char == '*' || char == '?' || char == '"' || char == '<' || char == '>' || char == '|':
			builder.WriteRune('_')
		case char < 0x20 || char == 0x7f:
			// 控制字符直接丢弃：它们在文件名里没有意义，还可能骗过终端显示。
		default:
			builder.WriteRune(char)
		}
	}

	// 开头的点/下划线/空格去掉（避免隐藏文件、避免 `..`），末尾的点也要去掉
	// （Windows 不允许文件名以点或空格结尾）。
	cleaned := strings.Trim(builder.String(), " ._")
	if cleaned == "" {
		return ""
	}

	cleaned = truncateRunes(cleaned, maxFileNameSegment)

	return strings.TrimRight(cleaned, " .")
}

// maxFileNameSegment 是单个文件名片段的字符上限。
const maxFileNameSegment = 120

// truncateRunes 按**字符**（不是字节）截断。
//
// 按字节切汉语名会切出半个 UTF-8 字符，文件名就成了非法 UTF-8：macOS 能存，
// 但归档、网络同步、Windows 盘上都会出问题。
func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}

	runes := []rune(value)

	return string(runes[:limit])
}

// Download 下载应用到指定路径，并通过 sink 推送结构化进度。
//
// 错误语义（重要，界面文案依赖它）：
//   - 失败时**绝不移除** `<OutputPath>.tmp`（以及并行下载的 `.ipatool-parts` 目录），
//     这是续传的基础；
//   - 上游自己在两种情况下会删 .tmp：包平台校验失败，以及下载成功收尾时。
//     所以「.tmp 还在」= 可以续传，「.tmp 消失且目标文件存在」= 已完成。
//   - 因此「重新下载」这个动作必须由**调用方**先删除半成品，再重新调用（见 ResetPartial）。
//   - 每次下载开始前会清掉崩溃残留的 `.ipatool-parts`：上游用 os.Mkdir 建它，
//     目录已存在会让本次下载直接失败。
func (s *Services) Download(ctx context.Context, req DownloadRequest, sink ProgressSink) (DownloadResult, error) {
	if req.OutputPath == "" {
		return DownloadResult{}, appErr(CodeUnknown, "缺少输出路径",
			"请先选择保存位置", false, nil)
	}

	if !filepath.IsAbs(req.OutputPath) {
		return DownloadResult{}, appErr(CodeUnknown, "输出路径必须是绝对路径",
			req.OutputPath, false, nil)
	}

	// 必须是**完整文件名**而不是目录：上游 resolveDestinationPath 虽然也接受目录，
	// 但那样 destination 要等下载响应里的 bundleShortVersionString 才知道，
	// 我们就没有办法在下载前算出 .tmp 的真实位置 —— 续传与「删除半成品」都会指错。
	// 与其静默地在错误的路径上找 .tmp，不如在这里明确拒绝。
	if info, statErr := os.Stat(req.OutputPath); statErr == nil && info.IsDir() {
		return DownloadResult{}, appErr(CodeUnknown, "输出路径不能是目录",
			req.OutputPath+"（请给出完整文件名，例如 "+filepath.Join(req.OutputPath, "app.ipa")+"）", false, nil)
	}

	platform := req.Platform
	if platform == "" {
		platform = string(appstore.PlatformIPhone)
	}

	parsed, err := ParsePlatform(platform)
	if err != nil {
		return DownloadResult{}, appErr(CodeUnknown, "不支持的平台", platform, false, err)
	}

	account, err := s.session.Credentials()
	if err != nil {
		return DownloadResult{}, err
	}

	app, err := s.lookupApp(account, req.AppID, req.BundleID, parsed)
	if err != nil {
		return DownloadResult{}, err
	}

	tmpPath := req.OutputPath + ".tmp"

	// 上一次进程崩溃可能留下并行下载的分片目录，上游 os.Mkdir 会因此直接失败。
	removeStaleParts(tmpPath)

	result := DownloadResult{DestinationPath: req.OutputPath, TmpPath: tmpPath}
	if size, ok := PartialSize(tmpPath); ok {
		result.Resumed = true
		result.BytesAtStart = size
	}

	// 进度条由我们创建并持有：上游只负责往里写，我们从外面轮询读。
	bar := NewProgressBar()

	stopPoll := PollProgressUntil("download", "download", bar, sink)
	defer stopPoll()

	var (
		output   appstore.DownloadOutput
		lastErr  error
		acquired bool
	)

	for attempt := 1; attempt <= maxDownloadAttempts; attempt++ {
		output, lastErr = s.deps.Store.Download(appstore.DownloadInput{
			Context:           ctx,
			Account:           account,
			App:               app,
			OutputPath:        req.OutputPath,
			Progress:          bar,
			ExternalVersionID: req.ExternalVersionID,
			Platform:          parsed,
		})

		if lastErr == nil {
			break
		}

		switch {
		case errors.Is(lastErr, appstore.ErrLicenseRequired) && req.AcquireLicense && !acquired:
			// 与 CLI 的 --purchase 等价：先拿许可，再重试下载。
			if purchaseErr := s.purchaseLocked(account, app, parsed); purchaseErr != nil {
				return result, purchaseErr
			}

			acquired = true
			result.AcquiredLicense = true

			continue

		case errors.Is(lastErr, appstore.ErrPasswordTokenExpired) && attempt < maxDownloadAttempts:
			// 令牌过期（也可能是下载中途过期）：丢弃内存缓存，重新读一次凭据再试。
			s.session.Forget()

			refreshed, refreshErr := s.session.Credentials()
			if refreshErr != nil {
				return result, refreshErr
			}

			account = refreshed

			continue

		default:
			// 注意：这里**不删除** tmpPath。
			return result, s.mapError(lastErr)
		}
	}

	if lastErr != nil {
		return result, s.mapError(lastErr)
	}

	result.DestinationPath = output.DestinationPath
	result.SinfCount = len(output.Sinfs)

	// Apple 的 CDN 包只有加密后的二进制，sinf 是下载响应里单独下发的许可证。
	// CLI 在下载后会调用 replicateDownloadSinf；GUI 也必须做同样的事，
	// 否则产出的 IPA 在「没有现成授权」的设备上安装后会因缺 sinf 无法启动。
	// 上游 fork 的 ReplicateSinf 在 Sinfs 为空时直接返回，不会改动包。
	if err := s.ReplicateSinf(result.DestinationPath, output.Sinfs); err != nil {
		return result, err
	}

	// 收尾快照：让界面看到 100%，而不是停在最后一帧轮询结果上。
	if sink != nil {
		sink(Snapshot("download", "done", bar))
	}

	return result, nil
}

// purchaseLocked 与 Purchase 相同，但复用已经取到的 account/app，避免重复 Lookup。
func (s *Services) purchaseLocked(account appstore.Account, app appstore.App, platform appstore.Platform) error {
	if err := s.deps.Store.Purchase(appstore.PurchaseInput{
		Account:  account,
		App:      app,
		Platform: platform,
	}); err != nil {
		return s.mapError(err)
	}

	return nil
}

// PartialSize 返回续传分片的总大小（.tmp 文件 + 并行下载暂存目录）；都不存在时返回 (0, false)。
//
// 上游 v2.8.1 起，超过 128 MB 的包会走 4 路 Range 并行下载，分片写在
// `<tmp>.ipatool-parts/` 目录里（见 partialPartsSuffix）。只统计 .tmp 会把
// 「已经下了几百 MB」显示成「半成品 0 B」，所以两处都要算。
func PartialSize(tmpPath string) (int64, bool) {
	var total int64

	found := false

	if info, err := os.Stat(tmpPath); err == nil && !info.IsDir() {
		total += info.Size()

		found = true
	}

	if size, ok := partsSize(tmpPath + partialPartsSuffix); ok {
		total += size

		found = true
	}

	return total, found
}

// partsSize 统计并行下载暂存目录里所有分片的大小；目录不存在时返回 (0, false)。
func partsSize(directory string) (int64, bool) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, false
	}

	var total int64

	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil || info.IsDir() {
			continue
		}

		total += info.Size()
	}

	return total, true
}

// ResetPartial 删除续传分片与并行下载暂存目录，让下一次下载从头开始。
//
// 只有用户明确选择「重新下载」时才调用它。任何自动流程都不该删半成品。
func ResetPartial(destinationPath string) error {
	err := os.Remove(destinationPath + ".tmp")
	if err != nil && !os.IsNotExist(err) {
		return MapError(err)
	}

	// 暂存目录是目录不是文件，Remove 会报 ENOTEMPTY；用 RemoveAll，
	// 且删不掉不阻断——上游下一次下载仍会自己尝试重建它的分片。
	_ = os.RemoveAll(destinationPath + ".tmp" + partialPartsSuffix)

	return nil
}

// partialPartsSuffix 是上游并行下载（v2.8.1 起）的暂存目录后缀。
//
// 上游在 `<目标>.tmp` 之外再建 `<目标>.tmp.ipatool-parts/`，把 4 路 Range 的分片
// 写进去，正常结束（成功 / 失败 / 取消）时自己删掉；只有进程被强杀或崩溃才会残留。
// 残留是硬故障：上游用 os.Mkdir 建这个目录，已存在就直接让本次下载失败，
// 所以 Download 开始前必须清一次（见 removeStaleParts）。
const partialPartsSuffix = ".ipatool-parts"

// removeStaleParts 清掉上一次崩溃残留的并行下载暂存目录。
//
// 删除失败不做处理：那属于权限/磁盘问题，交给上游随后的报错给出更具体的上下文。
// 同一时刻只有一次下载在跑（DownloadService 的 sem 容量为 1），不存在误删在用的分片。
func removeStaleParts(tmpPath string) {
	_ = os.RemoveAll(tmpPath + partialPartsSuffix)
}

// ReplicateSinf 把一个已下载的包按 sinf 重写（上游的 ReplicateSinf）。
// Download 成功后会调用它；单独保留这个入口，便于安装前重试或外部流程补写。
func (s *Services) ReplicateSinf(destinationPath string, sinfs []appstore.Sinf) error {
	if len(sinfs) == 0 {
		return nil
	}

	if err := s.deps.Store.ReplicateSinf(appstore.ReplicateSinfInput{
		Sinfs:       sinfs,
		PackagePath: destinationPath,
	}); err != nil {
		return s.mapError(err)
	}

	return nil
}
