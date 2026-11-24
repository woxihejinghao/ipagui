package services

import (
	"github.com/milan/ipagui/internal/ipa"
)

// CatalogService 是搜索/详情/版本的绑定层。
type CatalogService struct {
	runtime *Runtime
}

// NewCatalogService 构造目录服务。
func NewCatalogService(runtimeInstance *Runtime) *CatalogService {
	return &CatalogService{runtime: runtimeInstance}
}

// Search 在 App Store 里搜索。limit <= 0 时用上游默认值（5）。
func (s *CatalogService) Search(term string, limit int64, platform string) (ipa.SearchResult, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return ipa.SearchResult{}, err
	}

	return s.runtime.Catalog().Search(s.runtime.CallContext(), term, limit, platform)
}

// Lookup 按 app id 或 bundle id 取单个应用。
func (s *CatalogService) Lookup(appID int64, bundleID, platform string) (ipa.AppView, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return ipa.AppView{}, err
	}

	return s.runtime.Catalog().Lookup(s.runtime.CallContext(), appID, bundleID, platform)
}

// ListVersions 列出应用可下载的历史版本。
func (s *CatalogService) ListVersions(appID int64, bundleID, platform string) (ipa.VersionList, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return ipa.VersionList{}, err
	}

	return s.runtime.Catalog().ListVersions(s.runtime.CallContext(), appID, bundleID, platform)
}

// GetVersionMetadata 取某个外部版本号的显示版本与发布时间。
func (s *CatalogService) GetVersionMetadata(appID int64, bundleID, versionID, platform string) (ipa.VersionMetadata, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return ipa.VersionMetadata{}, err
	}

	return s.runtime.Catalog().GetVersionMetadata(s.runtime.CallContext(), appID, bundleID, versionID, platform)
}

// Purchase 获取应用许可（把 App「买」进账号）。
//
// 上游的购买接口只支持免费应用：付费应用在这里会失败，错误文本已经由 ipa 层
// 翻译成人话（见 errormap），前端拿到后直接展示并引导去 App Store —— 所以这个方法
// 对付费应用不做拦截，让「失败原因」由 Apple 那边说话。
func (s *CatalogService) Purchase(appID int64, bundleID, platform string) error {
	if err := s.runtime.EnsureReady(); err != nil {
		return err
	}

	return s.runtime.Catalog().Purchase(s.runtime.CallContext(), appID, bundleID, platform)
}

// ListPurchases 列出账号已购应用（page 从 1 开始，limit <= 0 用上游默认值）。
//
// 注意：上游每取一页都会把整份购买历史重拉一遍，界面**不要**用它翻页拼全量 ——
// 用 ListAllPurchases。
func (s *CatalogService) ListPurchases(page, limit int, platform string) (ipa.OwnedAppsPage, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return ipa.OwnedAppsPage{}, err
	}

	return s.runtime.Catalog().ListOwnedApps(s.runtime.CallContext(), page, limit, platform)
}

// ListAllPurchases 一次返回整个已购清单（后端缓存 + 后台刷新）。
//
// force=false：有缓存就立刻返回（Cached=true）；旧缓存会先返回，同时起后台刷新，
// 拉到之后以 purchases:updated 事件推一份新的上来。
// force=true：跳过缓存，同步等 Apple（用户点「刷新」）。
func (s *CatalogService) ListAllPurchases(platform string, force bool) (ipa.OwnedAppsList, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return ipa.OwnedAppsList{}, err
	}

	return s.runtime.Catalog().ListAllOwnedApps(s.runtime.CallContext(), platform, ipa.OwnedAppsOptions{
		Force: force,
		OnRefresh: func(list ipa.OwnedAppsList) {
			// 后台 goroutine 里回调：Runtime.Emit 只做一次 EventsEmit，可以这么用
			// （下载进度已经这么发）。没有窗口上下文时它自己会静默跳过。
			s.runtime.Emit("purchases:updated", list)
		},
	})
}
