import { useCallback, useEffect, useState } from "react";
import { confirmDialog, DownloadService, EventsOn, errText, humanBytes, shortPath, taskStateLabel } from "./api";
import type { Notify } from "./api";
import type { store } from "./api";

type Props = {
    notify: Notify;
};

type Progress = {
    taskId: string;
    phase: string;
    current: number;
    total: number;
    percent: number;
    bytesPerSec: number;
    etaSeconds: number;
    indeterminate: boolean;
};

function formatRate(bytesPerSecond: number): string {
    if (!bytesPerSecond || bytesPerSecond <= 0) {
        return "";
    }
    return `${humanBytes(bytesPerSecond)}/s`;
}

function formatETA(seconds: number): string {
    if (!seconds || seconds <= 0) {
        return "";
    }
    if (seconds < 60) {
        return `约 ${Math.round(seconds)} 秒`;
    }
    const minutes = Math.floor(seconds / 60);
    const rest = Math.round(seconds % 60);
    return `约 ${minutes} 分 ${rest} 秒`;
}

export default function DownloadsView({ notify }: Props) {
    const [tasks, setTasks] = useState<store.Task[]>([]);
    const [progress, setProgress] = useState<Record<string, Progress>>({});

    const load = useCallback(async () => {
        try {
            setTasks(await DownloadService.List());
        } catch (reason) {
            notify(errText(reason));
        }
    }, [notify]);

    useEffect(() => {
        void load();

        const offProgress = EventsOn("download:progress", (payload: Progress) => {
            if (!payload || !payload.taskId) {
                return;
            }
            setProgress((previous) => ({ ...previous, [payload.taskId]: payload }));
        });

        const offState = EventsOn("download:state", () => {
            void load();
        });

        return () => {
            offProgress();
            offState();
        };
    }, [load]);

    // 有任务在跑时低频兜底刷新：事件万一丢了，列表也不会永远停在「下载中」。
    useEffect(() => {
        const active = tasks.some((task) => task.state === "running" || task.state === "pending");
        if (!active) {
            return;
        }
        const timer = window.setInterval(() => void load(), 3000);
        return () => window.clearInterval(timer);
    }, [tasks, load]);

    const cancel = async (id: string) => {
        try {
            await DownloadService.Cancel(id);
            notify("已请求取消（半成品 .tmp 会保留）", "info");
        } catch (reason) {
            notify(errText(reason));
        }
    };

    const retry = async (id: string) => {
        try {
            await DownloadService.Retry(id);
            notify("已重新加入队列（会沿用已有的 .tmp 继续下载）", "info");
        } catch (reason) {
            notify(errText(reason));
        }
    };

    /** 把 .tmp 删掉、把占的空间还给磁盘（不删成品 .ipa）。 */
    const clearPartial = async (id: string) => {
        try {
            const size = await DownloadService.PartialSize(id);
            if (size <= 0) {
                notify("没有半成品要清理", "info");

                return;
            }

            if (!(await confirmDialog(`删掉半成品 ${humanBytes(size)}？\n删了之后下一次就是从头下载；已经下完的 .ipa 不受影响。`, "清理半成品"))) {
                return;
            }

            await DownloadService.ResetPartial(id);
            notify(`已删掉半成品，释放 ${humanBytes(size)}`, "info");
            await load();
        } catch (reason) {
            notify(errText(reason));
        }
    };

    // 失败了几个就一起重排：「重试」要一个一个点的话，几十个任务没法用。
    const retryAllFailed = async () => {
        const failed = tasks.filter((task) => task.state === "failed");
        if (failed.length === 0 || !(await confirmDialog(`把 ${failed.length} 个失败的任务重新排进队列？`, "重试失败任务"))) {
            return;
        }

        let ok = 0;
        const failures: string[] = [];

        for (const task of failed) {
            try {
                await DownloadService.Retry(task.id);
                ok += 1;
            } catch (reason) {
                failures.push(`${task.name || task.bundleId}: ${errText(reason)}`);
            }
        }

        if (failures.length === 0) {
            notify(`已重新排队 ${ok} 个任务`, "info");
        } else {
            notify(`重排了 ${ok} 个，${failures.length} 个没排上：${failures[0]}`, "error");
        }

        await load();
    };

    const reveal = async (id: string) => {
        try {
            await DownloadService.Reveal(id);
        } catch (reason) {
            notify(errText(reason));
        }
    };

    const remove = async (id: string) => {
        try {
            await DownloadService.Remove(id);
            await load();
        } catch (reason) {
            notify(errText(reason));
        }
    };

    const showPartial = async (id: string) => {
        try {
            const size = await DownloadService.PartialSize(id);
            notify(size > 0 ? `已缓存半成品：${humanBytes(size)}` : "没有半成品文件", "info");
        } catch (reason) {
            notify(errText(reason));
        }
    };

    // 已完成的任务不再和「已下载」争同一块注意力：默认折叠，展开才有。
    const doneTasks = tasks.filter((task) => task.state === "done");
    const openTasks = tasks.filter((task) => task.state !== "done");

    const renderRow = (task: store.Task) => {
        const state = taskStateLabel(task.state);
        const live = progress[task.id];
        const total = live?.total || task.bytesTotal;
        const current = live?.current || task.bytesDone;
        const percent = live
            ? Math.max(0, Math.min(1, live.percent)) * 100
            : total > 0
              ? Math.min(100, (current / total) * 100)
              : 0;
        const indeterminate = task.state === "running" && (!total || total <= 1);

        return (
            <tr key={task.id}>
                <td>
                    <div>{task.name || task.bundleId || `App ${task.appId}`}</div>
                    <div className="muted mono">
                        {task.platform} {task.versionId ? `· ${task.versionId}` : ""}{" "}
                        {task.bundleId ? `· ${task.bundleId}` : ""}
                    </div>
                    <div className="muted mono" title={task.outputPath}>
                        {shortPath(task.outputPath, 64)}
                    </div>
                </td>
                <td>
                    <span className={`badge ${state.tone}`}>{state.text}</span>
                    {task.errorMessage ? (
                        <div className="tone-bad" style={{ marginTop: 4, maxWidth: 260 }}>
                            {task.errorMessage}
                        </div>
                    ) : null}
                </td>
                <td>
                    <div className={indeterminate ? "progress indeterminate" : "progress"}>
                        <div style={{ width: `${percent}%` }} />
                    </div>
                    <div className="muted mono" style={{ marginTop: 4 }}>
                        {indeterminate
                            ? `正在接收数据${current ? ` · ${humanBytes(current)}` : ""}`
                            : total > 0
                              ? `${humanBytes(current)} / ${humanBytes(total)} · ${percent.toFixed(1)}%`
                              : "—"}
                        {live && !indeterminate && formatRate(live.bytesPerSec)
                            ? ` · ${formatRate(live.bytesPerSec)}`
                            : ""}
                        {live && !indeterminate && formatETA(live.etaSeconds)
                            ? ` · ${formatETA(live.etaSeconds)}`
                            : ""}
                    </div>
                </td>
                <td>
                    <div className="row">
                        {task.state === "running" || task.state === "pending" ? (
                            <button onClick={() => void cancel(task.id)}>取消</button>
                        ) : null}
                        {task.state === "failed" || task.state === "canceled" ? (
                            <>
                                <button className="primary" onClick={() => void retry(task.id)}>
                                    续传/重试
                                </button>
                                <button onClick={() => void showPartial(task.id)}>半成品</button>
                                <button
                                    onClick={() => void clearPartial(task.id)}
                                    title="只删 .tmp，不重新下载；删完点「续传/重试」就是从零开始"
                                >
                                    删半成品
                                </button>
                            </>
                        ) : null}
                        {task.state === "done" ? (
                            <>
                                <button onClick={() => void reveal(task.id)}>打开位置</button>
                                <button
                                    onClick={() => void clearPartial(task.id)}
                                    title="万一还留着 .tmp，顺手把空间要回来"
                                >
                                    清理半成品
                                </button>
                            </>
                        ) : null}
                        <button className="danger" onClick={() => void remove(task.id)}>
                            移除
                        </button>
                    </div>
                </td>
            </tr>
        );
    };

    return (
        <div>
            <div className="card">
                {/* 刷新/重试放在列表上方同一张卡片里：单独一个只装按钮的空卡片既占地又没信息。 */}
                <div className="row" style={{ marginBottom: 8 }}>
                    <span className="spacer" />
                    {tasks.some((task) => task.state === "failed") ? (
                        <button className="primary" onClick={() => void retryAllFailed()}>
                            重试全部失败（{tasks.filter((task) => task.state === "failed").length}）
                        </button>
                    ) : null}
                    <button onClick={() => void load()}>刷新</button>
                </div>
                {openTasks.length === 0 ? (
                    <div className="empty">
                        {doneTasks.length > 0 ? "没有进行中的任务" : "队列是空的，去「搜索」页下载"}
                    </div>
                ) : (
                    <table>
                        <thead>
                            <tr>
                                <th>任务</th>
                                <th>状态</th>
                                <th style={{ width: 240 }}>进度</th>
                                <th style={{ width: 250 }}>操作</th>
                            </tr>
                        </thead>
                        <tbody>{openTasks.map(renderRow)}</tbody>
                    </table>
                )}
            </div>

            {doneTasks.length > 0 ? (
                <details className="card done-fold">
                    <summary>已完成（{doneTasks.length}）</summary>
                    <table>
                        <thead>
                            <tr>
                                <th>任务</th>
                                <th>状态</th>
                                <th style={{ width: 240 }}>进度</th>
                                <th style={{ width: 250 }}>操作</th>
                            </tr>
                        </thead>
                        <tbody>{doneTasks.map(renderRow)}</tbody>
                    </table>
                </details>
            ) : null}
        </div>
    );
}
