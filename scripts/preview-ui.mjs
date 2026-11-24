#!/usr/bin/env node
// 在无头 Chrome 里把 frontend/preview.html 逐页走一遍，断言可见文本并截图。
//
// 为什么需要它：界面的大部分状态要有 Apple 账号才看得到（登录 / 风险告知 / 版本元数据 /
// 下载进度），而这些状态恰好是 PRD 里要验的。preview.html 伪造 window.go / window.runtime，
// 这个脚本负责点、看、存图 —— 不需要账号，也不需要真人解锁桌面。
//
//   cd frontend && npx vite --port 5199 --strictPort     # 一个终端起着
//   node scripts/preview-ui.mjs                          # 另一个终端跑
//
// 参数：--port 5199（dev server 端口）、--out /tmp/ipagui-shots（截图目录）
// 退出码非 0 表示有断言没过。

import { spawn } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";

const CHROME = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const DEBUG_PORT = 9334;

const args = process.argv.slice(2);
const option = (name, fallback) => {
    const at = args.indexOf(`--${name}`);
    return at >= 0 && args[at + 1] ? args[at + 1] : fallback;
};
const PORT = option("port", "5199");
const OUT = option("out", "/tmp/ipagui-shots");
const URL = `http://localhost:${PORT}/preview.html`;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function waitFor(url, tries = 80) {
    for (let i = 0; i < tries; i++) {
        try {
            const res = await fetch(url);
            if (res.ok) return res;
        } catch { /* 还没起来 */ }
        await sleep(250);
    }
    throw new Error(`${url} 没起来`);
}

function connect(url) {
    return new Promise((resolve, reject) => {
        const ws = new WebSocket(url);
        const pending = new Map();
        let nextId = 1;
        ws.onopen = () => resolve({
            send(method, params = {}) {
                const id = nextId++;
                ws.send(JSON.stringify({ id, method, params }));
                return new Promise((res, rej) => pending.set(id, { res, rej }));
            },
            close: () => ws.close(),
        });
        ws.onerror = () => reject(new Error("CDP WebSocket 连不上"));
        ws.onmessage = (event) => {
            const msg = JSON.parse(event.data);
            if (msg.id && pending.has(msg.id)) {
                const { res, rej } = pending.get(msg.id);
                pending.delete(msg.id);
                msg.error ? rej(new Error(JSON.stringify(msg.error))) : res(msg.result);
            }
        };
    });
}

const failures = [];

