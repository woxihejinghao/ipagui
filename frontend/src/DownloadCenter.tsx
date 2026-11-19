import { useState } from "react";
import DownloadsView from "./DownloadsView";
import LibraryView from "./LibraryView";
import type { Notify } from "./api";

type Props = {
    notify: Notify;
};

type SubTab = "queue" | "library";

const SUBTABS: Array<{ id: SubTab; label: string }> = [
    { id: "queue", label: "队列" },
    { id: "library", label: "已下载" },
];

/**
 * 「下载」页：一个入口，两层视图。
 *
 * 队列是**过程**（任务：排队/下载/失败/重试），已下载是**结果**（本地账本：
 * 磁盘上有哪几份 IPA）。数据上仍是两张表，界面上不再各占一个顶层 tab ——
 * 完成的任务不会再和账本分成两个页面重复展示。
 */
export default function DownloadCenter({ notify }: Props) {
    const [subtab, setSubtab] = useState<SubTab>("queue");

    return (
        <div>
            <div className="subtabs">
                {SUBTABS.map((item) => (
                    <button
                        key={item.id}
                        className={item.id === subtab ? "subtab active" : "subtab"}
                        onClick={() => setSubtab(item.id)}
                    >
                        {item.label}
                    </button>
                ))}
            </div>

            {subtab === "queue" ? (
                <DownloadsView notify={notify} />
            ) : (
                <LibraryView notify={notify} onQueued={() => setSubtab("queue")} />
            )}
        </div>
    );
}
