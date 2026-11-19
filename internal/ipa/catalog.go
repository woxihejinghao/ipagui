package ipa

import (
	"context"
	"sync"
	"time"

	"github.com/majd/ipatool/v2/pkg/appstore"
)

// Services 是面向上层（Wails 绑定）的业务门面。
//
// 每一层职责：
//   - Deps    : 一次装配（OS / Machine / CookieJar / Keychain / AppStore）
//   - Session : 登录态
//   - Services: 搜索、详情、版本、下载、许可
//
// 所有方法都返回 MapError 归一化过的错误（*AppError），调用方可以安全地
// 直接把它序列化给前端。
type Services struct {
	deps     *Deps
	session  *Session
	metadata *storeMetadataClient
	// owned 缓存整份已购清单（见 owned.go）：上游每次拉一页其实都是全量抓取，
	// 所以「一次拿全 + 缓存」是唯一能真正省掉重复请求的做法。
	owned *ownedCache

	// expiredHandler / expiredMu 见 expired.go：凭据过期时自动登出并通知上层。
	expiredMu      sync.Mutex
	expiredHandler func()
}

func NewServices(deps *Deps) *Services {
	// 配置目录拿不到时给空串：磁盘缓存不可用，只剩进程内缓存（功能不受影响）。
	cacheDir, err := ConfigDir()
	if err != nil {
		cacheDir = ""
	}

	return &Services{
		deps:     deps,
		session:  NewSession(deps),
		metadata: newStoreMetadataClient(),
		owned:    newOwnedCache(cacheDir),
	}
}

func (s *Services) Session() *Session { return s.session }
func (s *Services) Deps() *Deps       { return s.deps }

// PruneStaleAccountCookies 按钥匙串里的当前账号，剪掉 cookie jar 里其它账号的
// 账号 cookie（mz_at*-<dsid> / X-Dsid），返回删除的条目数。
//
// 登录/清凭据时 Session 已经会剪一次；这里是启动时补一刀 —— 让升级前换号留下的
// 旧账号 cookie 立刻失效，而不是等用户重新登录一次才修好。
// 读不到账号（未登录 / 用户主动退出过）时什么都不做。
func (s *Services) PruneStaleAccountCookies() int {
	account, err := s.session.Credentials()
	if err != nil || account.DirectoryServicesID == "" {
		return 0
	}

	removed, _ := pruneAccountCookies(s.deps.Jar, account.DirectoryServicesID)

	return removed
}

