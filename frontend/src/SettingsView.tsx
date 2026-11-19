import { useCallback, useEffect, useState } from "react";
import { PLATFORMS, SettingsService, errText } from "./api";
import type { Notify } from "./api";
import type { ipa, services } from "./api";
import AccountSection from "./AccountSection";

type Props = {
    notify: Notify;
    env: services.EnvironmentInfo | null;
    auth: services.AuthStatus | null;
    refresh: () => Promise<void>;
};

export default function SettingsView({ notify, env, auth, refresh }: Props) {
    const [settings, setSettings] = useState<ipa.Settings | null>(null);
    const [passphrase, setPassphrase] = useState<services.PassphraseStatus | null>(null);
    const [passphraseInput, setPassphraseInput] = useState("");
    const [busy, setBusy] = useState(false);

    const load = useCallback(async () => {
        try {
            const [current, status] = await Promise.all([
                SettingsService.Get(),
                SettingsService.PassphraseStatus(),
            ]);
            setSettings(current);
            setPassphrase(status);
        } catch (reason) {
            notify(errText(reason));
        }
    }, [notify]);

    useEffect(() => {
        void load();
    }, [load]);

    const patch = (changes: Partial<ipa.Settings>) => {
        setSettings((previous) => (previous ? { ...previous, ...changes } : previous));
    };

    const save = async () => {
        if (!settings) {
            return;
        }
        setBusy(true);
        try {
            await SettingsService.Save(settings);
            notify("已保存", "info");
            await load();
        } catch (reason) {
            notify(errText(reason));
        } finally {
            setBusy(false);
        }
    };

    const chooseDir = async () => {
        try {
            const chosen = await SettingsService.ChooseOutputDir();
            if (chosen) {
                patch({ outputDir: chosen });
                notify(`已选择：${chosen}（记得保存）`, "info");
            }
        } catch (reason) {
            notify(errText(reason));
        }
    };

    const setPass = async () => {
        if (!passphraseInput) {
            return;
        }
        try {
            await SettingsService.SetPassphrase(passphraseInput);
            setPassphraseInput("");
            notify("口令已存进系统凭据库", "info");
            await load();
        } catch (reason) {
            notify(errText(reason));
        }
    };

    const clearPass = async () => {
        try {
            await SettingsService.ClearPassphrase();
            notify("已删除保存的口令", "info");
            await load();
        } catch (reason) {
            notify(errText(reason));
        }
    };

    return (
        <div>
            <AccountSection env={env} auth={auth} refresh={refresh} notify={notify} />

            <div className="card">
                <div className="row">
                    <h3 style={{ margin: 0 }}>下载设置</h3>
                    <span className="spacer" />
                    <button className="primary" onClick={() => void save()} disabled={busy || !settings}>
                        {busy ? "保存中…" : "保存"}
                    </button>
                </div>
                {settings ? (
                    <>
                        <div className="row">
                            <label className="field grow">
                                下载目录
                                <input
                                    className="grow"
                                    type="text"
                                    value={settings.outputDir}
                                    onChange={(event) => patch({ outputDir: event.target.value })}
                                />
                            </label>
                            <button onClick={() => void chooseDir()}>选择…</button>
                        </div>
                        <div className="row">
                            <label className="field">
                                默认平台
                                <select
                                    value={settings.platform}
                                    onChange={(event) => patch({ platform: event.target.value })}
                                >
                                    {PLATFORMS.map((item) => (
                                        <option key={item.value} value={item.value}>
                                            {item.label}
                                        </option>
                                    ))}
                                </select>
                            </label>
                            <label className="field">
                                <span title="仅对免费 App 有效；付费 App 需先在 App Store 里买过">
                                    下载已下架/未购买时自动获取免费许可
                                </span>
                                <input
                                    type="checkbox"
                                    checked={settings.acquireLicense}
                                    onChange={(event) => patch({ acquireLicense: event.target.checked })}
                                />
                            </label>
                            <label className="field">
                                <span>下载完成后在访达里显示</span>
                                <input
                                    type="checkbox"
                                    checked={settings.revealAfterDownload}
                                    onChange={(event) => patch({ revealAfterDownload: event.target.checked })}
                                />
                            </label>
                        </div>
                    </>
                ) : (
                    <div className="empty">读取中…</div>
                )}
            </div>

            <div className="card">
                <h3>凭据后端</h3>
                {passphrase ? (
                    <>
                        <div className="row muted">{passphrase.hint}</div>
                        {/* 期望/实际后端是排障信息，默认收起：普通用户只需要知道要不要口令。 */}
                        <details className="muted" style={{ marginTop: 6, marginBottom: 8 }}>
                            <summary style={{ cursor: "pointer" }}>凭据后端自检</summary>
                            <div className="row" style={{ marginTop: 6 }}>
                                <span>期望</span>
                                <span className="mono">{passphrase.expectedBackend || "—"}</span>
                                <span>实际</span>
                                <span className="mono">{passphrase.pickedBackend || "—"}</span>
                                {passphrase.matchesExpected ? (
                                    <span className="badge good">一致</span>
                                ) : (
                                    <span className="badge warn">不一致</span>
                                )}
                            </div>
                        </details>

                        <div className="row">
                            <label className="field grow" title="保存进系统凭据库，不落明文">
                                文件后端口令
                                <input
                                    type="password"
                                    value={passphraseInput}
                                    placeholder={passphrase.hasPassphrase ? "已保存（重新输入可覆盖）" : "输入口令"}
                                    onChange={(event) => setPassphraseInput(event.target.value)}
                                />
                            </label>
                            <button onClick={() => void setPass()} disabled={!passphraseInput}>
                                保存口令
                            </button>
                            <button className="danger" onClick={() => void clearPass()} disabled={!passphrase.hasPassphrase}>
                                删除口令
                            </button>
                        </div>
                        <div className="row muted">
                            状态：{passphrase.hasPassphrase ? "已保存口令" : "尚未保存口令"} ·
                            系统凭据库
                            {passphrase.systemStoreAvailable
                                ? "可用"
                                : "不可用（可用环境变量 IPAGUI_KEYCHAIN_PASSPHRASE 代替）"}
                        </div>
                    </>
                ) : (
                    <div className="empty">读取中…</div>
                )}
            </div>

        </div>
    );
}
