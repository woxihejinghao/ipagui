package ipa

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/majd/ipatool/v2/pkg/appstore"
)

// fakeOwnedStore 只实现 OwnedApps：其余方法靠嵌入的 nil 接口兜住（被调用就会 panic，
// 正好证明「除了列已购之外不该碰别的东西」）。
type fakeOwnedStore struct {
	appstore.AppStore

	calls atomic.Int32
	// perPage 是「整份购买历史」，fake 自己按 page/limit 切片，行为与上游一致：
	// 每次调用都完整返回一页，而真实上游每次都会把整份历史重拉一遍。
	perPage  []appstore.App
	fail     bool
	failPage int // 只让这一页失败（0 表示不按页失败）
}

func (f *fakeOwnedStore) OwnedApps(input appstore.OwnedAppsInput) (appstore.OwnedAppsOutput, error) {
	f.calls.Add(1)

	if f.fail || (f.failPage > 0 && input.Page == f.failPage) {
		return appstore.OwnedAppsOutput{}, errors.New("apple 抽风了")
	}

	start := (input.Page - 1) * input.Limit
	if start > len(f.perPage) {
		start = len(f.perPage)
	}

	end := min(start+input.Limit, len(f.perPage))
	slice := f.perPage[start:end]

	return appstore.OwnedAppsOutput{
		Page:       input.Page,
		Count:      len(slice),
		TotalCount: len(f.perPage),
		Results:    slice,
	}, nil
}

func fakeOwnedApps(count int) []appstore.App {
	apps := make([]appstore.App, 0, count)
	for index := range count {
		apps = append(apps, appstore.App{
			ID:       int64(index + 1),
			BundleID: fmt.Sprintf("com.example.app%d", index+1),
			Name:     fmt.Sprintf("App %d", index+1),
			Version:  "1.0.0",
		})
	}

	return apps
}

// newOwnedTestService 直接拼一个 Services：不走 NewServices，免得碰真实的
// 状态目录/钥匙串；元数据客户端指向一个关掉的本地端口，让补图标那段立刻失败
// （enrichAppViews 故意不报错，这里正好利用这一点离线跑）。
func newOwnedTestService(store appstore.AppStore, accountEmail, cacheDir string) *Services {
	return &Services{
		deps:    &Deps{Store: store},
		session: &Session{account: &appstore.Account{Email: accountEmail, StoreFront: "143524-2,34"}},
		metadata: &storeMetadataClient{
			baseURL: "http://127.0.0.1:9",
			client:  &http.Client{Timeout: time.Second},
		},
		owned: newOwnedCache(cacheDir),
	}
}

func TestListAllOwnedAppsCachesWholeList(t *testing.T) {
	store := &fakeOwnedStore{perPage: fakeOwnedApps(55)}
	services := newOwnedTestService(store, "user@example.com", t.TempDir())
	ctx := context.Background()

	first, err := services.ListAllOwnedApps(ctx, "iphone", OwnedAppsOptions{})
	if err != nil {
		t.Fatalf("首次拉取失败: %v", err)
	}

	// 55 个 App 只该问一次 Apple：上游每页都是全量抓取，多问一次就是白等 3s。
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("55 个 App 应该只问一次 Apple，实际 %d 次", got)
	}

	if len(first.Apps) != 55 || first.TotalCount != 55 {
		t.Fatalf("清单数量 = %d / totalCount = %d，期望 55", len(first.Apps), first.TotalCount)
	}

	if first.Cached {
		t.Fatal("首次拉取不该标成缓存")
	}

	second, err := services.ListAllOwnedApps(ctx, "iphone", OwnedAppsOptions{})
	if err != nil {
		t.Fatalf("第二次拉取失败: %v", err)
	}

	if got := store.calls.Load(); got != 1 {
		t.Fatalf("缓存命中时不该再问 Apple，实际 %d 次", got)
	}

	if !second.Cached || len(second.Apps) != 55 {
		t.Fatalf("第二次应该秒回缓存，Cached = %v，数量 = %d", second.Cached, len(second.Apps))
	}

	// 用户点「刷新」：这次必须真的再问一次。
	if _, err := services.ListAllOwnedApps(ctx, "iphone", OwnedAppsOptions{Force: true}); err != nil {
		t.Fatalf("强制刷新失败: %v", err)
	}

	if got := store.calls.Load(); got != 2 {
		t.Fatalf("强制刷新应该再问一次 Apple，实际累计 %d 次", got)
	}
}

