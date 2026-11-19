package ipa

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/majd/ipatool/v2/pkg/appstore"
)

const (
	// ownedPageSize 取上游允许的最大页长（100）。
	//
	// 上游的 OwnedApps 每调用一次都会把**整份**购买历史重拉一遍，再在内存里切
	// 出请求的那一页（pkg/appstore/appstore_owned_apps.go）。所以这里能少一页就
	// 少一页：页长拉满，免得为了 101 个 App 问两遍 Apple。
	ownedPageSize = appstore.MaxOwnedAppsLimit

	// ownedMaxPages 是防御上限：totalCount 撒谎时不至于转到天荒地老。
	ownedMaxPages = 20

	// ownedPageConcurrency 是并发拉页的上限。
	//
	// 上游每取一页都会把整份购买历史重拉一遍，所以串行翻页等于把同一份历史重拉 N 遍。
	// 实测（359 个已购，4 页）：串行 28.3s，4 并发 5.3–6.5s，两次结果完全一致，
	// 连跑 4 次没有出现串会话或掉登录。取 3：常见库（<=300 个）两轮就拉完，
	// 又不至于同时向 DAAP 打太多请求。
	ownedPageConcurrency = 3

	// ownedEnrichChunk 是一次 /lookup 带的 id 数。整份清单可能有上千个 App，
	// 一条超长 URL 会被 Apple 拒掉（图标只是装饰，但不能因为 URL 太长就整批丢）。
	ownedEnrichChunk = 100

	// ownedFreshFor 是「这份清单还算新」的窗口：窗口内直接返回，不问 Apple。
	// 90s 足够覆盖「切页签 / 回来再看一眼」这类重复动作，又不至于让刚买的应用
	// 等太久才出现在搜索页。
	ownedFreshFor = 90 * time.Second

	// ownedDiskMaxAge 是磁盘缓存最长可用年龄。超过就当没有：拿两周前的清单判断
	// 「有没有买过」和拿不到清单一样危险（会把人引到错误的下载决策上）。
	ownedDiskMaxAge = 14 * 24 * time.Hour

	// ownedCacheVersion 变了就作废旧磁盘缓存（Apps 结构变动时改它）。
	ownedCacheVersion = 1
)

// OwnedAppsList 是一份**完整**的已购清单（不是一个分页）。
type OwnedAppsList struct {
	Platform   string    `json:"platform"`
	TotalCount int       `json:"totalCount"`
	FetchedAt  string    `json:"fetchedAt"`
	Apps       []AppView `json:"apps"`

	// Cached 为 true 表示这次没有问 Apple（进程内或磁盘缓存命中），数据可能不新。
	Cached bool `json:"cached"`

	// Refreshing 为 true 表示后台正在重拉：拉到（或拉失败）之后会以
	// purchases:updated 事件推一份新的给界面。
	Refreshing bool `json:"refreshing"`

	// Warning 非空表示「你看到的是旧数据」的原因（刷新失败等）。
	// 界面必须显式说出来 —— 旧清单和「没买过」是两件事。
	Warning string `json:"warning,omitempty"`
}

// OwnedAppsOptions 控制一次 ListAllOwnedApps。
type OwnedAppsOptions struct {
	// Force 为 true 时跳过缓存，同步等 Apple（用户点「刷新」）。
	Force bool
	// OnRefresh 在后台刷新结束后回调一次：成功给新清单，失败给旧清单 + Warning。
	// 它跑在后台 goroutine 里，调用方不要在里面碰界面对象（绑定层用它广播事件）。
	OnRefresh func(OwnedAppsList)
}

