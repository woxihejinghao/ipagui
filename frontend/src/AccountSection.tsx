import { useEffect, useRef, useState } from "react";
import { AuthService, confirmDialog, errText } from "./api";
import type { Notify } from "./api";
import type { services } from "./api";
import { switchAccount } from "./account";

type Props = {
    env: services.EnvironmentInfo | null;
    auth: services.AuthStatus | null;
    refresh: () => Promise<void>;
    notify: Notify;
};

type VerificationMethod = "trusted_device" | "sms";

/** 首次登录前必须确认的风险告知（PRD Story 1；依据上游 #584 / #627 的真实案例）。 */
const RISK_ACK_KEY = "ipagui.risk-acknowledged";

const RISK_POINTS = [
    "Apple 把第三方客户端登录当风险行为：轻则要验证码 / 临时锁定，重则停用账号（上游 #584 / #627 就是如此）。",
    "只调用 Apple 官方接口（与官方 CLI 同一套 API），不模拟登录、不破解、不注入，也不复用他人账号。",
    "任务串行（同一时刻只跑一个下载）；不要在多台机器 / 多个工具上同时登录同一个 Apple ID。",
    "密码只在登录过程中暂存内存，验证码仅用于本次登录；两者都不会写入日志与数据库。",
    "省的是手点的功夫，省不了 Apple 的风控 —— 介意风险就用小号。",
];

const RISK_CONFIRM_TEXT = `登录前请先读完风险告知：\n\n${RISK_POINTS.map(
    (point, index) => `${index + 1}. ${point}`,
).join("\n")}\n\n已了解并自行承担风险，继续登录？`;

function isAuthCodeRequired(text: string): boolean {
    return text.includes("auth_code_required") || text.includes("需要双重认证验证码");
}

/**
 * 账号卡片。验证码使用独立弹窗输入，避免把验证码长期留在设置页表单中。
 *
 * Apple 的登录接口只接受一个 6 位验证码，不提供「选择发送渠道」的参数。
 * 因此「受信任设备 / 手机短信」是取码提示，不会伪造一个 Apple 不支持的发送请求；
 * 用户仍可按 Apple 实际展示的渠道取得验证码，再提交给同一个登录接口。
 */
