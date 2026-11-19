import { CatalogService, EventsOn } from "./api";
import type { ipa } from "./api";

/**
 * 账号已购应用的一次拉取结果。
 *
 * 为什么需要它：搜索结果的「已购」不能靠搜索接口 —— 实测 Apple 的公开搜索响应
 * 对已购应用**也不带** `purchaseDate`（拿账号里确实买过的 App 搜过，返回里没有），
 * 所以搜索页看到的「是否已购」必须来自账号的已购列表（CLI 的 list-purchases）。
 */
export type OwnedIndex = {
    apps: ipa.AppView[];
    /** 按 app id 查（列表与搜索的 id 同源，最可靠）。 */
    byId: Map<number, ipa.AppView>;
    /** 按 bundle id 查（id 偶尔对不上时的兜底）。 */
    byBundle: Map<string, ipa.AppView>;
    /** 拉取时刻（毫秒），用于界面显示「几点拉的」。 */
    fetchedAt: number;
    /** 这份是后端缓存（这次没有现问 Apple），可能不是刚刚的。 */
    cached: boolean;
    /** 后端正在后台重拉：拉到（或拉失败）之后会用 purchases:updated 推一份新的上来。 */
    refreshing: boolean;
    /** 非空表示「你看到的是旧数据」的原因（刷新失败等）。 */
    warning: string;
};

/**
 * 为什么不再由前端翻页：上游的 list-purchases **每取一页都会把整份购买历史重拉
 * 一遍**（两个 storefront 各自 login/update/items，实测 ~3s），前端翻 N 页就是
 * N 次全量抓取。现在改成一次拿全（后端负责缓存 + 后台刷新），前端只管用。
 */
const cache = new Map<string, OwnedIndex>();
const inflight = new Map<string, Promise<OwnedIndex>>();
const listeners = new Set<(platform: string) => void>();
const invalidateListeners = new Set<() => void>();

/**
 * 缓存代号：每次账号变了（登录 / 退出 / 清凭据）就自增。
 *
 * 在途请求拿回来之后要先比对：对不上就说明这份清单属于上一个账号，不能写缓存、
 * 也不能拿去渲染。
 */
let generation = 0;

function build(list: ipa.OwnedAppsList): OwnedIndex {
    const apps = list.apps || [];
    const byId = new Map<number, ipa.AppView>();
    const byBundle = new Map<string, ipa.AppView>();
    for (const app of apps) {
        byId.set(app.id, app);
        if (app.bundleId) {
            byBundle.set(app.bundleId, app);
        }
    }
    const parsed = Date.parse(list.fetchedAt || "");
    return {
        apps,
        byId,
        byBundle,
        fetchedAt: Number.isNaN(parsed) ? Date.now() : parsed,
        cached: Boolean(list.cached),
        refreshing: Boolean(list.refreshing),
        warning: list.warning || "",
    };
}

/**
 * 后端后台刷新完成时会广播 purchases:updated（成功给新清单，失败给旧清单 + Warning）。
 * 这里在模块加载时订阅一次：更新缓存并通知各视图重渲染 —— 界面因此不需要为
 * 「后台刷新中」自己轮询。
 */
EventsOn("purchases:updated", (payload: ipa.OwnedAppsList) => {
    if (!payload || !payload.platform) {
        return;
    }
    cache.set(payload.platform, build(payload));
    listeners.forEach((listener) => listener(payload.platform));
});

/**
 * 账号变了（后端在登录 / 退出 / 清凭据后广播 purchases:invalidated）时，
 * 把前端这份缓存整个丢掉。
 *
 * 为什么非丢不可：缓存 key 只有平台、没有账号，不清就会继续把上一个账号的
 * 「已购」当成当前账号的显示出来 —— 那会直接误导下载决策。
 */
export function resetOwned(): void {
    generation += 1;
    cache.clear();
    inflight.clear();
    invalidateListeners.forEach((listener) => listener());
}