// ListAllOwnedApps 取整个已购清单。
//
// 为什么不让前端翻页：上游 OwnedApps 每次调用都会把整份购买历史重拉一遍（两个
// storefront 各自 login/update/items，实测 ~3.1s），前端翻 N 页就是 N 次全量抓取。
// 这里改成「一次拿全 + 缓存」：
//   - 90s 内重复调用直接返回内存里的那份，不再问 Apple；
//   - 更旧的缓存先立刻返回（界面秒开），同时起一个后台刷新，拉到了用
//     purchases:updated 推给界面；
//   - 进程重启后先拿磁盘缓存（同样秒开），再按上面的规则后台补。
//
// 诚实边界：缓存意味着「刚买的应用最多晚几秒出现」。所以界面上任何「未购买」
// 结论都必须来自这份**完整**清单，而这份清单在任何一页失败时整体失败（不会拿
// 半份清单冒充完整清单）。
func (s *Services) ListAllOwnedApps(ctx context.Context, platform string, opts OwnedAppsOptions) (OwnedAppsList, error) {
	account, err := s.session.Credentials()
	if err != nil {
		return OwnedAppsList{}, err
	}

	parsed, err := ParsePlatform(platform)
	if err != nil {
		return OwnedAppsList{}, appErr(CodeUnknown, "不支持的平台", platform, false, err)
	}

	key := string(parsed)
	slot := s.owned.slot(key)
	slot.warmUp(s.owned.dir, key, account.Email)

	// 不强制刷新：有缓存就先给界面，旧了再在后台补。
	if !opts.Force {
		if list, at, ok := slot.snapshot(key, account.Email); ok {
			list.Cached = true
			if time.Since(at) > ownedFreshFor {
				list.Refreshing = s.refreshOwnedAsync(ctx, slot, key, account, parsed, opts.OnRefresh)
			}

			return list, nil
		}
	}

	// 记下开始排队的时刻：如果等锁的这段时间里后台刷新刚好落地（缓存比这个时刻新），
	// 那就不用再问一遍 Apple —— 用户想要的就是「更新之后的结果」。
	waitStart := time.Now()

	slot.fetchMu.Lock()
	defer slot.fetchMu.Unlock()

	if list, at, ok := slot.snapshot(key, account.Email); ok && !at.Before(waitStart) {
		list.Cached = true

		return list, nil
	}

	list, err := s.fetchOwnedList(ctx, account, parsed)
	if err != nil {
		// 拉失败时把旧清单连同原因一起返回：界面不能把「不知道」当成「没买过」。
		if cached, _, ok := slot.snapshot(key, account.Email); ok {
			cached.Cached = true
			cached.Warning = "重新拉取已购列表失败，这是上一次的清单：" + err.Error()

			return cached, nil
		}

		return OwnedAppsList{}, err
	}

	slot.update(key, account.Email, list, time.Now())
	saveOwnedToDisk(s.owned.dir, key, account.Email, list)

	return list, nil
}

// InvalidateOwned 作废所有平台的已购缓存（内存 + 磁盘），并让在途的后台刷新作废。
//
// 用在登录 / 退出 / 清凭据之后：缓存里的「已购」属于上一个账号，换号后必须整份
// 重拉，否则界面会把上个账号的购买记录当成本账号的 —— 那会直接误导下载决策。
func (s *Services) InvalidateOwned() { s.owned.invalidate() }

