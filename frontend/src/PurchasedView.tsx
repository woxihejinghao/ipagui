import { useCallback, useEffect, useMemo, useState } from "react";
import { confirmDialog, DownloadService, PLATFORMS, errText, formatDate, humanBytes } from "./api";
import type { Notify, ipa, store } from "./api";
import AppIcon from "./AppIcon";
import { loadOwned, onOwnedInvalidated, onOwnedUpdated, peekOwned } from "./owned";
import type { OwnedIndex } from "./owned";

type Props = {
    notify: Notify;
};

/**
 * 已购应用：账号在 App Store 里买过/领过的应用。
 *
 * 与「本地库」的区别：本地库是**磁盘上有文件**的下载账本；这里是**你有权下载**的清单。
 *
 * 这一页每行只留一个动作「下载最新」。界面里没有版本列表了（搜索页那行的「版本」按钮
 * 已按用户要求移除）：要指定历史版本只能用 MCP / 命令行的 download --external-version-id。
 *
 * 这里**不看本地库**：已经下载过的应用照样可以再点一次重新下载（用户明确要求）。
 * 所以没有「本地已有」标记、没有「下过还要再下吗」的确认，也没有只会下没下过的那种批量。
 */
export default function PurchasedView({ notify }: Props) {
    const [platform, setPlatform] = useState("iphone");
    const [index, setIndex] = useState<OwnedIndex | null>(() => peekOwned("iphone"));
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState("");
    /** 这次刷新没成功，但手上还有一份完整旧清单（旧清单仍可用来判定，只是不新）。 */
    const [staleReason, setStaleReason] = useState("");
    const [query, setQuery] = useState("");
    const [batchBusy, setBatchBusy] = useState(false);

    const load = useCallback(
        async (target: string, force: boolean) => {
            setBusy(true);
            try {
                const index = await loadOwned(target, force);
                // 等待期间账号换了：这份清单是上一个账号的，loadOwned 会拒绝写缓存，
                // 这里也用 peekOwned 再拦一道（否则会把上一个账号的购买记录画上来）。
                if (peekOwned(target) !== index) {
                    return;
                }
                setIndex(index);
                setError("");
                setStaleReason("");
            } catch (reason) {
                // 拉不到就不显示任何「已购」结论 —— 把「不知道」说成「没买过」会误导下载决策。
                // 但如果手上本来有一份**完整**的旧清单，就留着它（完整清单的判定仍然可信），
                // 只把「这次没刷新成功」显式说出来。
                const cached = peekOwned(target);
                if (cached) {
                    setIndex(cached);
                    setError("");
                    setStaleReason(errText(reason));
                } else {
                    setIndex(null);
                    setError(errText(reason));
                    setStaleReason("");
                }
                if (force) {
                    notify(`已购列表没拉到：${errText(reason)}`);
                }
            } finally {
                setBusy(false);
            }
        },
        [notify],
    );

    useEffect(() => {
        void load(platform, false);
    }, [load, platform]);

    // 后端后台刷新完成（或失败）时会广播 purchases:updated：直接换上那份结果，
    // 界面不需要轮询，也就不会再出现「点了刷新要干等 3 秒」的等待感。
    useEffect(
        () =>
            onOwnedUpdated((updated) => {
                if (updated === platform) {
                    setIndex(peekOwned(platform));
                }
            }),
        [platform],
    );

    // 登录 / 退出 / 清凭据之后，后端会作废已购缓存并广播 purchases:invalidated：
    // 手上这份属于上一个账号，立刻丢掉并重拉（不 force：后端已经把缓存清了，
    // 正常请求就会真的去问 Apple）。
    useEffect(
        () =>
            onOwnedInvalidated(() => {
                setIndex(null);
                setError("");
                setStaleReason("");
                void load(platform, false);
            }),
        [load, platform],
    );

    const [taskBusy, setTaskBusy] = useState(false);

    /** 这个 App 是不是已经在队列/下载中（同一个 App 别排两遍）。 */
    const activeTaskFor = async (app: ipa.AppView): Promise<store.Task | null> => {
        try {
            const tasks = await DownloadService.List();

            return (
                tasks.find(
                    (task) =>
                        task.bundleId === app.bundleId &&
                        task.platform === platform &&
                        (task.state === "pending" || task.state === "running"),
                ) ?? null
            );
        } catch {
            return null;
        }
    };

    const request = (app: ipa.AppView): ipa.DownloadRequest =>
        ({
            appId: app.id,
            bundleId: app.bundleId,
            platform,
            externalVersionId: "",
            outputPath: "",
            acquireLicense: true,
        }) as ipa.DownloadRequest;

    /** 单独下「最新版本」：已购清单里点一下就能下，不用先跳到别处。 */
    const startLatest = async (app: ipa.AppView) => {
        setTaskBusy(true);
        try {
            if (await activeTaskFor(app)) {
                notify(`${app.name || app.bundleId} 已经在队列里了，去「下载 · 队列」看进度。`, "info");

                return;
            }

            const task = await DownloadService.Start(request(app));
            notify(`已加入下载队列：${task.name || app.name}（最新版本）—— 进度在「下载 · 队列」里看`, "info");
        } catch (reason) {
            notify(`下载没开始：${errText(reason)}`);
        } finally {
            setTaskBusy(false);
        }
    };

    /**
     * 批量下载这一页的**全部**应用（不管本地有没有下过）。
     *
     * 为什么要先算一笔总账再问：几百兆一个，几十个就是几十 G；顺手再报一下磁盘剩余，
     * 免得下到一半才发现空间不够。已在队列里的还会跳过，免得同一个 App 排两遍。
     */
    const startBatch = async () => {
        if (apps.length === 0) {
            notify("这一页没有可以下载的应用。", "info");

            return;
        }

        const known = apps.filter((app) => (app.fileSizeBytes || 0) > 0);
        const unknown = apps.length - known.length;
        const total = known.reduce((sum, app) => sum + (app.fileSizeBytes || 0), 0);

        let free = 0;
        let freeError = "";

        try {
            free = await DownloadService.FreeSpace();
        } catch (reason) {
            freeError = errText(reason);
        }

        const lines = [`这一页共 ${apps.length} 个应用（已经下过的也会重新下一份）。`];

        if (known.length > 0) {
            lines.push(`其中 ${known.length} 个有体积数据，合计 ${humanBytes(total)}${unknown > 0 ? `；另外 ${unknown} 个体积未知` : ""}。`);
        } else {
            lines.push("体积都未知 —— Apple 不提供时只能下完才知道。");
        }

        if (free > 0) {
            lines.push(
                free < total
                    ? `下载目录所在磁盘只剩 ${humanBytes(free)}，比合计体积还小，可能会下到一半失败。`
                    : `下载目录所在磁盘剩余 ${humanBytes(free)}。`,
            );
        } else if (freeError) {
            lines.push(`剩余空间查不到（${freeError}），这次不做预检。`);
        }

        lines.push("任务会依次排队，一次只跑一个。继续吗？");

        if (!(await confirmDialog(lines.join("\n"), "批量下载本页"))) {
            return;
        }

        setBatchBusy(true);
        try {
            let started = 0;
            const failures: string[] = [];

            for (const app of apps) {
                if (await activeTaskFor(app)) {
                    continue;
                }

                try {
                    await DownloadService.Start(request(app));
                    started += 1;
                } catch (reason) {
                    failures.push(`${app.name || app.bundleId}: ${errText(reason)}`);
                }
            }

            if (failures.length === 0) {
                notify(`已把 ${started} 个应用加入下载队列`, "info");
            } else {
                notify(`加入了 ${started} 个，${failures.length} 个没开始：${failures[0]}`, "error");
            }
        } finally {
            setBatchBusy(false);
        }
    };

    const apps = useMemo(() => {
        const keyword = query.trim().toLowerCase();
        const list = index ? index.apps : [];
        if (!keyword) {
            return list;
        }
        return list.filter(
            (app) =>
                (app.name || "").toLowerCase().includes(keyword) ||
                (app.bundleId || "").toLowerCase().includes(keyword),
        );
    }, [index, query]);

    return (
        <div>
            <div className="card">
                <div className="row" style={{ marginBottom: 8 }}>
                    <span className="muted" title="上次从 Apple 拉取已购清单的时间">
                        {index ? `共 ${index.apps.length} 个` : busy ? "拉取中…" : "—"}
                        {index ? ` · ${new Date(index.fetchedAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })} 更新` : ""}
                        {index?.refreshing ? " · 后台更新中…" : ""}
                    </span>
                    <span className="spacer" />
                    <select value={platform} onChange={(event) => setPlatform(event.target.value)}>
                        {PLATFORMS.map((item) => (
                            <option key={item.value} value={item.value}>
                                {item.label}
                            </option>
                        ))}
                    </select>
                    <button
                        onClick={() => void startBatch()}
                        disabled={batchBusy || busy || apps.length === 0}
                        title="把这一页的应用依次加入下载队列（一次只跑一个；已经下过的也会重新下一份）"
                    >
                        {batchBusy ? "加入中…" : `批量下载本页（${apps.length}）`}
                    </button>
                    <button onClick={() => void load(platform, true)} disabled={busy}>
                        {busy ? "拉取中…" : "刷新"}
                    </button>
                </div>
                <div className="row">
                    <input
                        className="grow"
                        type="text"
                        placeholder="按名称或 bundle id 过滤"
                        value={query}
                        onChange={(event) => setQuery(event.target.value)}
                    />
                </div>
                {staleReason || index?.warning ? (
                    <div className="notice">
                        <h4>这是上一次拉到的清单</h4>
                        <div className="muted">{staleReason || index?.warning}</div>
                        <div className="row">
                            <button onClick={() => void load(platform, true)} disabled={busy}>
                                {busy ? "拉取中…" : "重试"}
                            </button>
                        </div>
                    </div>
                ) : null}
                {error ? (
                    <div className="notice bad">
                        <h4>已购列表没拉到</h4>
                        <div className="muted">{error}</div>
                        <div className="row">
                            <button onClick={() => void load(platform, true)} disabled={busy}>
                                重试
                            </button>
                        </div>
                    </div>
                ) : null}
                {apps.length === 0 ? (
                    <div className="empty">{busy ? "拉取中…" : query ? "没有匹配的应用" : "这个平台下没有已购应用"}</div>
                ) : (
                    <table>
                        <thead>
                            <tr>
                                <th>应用</th>
                                <th>购买时间</th>
                                <th>当前版本 · 体积</th>
                                <th style={{ width: 130 }}>操作</th>
                            </tr>
                        </thead>
                        <tbody>
                            {apps.map((app) => (
                                <tr key={app.id}>
                                    <td>
                                        <div className="app-cell">
                                            <AppIcon url={app.artworkUrl} missingTitle="这个应用没有图标" />
                                            <div className="app-text">
                                                <div>{app.name || `App ${app.id}`}</div>
                                                <div className="muted mono">{app.bundleId || "—"}</div>
                                            </div>
                                        </div>
                                    </td>
                                    <td className="muted">{formatDate(app.purchasedAt)}</td>
                                    <td className="mono">
                                        {app.version || "—"}
                                        <div className="muted">{app.fileSizeBytes ? humanBytes(app.fileSizeBytes) : "—"}</div>
                                    </td>
                                    <td>
                                        <button
                                            className="primary"
                                            disabled={taskBusy}
                                            title="下载 App Store 当前版本"
                                            onClick={() => void startLatest(app)}
                                        >
                                            下载最新
                                        </button>
                                    </td>
                                </tr>
                            ))}
                        </tbody>
                    </table>
                )}
            </div>
        </div>
    );
}