EventsOn("purchases:invalidated", () => resetOwned());

/** 订阅「已购缓存被整体作废」（切号等）。返回取消订阅的函数。 */
export function onOwnedInvalidated(listener: () => void): () => void {
    invalidateListeners.add(listener);

    return () => {
        invalidateListeners.delete(listener);
    };
}

/** 订阅「已购清单变了」（来自后台刷新）。返回取消订阅的函数。 */
export function onOwnedUpdated(listener: (platform: string) => void): () => void {
    listeners.add(listener);

    return () => {
        listeners.delete(listener);
    };
}

/**
 * 取已购列表。有缓存时后端会立刻返回（cached = true），旧了就顺带起后台刷新 ——
 * 所以这里永远只发一次请求，不会翻页。
 *
 * force = true 表示用户明确点了「刷新」：跳过缓存、等 Apple（约 3s）。
 * 拉不到时会抛出，调用方负责「拉不到就不显示已购标记」。
 */
export function loadOwned(platform: string, force = false): Promise<OwnedIndex> {
    const key = platform || "iphone";

    if (!force) {
        const hit = cache.get(key);
        if (hit && !hit.warning) {
            return Promise.resolve(hit);
        }
        const running = inflight.get(key);
        if (running) {
            return running;
        }
    }

    const gen = generation;
    let promise: Promise<OwnedIndex>;
    promise = CatalogService.ListAllPurchases(key, force)
        .then((list) => {
            const index = build(list);
            if (gen !== generation) {
                // 拉取途中账号换了：这份清单是上一个账号的。不写缓存、不广播，
                // 调用方用 peekOwned 比对时会发现拿不到它，于是也不会采用。
                return index;
            }
            cache.set(key, index);
            if (inflight.get(key) === promise) {
                inflight.delete(key);
            }
            listeners.forEach((listener) => listener(key));

            return index;
        })
        .catch((reason: unknown) => {
            if (inflight.get(key) === promise) {
                inflight.delete(key);
            }
            throw reason;
        });
    inflight.set(key, promise);

    return promise;
}

/** 同步看缓存（渲染时用；没拉过就是 null）。 */
export function peekOwned(platform: string): OwnedIndex | null {
    return cache.get(platform || "iphone") || null;
}

/** 这个 App 在已购列表里吗（返回那条记录，含真实购买时间）。 */
export function findOwned(
    index: OwnedIndex | null,
    app: { id: number; bundleId: string },
): ipa.AppView | null {
    if (!index || !app) {
        return null;
    }
    const byId = index.byId.get(app.id);
    if (byId) {
        return byId;
    }
    return (app.bundleId && index.byBundle.get(app.bundleId)) || null;
}

/**
 * 单个 App 的已购判定，三态：
 *   true  —— 已购（来源：已购列表，或搜索响应里恰好带了 purchasedAt）
 *   false —— 已拉过已购列表且里面没有它
 *   null  —— 拉不到已购列表，无法判断（**不能当成未购买**，否则会把已购的付费应用误判成买不到）
 *
 * 注意：只有**完整**清单才能得出 false。后端在翻页中途失败时整体报错、不会给半份，
 * 所以 index 非空就等于「这份清单是完整的」。
 */
export function ownedState(
    index: OwnedIndex | null,
    app: ipa.AppView | null | undefined,
    hasList: boolean,
): { owned: boolean | null; purchasedAt: string } {
    if (!app) {
        return { owned: null, purchasedAt: "" };
    }
    const hit = findOwned(index, app);
    if (hit) {
        return { owned: true, purchasedAt: hit.purchasedAt || "" };
    }
    if (app.purchasedAt) {
        return { owned: true, purchasedAt: app.purchasedAt };
    }
    if (hasList) {
        return { owned: false, purchasedAt: "" };
    }
    return { owned: null, purchasedAt: "" };
}
