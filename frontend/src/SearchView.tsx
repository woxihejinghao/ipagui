import { useEffect, useRef, useState } from "react";
import {
    CatalogService,
    confirmDialog,
    DownloadService,
    EventsOn,
    LibraryService,
    errText,
    formatDate,
    humanBytes,
    PLATFORMS,
    shortPath,
    taskStateLabel,
} from "./api";
import type { Notify } from "./api";
import type { ipa, services, store } from "./api";
import AppIcon from "./AppIcon";
import DownloadFeed from "./DownloadFeed";
import type { Progress } from "./DownloadFeed";
import { loadOwned, onOwnedInvalidated, onOwnedUpdated, ownedState, peekOwned } from "./owned";
import type { OwnedIndex } from "./owned";

type Props = {
    notify: Notify;
    /** 「去下载页」：搜索页只给入口，重试/续传那些事在下载页做。 */
    onOpenDownloads?: () => void;
};

/**
 * 切到「已购 / 下载 / 设置」时 App 会把这个组件卸载（`tab === "search" ? ... : null`），
 * 所有 useState 跟着清空 —— 用户再切回来，输入框、条数、区域和搜索结果全没了，
 * 只能把刚才搜的东西重打一遍。
 *
 * 所以把「用户亲手做过的选择」和结果本身留一份模块级快照。只存这四项：已购清单、
 * 账本、下载任务都是派生数据，各有自己的拉取入口（挂载即重拉），存了反而会让
 * 旧账号 / 旧平台的数据串到新界面上。
 */
type SearchSnapshot = {
    term: string;
    limit: number;
    platform: string;
    result: ipa.SearchResult | null;
};

const snapshot: SearchSnapshot = { term: "", limit: 10, platform: "iphone", result: null };

/**
 * 把粘贴进来的东西认成 App：App Store 链接、纯数字的 App ID 都认。
 *
 * 为什么需要：付费应用得先去 App Store 买，用户手边最现成的东西就是商店链接；
 * 而链接里的 id 正是我们要的 app id。认不出来就退回正常搜索（可能只是搜了个带数字的名字）。
 */
type AppTarget = { kind: "id"; appID: number } | { kind: "bundle"; bundleID: string };

function parseAppTarget(term: string): AppTarget | null {
    const trimmed = term.trim();

    const linkMatch = /apps\.apple\.com\/[^\s]*?\/id(\d+)/.exec(trimmed);
    if (linkMatch) {
        return { kind: "id", appID: Number(linkMatch[1]) };
    }

    // App Store 的 id 都是六位以上；短数字更像是在搜「2048」这类名字。
    if (/^(id)?\d{6,}$/i.test(trimmed)) {
        return { kind: "id", appID: Number(trimmed.replace(/^id/i, "")) };
    }

    // Bundle ID（至少三段，例如 com.tencent.xin）。Apple 的**搜索**接口只是把它当普通
    // 关键词，几乎必然 0 条；但 lookup 接口按 bundleId 能精确命中 —— 所以这类输入要走
    // Lookup，别丢给搜索。
    if (/^[A-Za-z][A-Za-z0-9-]*(\.[A-Za-z0-9-]+){2,}$/.test(trimmed)) {
        return { kind: "bundle", bundleID: trimmed };
    }

    return null;
}

/**
 * Go 的空切片过了 JSON 会变成 null（nil slice → `"apps": null`）。
 * 在入口归一化一次，渲染层就不用处处写 `?? []`；少了这一层，一次「搜不到」的结果
 * 就能让 `result.apps.length` 抛 TypeError，把整棵 React 树卸载成黑屏。
 */
function normalizeSearchResult(result: ipa.SearchResult): ipa.SearchResult {
    // 生成的模型是 class（带 convertValues），展开出来的普通对象要显式断言回去：
    // 渲染只用到 term/count/apps，不至于为了这一步去 new 一个模型实例。
    return { ...result, apps: result.apps ?? [] } as ipa.SearchResult;
}

/** Apple 只给「当前商店版本」的体积，历史版本要下完才知道 —— 缺了就说缺了，别编。 */
function sizeText(bytes?: number): string {
    return bytes && bytes > 0 ? humanBytes(bytes) : "体积未知";
}

/**
 * 付费应用：上游的 `Purchase` 只支持免费应用，未购买时下载按钮必须禁用并解释。
 *
 * 「是否已购」不能只看搜索响应 —— 实测 Apple 的公开搜索接口对**已购**应用也不带
 * `purchaseDate`（拿账号里确实买过的 App 搜过），所以要以账号的已购列表为准
 * （`owned.ts`）。列表拉不到时**不拦**：把「不知道」当成「没买过」，会把已买的付费
 * 应用挡在门外（这曾经是真的 bug）。
 */
