import * as EnvService from "../wailsjs/go/services/EnvService";
import * as AuthService from "../wailsjs/go/services/AuthService";
import * as CatalogService from "../wailsjs/go/services/CatalogService";
import * as DownloadService from "../wailsjs/go/services/DownloadService";
import * as LibraryService from "../wailsjs/go/services/LibraryService";
import * as SettingsService from "../wailsjs/go/services/SettingsService";
import { EventsOn } from "../wailsjs/runtime/runtime";
import { ipa, services, store } from "../wailsjs/go/models";

export { EnvService, AuthService, CatalogService, DownloadService, LibraryService, SettingsService, EventsOn };
export type { ipa, services, store };

/** 统一的提示回调：所有视图用它把错误/结果送到界面上，而不是各自 alert。 */
export type Notify = (text: string, kind?: "error" | "info") => void;

/**
 * 系统原生确认框（返回是否点了「确定」）。
 *
 * 为什么不用 window.confirm：Wails v2 在 macOS 的 WKWebView 没有实现 JS 对话框的
 * WKUIDelegate，window.confirm 恒返回 false —— 于是「移除记录」这类二次确认就成了
 * 点了没反应。原生对话框在 Go 侧（EnvService.Confirm → runtime.MessageDialog），
 * macOS / Windows / Linux 行为一致。
 *
 * 拿不到窗口上下文时（极早期调用）退回 window.confirm：Windows 上它是可用的。
 */
export async function confirmDialog(message: string, title = "确认操作"): Promise<boolean> {
    try {
        return await EnvService.Confirm(title, message);
    } catch {
        return window.confirm(message);
    }
}

/** Wails 把 Go 的 error 文本作为拒绝原因抛出；统一成可读字符串。 */
export function errText(reason: unknown): string {
    if (reason instanceof Error) {
        return reason.message;
    }
    if (typeof reason === "string") {
        return reason;
    }
    try {
        return JSON.stringify(reason);
    } catch {
        return String(reason);
    }
}

/** 平台取值与后端 `ipa.PlatformChoices` 一致。 */
export const PLATFORMS: Array<{ value: string; label: string }> = [
    { value: "iphone", label: "iPhone" },
    { value: "ipad", label: "iPad" },
    { value: "appletv", label: "Apple TV" },
    { value: "watchos", label: "watchOS" },
    { value: "visionos", label: "visionOS" },
    { value: "macos", label: "macOS" },
];

export function platformLabel(value: string): string {
    const found = PLATFORMS.find((item) => item.value === value);
    return found ? found.label : value || "—";
}

export function humanBytes(bytes: number): string {
    if (!bytes || bytes < 0) {
        return "—";
    }
    const units = ["B", "KB", "MB", "GB", "TB"];
    let value = bytes;
    let index = 0;
    while (value >= 1024 && index < units.length - 1) {
        value /= 1024;
        index += 1;
    }
    return `${value.toFixed(value >= 100 || index === 0 ? 0 : 1)} ${units[index]}`;
}

/** 任务状态 → 中文标签 + 用于着色的类名。 */
export function taskStateLabel(state: string): { text: string; tone: string } {
    switch (state) {
        case "pending":
            return { text: "排队中", tone: "muted" };
        case "running":
            return { text: "下载中", tone: "active" };
        case "done":
            return { text: "已完成", tone: "good" };
        case "failed":
            return { text: "失败", tone: "bad" };
        case "canceled":
            return { text: "已取消", tone: "warn" };
        default:
            return { text: state || "未知", tone: "muted" };
    }
}

/** 本地库条目必须有 path 才能定位；这里统一成「显示用」的短路径。 */
/**
 * 把长长的绝对路径压成「…/文件名」。
 *
 * 表格里显示路径时，用户真正要读的是文件名（App 名称 + 版本 + 账号），
 * 而不是 /Users/xxx/Downloads 这段目录。所以先保住文件名，目录部分超长才省略；
 * 文件名自己就超过额度时，头尾各留一截（至少能看出 App 与扩展名）。
 */
export function shortPath(path: string, max = 48): string {
    if (!path || path.length <= max) {
        return path;
    }

    const at = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
    const name = at >= 0 ? path.slice(at) : path;
    const dir = at >= 0 ? path.slice(0, at) : "";

    if (name.length >= max) {
        const head = name.slice(0, Math.floor(max / 2) - 2);
        const tail = name.slice(name.length - Math.floor(max / 2) + 2);
        return `${head}…${tail}`;
    }

    const room = max - name.length;
    if (dir.length > room) {
        return `${dir.slice(0, Math.max(0, room - 1))}…${name}`;
    }

    return dir + name;
}

export function formatTime(value?: string): string {
    if (!value) {
        return "—";
    }
    const parsed = new Date(value);
    if (Number.isNaN(parsed.getTime())) {
        return value;
    }
    return parsed.toLocaleString();
}

/**
 * 只到「天」的时间：Apple 给的版本发布时间带时分秒，列表里不需要那么细
 * （「更新 2026/9/29」比「更新 2026/9/29 09:36:22」好读）。
 * 解析不了时把原值原样返回，宁可显示得丑也别显示成「无效日期」。
 */
export function formatDate(value?: string): string {
    if (!value) {
        return "—";
    }
    const parsed = new Date(value);
    if (Number.isNaN(parsed.getTime())) {
        return value;
    }
    return parsed.toLocaleDateString();
}