// AppView 是给界面的应用视图。
//
// 为什么不让前端直接用 appstore.App：上游结构体会随版本变（字段增删/改名），
// 而且它的字段语义（比如 PurchaseDate 的零值）不适合直接展示。这里做一层稳定 DTO，
// 前端只依赖我们自己的契约。
type AppView struct {
	ID          int64    `json:"id"`
	BundleID    string   `json:"bundleId"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Price       float64  `json:"price"`
	Free        bool     `json:"free"`
	PurchasedAt string   `json:"purchasedAt,omitempty"`
	Platforms   []string `json:"platforms"`

	// ArtworkURL / FileSizeBytes / UpdatedAt 来自 Apple 的公开目录接口（见 artwork.go），
	// 上游的解码结构体没有这几项。取不到时保持零值，界面必须当作「没有」处理。
	//
	// 注意 FileSizeBytes 是**当前商店版本**的体积：历史版本的体积 Apple 不给，
	// 所以只有「下载最新」能提前告诉用户要下多大。
	//
	// UpdatedAt 是**当前商店版本**的发布/更新时间（RFC3339），语义与 Version 配套：
	// Version 说「现在是哪个版本」，UpdatedAt 说「这个版本什么时候上线的」。
	ArtworkURL    string `json:"artworkUrl,omitempty"`
	FileSizeBytes int64  `json:"fileSizeBytes,omitempty"`
	UpdatedAt     string `json:"updatedAt,omitempty"`
}

func toAppView(app appstore.App) AppView {
	view := AppView{
		ID:       app.ID,
		BundleID: app.BundleID,
		Name:     app.Name,
		Version:  app.Version,
		Price:    app.Price,
		Free:     app.Price == 0,
	}

	if !app.PurchaseDate.IsZero() {
		view.PurchasedAt = app.PurchaseDate.Format(time.RFC3339)
	}

	for _, platform := range app.Platforms {
		view.Platforms = append(view.Platforms, string(platform))
	}

	return view
}

// SearchResult 是搜索结果。
type SearchResult struct {
	Term  string    `json:"term"`
	Count int       `json:"count"`
	Apps  []AppView `json:"apps"`
}

// Search 搜索 App Store。与 CLI 一致：必须已登录（上游用账号的 storefront 推国家码）。
func (s *Services) Search(ctx context.Context, term string, limit int64, platform string) (SearchResult, error) {
	if term == "" {
		return SearchResult{}, appErr(CodeUnknown, "请输入搜索关键词", "", false, nil)
	}

	account, err := s.session.Credentials()
	if err != nil {
		return SearchResult{}, err
	}

	parsed, err := ParsePlatform(platform)
	if err != nil {
		return SearchResult{}, appErr(CodeUnknown, "不支持的平台", platform, false, err)
	}

	if limit <= 0 {
		limit = 5
	}

	output, err := s.deps.Store.Search(appstore.SearchInput{
		Account:  account,
		Term:     term,
		Limit:    limit,
		Platform: parsed,
	})
	if err != nil {
		return SearchResult{}, s.mapError(err)
	}

	// Apps 必须是非 nil 的切片：nil 会被 encoding/json 序列化成 "apps": null，
	// 前端拿到 null 后一旦按数组用（.length/.map）就会抛异常、整棵 React 树卸载成黑屏。
	result := SearchResult{Term: term, Count: output.Count, Apps: make([]AppView, 0, len(output.Results))}

	for _, app := range output.Results {
		result.Apps = append(result.Apps, toAppView(app))
	}

	result.Apps = s.enrichAppViews(ctx, account.StoreFront, result.Apps)

	return result, nil
}

// Lookup 按 App ID 或 Bundle ID 查应用详情。
func (s *Services) Lookup(ctx context.Context, appID int64, bundleID string, platform string) (AppView, error) {
	account, err := s.session.Credentials()
	if err != nil {
		return AppView{}, err
	}

	parsed, err := ParsePlatform(platform)
	if err != nil {
		return AppView{}, appErr(CodeUnknown, "不支持的平台", platform, false, err)
	}

	app, err := s.lookupApp(account, appID, bundleID, parsed)
	if err != nil {
		return AppView{}, err
	}

	return s.enrichAppView(ctx, account.StoreFront, toAppView(app)), nil
}

// VersionList 是某应用可下载的历史版本。
type VersionList struct {
	App                AppView  `json:"app"`
	LatestVersionID    string   `json:"latestVersionId"`
	VersionIdentifiers []string `json:"versionIdentifiers"`
}

// ListVersions 列出版本（对应 CLI 的 list-versions）。
func (s *Services) ListVersions(ctx context.Context, appID int64, bundleID string, platform string) (VersionList, error) {
	account, err := s.session.Credentials()
	if err != nil {
		return VersionList{}, err
	}

	parsed, err := ParsePlatform(platform)
	if err != nil {
		return VersionList{}, appErr(CodeUnknown, "不支持的平台", platform, false, err)
	}

	app, err := s.lookupApp(account, appID, bundleID, parsed)
	if err != nil {
		return VersionList{}, err
	}

	output, err := s.deps.Store.ListVersions(appstore.ListVersionsInput{
		Context:  ctx,
		Account:  account,
		App:      app,
		Platform: parsed,
	})
	if err != nil {
		return VersionList{}, s.mapError(err)
	}

	return VersionList{
		App:                s.enrichAppView(ctx, account.StoreFront, toAppView(app)),
		LatestVersionID:    output.LatestExternalVersionID,
		VersionIdentifiers: output.ExternalVersionIdentifiers,
	}, nil
}

// VersionMetadata 是某个历史版本的元数据。
type VersionMetadata struct {
	DisplayVersion string    `json:"displayVersion"`
	ReleaseDate    time.Time `json:"releaseDate"`
}

// GetVersionMetadata 读取指定版本的显示版本号与发布时间。
func (s *Services) GetVersionMetadata(ctx context.Context, appID int64, bundleID, versionID, platform string) (VersionMetadata, error) {
	if versionID == "" {
		return VersionMetadata{}, appErr(CodeUnknown, "缺少版本 ID", "请先从版本列表中选择一个版本", false, nil)
	}

	account, err := s.session.Credentials()
	if err != nil {
		return VersionMetadata{}, err
	}

	parsed, err := ParsePlatform(platform)
	if err != nil {
		return VersionMetadata{}, appErr(CodeUnknown, "不支持的平台", platform, false, err)
	}

	app, err := s.lookupApp(account, appID, bundleID, parsed)
	if err != nil {
		return VersionMetadata{}, err
	}

	output, err := s.deps.Store.GetVersionMetadata(appstore.GetVersionMetadataInput{
		Context:   ctx,
		Account:   account,
		App:       app,
		VersionID: versionID,
		Platform:  parsed,
	})
	if err != nil {
		return VersionMetadata{}, s.mapError(err)
	}

	return VersionMetadata{
		DisplayVersion: output.DisplayVersion,
		ReleaseDate:    output.ReleaseDate,
	}, nil
}

// OwnedAppsPage 是「已购应用」的一页。
type OwnedAppsPage struct {
	Page       int       `json:"page"`
	Count      int       `json:"count"`
	TotalCount int       `json:"totalCount"`
	Apps       []AppView `json:"apps"`
}

// ListOwnedApps 列出账号已购应用（对应 CLI 的 list-purchases）。
func (s *Services) ListOwnedApps(ctx context.Context, page, limit int, platform string) (OwnedAppsPage, error) {
	account, err := s.session.Credentials()
	if err != nil {
		return OwnedAppsPage{}, err
	}

	parsed, err := ParsePlatform(platform)
	if err != nil {
		return OwnedAppsPage{}, appErr(CodeUnknown, "不支持的平台", platform, false, err)
	}

	if page <= 0 {
		page = 1
	}

	if limit <= 0 {
		limit = appstore.DefaultOwnedAppsLimit
	}

	output, err := s.deps.Store.OwnedApps(appstore.OwnedAppsInput{
		Account:  account,
		Page:     page,
		Limit:    limit,
		Platform: parsed,
	})
	if err != nil {
		return OwnedAppsPage{}, s.mapError(err)
	}

	// 同 Search：空账号时不能让 Apps 变成 null（前端按数组用）。
	result := OwnedAppsPage{
		Page:       output.Page,
		Count:      output.Count,
		TotalCount: output.TotalCount,
		Apps:       make([]AppView, 0, len(output.Results)),
	}

	for _, app := range output.Results {
		result.Apps = append(result.Apps, toAppView(app))
	}

	result.Apps = s.enrichAppViews(ctx, account.StoreFront, result.Apps)

	return result, nil
}

// Purchase 获取应用许可。上游只支持免费应用。
func (s *Services) Purchase(ctx context.Context, appID int64, bundleID string, platform string) error {
	account, err := s.session.Credentials()
	if err != nil {
		return err
	}

	parsed, err := ParsePlatform(platform)
	if err != nil {
		return appErr(CodeUnknown, "不支持的平台", platform, false, err)
	}

	app, err := s.lookupApp(account, appID, bundleID, parsed)
	if err != nil {
		return err
	}

	if err := s.deps.Store.Purchase(appstore.PurchaseInput{
		Account:  account,
		App:      app,
		Platform: parsed,
	}); err != nil {
		return s.mapError(err)
	}

	return nil
}

// lookupApp 是 Download / Purchase / ListVersions 的共同前置：拿不到 App
// （至少要有 ID、BundleID、Name）就无法继续。
func (s *Services) lookupApp(account appstore.Account, appID int64, bundleID string, platform appstore.Platform) (appstore.App, error) {
	if appID == 0 && bundleID == "" {
		return appstore.App{}, appErr(CodeUnknown, "缺少应用标识",
			"请提供 App ID 或 Bundle ID", false, nil)
	}

	output, err := s.deps.Store.Lookup(appstore.LookupInput{
		Account:  account,
		AppID:    appID,
		BundleID: bundleID,
		Platform: platform,
	})
	if err != nil {
		return appstore.App{}, s.mapError(err)
	}

	return output.App, nil
}