func TestListAllOwnedAppsPagesOverLimit(t *testing.T) {
	store := &fakeOwnedStore{perPage: fakeOwnedApps(150)}
	services := newOwnedTestService(store, "user@example.com", t.TempDir())

	list, err := services.ListAllOwnedApps(context.Background(), "iphone", OwnedAppsOptions{})
	if err != nil {
		t.Fatalf("拉取失败: %v", err)
	}

	if len(list.Apps) != 150 {
		t.Fatalf("应该拿满 150 个，实际 %d 个", len(list.Apps))
	}

	// 150 个 / 页长 100 = 2 页；页长没拉满（比如按 10 一页）就会变成 15 次全量抓取。
	if got := store.calls.Load(); got != 2 {
		t.Fatalf("150 个 App 应该只要 2 页，实际 %d 次", got)
	}
}

func TestListAllOwnedAppsFailsWholeWhenAPageFails(t *testing.T) {
	store := &fakeOwnedStore{perPage: fakeOwnedApps(150), failPage: 2}
	services := newOwnedTestService(store, "user@example.com", t.TempDir())

	// 半份清单不能当完整清单用：第二页失败就整体失败，界面据此显示「没拉到」，
	// 而不是把后 50 个应用判成「未购买」。
	if _, err := services.ListAllOwnedApps(context.Background(), "iphone", OwnedAppsOptions{}); err == nil {
		t.Fatal("有页失败时应该整体失败")
	}
}

func TestListAllOwnedAppsKeepsOldListWhenRefreshFails(t *testing.T) {
	store := &fakeOwnedStore{perPage: fakeOwnedApps(55)}
	services := newOwnedTestService(store, "user@example.com", t.TempDir())
	ctx := context.Background()

	if _, err := services.ListAllOwnedApps(ctx, "iphone", OwnedAppsOptions{}); err != nil {
		t.Fatalf("首次拉取失败: %v", err)
	}

	store.fail = true

	list, err := services.ListAllOwnedApps(ctx, "iphone", OwnedAppsOptions{Force: true})
	if err != nil {
		t.Fatalf("有旧清单时不该整体失败: %v", err)
	}

	if list.Warning == "" {
		t.Fatal("刷新失败必须带 Warning，否则界面会拿旧数据当刚拉到的")
	}

	if len(list.Apps) != 55 || !list.Cached {
		t.Fatalf("应该回退到旧清单，数量 = %d，Cached = %v", len(list.Apps), list.Cached)
	}
}

func TestListAllOwnedAppsUsesDiskCacheAfterRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	before := &fakeOwnedStore{perPage: fakeOwnedApps(3)}
	if _, err := newOwnedTestService(before, "user@example.com", dir).ListAllOwnedApps(ctx, "iphone", OwnedAppsOptions{}); err != nil {
		t.Fatalf("首次拉取失败: %v", err)
	}

	// 「重启」：新缓存实例、同一磁盘目录、新 store 计数器。
	after := &fakeOwnedStore{perPage: fakeOwnedApps(3)}
	list, err := newOwnedTestService(after, "user@example.com", dir).ListAllOwnedApps(ctx, "iphone", OwnedAppsOptions{})
	if err != nil {
		t.Fatalf("重启后拉取失败: %v", err)
	}

	if got := after.calls.Load(); got != 0 {
		t.Fatalf("重启后应该先吃磁盘缓存，实际问了 Apple %d 次", got)
	}

	if !list.Cached || len(list.Apps) != 3 {
		t.Fatalf("磁盘缓存没生效，Cached = %v，数量 = %d", list.Cached, len(list.Apps))
	}
}

