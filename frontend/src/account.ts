import { AuthService, confirmDialog } from "./api";

/**
 * 「切换账号」的确认 + 执行。
 *
 * 为什么抽成共享函数：顶部栏与设置页都有入口，两处的确认文案、失败处理必须一致 ——
 * 否则很容易变成「顶部点了立刻退登、设置页点了才确认」这种不同步的行为。
 *
 * 语义上它等于「退出登录」：清内存登录态、置登出标记、作废已购缓存、广播
 * `purchases:invalidated`；**不动**钥匙串（官方 CLI / MCP helper 仍可用），也**不清**
 * cookie —— 新账号登录成功时 `Session.Login` 会按新 DSID 剪掉旧账号的 `mz_at*`。
 *
 * 返回 false 表示用户取消了确认框（调用方不要再刷新/跳转）。
 */
export async function switchAccount(currentEmail: string): Promise<boolean> {
    const who = currentEmail ? `当前账号 ${currentEmail}` : "当前账号";

    const confirmed = await confirmDialog(
        `${who} 将被退出，回到登录表单用另一个 Apple ID 登录。\n\n` +
            "· 本机凭据仍保留在系统钥匙串里，切回旧账号不需要重新下载签名器；\n" +
            "· 已购清单会按新账号重新拉取；\n" +
            "· 正在下载的任务不会中断，会沿用启动时的账号跑完；\n" +
            "· 官方 ipatool CLI / MCP helper 与 GUI 共用同一份凭据，切换后一起变化。\n\n" +
            "继续切换？",
        "切换账号",
    );

    if (!confirmed) {
        return false;
    }

    await AuthService.SwitchAccount();

    return true;
}