export default function AccountSection({ env, auth, refresh, notify }: Props) {
    const [busy, setBusy] = useState(false);
    // 登录失败的错误常驻在表单下方：右下角的 toast 只活 6 秒，很容易错过，
    // 而登录又是「失败必须让用户知道下一步做什么」的链路。
    const [loginError, setLoginError] = useState("");

    const [email, setEmail] = useState("");
    const [password, setPassword] = useState("");
    // 「切换账号」退出来的提示：与「未登录」区分开，否则用户不知道自己是被主动切出来的。
    const [switchNotice, setSwitchNotice] = useState("");
    const emailRef = useRef<HTMLInputElement>(null);
    const [authCode, setAuthCode] = useState("");
    const [verificationOpen, setVerificationOpen] = useState(false);
    const [verificationMethod, setVerificationMethod] = useState<VerificationMethod>("trusted_device");
    const [verificationError, setVerificationError] = useState("");
    const dialogRef = useRef<HTMLDialogElement>(null);
    const loginPending = useRef(false);

    useEffect(() => {
        const dialog = dialogRef.current;
        if (verificationOpen && dialog && !dialog.open) {
            dialog.showModal();
        }
    }, [verificationOpen]);

    // 切换账号后把光标放进邮箱框：接下来要输入的是新账号，不要预填旧邮箱
    // （预填很容易让人以为「没切成功」）。
    useEffect(() => {
        if (switchNotice && !auth?.loggedIn) {
            emailRef.current?.focus();
        }
    }, [switchNotice, auth?.loggedIn]);

    const [riskAcked, setRiskAcked] = useState(() => {
        try {
            return window.localStorage.getItem(RISK_ACK_KEY) === "1";
        } catch {
            // 存储不可用（隐私模式等）就每次都问一遍，宁可啰嗦也不要静默跳过。
            return false;
        }
    });
    const [riskOpen, setRiskOpen] = useState(false);

    const acknowledgeRisk = () => {
        try {
            window.localStorage.setItem(RISK_ACK_KEY, "1");
        } catch {
            /* 存不下也只是下次再问一遍 */
        }
        setRiskAcked(true);
        setRiskOpen(false);
    };

    const riskVisible = !riskAcked || riskOpen;

    const finishLogin = async (code: string) => {
        // 上游登录无法中途取消，也不能由 Enter 连续触发并发登录。
        if (loginPending.current) {
            return;
        }
        loginPending.current = true;
        setBusy(true);
        setVerificationError("");
        setLoginError("");
        try {
            const account = await AuthService.Login(email.trim(), password, code);
            notify(`登录成功：${account.email || account.name}`, "info");
            setPassword("");
            setAuthCode("");
            setSwitchNotice("");
            setVerificationOpen(false);
            await refresh();
        } catch (reason) {
            const text = errText(reason);
            if (!code && isAuthCodeRequired(text)) {
                setAuthCode("");
                setVerificationMethod("trusted_device");
                setVerificationOpen(true);
            } else if (code) {
                setAuthCode("");
                setVerificationError(`本次验证未完成：${text}`);
            } else {
                setLoginError(text);
                // toast 也留着：登录可能跑几分钟，用户切到别的标签页时只有 toast 能看到结果。
                notify(text);
            }
        } finally {
            loginPending.current = false;
            setBusy(false);
        }
    };

    const doLogin = async () => {
        if (loginPending.current) {
            return;
        }

        // 首次点击登录：先把风险说清楚，用户点掉对话框才算数。
        if (!riskAcked) {
            if (!(await confirmDialog(RISK_CONFIRM_TEXT, "登录前先看一眼"))) {
                setRiskOpen(true);

                return;
            }
            acknowledgeRisk();
        }

        // 不要靠 disabled 来「阻止提交」：按钮点下去没反应，用户只会以为程序坏了。
        // 这里给出明确的中文错误，并常驻在表单下方。
        if (!email.trim()) {
            setLoginError("请填写 Apple ID（邮箱或手机号）");

            return;
        }
        if (!password) {
            setLoginError("请填写 Apple ID 密码");

            return;
        }

        await finishLogin("");
    };

    const submitVerification = async () => {
        const code = authCode.trim();
        if (!/^\d{6}$/.test(code)) {
            setVerificationError("验证码必须是 6 位数字");
            return;
        }

        await finishLogin(code);
    };

    const closeVerification = () => {
        if (loginPending.current) {
            return;
        }
        setVerificationOpen(false);
        setAuthCode("");
        setVerificationError("");
    };

    const doSwitch = async () => {
        if (busy) {
            return;
        }

        setBusy(true);

        try {
            if (!(await switchAccount(auth?.email || ""))) {
                return;
            }

            // 不预填旧邮箱：切号要输的是新账号，预填旧值容易让人以为「没切成功」。
            setEmail("");
            setPassword("");
            setLoginError("");
            setSwitchNotice("已退出当前账号，请输入新账号登录（本机凭据仍保留）");
            notify("已退出当前账号，请输入新账号登录", "info");
            await refresh();
        } catch (reason) {
            notify(errText(reason));
        } finally {
            setBusy(false);
        }
    };

    const doLogout = async () => {
        try {
            await AuthService.Logout();
            notify("已退出登录（本机凭据仍保留，重新登录即可继续）", "info");
            await refresh();
        } catch (reason) {
            notify(errText(reason));
        }
    };

    const doRevoke = async () => {
        if (
            !(await confirmDialog(
                "这会删除本机保存的登录凭据（系统钥匙串），与官方 ipatool CLI / MCP helper 共用的凭据也会一起登出。\n\n" +
                    "注意：它不会通知 Apple，其他机器上的会话不受影响；本机会同时清掉状态目录 cookies 里的账号授权 cookie（设备身份保留）。继续？",
                "清除本机凭据",
            ))
        ) {
            return;
        }
        try {
            await AuthService.Revoke();
            notify("已清除本机凭据（Apple 侧会话未受影响）", "info");
            await refresh();
        } catch (reason) {
            notify(errText(reason));
        }
    };

    return (
        <>
            <div className="card">
                <h3>账号</h3>
                {auth?.loggedIn ? (
                    <div>
                        <div className="row">
                            <span className="badge good">已登录</span>
                            <span>{auth.email}</span>
                            <span className="muted">{auth.name}</span>
                            <span className="muted">storefront {auth.storeFront || "—"}</span>
                            <span className="spacer" />
                            <button disabled={busy} onClick={() => void doSwitch()}>
                                切换账号
                            </button>
                            <button onClick={() => void doLogout()}>退出登录</button>
                            <button
                                className="danger"
                                title="删除本机保存的登录凭据（不会向 Apple 撤销令牌）"
                                onClick={() => void doRevoke()}
                            >
                                清除本机凭据
                            </button>
                        </div>
                        {auth.message ? <div className="row muted">{auth.message}</div> : null}
                    </div>
                ) : (
                    <div>
                        <div className="row muted">
                            未登录：搜索与下载都需要先登录。
                        </div>
                        {auth?.expired ? (
                            <div className="notice bad">
                                App Store 令牌已过期，当前账号已自动退出。重新登录即可继续（本机凭据仍在钥匙串里，
                                不需要重新下载签名器）。
                            </div>
                        ) : null}
                        {auth?.loggedOut && !switchNotice && !auth?.expired ? (
                            <div className="row muted">{auth.message}</div>
                        ) : null}
                        {switchNotice && !auth?.expired ? (
                            <div className="notice good">{switchNotice}</div>
                        ) : null}

                        {riskVisible ? (
                            <div className="notice bad">
                                <h4>⚠️ 先看这个：第三方登录有停用 Apple ID 的风险</h4>
                                <ul>
                                    {RISK_POINTS.map((point) => (
                                        <li key={point}>{point}</li>
                                    ))}
                                </ul>
                                <div className="row">
                                    {riskAcked ? (
                                        <button onClick={() => setRiskOpen(false)}>收起</button>
                                    ) : (
                                        <button className="primary" onClick={acknowledgeRisk}>
                                            我已了解，去登录
                                        </button>
                                    )}
                                </div>
                            </div>
                        ) : (
                            <div className="row muted">
                                已确认登录风险。
                                <button onClick={() => setRiskOpen(true)}>再看一遍</button>
                            </div>
                        )}

                        {/* 用 form 包起来，在输入框里按 Enter 也能提交；密码为空时
                            按钮不再禁用，改由 doLogin 给出可读的错误提示。 */}
                        <form
                            className="row"
                            onSubmit={(event) => {
                                event.preventDefault();
                                void doLogin();
                            }}
                        >
                            <label className="field">
                                Apple ID
                                <input
                                    ref={emailRef}
                                    type="text"
                                    value={email}
                                    placeholder="you@example.com 或手机号"
                                    onChange={(event) => {
                                        setEmail(event.target.value);
                                        setLoginError("");
                                    }}
                                />
                            </label>
                            <label className="field">
                                密码
                                <input
                                    type="password"
                                    value={password}
                                    placeholder="密码"
                                    onChange={(event) => {
                                        setPassword(event.target.value);
                                        setLoginError("");
                                    }}
                                />
                            </label>
                            <button className="primary" type="submit" disabled={busy}>
                                {busy ? "登录中…" : "登录"}
                            </button>
                        </form>
                        {loginError ? (
                            <div className="notice bad" role="alert">
                                {loginError}
                            </div>
                        ) : null}
                        {busy ? (
                            <div className="notice">
                                <div className="row">
                                    <span>正在登录…</span>
                                    <span className="muted">
                                        首次登录要准备 macOS 27 系统资产，这一步没有真实百分比可用。
                                    </span>
                                </div>
                                <div className="progress indeterminate" style={{ marginTop: 8 }}>
                                    <div />
                                </div>
                                <div className="row muted" style={{ marginTop: 8 }}>
                                    要从 Apple 下载十几 GB 系统资产，视网络可能十几分钟，请勿关闭窗口；
                                    失败会从头下载。缓存位置：{" "}
                                    <span className="mono">
                                        {env?.cacheDir ? `${env.cacheDir}/ipatool/sap/` : "<用户缓存目录>/ipatool/sap/"}
                                    </span>
                                </div>
                            </div>
                        ) : null}
                        {busy ? null : (
                            <div className="row muted">
                                首次登录要下载 macOS 27 系统资产（十几 GB），会比较慢，之后走缓存。
                                需要验证时会弹出验证码输入框。
                            </div>
                        )}
                    </div>
                )}
            </div>

            {verificationOpen ? (
                <dialog
                    ref={dialogRef}
                    className="modal"
                    aria-labelledby="verification-title"
                    aria-describedby="verification-description"
                    onCancel={(event) => {
                        event.preventDefault();
                        closeVerification();
                    }}
                >
                        <div className="modal-header">
                            <div>
                                <h3 id="verification-title">输入 Apple 验证码</h3>
                                <div className="muted">Apple 要求进行双重认证，请从 Apple 显示的验证渠道获取 6 位验证码</div>
                            </div>
                            <button className="icon-button" disabled={busy} onClick={closeVerification} aria-label="关闭">
                                ×
                            </button>
                        </div>

                        <div className="verification-methods" role="tablist" aria-label="验证码来源">
                            <button
                                className={verificationMethod === "trusted_device" ? "method active" : "method"}
                                onClick={() => setVerificationMethod("trusted_device")}
                                role="tab"
                                aria-selected={verificationMethod === "trusted_device"}
                            >
                                受信任设备
                            </button>
                            <button
                                className={verificationMethod === "sms" ? "method active" : "method"}
                                onClick={() => setVerificationMethod("sms")}
                                role="tab"
                                aria-selected={verificationMethod === "sms"}
                            >
                                手机短信
                            </button>
                        </div>

                        <div className="notice" id="verification-description">
                            {verificationMethod === "trusted_device" ? (
                                <>
                                    请在你的 iPhone、iPad 或 Mac 上查看 Apple 的登录通知，允许登录后填写显示的验证码。
                                </>
                            ) : (
                                <>
                                    如果 Apple 的验证页面提供“发送到受信任电话号码”等选项，请在那里选择短信方式，然后填写手机收到的验证码。
                                </>
                            )}
                        </div>
                        <div className="row muted verification-note">
                            发送渠道由 Apple 账号安全策略决定，本应用不会代替 Apple 发送短信；这里可以切换取码提示。
                        </div>
                        {verificationError ? <div className="notice bad">{verificationError}</div> : null}

                        <label className="field verification-code-field">
                            验证码
                            <input
                                type="text"
                                inputMode="numeric"
                                autoComplete="one-time-code"
                                autoFocus
                                maxLength={6}
                                value={authCode}
                                placeholder="6 位数字"
                                onChange={(event) => setAuthCode(event.target.value.replace(/\D/g, "").slice(0, 6))}
                                onKeyDown={(event) => {
                                    if (event.key === "Enter") {
                                        void submitVerification();
                                    }
                                }}
                            />
                        </label>
                        <div className="modal-actions">
                            <button disabled={busy} onClick={closeVerification}>
                                取消
                            </button>
                            <button className="primary" disabled={busy || authCode.length !== 6} onClick={() => void submitVerification()}>
                                {busy ? "验证中…" : "提交验证码"}
                            </button>
                        </div>
                </dialog>
            ) : null}
        </>
    );
}