// fetchOwnedList 把整份清单拉下来（含图标/体积补全）。
//
// 任何一页失败都整体失败：半份清单不能当完整清单用，否则搜索页会把没拉到的
// 应用判成「未购买」—— 那正是 docs/architecture.md §8.5 记录过的坑。
func (s *Services) fetchOwnedList(ctx context.Context, account appstore.Account, platform appstore.Platform) (OwnedAppsList, error) {
	// 第 1 页先单独拉：只有它知道 totalCount，后面的页数得先算出来。
	first, err := s.fetchOwnedPage(ctx, account, platform, 1)
	if err != nil {
		return OwnedAppsList{}, err
	}

	pageCount := 1
	if first.TotalCount > 0 {
		pageCount = (first.TotalCount + ownedPageSize - 1) / ownedPageSize
		if pageCount > ownedMaxPages {
			pageCount = ownedMaxPages
		}
	}

	// 按页号存结果，最后按页序拼起来 —— 并发跑也得保持上游的排序语义。
	views := make([][]AppView, pageCount)
	views[0] = toAppViews(first.Results)

	if pageCount > 1 {
		err := parallelFor(ctx, pageCount-1, ownedPageConcurrency, func(index int) error {
			page := index + 2

			output, err := s.fetchOwnedPage(ctx, account, platform, page)
			if err != nil {
				return err
			}

			views[page-1] = toAppViews(output.Results)

			return nil
		})
		if err != nil {
			return OwnedAppsList{}, err
		}
	}

	apps := make([]AppView, 0, first.TotalCount)
	for _, chunk := range views {
		apps = append(apps, chunk...)
	}

	// 并发分页时 totalCount 若在拉取途中变了，相邻页会重叠：去重后再交给界面。
	apps = dedupeAppViews(apps)

	// 补图标/体积也分批（一条 /lookup 带太多 id 会被 Apple 拒掉），批与批之间并发。
	chunks := (len(apps) + ownedEnrichChunk - 1) / ownedEnrichChunk
	if chunks > 0 {
		_ = parallelFor(ctx, chunks, ownedPageConcurrency, func(index int) error {
			start := index * ownedEnrichChunk
			end := min(start+ownedEnrichChunk, len(apps))
			copy(apps[start:end], s.enrichAppViews(ctx, account.StoreFront, apps[start:end]))

			return nil
		})
	}

	return OwnedAppsList{
		Platform:   string(platform),
		TotalCount: first.TotalCount,
		FetchedAt:  time.Now().Format(time.RFC3339),
		Apps:       apps,
	}, nil
}

func (s *Services) fetchOwnedPage(
	ctx context.Context,
	account appstore.Account,
	platform appstore.Platform,
	page int,
) (appstore.OwnedAppsOutput, error) {
	output, err := s.deps.Store.OwnedApps(appstore.OwnedAppsInput{
		Account:  account,
		Page:     page,
		Limit:    ownedPageSize,
		Platform: platform,
	})
	if err != nil {
		return appstore.OwnedAppsOutput{}, s.mapError(err)
	}

	return output, nil
}

func toAppViews(apps []appstore.App) []AppView {
	views := make([]AppView, 0, len(apps))
	for _, app := range apps {
		views = append(views, toAppView(app))
	}

	return views
}

// dedupeAppViews 按 app id 去重，保持首次出现的顺序。
func dedupeAppViews(apps []AppView) []AppView {
	seen := make(map[int64]struct{}, len(apps))
	unique := apps[:0]

	for _, app := range apps {
		if _, ok := seen[app.ID]; ok {
			continue
		}

		seen[app.ID] = struct{}{}
		unique = append(unique, app)
	}

	return unique
}

// parallelFor 以并发上限 concurrency 跑 count 个任务（索引 0..count-1）。
//
// 任一任务报错就整体失败（其余已在跑的不管：它们的调用方马上会拿到错误）。
func parallelFor(ctx context.Context, count, concurrency int, fn func(index int) error) error {
	if count <= 0 {
		return nil
	}

	if concurrency <= 0 {
		concurrency = 1
	}

	tokens := make(chan struct{}, concurrency)
	errs := make(chan error, count)

	var wg sync.WaitGroup

	for index := range count {
		if err := ctx.Err(); err != nil {
			return MapError(err)
		}

		wg.Add(1)

		go func(index int) {
			defer wg.Done()

			tokens <- struct{}{}
			defer func() { <-tokens }()

			if err := fn(index); err != nil {
				errs <- err
			}
		}(index)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			return err
		}
	}

	return nil
}