function canDownload(app: ipa.AppView, owned: OwnedIndex | null): boolean {
    if (app.free) {
        return true;
    }

    return ownedState(owned, app, Boolean(owned)).owned !== false;
}

function blockedReason(app: ipa.AppView, owned: OwnedIndex | null): string {
    if (canDownload(app, owned)) {
        return "";
    }

    return "这是付费应用，当前账号没有购买记录。上游的 Purchase 只支持免费应用，买不了它 —— 请在 App Store 里买过之后再回来下载。";
}

/**
 * 历史版本面板的状态。
 *
 * 版本标识列表（Apple 的 external version id）一次就能拿到，但显示版本号（9.3.75）
 * 与发布时间只能一个版本一次请求 —— 所以面板先把标识列出来，再在后台逐个补齐，
 * 不让用户对着一个空表格转圈。
 */
type VersionPanel = {
    app: ipa.AppView;
    latestVersionId: string;
    versionIds: string[];
    /** 版本标识 → 元数据（缺项 = 还在查）。 */
    meta: Record<string, ipa.VersionMetadata>;
    /** 查失败的那些：标记出来，避免看起来永远“在查”。 */
    failed: Record<string, true>;
    loading: boolean;
    error: string;
};

/**
 * 购买（获取应用许可）的结果面板。
 *
 * 只装「结果」：请求进行中时表格里的按钮自己显示「购买中…」，不弹阻塞窗口——
 * 点下去就把人锁在弹窗里等 Apple 的说法，比等按钮转圈更难受。
 */
type PurchasePanel = {
    app: ipa.AppView;
    state: "done" | "failed";
    message: string;
};

