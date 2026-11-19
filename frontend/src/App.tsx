import { useCallback, useEffect, useState } from "react";
import { AuthService, EnvService, EventsOn, errText } from "./api";
import type { Notify } from "./api";
import type { services } from "./api";
import { switchAccount } from "./account";
import DownloadCenter from "./DownloadCenter";
import PurchasedView from "./PurchasedView";
import SearchView from "./SearchView";
import SettingsView from "./SettingsView";

type TabID = "search" | "purchased" | "downloads" | "settings";

const TABS: Array<{ id: TabID; label: string }> = [
    { id: "search", label: "搜索" },
    { id: "purchased", label: "已购" },
    { id: "downloads", label: "下载" },
    { id: "settings", label: "设置" },
];

export default function App() {
    const [tab, setTab] = useState<TabID>("search");
    const [env, setEnv] = useState<services.EnvironmentInfo | null>(null);
    const [auth, setAuth] = useState<services.AuthStatus | null>(null);
    const [toast, setToast] = useState<{ text: string; kind: "error" | "info"; id: number } | null>(null);

    const notify = useCallback<Notify>((text, kind = "error") => {
        setToast({ text, kind, id: Date.now() });
    }, []);

    const refreshEnv = useCallback(async () => {
        try {
            const [info, status] = await Promise.all([EnvService.Info(), AuthService.Status()]);
            setEnv(info);
            setAuth(status);
        } catch (reason) {
            notify(errText(reason));
        }
    }, [notify]);

    useEffect(() => {
        void refreshEnv();
    }, [refreshEnv]);

    useEffect(() => {
        // 留一条「界面真的渲染了」的痕迹：WebView 空白时 Go 侧什么都收不到，
        // 有了这一行才能区分「界面没起来」和「界面起来了、后端有问题」。
        void EnvService.ClientReady().catch(() => undefined);
    }, []);

    useEffect(() => {
        if (!toast) {
            return;
        }
        const timer = window.setTimeout(() => setToast(null), 6000);
        return () => window.clearTimeout(timer);
    }, [toast]);

    useEffect(() => {
        // 后端检测到 App Store 令牌过期时会自己退掉登录态，并广播 auth:state
        // （phase=expired）。这里把界面跟着切过去：刷新顶部/账号卡片，跳设置页
        // 让用户直接看到登录表单 —— 否则用户只看到一次次「令牌已失效」的报错。
        return EventsOn("auth:state", (payload: { phase?: string; message?: string }) => {
            if (payload?.phase !== "expired") {
                return;
            }

            notify(payload.message || "登录凭据已过期，请重新登录", "info");
            void refreshEnv();
            setTab("settings");
        });
    }, [notify, refreshEnv]);

    // 顶部栏的「切换账号」：确认 → 退出现在账号 → 跳到设置页的登录表单。
    // 与设置页那颗按钮共用 switchAccount()，保证两处行为一致。
    const handleSwitch = useCallback(async () => {
        try {
            if (!(await switchAccount(auth?.email || ""))) {
                return;
            }
            await refreshEnv();
            setTab("settings");
            notify("已退出当前账号，请输入新账号登录", "info");
        } catch (reason) {
            notify(errText(reason));
        }
    }, [auth?.email, notify, refreshEnv]);

    return (
        <div className="app">
            <div className="topbar">
                <span className="title">IPA GUI</span>
                <span className="badge">{env ? `v${env.version}` : "加载中…"}</span>
                {env && !env.initialized ? <span className="badge bad">环境未就绪</span> : null}
                <span className="spacer" />
                <span className="who">
                    {auth?.loggedIn
                        ? `${auth.email || "已登录"}${auth.storeFront ? `（${auth.storeFront}）` : ""}`
                        : "未登录"}
                </span>
                {auth?.loggedIn ? (
                    <button onClick={() => void handleSwitch()}>切换账号</button>
                ) : (
                    <button onClick={() => setTab("settings")}>登录</button>
                )}
                <button onClick={() => void refreshEnv()}>刷新</button>
            </div>

            <div className="tabs">
                {TABS.map((item) => (
                    <button
                        key={item.id}
                        className={item.id === tab ? "tab active" : "tab"}
                        onClick={() => setTab(item.id)}
                    >
                        {item.label}
                    </button>
                ))}
            </div>

            <div className="content">
                {tab === "search" ? (
                    <SearchView notify={notify} onOpenDownloads={() => setTab("downloads")} />
                ) : null}
                {tab === "purchased" ? <PurchasedView notify={notify} /> : null}
                {tab === "downloads" ? <DownloadCenter notify={notify} /> : null}
                {tab === "settings" ? (
                    <SettingsView notify={notify} env={env} auth={auth} refresh={refreshEnv} />
                ) : null}
            </div>

            {toast ? (
                <div className={toast.kind === "info" ? "toast info" : "toast"} onClick={() => setToast(null)}>
                    {toast.text}
                </div>
            ) : null}
        </div>
    );
}