func TestListAllOwnedAppsIgnoresOtherAccountsCache(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	if _, err := newOwnedTestService(&fakeOwnedStore{perPage: fakeOwnedApps(3)}, "a@example.com", dir).
		ListAllOwnedApps(ctx, "iphone", OwnedAppsOptions{}); err != nil {
		t.Fatalf("首次拉取失败: %v", err)
	}

	// 换个账号：上一个账号的磁盘缓存绝不能被当成这个账号的清单。
	otherStore := &fakeOwnedStore{perPage: fakeOwnedApps(5)}
	other, err := newOwnedTestService(otherStore, "b@example.com", dir).
		ListAllOwnedApps(ctx, "iphone", OwnedAppsOptions{})
	if err != nil {
		t.Fatalf("换号后拉取失败: %v", err)
	}

	if got := otherStore.calls.Load(); got != 1 {
		t.Fatalf("换号后必须重新问 Apple，实际 %d 次", got)
	}

	if len(other.Apps) != 5 {
		t.Fatalf("换号后应该拿到新账号的 5 个 App，实际 %d 个", len(other.Apps))
	}
}

func TestListAllOwnedAppsPlatformIsolation(t *testing.T) {
	store := &fakeOwnedStore{perPage: fakeOwnedApps(4)}
	services := newOwnedTestService(store, "user@example.com", t.TempDir())
	ctx := context.Background()

	if _, err := services.ListAllOwnedApps(ctx, "iphone", OwnedAppsOptions{}); err != nil {
		t.Fatalf("iPhone 拉取失败: %v", err)
	}

	ipad, err := services.ListAllOwnedApps(ctx, "ipad", OwnedAppsOptions{})
	if err != nil {
		t.Fatalf("iPad 拉取失败: %v", err)
	}

	// 平台之间不能共用缓存：iPad 清单是另一份数据。
	if got := store.calls.Load(); got != 2 {
		t.Fatalf("不同平台应该各问一次 Apple，实际 %d 次", got)
	}

	if ipad.Platform != "ipad" {
		t.Fatalf("Platform = %q，期望 ipad", ipad.Platform)
	}
}

// TestInvalidateOwnedDropsMemoryAndDiskCache 盯住「切号后必须整份重拉」：
// 作废后同一账号再拉一次，内存与磁盘缓存都不能再命中。
func TestInvalidateOwnedDropsMemoryAndDiskCache(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store := &fakeOwnedStore{perPage: fakeOwnedApps(3)}
	services := newOwnedTestService(store, "user@example.com", dir)

	if _, err := services.ListAllOwnedApps(ctx, "iphone", OwnedAppsOptions{}); err != nil {
		t.Fatalf("首次拉取失败: %v", err)
	}

	// 账号没变但缓存被显式作废（重新登录同一账号也是这种情形）——
	// 这时唯一正确的行为是重新问 Apple，而不是把旧清单再拿出来。
	services.InvalidateOwned()

	// 磁盘那份也删了：在重新拉取之前检查，否则会被本次的新结果写回来。
	paths, err := filepath.Glob(filepath.Join(dir, "owned-*.json"))
	if err != nil {
		t.Fatalf("列磁盘缓存失败: %v", err)
	}

	if len(paths) != 0 {
		t.Fatalf("作废后磁盘缓存应该清空，实际还剩 %v", paths)
	}

	list, err := services.ListAllOwnedApps(ctx, "iphone", OwnedAppsOptions{})
	if err != nil {
		t.Fatalf("作废后拉取失败: %v", err)
	}

	if got := store.calls.Load(); got != 2 {
		t.Fatalf("作废后必须重新问 Apple，实际 %d 次", got)
	}

	if list.Cached {
		t.Fatal("作废后的结果不应该标成缓存命中")
	}
}