export default function SearchView({ notify, onOpenDownloads }: Props) {
    const [term, setTerm] = useState(snapshot.term);
    const [limit, setLimit] = useState(snapshot.limit);
    const [platform, setPlatform] = useState(snapshot.platform);
    const [busy, setBusy] = useState(false);
    const [result, setResult] = useState<ipa.SearchResult | null>(snapshot.result);

    // 账号的已购清单：搜索结果里的「是否已购」以它为准（搜索接口不告诉我们）。
    const [owned, setOwned] = useState<OwnedIndex | null>(() => peekOwned(snapshot.platform));
    const [ownedError, setOwnedError] = useState("");

    // bundle id → 本地库里的那一条：结果列表上提前标「本地已有」。
    const [ledger, setLedger] = useState<Record<string, services.LibraryEntry>>({});

    // 本页认识的下载任务 + 它们最新的进度快照：结果上方的提示条和「下载中 x%」按钮都看这两个。
    const [taskList, setTaskList] = useState<store.Task[]>([]);
    const [liveProgress, setLiveProgress] = useState<Record<string, Progress>>({});
    // 历史版本面板（同时只开一个 App）：见 VersionPanel。
    const [versionPanel, setVersionPanel] = useState<VersionPanel | null>(null);
    const versionDialogRef = useRef<HTMLDialogElement>(null);
    // 购买结果弹窗：见 PurchasePanel。purchasingID 表示哪一行正在请求许可。
    const [purchasePanel, setPurchasePanel] = useState<PurchasePanel | null>(null);
    const [purchasingID, setPurchasingID] = useState<number | null>(null);
    const purchaseDialogRef = useRef<HTMLDialogElement>(null);

    const openDownloads = onOpenDownloads ?? (() => notify("下载页在顶部的「下载」标签里。", "info"));

    const mergeTasks = (incoming: store.Task[]) => {
        setTaskList((current) => {
            const byId = new Map(current.map((item) => [item.id, item]));
            // 已经认识的任务用新状态覆盖（这样刚跑完的不会从提示条里消失）；
            // 另外把别处启动、还在跑的任务也纳进来，免得切走再切回来就看不见了。
            incoming.forEach((item) => {
                if (byId.has(item.id) || item.state === "pending" || item.state === "running") {
                    byId.set(item.id, item);
                }
            });

            return [...byId.values()].sort((a, b) => (a.createdAt < b.createdAt ? 1 : -1));
        });
    };

    const loadTasks = async () => {
        try {
            mergeTasks(await DownloadService.List());
        } catch {
            // 提示条只是锦上添花：读不到任务列表不该影响搜索。
        }
    };

    /**
     * 拉账号的已购清单。它比搜索慢一点（要问购买历史接口），所以只在真的要显示结论时拉：
     * 搜索出结果之后、或从已购页跳过来时。失败就把结论收起来（`owned = null`），
     * 界面上表现为「已购未确认」而不是「未购买」。
     */
    const loadOwnedIndex = async (target: string, force = false): Promise<OwnedIndex | null> => {
        try {
            const index = await loadOwned(target, force);
            // 等待期间账号换了：这份清单是上一个账号的，不能拿来当「已购」依据。
            if (peekOwned(target) !== index) {
                setOwned(null);
                setOwnedError("");

                return null;
            }
            setOwned(index);
            setOwnedError("");

            return index;
        } catch (reason) {
            setOwned(null);
            setOwnedError(errText(reason));

            return null;
        }
    };

    // 已购清单是后端缓存的：后台刷新完成（或失败）时会推 purchases:updated，
    // 这里跟着换一份，免得「刚买的应用」要等用户手动点重新检查才认。
    useEffect(
        () =>
            onOwnedUpdated((updated) => {
                if (updated === platform) {
                    const fresh = peekOwned(platform);
                    setOwned(fresh);
                    if (fresh) {
                        setOwnedError("");
                    }
                }
            }),
        [platform],
    );

    // 登录 / 退出 / 清凭据后，缓存里那份「已购」就不属于当前账号了：
    // 先收成「未确认」（null），下次真要下结论时会重新拉。
    useEffect(
        () =>
            onOwnedInvalidated(() => {
                setOwned(null);
                setOwnedError("");
            }),
        [],
    );

    /** 在 App Store 里打开这个 App：付费应用只能先在那里买。 */
    const openStore = async (app: ipa.AppView) => {
        try {
            await CatalogService.OpenInAppStore(app.id);
            notify(`已在 App Store 里打开 ${app.name}，买好之后回来点「下载最新」。`, "info");
        } catch (reason) {
            notify(`打开 App Store 失败：${errText(reason)}`);
        }
    };

    /** 刚买完就回到这里：强制重拉一次已购清单（缓存里那份可能还是「没买」）。 */
    const reloadOwned = async () => {
        const index = await loadOwnedIndex(platform, true);
        if (index) {
            notify(`已重新检查：账号的已购列表里有 ${index.apps.length} 个应用`, "info");
        } else {
            notify("已购列表还是没拉到（Apple 侧可能不稳定），过一会儿再试。");
        }
    };

    /** 把本地库按 bundle id 拉一遍：结果列表要提前标「本地已有」。 */
    const loadLedger = async (target: string) => {
        try {
            const entries = await LibraryService.List({ Query: "", Platform: target } as store.EntryFilter);
            const byBundle: Record<string, services.LibraryEntry> = {};
            entries.forEach((entry) => {
                byBundle[entry.bundleId] = entry;
            });
            setLedger(byBundle);
        } catch {
            setLedger({});
        }
    };

    // 每次渲染后把快照刷成最新（不写依赖数组）：这样不管用户是先改了条件还是先拿到了
    // 结果，卸载后再回来看到的都是最后一次的样子。
    useEffect(() => {
        snapshot.term = term;
        snapshot.limit = limit;
        snapshot.platform = platform;
        snapshot.result = result;
    });

    useEffect(() => {
        void loadTasks();

        // 从快照恢复了一份旧结果时，账本和已购清单要重新对一遍：它们是随组件一起被卸掉的
        // 派生数据，不重拉的话「本地已有 / 已购」标记会缺一块，而结果本身还挂在屏幕上。
        if (snapshot.result) {
            void loadLedger(platform);
            void loadOwnedIndex(platform);
        }

        const offProgress = EventsOn("download:progress", (payload: Progress) => {
            if (!payload || !payload.taskId) {
                return;
            }
            setLiveProgress((current) => ({ ...current, [payload.taskId]: payload }));
        });
        // state 事件的载荷里没有 bundleId/versionId，只能按 id 对；下载页负责全量刷新，
        // 这里只要让提示条跟上「排队中 → 下载中 → 完成 / 失败」。
        const offState = EventsOn("download:state", () => {
            void loadTasks();
        });

        return () => {
            offProgress();
            offState();
        };
    }, []);

    // 搜到结果先写快照再进 state：用户在「搜索中…」的时候切走，组件已经卸载，
    // setResult 不会再渲染出新界面，但快照留住了结果 —— 回来还看得见。
    const applyResult = (next: ipa.SearchResult | null) => {
        snapshot.result = next;
        setResult(next);
    };

    const doSearch = async () => {
        if (!term.trim()) {
            return;
        }
        const target = parseAppTarget(term);

        setBusy(true);
        void loadLedger(platform);
        try {
            if (target) {
                // 粘的是商店链接 / App ID / Bundle ID：直接查这个 App，结果就一条 ——
                // 不拿整条 URL 或 bundle id 去当关键词搜（那样搜不到任何东西）。
                const app =
                    target.kind === "id"
                        ? await CatalogService.Lookup(target.appID, "", platform)
                        : await CatalogService.Lookup(0, target.bundleID, platform);
                applyResult({
                    term: app.name || term.trim(),
                    count: 1,
                    apps: [app],
                } as ipa.SearchResult);
                void loadOwnedIndex(platform);

                return;
            }

            applyResult(normalizeSearchResult(await CatalogService.Search(term.trim(), limit, platform)));
            // 有了结果才有「已购」要标，这时候才去问购买历史。
            void loadOwnedIndex(platform);
        } catch (reason) {
            notify(errText(reason));
        } finally {
            setBusy(false);
        }
    };

    // PRD Story 5 AC2：同应用同版本同平台重复下载要先提示，并且「跳过」要真的能跳过。
    // 这是「多问一句」的守卫，不是硬拦截：判断失败、或用户就是要重下一份（文件被删了等），
    // 点掉对话框就照常下载 —— 所以下面的每条分支都返回「继续」而不是报错。
    const confirmDuplicate = async (app: ipa.AppView): Promise<boolean> => {
        try {
            const tasks = await DownloadService.List();
            const running = tasks.find(
                (task) =>
                    task.bundleId === app.bundleId &&
                    task.platform === platform &&
                    (task.state === "pending" || task.state === "running"),
            );
            if (running) {
                return confirmDialog(
                    `这个 App 已经在下载队列里了（任务 ${running.id.slice(0, 8)}，${taskStateLabel(running.state).text}）。\n\n` +
                        "同一时刻只跑一个下载，再点一次只会排在它后面，把同一份文件重下一遍。\n\n" +
                        "仍然再下一个？",
                    "这个 App 已在队列里",
                );
            }

            // 这一页只下最新版，账本里记账记的就是 App Store 的显示版本号，直接拿它对。
            const entries = await LibraryService.List({
                Query: app.bundleId,
                Platform: platform,
            } as store.EntryFilter);
            const entry = app.version ? entries.find((item) => item.version === app.version) : undefined;
            if (!entry) {
                return true;
            }

            const hash = entry.sha256
                ? `已存在（哈希一致）：账本里这一份的 sha256 是 ${entry.sha256.slice(0, 16)}…`
                : "已存在：账本里这一份还没算过 sha256，没法比对内容";

            const sizeLine = app.fileSizeBytes
                ? `App Store 当前版本的体积是 ${humanBytes(app.fileSizeBytes)}。\n\n`
                : "";

            return confirmDialog(
                `${hash}\n\n文件${entry.exists ? "还在磁盘上" : "已不在磁盘上"}：${entry.path}\n\n` +
                    sizeLine +
                    "重下一份，内容应当与账本里那份一致 —— 下完可以在「下载 → 已下载」里对照 sha256 前几位。\n\n" +
                    "仍然重新下载？",
                "本地已经有了",
            );
        } catch (reason) {
            notify(`重复下载检查没做成（不拦着下载）：${errText(reason)}`);

            return true;
        }
    };

    /** 这个 App 的最新版本现在正在下（或排队）：用来把按钮换成「下载中 43%」并禁用重复点击。 */
    const activeTask = (app: ipa.AppView): store.Task | undefined =>
        taskList.find(
            (item) =>
                item.bundleId === app.bundleId &&
                item.platform === platform &&
                item.versionId === "" &&
                (item.state === "pending" || item.state === "running"),
        );

    const downloadLabel = (app: ipa.AppView, fallback: string): string => {
        const task = activeTask(app);
        if (!task) {
            return fallback;
        }

        if (task.state === "pending") {
            return "排队中…";
        }

        const snapshot = liveProgress[task.id];
        const total = snapshot?.total || task.bytesTotal;
        const done = snapshot?.current || task.bytesDone;

        return total > 1 ? `下载中 ${Math.floor((done / total) * 100)}%` : "下载中…";
    };

    const downloadTitle = (app: ipa.AppView): string => {
        const task = activeTask(app);
        if (!task) {
            return blockedReason(app, owned);
        }

        return task.state === "pending" ? "已经在队列里排队，前面的任务结束后自动开始" : "正在下载，进度见上方提示条";
    };

    const download = async (app: ipa.AppView) => {
        if (!canDownload(app, owned)) {
            notify(blockedReason(app, owned));

            return;
        }

        if (!(await confirmDuplicate(app))) {
            notify(`已跳过：${app.name} 的最新版本已经在下载队列或已下载里`, "info");

            return;
        }

        try {
            const task = await DownloadService.Start({
                appId: app.id,
                bundleId: app.bundleId,
                platform,
                externalVersionId: "",
                outputPath: "",
                acquireLicense: true,
            });
            mergeTasks([task]);
            setLiveProgress((current) => {
                const next = { ...current };
                delete next[task.id];

                return next;
            });
            // 光弹一句 6 秒的 toast 说明不了什么：提示条会一直显示到这一单有了结果。
            notify(`已开始下载：${task.name || app.name} —— 进度和结果就在结果列表上方`, "info");
        } catch (reason) {
            notify(errText(reason));
        }
    };

    // 历史版本面板：<dialog> 要用 showModal 才有遮罩与焦点锁；只加 open 属性不行。
    useEffect(() => {
        const dialog = versionDialogRef.current;
        if (!dialog) {
            return;
        }
        if (versionPanel && !dialog.open) {
            dialog.showModal();
        }
        if (!versionPanel && dialog.open) {
            dialog.close();
        }
    }, [versionPanel]);

    const closeVersions = () => setVersionPanel(null);

    // 购买结果弹窗同样要走 showModal（只加 open 属性没有焦锁与遮罩）。
    useEffect(() => {
        const dialog = purchaseDialogRef.current;
        if (!dialog) {
            return;
        }
        if (purchasePanel && !dialog.open) {
            dialog.showModal();
        }
        if (!purchasePanel && dialog.open) {
            dialog.close();
        }
    }, [purchasePanel]);

    const closePurchase = () => setPurchasePanel(null);

    /**
     * 购买 = 向 Apple 请求这个应用的应用许可（免费应用直接入账）。
     *
     * 付费应用上游接口不支持，必然失败 —— 不在这里拦，把 Apple 的报错原样交给弹窗，
     * 弹窗里再给「去 App Store 购买」的出口（见下面 purchasePanel 的渲染）。
     */
    const purchase = async (app: ipa.AppView) => {
        if (purchasingID !== null) {
            return;
        }

        setPurchasingID(app.id);
        try {
            await CatalogService.Purchase(app.id, app.bundleId, platform);
            setPurchasePanel({
                app,
                state: "done",
                message: `已为 ${app.name} 拿到应用许可，现在可以直接点「下载最新」。`,
            });
            notify(`已购买：${app.name}`, "info");
            // 已购清单刚过期：强制重拉一份，「已购」标记与下载按钮才会跟着放开。
            void loadOwnedIndex(platform, true);
        } catch (reason) {
            setPurchasePanel({ app, state: "failed", message: errText(reason) });
        } finally {
            setPurchasingID(null);
        }
    };

    /** 打开版本面板：版本标识列表秒回，显示版本号/发布时间在后台逐个补齐。 */
    const openVersions = async (app: ipa.AppView) => {
        setVersionPanel({
            app,
            latestVersionId: "",
            versionIds: [],
            meta: {},
            failed: {},
            loading: true,
            error: "",
        });

        try {
            const list = await CatalogService.ListVersions(app.id, app.bundleId, platform);
            const ids = list.versionIdentifiers ?? [];

            setVersionPanel((current) =>
                current && current.app.id === app.id
                    ? { ...current, latestVersionId: list.latestVersionId || "", versionIds: ids, loading: false }
                    : current,
            );

            void fillVersionMeta(app, ids);
        } catch (reason) {
            setVersionPanel((current) =>
                current && current.app.id === app.id ? { ...current, loading: false, error: errText(reason) } : current,
            );
        }
    };

    /**
     * 逐个版本补显示版本号与发布时间（限 3 并发）。
     *
     * 为什么不让后端一次给：上游没有批量接口，每个版本都是一次单独请求
     * （`get-version-metadata`）；一个常更新的 App 能有几十个历史版本，一股脑全
     * 打过去既慢又容易被限流。所以先出列表，这些信息陆续填。
     */
    const fillVersionMeta = async (app: ipa.AppView, versionIds: string[]) => {
        const queue = [...versionIds];

        const worker = async () => {
            for (;;) {
                const versionId = queue.shift();
                if (!versionId) {
                    return;
                }

                try {
                    const meta = await CatalogService.GetVersionMetadata(app.id, app.bundleId, versionId, platform);
                    setVersionPanel((current) =>
                        current && current.app.id === app.id
                            ? { ...current, meta: { ...current.meta, [versionId]: meta } }
                            : current,
                    );
                } catch {
                    // 单条失败不能拖黑整个面板：标出来，其它版本照常。
                    setVersionPanel((current) =>
                        current && current.app.id === app.id
                            ? { ...current, failed: { ...current.failed, [versionId]: true } }
                            : current,
                    );
                }
            }
        };

        await Promise.all(Array.from({ length: Math.min(3, queue.length) }, worker));
    };

    /** 这个 App 的某个历史版本现在在队列里吗（用来把按钮换成状态）。 */
    const versionTask = (app: ipa.AppView, versionId: string): store.Task | undefined =>
        taskList.find(
            (item) =>
                item.bundleId === app.bundleId &&
                item.platform === platform &&
                item.versionId === versionId &&
                (item.state === "pending" || item.state === "running"),
        );

    /** 历史版本的重复下载检查：账本里历史版本记的是版本标识，最新版记的是显示版本号，两种都对。 */
    const confirmVersionDuplicate = async (
        app: ipa.AppView,
        versionId: string,
        displayVersion: string,
    ): Promise<boolean> => {
        try {
            const tasks = await DownloadService.List();
            const running = tasks.find(
                (task) =>
                    task.bundleId === app.bundleId &&
                    task.platform === platform &&
                    task.versionId === versionId &&
                    (task.state === "pending" || task.state === "running"),
            );
            if (running) {
                return confirmDialog(
                    `这个版本已经在下载队列里了（任务 ${running.id.slice(0, 8)}，${taskStateLabel(running.state).text}）。\n\n仍然再下一个？`,
                    "这个版本已在队列里",
                );
            }

            const entries = await LibraryService.List({
                Query: app.bundleId,
                Platform: platform,
            } as store.EntryFilter);
            const entry = entries.find(
                (item) => item.version === versionId || (displayVersion !== "" && item.version === displayVersion),
            );
            if (!entry) {
                return true;
            }

            return confirmDialog(
                `这个版本已经在本地了：${entry.path}\n文件${entry.exists ? "还在磁盘上" : "已不在磁盘上"}。\n\n仍然重新下载？`,
                "本地已经有了",
            );
        } catch (reason) {
            notify(`重复下载检查没做成（不拦着下载）：${errText(reason)}`);

            return true;
        }
    };

    /** 下载某个历史版本（externalVersionId 非空 = 不是最新）。 */
    const downloadVersion = async (app: ipa.AppView, versionId: string) => {
        if (!canDownload(app, owned)) {
            notify(blockedReason(app, owned));

            return;
        }

        const displayVersion = versionPanel?.meta[versionId]?.displayVersion || "";

        if (!(await confirmVersionDuplicate(app, versionId, displayVersion))) {
            notify(`已跳过：${app.name} ${displayVersion || versionId}`, "info");

            return;
        }

        try {
            const task = await DownloadService.Start({
                appId: app.id,
                bundleId: app.bundleId,
                platform,
                externalVersionId: versionId,
                outputPath: "",
                acquireLicense: true,
            });
            mergeTasks([task]);
            notify(`已开始下载：${app.name} ${displayVersion || `版本 ${versionId}`} —— 进度见结果列表上方`, "info");
        } catch (reason) {
            notify(errText(reason));
        }
    };

    /**
     * 价格列：已购（绿）／免费／付费未买（红）／付费但已购清单没拉到（黄，不下结论）。
     */
    const priceCell = (app: ipa.AppView) => {
        const state = ownedState(owned, app, Boolean(owned));

        if (state.owned) {
            return (
                <>
                    <span className="badge good" title="账号的已购列表里有它">
                        已购
                    </span>
                    <div className="muted" title="账号的购买时间（来自已购列表）">
                        {formatDate(state.purchasedAt)}
                    </div>
                    {app.price > 0 ? <div className="muted">{app.price}</div> : null}
                </>
            );
        }

        if (app.free) {
            return <span className="badge good">免费</span>;
        }

        return (
            <>
                <span className="badge warn">{app.price}</span>
                {state.owned === false ? (
                    <div className="tone-warn">不能下载：未购买</div>
                ) : (
                    <div className="tone-warn" title={ownedError || "已购列表还没拉到"}>
                        已购未确认
                    </div>
                )}
                <button className="link-button" onClick={() => void openStore(app)}>
                    去 App Store 买
                </button>
            </>
        );
    };

    // 提示条显示：正在排队/下载的（最多 3 个），外加最近刚跑完的一个 ——
    // 否则「下载完成 / 失败」的消息会跟着 toast 一起消失，用户还是不知道结果。
    const feed = taskList.filter((item) => item.state === "pending" || item.state === "running").slice(0, 3);
    const newestSettled = taskList.find((item) => item.state !== "pending" && item.state !== "running");
    if (newestSettled && !feed.some((item) => item.id === newestSettled.id)) {
        feed.push(newestSettled);
    }

    return (
        <div>
            <div className="card">
                <div className="row">
                    <input
                        className="grow"
                        type="text"
                        value={term}
                        placeholder="应用名、bundle id、App Store 链接或 App ID，例如微信 / com.tencent.xin / id414478124"
                        onChange={(event) => setTerm(event.target.value)}
                        onKeyDown={(event) => {
                            if (event.key === "Enter") {
                                void doSearch();
                            }
                        }}
                    />
                    <label className="field" title="结果按账号所在区域返回；换区只能换账号">
                        区域
                        <select value={platform} onChange={(event) => setPlatform(event.target.value)}>
                            {PLATFORMS.map((item) => (
                                <option key={item.value} value={item.value}>
                                    {item.label}
                                </option>
                            ))}
                        </select>
                    </label>
                    <label className="field">
                        条数
                        <input
                            type="number"
                            min={1}
                            max={50}
                            value={limit}
                            style={{ width: 70 }}
                            onChange={(event) => setLimit(Number(event.target.value) || 10)}
                        />
                    </label>
                    <button className="primary" disabled={busy || !term.trim()} onClick={() => void doSearch()}>
                        {busy ? "搜索中…" : "搜索"}
                    </button>
                </div>
            </div>

            {feed.length > 0 ? (
                <DownloadFeed
                    tasks={feed}
                    progress={liveProgress}
                    onCancel={(taskId) => {
                        void DownloadService.Cancel(taskId).catch((reason) => notify(errText(reason)));
                    }}
                    onReveal={(taskId) => {
                        void DownloadService.Reveal(taskId).catch((reason) => notify(errText(reason)));
                    }}
                    onOpenDownloads={openDownloads}
                />
            ) : null}

            {result ? (
                <div className="card">
                    <h3>
                        结果 · “{result.term}” · {result.count} 条
                    </h3>
                    {(result.apps ?? []).length === 0 ? (
                        <div className="empty">没有结果</div>
                    ) : (
                        <table>
                            <thead>
                                <tr>
                                    <th>应用</th>
                                    <th>Bundle ID</th>
                                    <th>版本 · 更新时间 / 体积</th>
                                    <th>价格</th>
                                    <th style={{ width: 240 }}>操作</th>
                                </tr>
                            </thead>
                            <tbody>
                                {(result.apps ?? []).map((app) => (
                                    <tr key={`${app.id}-${app.bundleId}`}>
                                        <td>
                                            <div className="app-cell">
                                                <AppIcon url={app.artworkUrl} />
                                                <div className="app-text">
                                                    <div>{app.name}</div>
                                                    <div className="muted mono">ID {app.id}</div>
                                                    {ledger[app.bundleId] ? (
                                                        <div
                                                            className="row"
                                                            style={{ marginTop: 2 }}
                                                            title={ledger[app.bundleId].path}
                                                        >
                                                            <span
                                                                className={`badge ${
                                                                    ledger[app.bundleId].exists ? "good" : "warn"
                                                                }`}
                                                            >
                                                                {ledger[app.bundleId].exists ? "本地已有" : "本地丢失"}
                                                            </span>
                                                            <span className="muted">
                                                                {ledger[app.bundleId].version || "—"}
                                                            </span>
                                                        </div>
                                                    ) : null}
                                                </div>
                                            </div>
                                        </td>
                                        <td className="mono">{app.bundleId}</td>
                                        <td className="mono">
                                            {app.version || "—"}
                                            <div
                                                className="muted"
                                                title="App Store 当前版本的发布时间"
                                            >
                                                更新 {formatDate(app.updatedAt)}
                                            </div>
                                            <div
                                                className="muted"
                                                title="App Store 当前版本的体积；历史版本的体积 Apple 不提供"
                                            >
                                                {sizeText(app.fileSizeBytes)}
                                            </div>
                                        </td>
                                        <td>{priceCell(app)}</td>
                                        <td>
                                            <div className="row">
                                                <button
                                                    className="primary"
                                                    disabled={!canDownload(app, owned) || Boolean(activeTask(app))}
                                                    title={downloadTitle(app)}
                                                    onClick={() => void download(app)}
                                                >
                                                    {downloadLabel(app, "下载最新")}
                                                </button>
                                                <button
                                                    disabled={
                                                        purchasingID !== null ||
                                                        ownedState(owned, app, Boolean(owned)).owned === true
                                                    }
                                                    title={
                                                        ownedState(owned, app, Boolean(owned)).owned === true
                                                            ? "账号的已购列表里已经有它了，不用再买"
                                                            : app.free
                                                              ? "获取应用许可（免费应用，直接入账）"
                                                              : "尝试购买：上游只支持免费应用，付费应用请用「去 App Store 购买」"
                                                    }
                                                    onClick={() => void purchase(app)}
                                                >
                                                    {purchasingID === app.id ? "购买中…" : "购买"}
                                                </button>
                                                <button
                                                    onClick={() => void openVersions(app)}
                                                    title="查看历史版本，并按版本下载"
                                                >
                                                    版本
                                                </button>
                                            </div>
                                        </td>
                                    </tr>
                                ))}
                            </tbody>
                        </table>
                    )}
                </div>
            ) : null}

            {versionPanel ? (
                <dialog
                    ref={versionDialogRef}
                    className="modal"
                    style={{ width: "min(720px, calc(100% - 40px))" }}
                    aria-labelledby="versions-title"
                    onCancel={(event) => {
                        event.preventDefault();
                        closeVersions();
                    }}
                >
                    <div className="modal-header">
                        <div>
                            <h3 id="versions-title">{versionPanel.app.name} · 历史版本</h3>
                            <div className="muted">
                                {versionPanel.loading
                                    ? "正在向 Apple 拉版本列表…"
                                    : `共 ${versionPanel.versionIds.length} 个可下载版本`}
                            </div>
                        </div>
                        <button className="icon-button" onClick={closeVersions} aria-label="关闭">
                            ×
                        </button>
                    </div>

                    {versionPanel.error ? (
                        <div className="notice bad" style={{ marginTop: 12 }}>
                            <div className="muted">{versionPanel.error}</div>
                            <div className="row">
                                <button onClick={() => void openVersions(versionPanel.app)}>重试</button>
                            </div>
                        </div>
                    ) : versionPanel.loading ? (
                        <div className="empty">正在拉取…</div>
                    ) : versionPanel.versionIds.length === 0 ? (
                        <div className="empty">Apple 没有返回任何版本标识</div>
                    ) : (
                        <>
                            <table style={{ marginTop: 10 }}>
                                <thead>
                                    <tr>
                                        <th>版本</th>
                                        <th>发布时间</th>
                                        <th style={{ width: 120 }}>操作</th>
                                    </tr>
                                </thead>
                                <tbody>
                                    {versionPanel.versionIds.map((versionId) => {
                                        const meta = versionPanel.meta[versionId];
                                        const failed = versionPanel.failed[versionId];
                                        const task = versionTask(versionPanel.app, versionId);
                                        const isLatest = versionPanel.latestVersionId === versionId;

                                        return (
                                            <tr key={versionId}>
                                                <td>
                                                    <div className="mono">
                                                        {meta?.displayVersion || versionId}
                                                        {isLatest ? (
                                                            <span className="badge good" style={{ marginLeft: 6 }}>
                                                                最新
                                                            </span>
                                                        ) : null}
                                                    </div>
                                                    <div className="muted mono" title="Apple 的版本标识（下载时用的就是它）">
                                                        {versionId}
                                                    </div>
                                                    {!meta && !failed ? <div className="muted">查询中…</div> : null}
                                                    {failed ? <div className="tone-warn">这个版本的元数据没查到</div> : null}
                                                </td>
                                                <td className="muted">{meta ? formatDate(meta.releaseDate) : "—"}</td>
                                                <td>
                                                    <button
                                                        className={task ? undefined : "primary"}
                                                        disabled={!canDownload(versionPanel.app, owned) || Boolean(task)}
                                                        title={
                                                            task
                                                                ? "已经在下载队列里"
                                                                : blockedReason(versionPanel.app, owned) ||
                                                                  "下载这个历史版本"
                                                        }
                                                        onClick={() => void downloadVersion(versionPanel.app, versionId)}
                                                    >
                                                        {task
                                                            ? task.state === "pending"
                                                                ? "排队中…"
                                                                : "下载中…"
                                                            : "下载此版本"}
                                                    </button>
                                                </td>
                                            </tr>
                                        );
                                    })}
                                </tbody>
                            </table>
                        </>
                    )}
                </dialog>
            ) : null}

            {purchasePanel ? (
                <dialog
                    ref={purchaseDialogRef}
                    className="modal"
                    aria-labelledby="purchase-title"
                    onCancel={(event) => {
                        event.preventDefault();
                        closePurchase();
                    }}
                >
                    <div className="modal-header">
                        <div>
                            <h3 id="purchase-title">购买结果 · {purchasePanel.app.name}</h3>
                            <div className="muted mono">{purchasePanel.app.bundleId}</div>
                        </div>
                        <button className="icon-button" onClick={closePurchase} aria-label="关闭">
                            ×
                        </button>
                    </div>

                    {purchasePanel.state === "done" ? (
                        <div className="notice good">
                            <h4>已获取许可</h4>
                            <div className="muted">{purchasePanel.message}</div>
                        </div>
                    ) : (
                        <div className="notice bad">
                            <h4>购买没成功</h4>
                            <div className="muted" style={{ whiteSpace: "pre-wrap" }}>
                                {purchasePanel.message}
                            </div>
                        </div>
                    )}

                    <div className="modal-actions">
                        {purchasePanel.state === "done" ? (
                            <button
                                className="primary"
                                onClick={() => {
                                    const app = purchasePanel.app;
                                    closePurchase();
                                    void download(app);
                                }}
                            >
                                下载最新
                            </button>
                        ) : null}
                        {purchasePanel.state === "failed" && !purchasePanel.app.free ? (
                            <button onClick={() => void openStore(purchasePanel.app)}>去 App Store 购买</button>
                        ) : null}
                        <button onClick={closePurchase}>关闭</button>
                    </div>
                </dialog>
            ) : null}

        </div>
    );
}