// refreshOwnedAsync 起一次后台刷新（同一平台同时只跑一个）。返回「现在是否在刷」。
func (s *Services) refreshOwnedAsync(
	ctx context.Context,
	slot *ownedSlot,
	key string,
	account appstore.Account,
	platform appstore.Platform,
	onRefresh func(OwnedAppsList),
) bool {
	if !slot.beginRefresh() {
		return true
	}

	// 记下开始刷新时的 generation：拉取途中若用户换了账号（InvalidateOwned 会自增），
	// 这次结果就属于上一个账号，不能再写缓存、也不能再推给界面。
	gen := s.owned.generation()

	go func() {
		defer slot.endRefresh()

		slot.fetchMu.Lock()
		defer slot.fetchMu.Unlock()

		list, err := s.fetchOwnedList(ctx, account, platform)
		if !s.owned.current(gen) {
			return
		}

		if err != nil {
			cached, _, ok := slot.snapshot(key, account.Email)
			if !ok {
				return
			}

			cached.Cached = true
			cached.Warning = "后台刷新已购列表失败，这是上一次的清单：" + err.Error()

			if onRefresh != nil {
				onRefresh(cached)
			}

			return
		}

		slot.update(key, account.Email, list, time.Now())
		saveOwnedToDisk(s.owned.dir, key, account.Email, list)

		if onRefresh != nil {
			onRefresh(list)
		}
	}()

	return true
}

// ownedCache 按平台缓存整份已购清单。
//
// 只按平台分槽、再按账号校验：换号后旧槽里的数据会因为 account 不匹配而作废，
// 不会把上一个账号的购买记录当成这个账号的。
type ownedCache struct {
	// dir 是磁盘缓存目录；空串表示不落盘（配置目录拿不到时）。
	dir string

	mu    sync.Mutex
	slots map[string]*ownedSlot

	// gen 在每次 invalidate 时自增：在途的后台刷新据此判断「我的结果是不是已经过期」。
	gen uint64
}

func newOwnedCache(dir string) *ownedCache {
	return &ownedCache{dir: dir, slots: make(map[string]*ownedSlot)}
}

// invalidate 作废所有平台的缓存：清内存槽、删磁盘文件，并让在途刷新作废。
func (c *ownedCache) invalidate() {
	c.mu.Lock()
	c.gen++
	for key := range c.slots {
		delete(c.slots, key)
	}
	dir := c.dir
	c.mu.Unlock()

	if dir == "" {
		return
	}

	// 磁盘缓存同样是「上一个账号的清单」，一并删掉；删不掉也无妨 ——
	// loadOwnedFromDisk 会按账号校验，换号后本来就对不上。
	paths, err := filepath.Glob(filepath.Join(dir, "owned-*.json"))
	if err != nil {
		return
	}

	for _, path := range paths {
		_ = os.Remove(path)
	}
}

// generation 返回当前缓存代号（给在途刷新记下起点）。
func (c *ownedCache) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.gen
}

// current 报告缓存是否还是 gen 那一代（false 表示期间已被作废）。
func (c *ownedCache) current(gen uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.gen == gen
}

func (c *ownedCache) slot(platform string) *ownedSlot {
	c.mu.Lock()
	defer c.mu.Unlock()

	if slot, ok := c.slots[platform]; ok {
		return slot
	}

	slot := &ownedSlot{}
	c.slots[platform] = slot

	return slot
}

type ownedSlot struct {
	// fetchMu 串行化同一平台对 Apple 的拉取（含后台刷新），避免两份结果互相覆盖。
	fetchMu sync.Mutex

	mu         sync.Mutex
	warmed     bool // 是否尝试读过磁盘
	account    string
	data       []AppView
	total      int
	fetchedAt  time.Time
	refreshing bool
}

// warmUp 第一次用到这个平台时把磁盘缓存读进内存（每个平台只读一次）。
func (slot *ownedSlot) warmUp(dir, platform, account string) {
	slot.mu.Lock()
	if slot.warmed {
		slot.mu.Unlock()

		return
	}

	slot.warmed = true
	slot.mu.Unlock()

	list, at, ok := loadOwnedFromDisk(dir, platform, account)
	if !ok {
		return
	}

	slot.mu.Lock()
	defer slot.mu.Unlock()

	if slot.fetchedAt.IsZero() || at.After(slot.fetchedAt) {
		slot.account = account
		slot.data = list.Apps
		slot.total = list.TotalCount
		slot.fetchedAt = at
	}
}