async function main() {
    mkdirSync(OUT, { recursive: true });
    await waitFor(`http://localhost:${PORT}/preview.html`);

    const chrome = spawn(CHROME, [
        "--headless=new", "--hide-scrollbars", "--no-first-run", "--no-default-browser-check",
        "--disable-extensions", "--force-device-scale-factor=1",
        `--remote-debugging-port=${DEBUG_PORT}`, "--user-data-dir=/tmp/ipagui-chrome-preview",
        "--window-size=1440,1100", "about:blank",
    ], { stdio: "ignore" });

    try {
        await waitFor(`http://127.0.0.1:${DEBUG_PORT}/json/version`);
        const target = await (await fetch(
            `http://127.0.0.1:${DEBUG_PORT}/json/new?${URL}`, { method: "PUT" })).json();
        const client = await connect(target.webSocketDebuggerUrl);
        await client.send("Page.enable");
        await client.send("Runtime.enable");
        await client.send("Emulation.setDeviceMetricsOverride",
            { width: 1440, height: 1100, deviceScaleFactor: 1, mobile: false });
        await client.send("Page.navigate", { url: URL });
        await sleep(2500);

        const evaluate = async (expression) => {
            const r = await client.send("Runtime.evaluate",
                { expression, awaitPromise: true, returnByValue: true });
            if (r.exceptionDetails) {
                throw new Error(r.exceptionDetails.exception?.description || "页面里执行失败");
            }
            return r.result.value;
        };

        // 顶部 tab、顶栏的「刷新」和内容区按钮都可能重名（「搜索」「下载」），按区域找。
        const click = (label, scope = ".content") => evaluate(
            `(()=>{const b=[...document.querySelectorAll(${JSON.stringify(scope + " button")})]
                .find(b=>b.textContent.trim()===${JSON.stringify(label)});
              if(!b) return 'NOT FOUND'; b.click(); return 'clicked';})()`);
        const clickTab = (label) => click(label, ".tabs");
        // 「下载」页里的二级视图（队列 / 已下载）：先保证在下载页，再点胶囊。
        const clickSubTab = async (label) => {
            const top = await clickTab("下载");
            if (top !== "clicked") {
                return top;
            }
            await sleep(150);
            return await click(label, ".subtabs");
        };
        const clickButton = (label) => click(label);
        const type = (selector, value) => evaluate(
            `(()=>{const el=document.querySelector(${JSON.stringify(selector)}); if(!el) return 'NOT FOUND';
              const set=Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype,'value').set;
              set.call(el,${JSON.stringify(value)}); el.dispatchEvent(new Event('input',{bubbles:true}));
              return el.value;})()`);

        // 步骤 11 点出来的那一单，步骤 12 要接着推进它（id 是后端生成的，不能写死）。
        let clickedTaskId = "";

        const steps = [
            {
                name: "01 搜索页初始",
                expect: ["hejinghao@changzhi.top", "区域", "条数", "搜索"],
            },
            {
                name: "02 搜索结果（图标 + 更新时间 + 体积 + 正在下的那一行有状态）",
                // 桩里 微信 有一个 running 任务：进页面时提示条就该是满的（这是「刷新/切回来也知道
                // 下载还在跑」的那条路，状态来自 DownloadService.List，不是来自本页的点击），
                // 而且那一行的下载按钮要显示状态、不能还是「下载最新」。
                expect: ["com.tencent.xin", "免费", "922 MB", "打开下载页",
                    "正在下载 · 43.7% · 201 MB / 460 MB", "下载中 43%",
                    '"icons":1', '"placeholders":0', "data:image/png;base64", '"updatedCells":1', '"updateMatches":true'],
                async run() {
                    await type('input[placeholder^="应用名"]', "微信");
                    await clickButton("搜索");
                    await sleep(1500);

                    // 图标与体积是这一轮新加的：断言 DOM 里真的有 <img>，且 src 就是后端给的那个 URL。
                    //
                    // 「更新 2026/9/29」这条断言故意在页面里现算期望值（用同一条 ISO 串），
                    // 而不是把日期写死在 expect 里 —— 否则断言会依赖跑它的机器的时区，
                    // 换个 TZ 就红/绿乱跳。期望值算出来后是拿去和**DOM 里实际渲染的文本**比，
                    // 所以它不是自证：后端少给 updatedAt 时 DOM 会是「更新 —」，这里就是 false。
                    return await evaluate(`(() => {
                        const expected = "更新 " + new Date("2026-09-29T01:36:22Z").toLocaleDateString();
                        const cells = [...document.querySelectorAll("table tbody tr")].map((tr) => tr.children[2]?.innerText || "");
                        return JSON.stringify({
                            icons: document.querySelectorAll('table img.icon').length,
                            placeholders: document.querySelectorAll('table .icon-placeholder').length,
                            firstSrc: (document.querySelector('table img.icon') || {}).src?.slice(0, 22) || '',
                            updatedCells: cells.filter((text) => text.includes("更新")).length,
                            updateMatches: cells.some((text) => text.includes(expected)),
                            expected,
                        });
                    })()`);
                },
            },
            {
                name: "03 搜索结果行：「下载最新」+「购买」+「版本」三个动作",
                // 用户要求把历史版本入口做回搜索页、并在下载旁边加购买：每行三个动作（下最新 / 购买 / 看版本）。
                // 微信那行正在下，下载按钮该是状态而不是「下载最新」。
                expect: [
                    '"rows":1',
                    '"actions":[["下载中 43%","购买","版本"]]',
                    '"hasVersionButton":true',
                    '"purchaseEnabled":true',
                ],
                async run() {
                    return await evaluate(`JSON.stringify({
                        rows: document.querySelectorAll(".content table tbody tr").length,
                        actions: [...document.querySelectorAll(".content table tbody tr")].map((tr) =>
                            [...tr.querySelectorAll("button")].map((b) => b.textContent.trim())),
                        hasVersionButton: [...document.querySelectorAll(".content table tbody tr button")]
                            .some((b) => b.textContent.includes("版本")),
                        purchaseEnabled: [...document.querySelectorAll(".content table tbody tr button")]
                            .some((b) => b.textContent.trim() === "购买" && !b.disabled),
                    })`);
                },
            },
            {
                name: "03b 购买弹窗：免费应用拿到许可，弹窗报结果",
                // 点「购买」只把按钮换成「购买中…」，Apple 给了结果才弹窗 —— 这里等桩里的 600ms。
                expect: ["购买结果 · 微信", "已获取许可", '"dialogOpen":true'],
                async run() {
                    const clicked = await evaluate(`(()=>{
                        const tr = document.querySelector(".content table tbody tr");
                        const b = tr && [...tr.querySelectorAll("button")].find((x) => x.textContent.trim() === "购买");
                        if (!b) return "NOT FOUND";
                        b.click();
                        return "clicked";
                    })()`);
                    await sleep(1400);

                    const snapshot = await evaluate(`JSON.stringify({
                        clicked: ${JSON.stringify(clicked)},
                        dialogOpen: Boolean(document.querySelector("dialog[open] #purchase-title")),
                        title: (document.querySelector("dialog[open] #purchase-title") || {}).textContent || "",
                        notice: (document.querySelector("dialog[open] .notice") || {}).innerText || "",
                        buttons: [...document.querySelectorAll("dialog[open] .modal-actions button")]
                            .map((b) => b.textContent.trim()),
                    })`);

                    // 关掉弹窗：下一步要接着用干净的搜索结果页（modal 会遮罩住整页）。
                    // 标题与提示文本已经进了 snapshot，断言不靠「弹窗还开着」。
                    // 弹窗本身也得有图：步骤截图是关闭之后拍的，看不到它。
                    const shot = await client.send("Page.captureScreenshot", { format: "png" });
                    writeFileSync(resolve(OUT, "03b-dialog.png"), Buffer.from(shot.data, "base64"));
                    await evaluate(`(()=>{const b=[...document.querySelectorAll("dialog[open] .modal-actions button")]
                        .find((x) => x.textContent.trim() === "关闭"); if (b) b.click(); return "closed";})()`);
                    await sleep(300);

                    return snapshot;
                },
            },
            {
                name: "04 搜索框直接吃 App Store 链接 / 数字 ID",
                // 付费应用要先在 App Store 里买：用户手里最现成的是商店链接，贴进来就该直接查到这一个
                // App，而不是拿整条 URL 去当关键词搜（那样搜不到任何东西）。现在查到就只出一条结果，
                // 不再顺手弹出版本面板。
                // 标题里的名字来自 lookup 回来的 App（桩把 id 414478124 落到微信上），
                // 不是链接原文 —— 钉住的是「贴链接 = 精确查这一个 App」，不是某个标题措辞。
                expect: [
                    "结果 · “微信” · 1 条",
                    "com.tencent.xin",
                    "8.0.50",
                    '"rows":1',
                    '"hasVersionButton":true',
                ],
                async run() {
                    await type('input[placeholder^="应用名"]', "https://apps.apple.com/cn/app/id414478124");
                    await clickButton("搜索");
                    await sleep(1500);

                    return await evaluate(`JSON.stringify({
                        rows: document.querySelectorAll(".content table tbody tr").length,
                        hasVersionButton: [...document.querySelectorAll(".content table tbody tr button")]
                            .some((b) => b.textContent.includes("版本")),
                    })`);
                },
            },
            {
                name: "04b 版本面板：列出历史版本（标识 + 显示版本号陆续补齐）",
                // 列表先出来、显示版本号/发布时间是一个版本一次请求陆续填的，
                // 所以这里只断言行数、最新标记与可下载按钮数，不要求某一刻全部补齐。
                expect: [
                    "微信 · 历史版本",
                    "共 4 个可下载版本",
                    '"rows":4',
                    '"latestBadges":1',
                    '"downloadButtons":4',
                    "8.0.49",
                ],
                async run() {
                    await clickButton("版本");
                    await sleep(900);

                    return await evaluate(`JSON.stringify({
                        rows: document.querySelectorAll("dialog[open] table tbody tr").length,
                        latestBadges: [...document.querySelectorAll("dialog[open] .badge")]
                            .filter((b) => b.textContent.includes("最新")).length,
                        downloadButtons: [...document.querySelectorAll("dialog[open] table tbody tr button")]
                            .filter((b) => b.textContent.includes("下载此版本")).length,
                    })`);
                },
            },
            {
                name: "04c 版本面板里能下指定历史版本（externalVersionId 非空）",
                // 点第二行（8.0.49）：它不在账本里，不该弹重复确认；下载后排入队列，
                // 该行按钮应该变成「排队中…」而不是还可以再点一次。
                expect: ['"clicked":"clicked"', "排队中"],
                async run() {
                    const clicked = await evaluate(`(()=>{
                        const tr = [...document.querySelectorAll("dialog[open] table tbody tr")][1];
                        const b = tr && [...tr.querySelectorAll("button")].find((x) => x.textContent.includes("下载此版本"));
                        if (!b) return "NOT FOUND";
                        b.click();
                        return "clicked";
                    })()`);
                    await sleep(900);

                    const snapshot = await evaluate(`JSON.stringify({
                        clicked: ${JSON.stringify(clicked)},
                        rows: [...document.querySelectorAll("dialog[open] table tbody tr")]
                            .map((tr) => tr.innerText.replace(/\\s+/g, " ")),
                    })`);

                    // 断言后把面板关掉：后面的步骤要看到干净的搜索结果页。
                    await evaluate(`(()=>{const b=document.querySelector("dialog[open] .icon-button"); if(b) b.click(); return "closed";})()`);
                    await sleep(300);

                    return snapshot;
                },
            },
            {
                name: "05 付费应用门禁（已购判定来自已购列表）",
                // 桩里的搜索响应**不带** purchasedAt（真机就是这样），所以「已购」和那个
                // 2023/11/11 的购买时间只可能来自账号的已购列表 —— 这正是在验它。
                expect: [
                    "已购",
                    "2023/11/11",
                    "不能下载：未购买",
                    "5.0 MB",
                    "体积未知",
                    '"icons":2',
                    '"placeholders":1',
                    '"ownedBadge":1',
                    '"enabled":[true,false,true]',
                    // 已购那行的「购买」该禁掉；未购的付费行不禁（点了才知道 Apple 怎么说）。
                    '"purchase":[false,true,true]',
                ],
                async run() {
                    await type('input[placeholder^="应用名"]', "软件");
                    await clickButton("搜索");
                    await sleep(1500);

                    // 三条结果里两条有图标、一条没有：没有的那条必须是占位块（否则行高会跳）。
                    // 下载按钮：已购的付费应用可点、没买过的付费应用禁掉、免费的可点。
                    return await evaluate(`(() => {
                        const rows = [...document.querySelectorAll("table tbody tr")];
                        const enabled = rows.map((tr) => {
                            const button = [...tr.querySelectorAll("button")]
                                .find((b) => b.textContent.includes("下载最新"));
                            return button ? !button.disabled : null;
                        });
                        const purchase = rows.map((tr) => {
                            const button = [...tr.querySelectorAll("button")]
                                .find((b) => b.textContent.trim() === "购买");
                            return button ? !button.disabled : null;
                        });
                        return JSON.stringify({
                            icons: document.querySelectorAll('table img.icon').length,
                            placeholders: document.querySelectorAll('table .icon-placeholder').length,
                            ownedBadge: [...document.querySelectorAll("table .badge")]
                                .filter((b) => b.textContent.trim() === "已购").length,
                            enabled,
                            purchase,
                        });
                    })()`);
                },
            },
            {
                name: "05b 购买弹窗：付费应用买不了，给「去 App Store 购买」的出口",
                // 上游购买接口只支持免费应用：失败弹窗要把 Apple 的说法原样显示，并给出下一步。
                expect: ["购买结果 · 没买过的付费软件", "购买没成功", "上游的购买接口只支持免费应用", '"dialogOpen":true'],
                async run() {
                    const clicked = await evaluate(`(()=>{
                        const row = [...document.querySelectorAll(".content table tbody tr")]
                            .find((tr) => tr.innerText.includes("com.milan.other"));
                        const b = row && [...row.querySelectorAll("button")].find((x) => x.textContent.trim() === "购买");
                        if (!b) return "NOT FOUND";
                        b.click();
                        return "clicked";
                    })()`);
                    await sleep(1400);

                    const snapshot = await evaluate(`JSON.stringify({
                        clicked: ${JSON.stringify(clicked)},
                        dialogOpen: Boolean(document.querySelector("dialog[open] #purchase-title")),
                        title: (document.querySelector("dialog[open] #purchase-title") || {}).textContent || "",
                        notice: (document.querySelector("dialog[open] .notice") || {}).innerText || "",
                        buttons: [...document.querySelectorAll("dialog[open] .modal-actions button")]
                            .map((b) => b.textContent.trim()),
                    })`);

                    const shot = await client.send("Page.captureScreenshot", { format: "png" });
                    writeFileSync(resolve(OUT, "05b-dialog.png"), Buffer.from(shot.data, "base64"));
                    await evaluate(`(()=>{const b=[...document.querySelectorAll("dialog[open] .modal-actions button")]
                        .find((x) => x.textContent.trim() === "关闭"); if (b) b.click(); return "closed";})()`);
                    await sleep(300);

                    return snapshot;
                },
            },
            {
                name: "06 下载队列（进行中）",
                expect: ["下载中", "201 MB / 460 MB", "43.7%"],
                async run() {
                    await clickTab("下载");
                    await sleep(1200);
                },
            },
            {
                name: "07 已下载（包内图标 + 行内动作）",
                // 「算 sha256」「图标」两个按钮已按用户要求删除：行内只剩打开位置 / 重新下载 / 移除记录。
                expect: [
                    "文件已不在磁盘上",
                    '"icons":1',
                    "data:image/png;base64",
                    '"placeholderTitles":"文件已不在磁盘上，读不到图标|no iTunesArtwork in this IPA"',
                    '"hasHashButton":false',
                    '"hasIconButton":false',
                ],
                async run() {
                    await clickSubTab("已下载");
                    // 图标是一份一份顺序读的（每个请求都要开一个大文件），多等一会儿。
                    await sleep(2000);

                    return await evaluate(`JSON.stringify({
                        icons: document.querySelectorAll('table img.icon').length,
                        firstSrc: (document.querySelector('table img.icon') || {}).src?.slice(0, 22) || '',
                        placeholderTitles: [...document.querySelectorAll('table .icon-placeholder')]
                            .map(el => el.title).join('|'),
                        hasHashButton: [...document.querySelectorAll(".content button")]
                            .some((b) => b.textContent.includes("sha256")),
                        hasIconButton: [...document.querySelectorAll(".content button")]
                            .some((b) => ["图标", "重取图标"].includes(b.textContent.trim())),
                    })`);
                },
            },
            {
                // 顶部栏的「切换账号」：确认（预览桩默认确认）之后真的退登，并跳到
                // 设置页的登录表单。断言三件事：顶部文案变未登录、按钮变「登录」、
                // 内容区出现密码框（登录表单）。
                name: "07b 顶部栏切换账号：确认后退登并跳到登录表单",
                expect: [
                    '"switchButton":"clicked"',
                    '"who":"未登录"',
                    '"loginButton":true',
                    '"hasLoginForm":true',
                ],
                async run() {
                    // 顶部按钮存在且可点（找不到会返回 NOT FOUND，直接断言失败）。
                    const clicked = await click("切换账号", ".topbar");
                    await sleep(900);
                    return await evaluate(`JSON.stringify({
                        switchButton: ${JSON.stringify(clicked)},
                        who: document.querySelector(".topbar .who").textContent.trim(),
                        loginButton: [...document.querySelectorAll(".topbar button")]
                            .some((b) => b.textContent.trim() === "登录"),
                        hasLoginForm: Boolean(document.querySelector('.content input[type="password"]')),
                    })`);
                },
            },
            {
                // 设置页账号卡片上的「切换账号」：走的是同一个 switchAccount()，
                // 但额外有 notice 提示、清空表单、把光标放进 Apple ID 框。
                // 07b 已经把登录态切掉了，这里先用桩把登录态恢复成已登录。
                name: "07c 设置页切换账号：提示 + 清空表单 + 光标回 Apple ID",
                expect: [
                    '"switchButton":"clicked"',
                    '"hasNotice":true',
                    '"email":""',
                    '"focused":"email"',
                ],
                async run() {
                    await evaluate(`(() => {
                        Object.assign(window.__previewAuth, {
                            loggedIn: true, email: "hejinghao@changzhi.top", name: "何静豪",
                            storeFront: "143524-2,34", message: "", loggedOut: false,
                        });
                        window.go.services.AuthService.Status = async () => window.__previewAuth;
                        return 'restored';
                    })()`);
                    await click("刷新", ".topbar");
                    await sleep(500);
                    await clickTab("设置");
                    await sleep(800);

                    const clicked = await click("切换账号", ".content");
                    await sleep(800);
                    return await evaluate(`JSON.stringify({
                        switchButton: ${JSON.stringify(clicked)},
                        hasNotice: /已退出当前账号，请输入新账号登录/.test(document.querySelector(".content").innerText),
                        email: document.querySelector('.content input[type="text"]')?.value ?? null,
                        focused: document.activeElement === document.querySelector('.content input[type="text"]')
                            ? "email" : (document.activeElement?.tagName || "-"),
                    })`);
                },
            },
            {
                // 凭据过期：后端会自己退掉登录态并广播 auth:state(phase=expired)。
                // 前端应该刷新登录态、跳设置页、给出「请重新登录」的提示。
                name: "07d 凭据过期：自动登出、跳设置页并提示重新登录",
                expect: [
                    '"who":"未登录"',
                    '"tab":"设置"',
                    "App Store 令牌已过期",
                    "登录凭据已过期，请重新登录",
                ],
                async run() {
                    // 先恢复成已登录（07c 已经把登录态切掉了）。
                    await evaluate(`(() => {
                        Object.assign(window.__previewAuth, {
                            loggedIn: true, email: "tester@example.com", name: "Tester",
                            storeFront: "143441-1", message: "", loggedOut: false, expired: false,
                        });
                        window.go.services.AuthService.Status = async () => window.__previewAuth;
                        return "restored";
                    })()`);
                    await click("刷新", ".topbar");
                    await sleep(400);

                    const emitted = await evaluate(`(() => {
                        window.__previewExpire();
                        window.__emit("auth:state", {
                            loggedIn: false, phase: "expired", expired: true,
                            message: "登录凭据已过期，请重新登录",
                        });
                        return "emitted";
                    })()`);
                    await sleep(800);
                    return await evaluate(`JSON.stringify({
                        emit: ${JSON.stringify(emitted)},
                        who: document.querySelector(".topbar .who").textContent.trim(),
                        tab: document.querySelector(".tabs .tab.active").textContent.trim(),
                        text: document.querySelector(".content").innerText,
                        toast: document.querySelector(".toast")?.textContent.trim() || "",
                    })`);
                },
            },
            {
                name: "08 设置页：未登录风险告知（账号已并进设置）",
                // 也钉住设置页的卡片刻表：账号 → 下载设置 → 凭据后端。
                // 「MCP 接入」那张卡片已按用户要求移除（连 internal/services/mcp.go 一起删）。
                expect: [
                    "停用 Apple ID 的风险",
                    "我已了解，去登录",
                    "下载设置",
                    '"cards":["账号","下载设置","凭据后端"]',
                    '"hasMCP":false',
                ],
                async run() {
                    await evaluate(`window.go.services.AuthService.Status =
                        async () => ({loggedIn:false, message:'未登录'}); 'patched'`);
                    await click("刷新", ".topbar");
                    await sleep(600);
                    await clickTab("设置");
                    await sleep(1200);
                    return await evaluate(`JSON.stringify({
                        cards: [...document.querySelectorAll(".content .card h3")].map((h) => h.textContent.trim()),
                        hasMCP: /MCP|一键全部接入/.test(document.querySelector(".content").innerText),
                    })`);
                },
            },
            {
                // 重复下载的提示现在是 Go 侧原生确认框（macOS 上 window.confirm 恒返回 false），
                // 所以换成预览里的确认钩子，把「拒绝 / 接受」两次的结果当成文本喂给断言。
                // 用「某付费软件」：账本里有它的 3.2.0（就是当前商店版本），按钮点得动；微信那行
                // 正在下载，按钮已经是「下载中 43%」了，正是不该拿来测弹窗的那种状态。
                name: "09 重复下载提示（账本里已有）",
                expect: [
                    "已存在：账本里这一份还没算过 sha256",
                    "还在磁盘上",
                    "某付费软件_3.2.0_hejinghao@changzhi.top.ipa",
                    '"started":0',
                    '"started":1',
                    '"skippedToast":true',
                ],
                async run() {
                    await evaluate(`(()=>{
                        window.__started = [];
                        const realStart = window.go.services.DownloadService.Start;
                        window.go.services.DownloadService.Start = async (req) => {
                            window.__started.push(req);
                            return realStart(req);
                        };
                        window.__confirms = [];
                        window.__answers = [false, true];   // 第一次「跳过」，第二次「仍然下载」
                        window.__preview.confirm = (text) => {
                            window.__confirms.push(text);
                            return window.__answers.shift() ?? false;
                        };
                        return 'hooked';
                    })()`);
                    await clickTab("搜索");
                    await sleep(600);
                    await type('input[placeholder^="应用名"]', "软件");
                    await clickButton("搜索");
                    await sleep(1500);

                    await clickButton("下载最新");
                    await sleep(900);
                    const declined = await evaluate(`JSON.stringify({
                        started: window.__started.length,
                        skippedToast: document.body.innerText.includes("已跳过"),
                        prompts: window.__confirms.slice(),
                    })`);

                    await clickButton("下载最新");
                    await sleep(900);
                    const accepted = await evaluate(`JSON.stringify({
                        started: window.__started.length,
                        prompts: window.__confirms.slice(),
                    })`);

                    return `${declined} ${accepted}`;
                },
            },
            {
                name: "10 同版本正在下时不给点（按钮变状态、不用弹窗问）",
                // 设计选择：账本里已有同一份时用弹窗问（AC2），但**正在下/排队中的版本**直接把
                // 按钮禁掉、并把状态写在按钮上 —— 少一次「确定要点第二遍吗」的来回。
                // 队列那一段提示仍然留着：React 的 disabled 来自上一次渲染，别的窗口/MCP 刚
                // 启动的下载可能还不在这一帧里，那种竞态下它仍然会拦一下。
                expect: ["下载中 43%", '"disabled":true', '"label":"下载中 43%"', '"started":1'],
                async run() {
                    await clickTab("搜索");
                    await sleep(500);
                    await type('input[placeholder^="应用名"]', "微信");
                    await clickButton("搜索");
                    await sleep(1500);
                    await evaluate(`window.__answers = [false]; 'reset'`);
                    await clickButton("下载中 43%");
                    await sleep(900);

                    return await evaluate(`(() => {
                        const row = [...document.querySelectorAll("table tbody tr")]
                            .find((tr) => tr.innerText.includes("com.tencent.xin"));
                        const button = row
                            ? [...row.querySelectorAll("button")].find((b) => b.textContent.includes("下载中"))
                            : null;
                        return JSON.stringify({
                            disabled: button ? button.disabled : null,
                            label: button ? button.textContent.trim() : "",
                            started: window.__started.length,
                        });
                    })()`);
                },
            },
            {
                name: "11 点击下载后立刻有反馈（提示条 + 按钮变排队中）",
                expect: [
                    "排队中 —— 前面的任务结束后会自动开始。",
                    "排队中…",
                    "已开始下载：ChatGPT",
                    "打开下载页",
                    "正在下载 · 43.7%",
                    '"newTask":"click-',
                    '"rowLabel":"排队中…"',
                    '"stripVisible":true',
                ],
                async run() {
                    // 09/10 为了验「会不会拦住」把 Start 和 confirm 换成了桩，这里换回真的 ——
                    // 否则验不到「点下去真的会开始」。
                    await evaluate(`window.go.services.DownloadService.Start = window.__preview.start;
                        window.__preview.confirm = () => true;
                        // 前面的步骤（04c 的历史版本、09 的重复下载）会在队列里留下排队/在跑的任务，
                        // 而提示条只显示前 3 个、且已认识的任务不会自动消失 —— 留着它们会把
                        // 「正在下载的微信」（demo-1）挤出提示条。这一步验的是「点下去立刻有反馈」，
                        // 所以先把其它任务置为完成（发 state 事件让界面重读）。
                        (() => {
                            window.__preview.tasks
                                .filter((t) => t.id !== "demo-1")
                                .forEach((t) => {
                                    t.state = "done";
                                    window.__emit("download:state", {taskId: t.id, state: "done"});
                                });
                        })();
                        'restored'`);
                    await clickTab("搜索");
                    await sleep(400);
                    await type('input[placeholder^="应用名"]', "ChatGPT");
                    await clickButton("搜索");
                    await sleep(1200);
                    await clickButton("下载最新");
                    await sleep(900);

                    const seen = await evaluate(`(() => {
                        const strip = [...document.querySelectorAll(".content .card")]
                            .find((card) => [...card.querySelectorAll("button")].some((b) => b.textContent.includes("打开下载页")));
                        const button = [...document.querySelectorAll(".content button")]
                            .find((b) => b.textContent.includes("下载中") || b.textContent.includes("排队中"));
                        return JSON.stringify({
                            newTask: (window.__preview.tasks[0] || {}).id || "",
                            rowLabel: button ? button.textContent.trim() : "",
                            stripVisible: Boolean(strip) && strip.querySelectorAll(".row").length >= 3,
                        });
                    })()`);
                    clickedTaskId = JSON.parse(seen).newTask;

                    return seen;
                },
            },
            {
                name: "12 进度事件推进 → 下载完成（下载中 50% → 打开位置）",
                expect: [
                    "正在下载 · 50.0%",
                    "35.0 MB / 70.0 MB",
                    "8.0 MB/s",
                    "下载完成：",
                    "ChatGPT_latest.ipa",
                    "打开位置",
                    '"satellite":"下载中 50%"',
                    '"afterLabel":"下载最新"',
                ],
                async run() {
                    // 真机顺序就是「先 state=running，再一串 progress」——只发 progress 的话
                    // 任务还是 pending，界面本来就不该显示百分比。
                    await evaluate(`window.__preview.setTask(${JSON.stringify(clickedTaskId)}, {state: "running", bytesTotal: 73400320});
                        window.__emit("download:state", {taskId: ${JSON.stringify(clickedTaskId)}, state: "running"})`);
                    await sleep(400);
                    await evaluate(`window.__emit("download:progress", {taskId: ${JSON.stringify(clickedTaskId)}, phase: "download",
                        current: 36700160, total: 73400320, percent: 0.5, bytesPerSec: 8388608,
                        etaSeconds: 4, indeterminate: false})`);
                    await sleep(400);
                    const mid = await evaluate(`(() => {
                        const button = [...document.querySelectorAll(".content button")]
                            .find((b) => b.textContent.includes("下载中"));
                        const strip = [...document.querySelectorAll(".content .card")]
                            .find((card) => [...card.querySelectorAll("button")].some((b) => b.textContent.includes("打开下载页")));
                        return JSON.stringify({
                            satellite: button ? button.textContent.trim() : "",
                            stripText: strip ? strip.innerText : "",
                        });
                    })()`);

                    await evaluate(`window.__preview.finish(${JSON.stringify(clickedTaskId)}, "done",
                        {bytesDone: 73400320, bytesTotal: 73400320})`);
                    await sleep(900);
                    const after = await evaluate(`(() => {
                        const row = [...document.querySelectorAll("table tbody tr")]
                            .find((tr) => tr.innerText.includes("com.openai.chat"));
                        const button = row ? [...row.querySelectorAll("button")].find((b) => b.textContent.includes("下载")) : null;
                        return JSON.stringify({afterLabel: button ? button.textContent.trim() : ""});
                    })()`);

                    return mid + " " + after;
                },
            },
            {
                name: "13 已购页（账号的购买历史）",
                expect: [
                    "共 4 个",
                    "更新",
                    "Island Pocket",
                    "雷电Claw",
                    "2026/10/14",
                    "购买时间",
                    "下载最新",
                    '"rows":4',
                    '"active":"已购"',
                ],
                async run() {
                    await clickTab("已购");
                    await sleep(1800);

                    return await evaluate(`JSON.stringify({
                        rows: document.querySelectorAll(".content table tbody tr").length,
                        active: (document.querySelector(".tabs button.active") || {}).textContent || "",
                    })`);
                },
            },
            {
                name: "14 已购页每行只剩「下载最新」（版本入口已移除）",
                // 用户要的是「列表里别再放版本 · 下载」：这一页每行只该有一个动作按钮。
                // 老版本现在只能走 MCP / 命令行（界面里的版本入口已按用户要求移除，03/04 断言的就是搜索页那侧）。
                expect: [
                    '"rows":4',
                    '"actions":[["下载最新"],["下载最新"],["下载最新"],["下载最新"]]',
                    '"hasVersionButton":false',
                ],
                async run() {
                    return await evaluate(`JSON.stringify({
                        rows: document.querySelectorAll(".content table tbody tr").length,
                        actions: [...document.querySelectorAll(".content table tbody tr")].map((tr) =>
                            [...tr.querySelectorAll("button")].map((b) => b.textContent.trim())),
                        hasVersionButton: [...document.querySelectorAll(".content table tbody tr button")]
                            .some((b) => b.textContent.includes("版本")),
                    })`);
                },
            },
            {
                name: "15 已购页批量下载（先报体积与剩余空间）",
                // 已购页不再区分“本地有没有下过”：批量下载就是整页重下一份，确认框里会直说。
                expect: ["这一页共", "个应用（已经下过的也会重新下一份）",
                    "下载目录所在磁盘剩余 64.0 GB", "继续吗？", "个应用加入下载队列", '"startedPositive":true'],
                async run() {
                    await clickTab("已购");
                    await sleep(1200);

                    return await evaluate(`(async () => {
                        const confirms = [];
                        window.__preview.confirm = (text) => { confirms.push(text); return true; };
                        const before = window.__preview.tasks.length;
                        const button = [...document.querySelectorAll(".content button")]
                            .find((b) => b.textContent.includes("批量下载本页"));
                        if (!button) {
                            return JSON.stringify({
                                error: "NO BATCH BUTTON", startedPositive: false,
                                labels: [...document.querySelectorAll(".content button")]
                                    .map((b) => b.textContent.trim()).join("|"),
                            });
                        }
                        button.click();
                        await new Promise((r) => setTimeout(r, 900));
                        window.__preview.confirm = () => true;
                        const started = window.__preview.tasks.length - before;
                        return JSON.stringify({
                            started,
                            startedPositive: started > 0,
                            confirms: confirms.join(" "),
                        });
                    })()`);
                },
            },
            {
                name: "16 已下载：文件丢了就从 App Store 重下",
                expect: ["重新下载", "历史版本 857001122", '"started":1'],
                async run() {
                    await clickSubTab("已下载");
                    await sleep(1200);

                    return await evaluate(`(async () => {
                        const confirms = [];
                        window.__preview.confirm = (text) => { confirms.push(text); return true; };
                        const before = window.__preview.tasks.length;
                        const row = [...document.querySelectorAll(".content table tbody tr")]
                            .find((tr) => tr.innerText.includes("Pages"));
                        const button = row ? [...row.querySelectorAll("button")]
                            .find((b) => b.textContent.trim() === "重新下载") : null;
                        if (!button) return JSON.stringify({error: "NO REDOWNLOAD BUTTON", started: 0});
                        button.click();
                        await new Promise((r) => setTimeout(r, 900));
                        window.__preview.confirm = () => true;
                        return JSON.stringify({
                            started: window.__preview.tasks.length - before,
                            confirms: confirms.join(" "),
                        });
                    })()`);
                },
            },
            {
                name: "17 下载页：删半成品 + 重试全部失败",
                expect: ['"deletedPartial":true', '"toastAfterDelete":true', '"retriedAll":true',
                    "重新排进队列"],
                async run() {
                    await clickSubTab("队列");
                    await sleep(600);

                    return await evaluate(`(async () => {
                        window.__preview.addTask({
                            id: "demo-failed", appId: 5, bundleId: "com.milan.other", name: "没买过的付费软件",
                            platform: "iphone", versionId: "", outputPath: "/Users/milan/Downloads/demo.ipa",
                            state: "failed", bytesDone: 2048, bytesTotal: 20971520,
                            errorCode: "network", errorMessage: "网络中断（演示）",
                            createdAt: "2026-10-16T17:10:00Z", updatedAt: "2026-10-16T17:11:00Z",
                        });
                        await new Promise((r) => setTimeout(r, 700));

                        const taskNames = window.__preview.tasks.map((item) => item.name).join(",");
                        const confirms = [];
                        window.__preview.confirm = (text) => { confirms.push(text); return true; };

                        const row = [...document.querySelectorAll(".content table tbody tr")]
                            .find((tr) => tr.innerText.includes("没买过的付费软件"));
                        const rowButtons = (node) => [...node.querySelectorAll("button")]
                            .map((b) => b.textContent.trim());
                        const del = row ? [...row.querySelectorAll("button")]
                            .find((b) => b.textContent.trim() === "删半成品") : null;
                        const deletedPartial = Boolean(del);
                        if (del) del.click();
                        await new Promise((r) => setTimeout(r, 700));
                        const toastAfterDelete = document.body.innerText.includes("已删掉半成品");

                        const retryAll = [...document.querySelectorAll(".content button")]
                            .find((b) => b.textContent.includes("重试全部失败"));
                        const retriedAll = Boolean(retryAll);
                        if (retryAll) retryAll.click();
                        await new Promise((r) => setTimeout(r, 900));

                        window.__preview.confirm = () => true;
                        return JSON.stringify({
                            deletedPartial,
                            toastAfterDelete,
                            retriedAll,
                            retried: (window.__preview.retried || []).join(","),
                            confirms: confirms.join(" | "),
                            rows: row ? rowButtons(row).join("/") : "NO ROW",
                            taskNames,
                            rowTexts: [...document.querySelectorAll(".content table tbody tr")]
                                .map((tr) => tr.innerText.slice(0, 24)).join(" ;; "),
                        });
                    })()`);
                },
            },
            {
                name: "18 付费未购：用 macappstore:// 打开商店",
                expect: ["已在 App Store 里打开 没买过的付费软件", '"opened":"5"'],
                async run() {
                    await clickTab("搜索");
                    await type('input[placeholder^="应用名"]', "软件");
                    await clickButton("搜索");
                    await sleep(1500);

                    return await evaluate(`(async () => {
                        const row = [...document.querySelectorAll(".content table tbody tr")]
                            .find((tr) => tr.innerText.includes("com.milan.other"));
                        const button = row ? [...row.querySelectorAll("button")]
                            .find((b) => b.textContent.includes("去 App Store 买")) : null;
                        if (!button) return JSON.stringify({error: "NO STORE BUTTON", opened: ""});
                        button.click();
                        await new Promise((r) => setTimeout(r, 500));
                        return JSON.stringify({opened: (window.__preview.opened || []).join(",")});
                    })()`);
                },
            },
        ];

        for (const step of steps) {
            const extra = step.run ? await step.run() : "";
            const text = (await evaluate("document.body.innerText")) + (extra ? ` ${extra}` : "");
            const { data } = await client.send("Page.captureScreenshot",
                { format: "png", captureBeyondViewport: true });
            const file = resolve(OUT, `${step.name.split(" ")[0]}.png`);
            writeFileSync(file, Buffer.from(data, "base64"));

            const missing = step.expect.filter((needle) => !text.includes(needle));
            if (missing.length) {
                failures.push(`${step.name}: 少了 ${JSON.stringify(missing)}`);
            }
            console.log(`${missing.length ? "✗" : "✓"} ${step.name}  → ${file}`);
            if (missing.length) {
                console.log(`   看到的文本：\n${text}`);
            }
        }

        client.close();
    } finally {
        chrome.kill();
    }

    if (failures.length) {
        console.error(`\n${failures.length} 个状态没通过：`);
        failures.forEach((line) => console.error(` - ${line}`));
        process.exit(1);
    }
    console.log("\n全部状态通过。");
}

main().catch((err) => {
    console.error("跑挂了:", err.message);
    process.exit(1);
});
