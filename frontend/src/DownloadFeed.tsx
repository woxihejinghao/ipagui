import { humanBytes, shortPath } from "./api";
import type { store } from "./api";

export type Progress = {
    taskId: string;
    phase: string;
    current: number;
    total: number;
    percent: number;
    bytesPerSec: number;
    etaSeconds: number;
    indeterminate: boolean;
};

type Props = {
    tasks: store.Task[];
    progress: Record<string, Progress>;
    onCancel: (taskId: string) => void;
    onReveal: (taskId: string) => void;
    onOpenDownloads: () => void;
};

function versionLabel(task: store.Task): string {
    return task.versionId ? `版本 ${task.versionId}` : "最新版本";
}

function formatRate(bytesPerSecond: number): string {
    return bytesPerSecond > 0 ? `${humanBytes(bytesPerSecond)}/s` : "";
}

function formatETA(seconds: number): string {
    if (seconds <= 0) {
        return "";
    }

    if (seconds < 60) {
        return `剩约 ${Math.round(seconds)} 秒`;
    }

    return `剩约 ${Math.floor(seconds / 60)} 分 ${Math.round(seconds % 60)} 秒`;
}

/**
 * 搜索页的「刚点的下载现在怎么样了」提示条。
 *
 * 为什么需要它：点「下载最新」之后，界面只弹一句 6 秒就消失的 toast，其余什么都不变 ——
 * 用户根本看不出下载有没有开始（真机上第一次下载还要先跟 App Store 协商许可，那几秒
 * 连字节都没有，看起来更像「点了没反应」）。这里把队列里的事直接摆在搜索结果上方：
 * 进行中的进度、刚跑完的结果、以及一个去下载页的入口。
 */
export default function DownloadFeed({ tasks, progress, onCancel, onReveal, onOpenDownloads }: Props) {
    if (tasks.length === 0) {
        return null;
    }

    return (
        <div className="card">
            <div className="row">
                <h3 style={{ margin: 0 }}>下载</h3>
                <span className="spacer" />
                <button onClick={onOpenDownloads}>打开下载页</button>
            </div>

            {tasks.map((task) => {
                const snapshot = progress[task.id];
                const total = snapshot?.total || task.bytesTotal;
                const done = snapshot?.current || task.bytesDone;
                const running = task.state === "running";
                // 上游拿到 HTTP 响应头之前进度条的分母是 1（见 ipa.Snapshot）：那时候
                // 连「总共多大」都还不知道，显示 0% 会是假的。许可协商就发生在这一段。
                const waiting = running && total <= 1;
                const percent = running && total > 1 ? (snapshot?.percent ?? done / total) * 100 : 0;

                return (
                    <div key={task.id} style={{ marginTop: 10 }}>
                        <div className="row">
                            <strong>{task.name || task.bundleId}</strong>
                            <span className="muted">{versionLabel(task)}</span>
                            <span className="spacer" />
                            {task.state === "pending" || task.state === "running" ? (
                                <button onClick={() => onCancel(task.id)}>取消</button>
                            ) : null}
                            {task.state === "done" ? (
                                <button onClick={() => onReveal(task.id)}>打开位置</button>
                            ) : null}
                            {task.state === "failed" || task.state === "canceled" ? (
                                <button onClick={onOpenDownloads}>去下载页</button>
                            ) : null}
                        </div>

                        {task.state === "pending" ? (
                            <div className="muted">排队中 —— 前面的任务结束后会自动开始。</div>
                        ) : null}

                        {waiting ? (
                            <>
                                <div className="progress indeterminate">
                                    <div />
                                </div>
                                <div className="muted">
                                    已开始：正在向 App Store 请求下载（免费应用会先获取许可）。
                                    刚开始的几秒还没有字节进度是正常的，别关窗口。
                                </div>
                            </>
                        ) : null}

                        {running && total > 1 ? (
                            <>
                                <div className="progress">
                                    <div style={{ width: `${Math.min(100, Math.max(0, percent))}%` }} />
                                </div>
                                <div className="muted">
                                    正在下载 · {percent.toFixed(1)}% · {humanBytes(done)} / {humanBytes(total)}
                                    {snapshot && formatRate(snapshot.bytesPerSec) ? ` · ${formatRate(snapshot.bytesPerSec)}` : ""}
                                    {snapshot && formatETA(snapshot.etaSeconds) ? ` · ${formatETA(snapshot.etaSeconds)}` : ""}
                                </div>
                            </>
                        ) : null}

                        {task.state === "done" ? (
                            <div className="tone-good">
                                下载完成：<span className="mono" title={task.outputPath}>{shortPath(task.outputPath, 64)}</span>
                            </div>
                        ) : null}

                        {task.state === "failed" ? (
                            <div className="tone-warn">
                                下载失败：{task.errorMessage || task.errorCode || "未知错误"}
                            </div>
                        ) : null}

                        {task.state === "canceled" ? (
                            <div className="muted">已取消（半成品 .tmp 保留，可在下载页续传）。</div>
                        ) : null}
                    </div>
                );
            })}
        </div>
    );
}
