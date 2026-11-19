import { useCallback, useEffect, useRef, useState } from "react";
import { confirmDialog, DownloadService, EventsOn, LibraryService, errText, formatTime, humanBytes, platformLabel, shortPath } from "./api";
import type { Notify } from "./api";
import type { ipa, services, store } from "./api";
import AppIcon from "./AppIcon";

type Props = {
    notify: Notify;
    /** 重新下载排进队列后切回「队列」页 —— 不然用户留在已下载里看不到进度。 */
    onQueued?: () => void;
};

type IconState = {
    loading?: boolean;
    uri?: string;
    error?: string;
};

export default function LibraryView({ notify, onQueued }: Props) {
    const [entries, setEntries] = useState<services.LibraryEntry[]>([]);
    const [query, setQuery] = useState("");
    const [platform, setPlatform] = useState("");
    const [busy, setBusy] = useState(false);

    // 账本里只有路径：图标得从磁盘上那个包里的 iTunesArtwork 读出来（后端做）。
    const [icons, setIcons] = useState<Record<number, IconState>>({});
    const requested = useRef<Set<number>>(new Set());

    const load = useCallback(async () => {
        setBusy(true);
        try {
            const filter = { Query: query.trim(), Platform: platform } as store.EntryFilter;
            setEntries(await LibraryService.List(filter));
        } catch (reason) {
            notify(errText(reason));
        } finally {
            setBusy(false);
        }
    }, [notify, query, platform]);

    useEffect(() => {
        void load();
    }, [load]);

    useEffect(() => {
        // 下载完成会广播 library:changed，本地库自动跟上。
        const off = EventsOn("library:changed", () => {
            void load();
        });

        return () => off();
    }, [load]);

    /**
     * 读一份包的图标。一份一份来（下面那个循环是顺序 await）：每个请求都会去开一个大文件，
     * 并发打过去等于同时开一堆 GB 级文件；而且图标是装饰，慢一点无所谓。
     */
    const loadIcon = useCallback(
        async (entry: services.LibraryEntry) => {
            setIcons((prev) => ({ ...prev, [entry.id]: { loading: true } }));
            try {
                const uri = await LibraryService.Artwork(entry.id);
                setIcons((prev) => ({ ...prev, [entry.id]: { uri } }));
            } catch (reason) {
                setIcons((prev) => ({ ...prev, [entry.id]: { error: errText(reason) } }));
            }
        },
        [],
    );

    useEffect(() => {
        let cancelled = false;

        const loadAll = async () => {
            for (const entry of entries) {
                if (cancelled) {
                    return;
                }
                // 文件都不在了就别去读了，读也只会拿到一个错误。
                if (!entry.exists || requested.current.has(entry.id)) {
                    continue;
                }
                requested.current.add(entry.id);
                await loadIcon(entry);
            }
        };

        void loadAll();

        return () => {
            cancelled = true;
        };
    }, [entries, loadIcon]);

    /**
     * 文件没了就从 App Store 再下一份。
     *
     * 账本里的版本列有两种形态：历史版本记的是 Apple 的版本标识（一串数字），
     * 「下载最新」那次记的是显示版本号（8.0.50）—— 后者当不了版本标识，只能重下最新版本。
     * 这个区别必须在确认框里说清楚，否则用户以为下的是老版本。
     */
    const redownload = async (entry: services.LibraryEntry) => {
        const versionID = /^\d{7,}$/.test(entry.version || "") ? entry.version : "";
        const what = versionID
            ? `历史版本 ${versionID}`
            : `最新版本${entry.version ? `（账本记的显示版本是 ${entry.version}）` : ""}`;

        if (!(await confirmDialog(`重新下载 ${entry.name || entry.bundleId} 的${what}？\n会存进设置里的下载目录。`, "重新下载"))) {
            return;
        }

        try {
            await DownloadService.Start({
                appId: entry.appId,
                bundleId: entry.bundleId,
                platform: entry.platform,
                externalVersionId: versionID,
                outputPath: "",
                acquireLicense: true,
            } as ipa.DownloadRequest);

            notify(`已加入下载队列：${entry.name || entry.bundleId}（${what}）—— 进度在「下载 · 队列」里看`, "info");
            onQueued?.();
        } catch (reason) {
            notify(`重新下载没开始：${errText(reason)}`);
        }
    };

    const reveal = async (id: number) => {
        try {
            await LibraryService.Reveal(id);
        } catch (reason) {
            notify(errText(reason));
        }
    };

    const remove = async (id: number) => {
        if (!(await confirmDialog("只删除这里的记录，磁盘上的 IPA 不会被删除。继续？", "移除记录"))) {
            return;
        }
        try {
            await LibraryService.Delete(id);
            await load();
        } catch (reason) {
            notify(errText(reason));
        }
    };

    return (
        <div>
            <div className="card">
                {/* 工具栏与列表同一张卡片：单独一个只装控件的卡片没必要。 */}
                <div className="row" style={{ marginBottom: 8 }}>
                    <span className="spacer" />
                    <input
                        type="text"
                        value={query}
                        placeholder="按名称 / bundle id / 版本过滤"
                        className="grow"
                        style={{ maxWidth: 280 }}
                        onChange={(event) => setQuery(event.target.value)}
                    />
                    <select value={platform} onChange={(event) => setPlatform(event.target.value)}>
                        <option value="">全部平台</option>
                        <option value="iphone">iPhone</option>
                        <option value="ipad">iPad</option>
                        <option value="appletv">Apple TV</option>
                        <option value="watchos">watchOS</option>
                        <option value="visionos">visionOS</option>
                        <option value="macos">macOS</option>
                    </select>
                    <button onClick={() => void load()} disabled={busy}>
                        {busy ? "读取中…" : "刷新"}
                    </button>
                </div>
                {entries.length === 0 ? (
                    <div className="empty">还没有记录</div>
                ) : (
                    <table>
                        <thead>
                            <tr>
                                <th>应用</th>
                                <th>平台 / 版本</th>
                                <th>大小</th>
                                <th>下载时间</th>
                                <th>文件</th>
                                <th style={{ width: 300 }}>操作</th>
                            </tr>
                        </thead>
                        <tbody>
                            {entries.map((entry) => (
                                <tr key={entry.id}>
                                    <td>
                                        <div className="app-cell">
                                            <AppIcon
                                                url={icons[entry.id]?.uri}
                                                missingTitle={
                                                    icons[entry.id]?.error ??
                                                    (entry.exists
                                                        ? "还没有读图标"
                                                        : "文件已不在磁盘上，读不到图标")
                                                }
                                            />
                                            <div className="app-text">
                                                <div>{entry.name || entry.bundleId || `App ${entry.appId}`}</div>
                                                <div className="muted mono">{shortPath(entry.path, 60)}</div>
                                            </div>
                                        </div>
                                    </td>
                                    <td className="mono">
                                        {platformLabel(entry.platform)}
                                        <div className="muted">{entry.version || "—"}</div>
                                    </td>
                                    <td className="mono">{humanBytes(entry.sizeBytes)}</td>
                                    <td className="muted">{formatTime(entry.downloadedAt)}</td>
                                    <td title={entry.sha256 ? `sha256 ${entry.sha256}` : undefined}>
                                        {entry.exists ? (
                                            <span className="badge good">在</span>
                                        ) : (
                                            <span className="badge bad">已丢失</span>
                                        )}
                                        {entry.note ? <div className="tone-warn">{entry.note}</div> : null}
                                    </td>
                                    <td>
                                        <div className="row">
                                            <button onClick={() => void reveal(entry.id)} disabled={!entry.exists}>
                                                打开位置
                                            </button>
                                        </div>
                                        <div className="row" style={{ marginTop: 6 }}>
                                            <button
                                                className={entry.exists ? undefined : "primary"}
                                                onClick={() => void redownload(entry)}
                                                title="从 App Store 再下一份到这个账本里"
                                            >
                                                重新下载
                                            </button>
                                            <button className="danger" onClick={() => void remove(entry.id)}>
                                                移除记录
                                            </button>
                                        </div>
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