// snapshot 返回当前缓存（调用方只读 Apps 切片，不要原地改）。
func (slot *ownedSlot) snapshot(platform, account string) (OwnedAppsList, time.Time, bool) {
	slot.mu.Lock()
	defer slot.mu.Unlock()

	if slot.fetchedAt.IsZero() || slot.account != account {
		return OwnedAppsList{}, time.Time{}, false
	}

	return OwnedAppsList{
		Platform:   platform,
		TotalCount: slot.total,
		FetchedAt:  slot.fetchedAt.Format(time.RFC3339),
		Apps:       slot.data,
	}, slot.fetchedAt, true
}

func (slot *ownedSlot) update(platform, account string, list OwnedAppsList, at time.Time) {
	slot.mu.Lock()
	defer slot.mu.Unlock()

	slot.warmed = true
	slot.account = account
	slot.data = list.Apps
	slot.total = list.TotalCount
	slot.fetchedAt = at
}

func (slot *ownedSlot) beginRefresh() bool {
	slot.mu.Lock()
	defer slot.mu.Unlock()

	if slot.refreshing {
		return false
	}

	slot.refreshing = true

	return true
}

func (slot *ownedSlot) endRefresh() {
	slot.mu.Lock()
	defer slot.mu.Unlock()

	slot.refreshing = false
}

// ownedDiskCache 是磁盘上那份缓存的形状（与 OwnedAppsList 分开，便于演进版本号）。
type ownedDiskCache struct {
	Version    int       `json:"version"`
	Platform   string    `json:"platform"`
	Account    string    `json:"account"`
	TotalCount int       `json:"totalCount"`
	FetchedAt  string    `json:"fetchedAt"`
	Apps       []AppView `json:"apps"`
}

func ownedCachePath(dir, platform string) string {
	return filepath.Join(dir, "owned-"+platform+".json")
}

// saveOwnedToDisk 尽力而为：缓存是加速手段，写失败不影响这次结果。
func saveOwnedToDisk(dir, platform, account string, list OwnedAppsList) {
	if dir == "" {
		return
	}

	path := ownedCachePath(dir, platform)

	data, err := json.Marshal(ownedDiskCache{
		Version:    ownedCacheVersion,
		Platform:   platform,
		Account:    account,
		TotalCount: list.TotalCount,
		FetchedAt:  list.FetchedAt,
		Apps:       list.Apps,
	})
	if err != nil {
		return
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}

// loadOwnedFromDisk 读磁盘缓存。任何一项对不上（版本/账号/年龄/解析）都当作没有。
func loadOwnedFromDisk(dir, platform, account string) (OwnedAppsList, time.Time, bool) {
	if dir == "" {
		return OwnedAppsList{}, time.Time{}, false
	}

	data, err := os.ReadFile(ownedCachePath(dir, platform))
	if err != nil {
		return OwnedAppsList{}, time.Time{}, false
	}

	var cached ownedDiskCache
	if err := json.Unmarshal(data, &cached); err != nil {
		return OwnedAppsList{}, time.Time{}, false
	}

	// 版本对不上、账号换过、时间解析不了 —— 都不能拿来当「这个账号的清单」。
	if cached.Version != ownedCacheVersion || cached.Account != account {
		return OwnedAppsList{}, time.Time{}, false
	}

	at, err := time.Parse(time.RFC3339, cached.FetchedAt)
	if err != nil {
		return OwnedAppsList{}, time.Time{}, false
	}

	age := time.Since(at)
	if age > ownedDiskMaxAge || age < 0 {
		return OwnedAppsList{}, time.Time{}, false
	}

	return OwnedAppsList{
		Platform:   platform,
		TotalCount: cached.TotalCount,
		FetchedAt:  cached.FetchedAt,
		Apps:       cached.Apps,
	}, at, true
}
