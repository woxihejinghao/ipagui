# Product Requirements Document: IPAtool GUI（工作名 IPA-GUI）

**Version**: 1.0
**Date**: 2026-10-16
**Author**: Sarah (Product Owner)
**Quality Score**: 91/100
**上游依赖**: [majd/ipatool](https://github.com/majd/ipatool) v2.7.0（MIT）· 调研依据：[ipatool-GUI-调研报告.md](../ipatool-GUI-调研报告.md)

---

## Executive Summary

`ipatool` 是目前唯一能在 Apple 官方渠道按 Apple ID 检索、购买（免费应用）并下载 **App Store 加密 IPA** 的开源工具，v2.7.0 支持 iPhone/iPad/tvOS/watchOS/visionOS/macOS 六个平台、版本考古与 MCP 集成。但它是一个纯命令行工具：完成一次「搜索 → 选版本 → 下载」需要记住 5 个以上 flag、在 2FA 场景下被迫「跑两次命令」（第一次退出码为 0 却并未登录成功）、下载进度只能在终端里看一维字符条，且历史版本与已下载文件没有任何本地索引。

本产品是一套 **macOS + Windows 桌面 GUI**，把 ipatool 的 Go 业务层（`github.com/majd/ipatool/v2/pkg/appstore`）直接嵌入应用后端，用 Wails v3 承载界面，交付一条「登录 → 搜索 → 详情/版本考古 → 下载（可续传、精确进度） → 本地库检索 → MCP 一键接入」的闭环（**「MCP 一键接入」这个 GUI 入口 2026-10-19 按用户要求移除，见 docs/architecture.md §8.11；helper 本身保留**）。**定位为自用工具**，因此在 MVP 阶段可以砍掉公开产品的合规与分发包袱（公证、多语言、遥测、许可校验），把全部预算压在「流程串联 + 进度可见 + 版本可考」；项目现已开源，以源码形式发布，仍不提供预编译包。

预期影响：把一次下载的交互步数从 CLI 的 6+ 步压到 ≤3 步，把「中断后重下 4GB」变成「原地续传」，把「我记得下过这个版本」变成一次 SQLite 查询。技术风险集中在一个点上——我们直接依赖上游 `pkg/` 的内部符号而非稳定 API，需要用「版本钉死 + 启动自检」把上游破坏性变更变成会失败的断言，而不是静默失效。

---

## Problem Statement

**Current Situation**

1. **命令与状态离散**：每次下载都要重新拼 `ipatool download -b com.x.y --platform iphone -o ~/Downloads --purchase`；账号是否已登录、cookie 是否过期、上次下到哪，全靠用户记忆与 `--verbose` 猜。
2. **2FA 流程是「假成功」陷阱**：实测 `auth login --non-interactive` 在需要 2FA 时**打印提示后 `return nil`，进程退出码 0 且不输出 `success` 字段**（cmd/auth.go:84-88）。GUI 若按「退出码 0」判成功，会直接进入未登录状态。
3. **进度只有渲染后的文本**：下载进度由 `*progressbar.ProgressBar` 渲染成字符条写 stdout（cmd/download.go:105-122），结构化百分比、剩余时间、当前字节数都拿不到；且进度条**只在 interactive 时创建**。
4. **版本考古缺失**：`list-versions` / `get-version-metadata` 能拿到历史版本与元数据，但 CLI 只吐 JSON 行，用户要自己比对、记住 `externalVersionID` 再回头下载。
5. **本地无账本**：下载到 `~/Downloads` 的 IPA 没有索引，无法回答「这个版本我下过吗 / 哈希对不对 / 在哪」。
6. **错误码裸奔**：`-5000` 凭据无效、`2034` 密码令牌过期、`2042` 需重新登录、`9610` 无授权、`2059` 服务暂不可用——CLI 只把数字塞进 `error` 字段，用户无从判断「该重试、该重登、还是该放弃」。
7. **账号风险无告知**：上游 issue #584 报告在 macOS 27 上登录后 3 个账号被封，维护者归因于错误凭据被 Apple 判定；#627（2026-10-16，未关闭）出现登录/购买报 `Your account is disabled.` 而下载类命令仍可用。GUI 若不前置风险告知与「单账号串行、复用 cookie、降低登录频率」的约束，等于替用户放大风险。

**Proposed Solution**

Wails v3 桌面应用（Go 后端 + 系统 WebView）。后端**不 fork、不 sidecar 包装 CLI**，而是直接 `import` 上游 `pkg/appstore`、`pkg/keychain`、`pkg/http`、`pkg/util/{operatingsystem,machine}`，复用同一条 Apple 私有接口通路与设备身份；用**持有的 `*progressbar.ProgressBar` 指针反查结构化进度**（`GetMax64()` + `State()`），用**上游导出的错误类型**（`ErrAuthCodeRequired` / `ErrPasswordTokenExpired` …）驱动 UI 状态机；用 SQLite 建立本地下载账本；再附一个 10 行的 MCP helper 二进制复用上游 `ipatool mcp`，由 GUI 一键写入 Claude Desktop / Cursor 配置。

**Business Impact**

- 单次「搜索 → 下载」耗时与认知负担显著下降（KPI 见下节），且**所有失败都有可执行的中文处置建议**（重试 / 重新登录 / 换免费应用 / 稍后再试）。
- 历史版本能力从「能拿到但没法用」变成「列表里点一下就下」，这是现有 GUI 竞品普遍缺失的一段（现有封装如 IPARanger、ipatoolUI、ipatool-webGUI 基本停留在「包一层命令」）。
- MCP 一键注册把本工具接入 AI 工作流，v2.7.0 刚发布该能力（2026-10-09），竞品大概率尚未跟进。

---

## Success Metrics

**Primary KPIs**（自用工具，指标可当面核对，不做遥测上报）

| KPI | 目标值 | 测量方法 |
|---|---|---|
| 完成一次「搜索 → 下载」的交互步数 | ≤ 3 步（CLI ≥ 2 条命令 + 记忆 ≥5 个 flag） | 手工走查 + 录屏计步 |
| 含 2FA 的首次登录成功率 | ≥ 95%（10 次内失败 ≤ 0.5 次） | 登录流程埋点写本地日志表，按次统计 |
| 中断后续传成功率 | 100%（网络中断 → 重连 → 从 `.tmp` 偏移继续，不重下） | 故意断网/杀进程 20 次，检查最终 IPA 与哈希 |
| 进程崩溃/重启后任务不丢 | 队列与历史 100% 恢复 | 下载中强杀进程，重启核对 SQLite 记录与 `.tmp` 大小 |
| 单次下载「人工介入次数」 | < 1（理想 0） | 记录用户点击「重试 / 重新登录」的次数 |
| 冷启动到可搜索 | ≤ 1.5 s（缓存命中） | `time` 从进程启动到搜索框可用 |
| MCP 一键注册后 AI 端可用 | 1 次注册即生效，无需手工编辑 JSON | 注册后重启客户端，调用 `list_purchases` 验证 |

**Validation**：MVP 完成后连续两周自用，每次下载后填一行本地日志（耗时、是否续传、是否需介入）；第 2 周周末对照上表逐项判定，未达标项进入 Phase 2 backlog。

---

## User Personas

### Primary: 维护者本人（单人自用）
- **Role**: 独立开发者 / 逆向与测试工程师，本机 macOS（Apple Silicon）为主力，Windows 机器用于交叉验证与打包测试。
- **Goals**: 快速拿到任意 App 的指定历史版本 IPA 做兼容性测试；同一批账号-应用组合反复下载不同版本；把「按需下载」接进 AI 工作流（MCP）；下载大文件时不盯着终端。
- **Pain Points**: 记不住 flag；2FA 要跑两次命令且第一次「看起来成功了」；终端进度条断网即从头再来；历史版本 ID 复制粘贴容易错；不知道本地下过的文件在哪、是不是完整。
- **Technical Level**: Advanced（能读源码、能自己写脚本，所以 GUI 的价值必须体现在「省事」而非「降门槛」）。

### Secondary: 偶尔使用的同事（内部共享）
- **Role**: QA / 需要特定版本 IPA 的同事，不熟悉 ipatool。
- **Goals**: 在被告知 bundle id 的前提下自己完成下载，不必学命令行。
- **Pain Points**: 完全不知道 flag 与平台名；遇到 `2034`/`2042` 会以为是「工具坏了」。
- **Technical Level**: Novice–Intermediate（因此错误提示必须是中文可执行动作，而非错误码）。

---

## User Stories & Acceptance Criteria

> 这里只放**规格**：下面的复选框一直保持未勾选状态，不代表没做。
> 每一条的实测结论（做没做、怎么验的、有什么偏差）统一记在 `docs/architecture.md` §8；还没做的记在 §7。

### Story 1: 首次登录与 Apple ID 风险告知
**As a** 维护者 **I want to** 在 GUI 里用 Apple ID + 密码登录，并在需要时被明确要求输入 2FA 验证码 **So that** 我不用跑两次命令，也不会误以为已经登录成功。

**Acceptance Criteria:**
- [ ] 首次点击登录前弹出风险告知：Apple ID 存在被停用风险（依据上游 #584 / #627），并说明本工具只做官方接口调用、不复用他人账号、单账号串行。
- [ ] 提交密码后若后端返回 `ErrAuthCodeRequired`，界面**必须**切到「输入 6 位验证码」状态并在同一逻辑会话内继续登录，**不得**以「退出码 0 / 无错误」判定成功。
- [ ] 登录成功后展示 `name` + `email`（来自 `LoginOutput`），并在顶部常驻显示账号与区域（storefront → 国家码）。
- [ ] 凭据只写入操作系统安全存储（macOS Keychain / Windows Credential Manager），应用自有数据库与日志中**不得**出现密码或验证码明文。
- [ ] 凭据缺失时（Keychain 中无条目）界面显示「未登录」引导，而非抛出原始错误字符串。

### Story 2: 搜索应用
**As a** 维护者 **I want to** 输入关键词或 bundle id 直接定位应用 **So that** 我不用记 `--platform` 与 `-l` 的取值。

**Acceptance Criteria:**
- [ ] 支持按关键词搜索（`Search`，默认 limit 5，可调；VisionOS 上限 12）与按 bundle id 直查（`Lookup`）。
- [ ] 结果列表显示图标、名称、bundle id、当前版本、价格（`App.Price`）、是否已购（`PurchaseDate` 非零）。
- [ ] 平台选择器覆盖 `iphone/ipad/appletv/watch/vision/mac`（映射上游 `ParsePlatform`），切换平台后结果与详情同步刷新且不串号。
- [ ] 搜索必须先有账号上下文（上游 `cmd/search.go:20` 要求 `AccountInfo`），未登录时按钮禁用并给出「先登录」提示。
- [ ] 请求失败/无结果时展示可操作文案，不得出现空白列表。

### Story 3: 应用详情与版本考古
**As a** 维护者 **I want to** 看到该应用的全部历史版本与元数据，并直接下载指定版本 **So that** 我能做兼容性回归。

**Acceptance Criteria:**
- [ ] 详情页列出 `list-versions` 结果与当前 `lookup` 结果，标注「最新」。
- [ ] 选中某历史版本调用 `get-version-metadata`（`externalVersionID` 必填）展示元数据。
- [ ] 「下载此版本」把 `--external-version-id` 对应的 `ExternalVersionID` 传入下载任务，最终文件名包含版本号且与最新版不冲突。
- [ ] 历史版本列表可被本地库交叉引用：已下载过的版本在行内显示「已下载」标记与文件路径。
- [ ] 若该版本已无法下载（Apple 侧下架/授权失效），提示 `9610` 语义的文案并保留在列表中。

### Story 4: 下载任务与精确进度
**As a** 维护者 **I want to** 看到真实的百分比、已下载/总字节、剩余时间，并能在中断后原地续传 **So that** 我不用盯终端，也不必重下 4GB。

**Acceptance Criteria:**
- [ ] 进度来源为结构化数据：后端调用 `Download` 时传入自持的 `*progressbar.ProgressBar`，另起 goroutine 以 ~10Hz 读取 `GetMax64()` 与 `State()`（`CurrentPercent` / `CurrentBytes` / `SecondsLeft` / `KBsPerSecond`），通过 Wails 事件推送给前端。
- [ ] 界面以进度环 + 字节数 + 剩余时间 + 速率呈现；**不得**依赖解析进度条渲染文本。后端 stdout 必须保持干净（进度条 writer 指向 `io.Discard`）。
- [ ] 总大小未知时（`GetMax64() <= 1`）显示不确定态而不是 0%；总大小增长时（续传/Range 协商后）进度条平滑扩张，百分比不回退为负。
- [ ] 网络中断后可恢复：下载使用 `destination + ".tmp"`，恢复时携带 `range: bytes=<已下载字节>-`；GUI 在 `.tmp` 存在且大小 < 总大小时提供「继续下载」。
- [ ] 任务队列默认**并发 1**（Apple 侧限流），支持排队/暂停/取消；取消后 `.tmp` 默认保留（可配置删除）。
- [ ] 收到 `2059`（服务暂不可用）或 `2034`（密码令牌过期）时，按上游策略重试（`retry.Attempts(3)`），重试过程在 UI 可见；`2034` 最终失败时引导重新登录。
- [ ] 下载完成后计算 SHA-256、文件大小、平台、`externalVersionID` 并写入本地库。

### Story 5: 本地库（已下载清单）
**As a** 维护者 **I want to** 查询我下过哪些应用/版本、文件在哪、哈希是什么 **So that** 我不用在文件夹里翻。

**入口**：顶部「下载」页 → 二级视图「已下载」（2026-10-19 起不再是顶层页签，见 `architecture.md` §8.7）。

**Acceptance Criteria:**
- [ ] 本地库页面支持按应用名 / bundle id / 版本 / 平台 / 日期筛选，并可「在访达/资源管理器中显示」。
- [ ] 同应用同版本同平台重复下载时提示「已存在（哈希一致）」并可跳过。
- [ ] 文件被外部移动或删除时，行内标记「文件缺失」并允许重新下载。
- [ ] 哈希用于本地去重与完整性校验；界面明确标注「哈希仅用于本地校验，Apple 不公布 IPA 官方哈希，不代表真实性」。

### Story 6: MCP 一键注册
**As a** 维护者 **I want to** 把本工具的能力一键注册到 Claude Desktop / Cursor **So that** AI 客户端能直接查版本、列已购、下应用。

**Acceptance Criteria:**
- [ ] 设置页可勾选目标客户端，点击后写入对应配置（macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`、`~/.cursor/mcp.json`；Windows: `%APPDATA%\\Claude\\claude_desktop_config.json`、`%USERPROFILE%\\.cursor\\mcp.json`），写入前**备份原文件**，写入采用「读取 → 合并 `mcpServers` 键 → 原子替换」。
- [ ] 注册的命令指向随包分发的 `ipa-mcp` helper 二进制（绝对路径），而非 GUI 主程序（避免 Windows GUI 子系统无 stdio 的问题）。
- [ ] 注册后可用性自检：以 stdio 拉起 helper 完成 `initialize` 握手，断言 `serverInfo.name == "ipatool"` 且工具列表含 `search_apps`/`download_app`/`list_app_versions`/`get_version_metadata`/`list_purchases`/`purchase_app`；失败则展示原始 stderr。
- [ ] helper 与 GUI 共享同一状态目录与 Keychain 服务名，因此「GUI 已登录」即「MCP 可用」；若因钥匙串 ACL 触发系统授权弹窗，界面给出对应说明。
- [ ] 提供「取消注册」把配置还原为注册前备份。

**现状（2026-10-19，用户要求）**：上面这条 Story 的 GUI 部分已整体下线 —— 设置页不再有「MCP 接入」卡片，`internal/services/mcp.go`（写入/还原/自检）连同 Wails 绑定一起删除，客户端配置改为手写。`ipa-mcp` helper 与它的工具集不受影响。详见 `docs/architecture.md` §8.11。

### Story 7: 首次准备向导（首次运行的静默长耗时）
**As a** 维护者 **I want to** 在首次登录/首次下载时看到「正在准备」而不是卡住 **So that** 我不会误杀进程。

**Acceptance Criteria:**
- [ ] 首次登录前展示准备阶段说明（上游会打印 `preparing authentication; the first login may take a few minutes`）：下载 Apple 资产并缓存到 `os.UserCacheDir()/ipatool/sap/apple-assets-v2`，以及按平台准备 Unicorn 运行时到 `os.UserCacheDir()/ipatool/unicorn/2.1.4`。
- [ ] 准备阶段显示不定进度 + 说明文案，失败时给出网络/代理相关建议，并允许重试。
- [ ] 已缓存时跳过准备，直接进入登录或下载。

### Story 8: 失败即指引（错误码 → 动作）
**As a** 同事用户 **I want to** 看到中文的「你应该做什么」**So that** 我不会以为工具坏了。

**Acceptance Criteria:**
- [ ] 建立错误类型到文案/动作的映射表（见附录 A），**基于上游导出的错误类型（`errors.Is`）判定**，而非解析 CLI JSON 的 `error` 字符串。
- [ ] `-5000` 凭据无效 → 引导重新输入密码；`2042` → 引导重新登录；`2034` → 先自动重试再引导重登；`9610` → 说明该应用未购买且无法获取授权；`5002` → 提示已购买，可直接下载；`1008` → 提示设备验证失败并给排查建议；`2059` → 提示稍后重试（自动退避）。
- [ ] 付费应用（`App.Price > 0`）在详情页与下载按钮上明确禁用并解释「上游不支持购买付费应用」（`Purchase` 仅支持免费应用）。
- [ ] 任何错误都可展开查看原始错误对象（供自用排障）。

### Story 9: 平台与打包（mac + win）
**As a** 维护者 **I want to** 在 macOS 与 Windows 上各拿到一个可双击运行的产物 **So that** 我两边都能用。

**Acceptance Criteria:**
- [ ] 后端保持 **cgo-free**（与上游一致），以便交叉编译；macOS 产出 `arm64` 与 `amd64`，Windows 产出 `amd64`（`arm64` 视需要）。
- [ ] macOS 产物为 `.app`（内部自用可先 ad-hoc 签名 + 首次运行「仍要打开」绕过 Gatekeeper，公证列为 Phase 2）；Windows 产物为安装包或免安装 `.exe`（SmartScreen 提示可接受）。
- [ ] 内置版本自检：启动时断言所依赖的上游符号存在且行为符合预期（见技术约束 §「启动自检」），不满足则拒绝启动并给出明确说明。

---

## Functional Requirements

### Core Features

**F1. 账号与会话（auth）**
- Description：封装 `appstore.Login` / `AccountInfo` / `Revoke`，提供登录、登出、会话状态、区域展示。
- User flow：输入邮箱密码 → （可能）输入 2FA → 校验 `AccountInfo` → 顶部显示账号 + 区域 → 后续请求复用同一 `AppStore` 实例与 cookie jar。
- Edge cases：需要 2FA（`ErrAuthCodeRequired`）；账号被停用（`ErrAccountDisabled` 语义，来自 `CustomerMessage`）；Cookie 过期但 Keychain 仍在；用户登出后 `.tmp` 是否保留；区域切换不可用（storefront 由账号国家决定，**无 `--country` 参数**）。
- Error handling：全部走错误类型映射（附录 A）；连续失败 3 次后暂停登录 5 分钟，避免高频登录触发风控。

**F2. 搜索与详情（search / lookup）**
- Description：关键词搜索（`Search`，limit 可控）、bundle id 直查（`Lookup`）、应用详情聚合（图标、名称、价格、已购、当前版本、平台列表）。
- User flow：输入 → 结果列表（虚拟滚动）→ 选中 → 详情抽屉 → 「查看版本」。
- Edge cases：VisionOS 搜索每页上限 12；关键词为空；结果为空；平台与应用不匹配（如对 mac 平台查 iOS-only 应用）；网络失败。
- Error handling：区分「无结果」与「请求失败」，不得混淆为空列表。

**F3. 版本考古（list-versions / get-version-metadata）**
- Description：列出全部历史版本，展示元数据，支持对指定 `externalVersionID` 直接下单。
- User flow：详情 → 版本列表 → 选版本 → 查看元数据 / 下载此版本。
- Edge cases：版本数量大（分页/虚拟滚动）；`externalVersionID` 必填缺失；版本已下架；tvOS 数字 ID 应用；watchOS 独立应用；Apple Arcade 应用；旧版 macOS 安装包。
- Error handling：`9610` → 该版本无授权；元数据接口失败不影响列表展示。

**F4. 下载引擎（download + 队列 + 续传 + 进度）**
- Description：串行任务队列，每任务持有独立的 `*progressbar.ProgressBar`（writer 丢弃输出，仅作数据源），10Hz 推送进度；`.tmp` 续传；完成后落库。
- User flow：加入队列 → 运行中显示进度 → 完成/失败 → 「下载 → 已下载」出现记录。
- Edge cases：`.tmp` 存在但服务端不支持 Range（HTTP 200 → 上游会 `Truncate(0)` 重下）；磁盘空间不足；目标路径为空（上游默认落到**进程工作目录**，GUI 必须显式传绝对输出路径）；输出路径是目录 vs 文件（上游两种语义不同）；同名文件冲突；`--purchase` 语义下遇 `ErrLicenseRequired` 会先购买再下载（免费应用）。
- Error handling：可重试错误自动退避重试 3 次；不可重试错误立即失败并保留 `.tmp`；任务取消后状态可从 SQLite 恢复。

**F5. 本地库（SQLite 账本）**
- Description：记录应用、版本、下载文件、哈希、平台、时间、任务状态。
- User flow：「下载 → 已下载」页筛选/排序 → 打开文件位置 / 重新下载 / 删除记录。
- Edge cases：文件被移动/删除；同一版本多平台；重复下载；数据库被旧版本结构打开（迁移）。
- Error handling：数据库损坏时降级为「只读模式 + 提示重建索引」，不阻塞下载主链路。

**F6. MCP 接入（一键注册 / 取消注册）**
- Description：随包分发 `ipa-mcp` helper（复用上游 MCP 服务），GUI 负责写/还原客户端配置并做可用性自检。
- **现状（2026-10-19）**：GUI 侧的「MCP 接入」卡片、`MCPService`（写/还原客户端配置）已按用户要求整体移除，见 `docs/architecture.md` §8.11。`ipa-mcp` helper 仍在随包分发，客户端配置需手动写。
- User flow：设置页勾选客户端 → 注册 → 自检通过 → 提示重启客户端。
- Edge cases：客户端未安装（路径不存在）；配置文件不存在（需创建）；已有同名 `mcpServers` 键（需覆盖确认）；JSON 语法损坏（备份后拒绝写入）；Windows 路径转义。
- Error handling：任何写入失败都先还原备份；自检失败展示 helper 原始 stderr。

**F7. 首次准备与缓存管理**
- Description：展示 Apple 资产 / Unicorn 运行时的准备过程；提供缓存大小查看与清理。
- User flow：首次登录 → 显示准备中 → 完成后进入登录。
- Edge cases：缓存目录不可写；下载中断（上游有 `maxArtifactSize` 与 2 分钟超时）；Windows 侧资产格式为 `.7z` / `.pkg.tar.zst`（见风险 R4）。
- Error handling：准备失败可重试；提供「跳过并使用已有缓存」入口。

**F8. 设置与可观测性**
- Description：下载目录、并发数（默认 1）、`.tmp` 保留策略、日志级别与日志文件、代理设置（如上游 HTTP 层支持）、版本信息与上游版本号。
- User flow：设置页修改即时生效。
- Edge cases：下载目录无权限；日志文件轮转。
- Error handling：设置项校验失败时给出具体原因。

### Out of Scope（本版明确不做）
- **解密 / 去壳**：下载得到的是 FairPlay 加密 IPA，本工具**不做**任何解密、重签名、注入或越狱相关处理。
- **安装到设备/模拟器**：不调用 `ideviceinstaller` / Xcode，不处理 provisioning profile。
- **付费应用购买**：上游不支持（`Purchase` 仅免费应用），UI 明确阻止而非模拟成功。
- **Linux 平台**（用户决策：mac + win）。
- **公开发布 / 开源 / 商业化**：内部自用；但保留上游 MIT 版权声明与 NOTICE（Go 库集成构成衍生作品）。
- **多账号 / 多区域隔离**（Phase 2）。
- **TestFlight 链接、家庭共享授权、FIDO 登录、按分类搜索**（上游未支持，见报告 §7）。

---

## Technical Constraints

### 架构决策（本 PRD 的承重判断）

**T1. 集成方式：Go 库内嵌，而非 CLI sidecar。**
理由：用户明确选择「精确下载进度」为 MVP 项，而结构化进度只有拿到 `*progressbar.ProgressBar` 指针才可能（CLI 只能解析渲染文本）；库内嵌同时让错误判定从「解析 JSON 字符串」升级为 `errors.Is(err, appstore.ErrAuthCodeRequired)`，消除了 CLI 的「退出码 0 但未登录」陷阱。代价是我们直接依赖上游 `pkg/` 内部符号，需要用版本钉死 + 启动自检兜底。

**T2. 必须自行复刻的胶水（上游未导出）。**
`cmd/common.go:108-118` 的装配逻辑（`operatingsystem.New()` → `machine.New(machine.Args{OS})` → `prepareStateDirectory` → `newCookieJar` → `newKeychain` → `appstore.NewAppStore(appstore.Args{CookieJar, OperatingSystem, Keychain, Machine})`）中，`prepareStateDirectory` / `newCookieJar` / `newKeychain` **均为未导出**，需在 GUI 后端复刻约 20 行；其中 `KeychainServiceName = "ipatool-auth.service"` 定义在 `cmd/constants.go:6`（**不在 `pkg/`**），必须自行硬编码同一字符串，否则无法与既有 CLI 会话互通。

**T3. 状态目录与「共享还是隔离」的取舍。**
状态目录为 `$HOME/.ipatool`（`cmd/constants.go` 的 `.ipatool`；若设置了 `$XDG_STATE_HOME` / `$XDG_DATA_HOME` 则走 `cmd/state_directory.go:16-22`），目录以 0700 创建，内含 cookie jar（`cookies`）与（若用文件后端）keyring 文件。
**MVP 决策：与官方 CLI 共享同一状态目录与 Keychain 服务名**——好处是「GUI 登录过，CLI 与 MCP helper 都能直接用」，与 Story 6 一致；代价是存在并发访问。约束：GUI 内下载并发固定为 1；检测到外部 CLI 正在运行时给出提示而非硬锁。Phase 2 再评估「GUI 独立状态目录 + 导入向导」。

**T4. 进度实现（已核实的 API）。**
`pkg/appstore/appstore_download.go:23-31` 的 `DownloadInput` 含 `Progress *progressbar.ProgressBar`（具体类型，无法自定义实现）；上游在 `:351-372` 与 `downloadFile` 里调用 `progress.ChangeMax64(total)` 与 `progress.Set64(offset)`。库版本为 `github.com/schollz/progressbar/v3 v3.13.1`（go.mod:18），其公开只读 API 已核实：`GetMax() int`（progressbar.go:578）、`GetMax64() int64`（:583）、`ChangeMax64(newMax int64)`（:598）、`State() State`（:670），`type State struct { CurrentPercent float64; CurrentBytes float64; SecondsSince float64; SecondsLeft float64; KBsPerSecond float64 }`。
**实现**：以 `OptionSetWriter(io.Discard)` 建条（不产生终端输出），自持指针，另起 goroutine 轮询 `GetMax64()` + `State()` 推事件。注意上游只在 interactive 时创建进度条（cmd/download.go:105-122）——库集成路径下我们**始终**创建并传入。

**已实测（探针直接跑 v3.13.1，见附录 E）**：`NewOptions64(1)` 刚建好时 `GetMax64()==1` 且 `State()` 全零；`ChangeMax64(1000)` + `Set64(250)` 后读到 `GetMax64=1000 / CurrentBytes=250 / CurrentPercent=0.2500`；再 `ChangeMax64(4096)` + `Set64(3000)` 后读到 `4096 / 3000 / 0.7324`。结论：**结构化数值精确可用、无需解析渲染文本；总大小会动态增长（续传/未知长度场景），轮询天然跟上**。判定不确定态的条件是 `GetMax64() <= 1`，而非等待某个"完整"状态。

**T5. 续传契约（已实测）。**
`pkg/appstore/appstore_download.go:119`：`tmpPath := fmt.Sprintf("%s.tmp", destination)`；`downloadFile` 以 `O_CREATE|O_RDWR` 打开、`Stat()` 取大小后发 `range: bytes=<size>-`；若响应为 200（服务端不支持 Range）上游会 `Truncate(0)` 重下；`2059`/`2034` 由 `retry.Attempts(3)` 包裹。
**推论**：GUI 的「续传」= 复用同一 destination（`.tmp` 自然命中）；**绝不能在失败时删除 `.tmp`**；「重新下载」= 显式删除 `.tmp` 后再入队。目标路径语义：`output` 为空 → 落到 `t.os.Getwd()`（**GUI 必须显式传绝对路径**）；`output` 是已存在目录 → 拼接生成文件名；否则按字面路径写。

**T6. 启动自检（把上游漂移变成会失败的断言）。**
启动时断言：① 能 `Lookup` 到包内符号版本（通过构建期 `ldflags` 记录上游 commit/tag）；② 错误类型集合存在且 `errors.Is` 生效；③ `progressbar` 的 `GetMax64`/`State` 存在（编译期即保证）；④ 初始化 keychain/cookie jar 成功且状态目录可写。
不满足时拒绝启动并提示「上游 ipatool 已变更，请同步升级 GUI」。

**T7. 平台注意事项（mac + win）。**
- **Windows 官方包实证（2026-10-16 下载校验）**：只有 `.tar.gz`（无 `.zip`）；解包内**仅一个文件**，且文件名带版本号——`bin/ipatool-2.7.0-windows-amd64.exe`（43,183,104 字节，sha256 `b729608716908be287c2e6972f9e8cd05f879810ae5d182fc856c9e57cca0dd5`，已本地校验通过）；arm64 为 `bin/ipatool-2.7.0-windows-arm64.exe`（40,966,144 字节，sha256 `85ab35a268b47e1729412178063fe11621cae1135f33d3b9ab5233d436bf2562`）。
- **Windows 二进制无外部运行时依赖**：手工遍历 PE 导入描述符（`DataDirectory[1]`）确认**只导入 `kernel32.dll`**，与本地新构建的空白 Go Windows 程序一致（注：`debug/pe` 的 `ImportedLibraries()` 对 Go 二进制会返回空列表，不能用作判据——必须手工遍历描述符或建对照组）。因此**不需要** `libwinpthread-1.dll` / `libgcc_s_seh-1.dll` / 预先放置 `libunicorn.dll`；`libunicorn.dll` 是首次准备时下载并加载的（见下条）。子系统为 **CUI（控制台，Subsystem=3）**——对 MCP stdio 友好，但 GUI 若调用它会弹黑窗。
- **7z 解压是纯 Go 实现，不需要外部解压工具**：Windows 二进制内嵌 `github.com/bodgit/sevenzip`（证据：`github.com/bodgit/sevenzip/internal/aes7z.cacheKey`、`7z.decoder`、`*h7z.rarNode`）。故「首次准备」在 Windows 上是自包含的，仅依赖第三方镜像可达（见下条）。
- 运行时会下载平台相关二进制：macOS 取 PyPI 上的 `unicorn-2.1.4-cp37-abi3-macosx_11_0_arm64.whl` / `macosx_10_9_x86_64.whl`；Windows x64 取 GitHub Release 的 `windows-mingw64-shared.7z`；Windows arm64 取 msys2 镜像的 `mingw-w64-clang-aarch64-unicorn-2.1.4-5-any.pkg.tar.zst`。缓存根为 `os.UserCacheDir()/ipatool/unicorn/2.1.4`（`internal/sap/unicorn/cache_runtime.go:38`）。**风险点已从「格式能否解压」收窄为「第三方镜像是否可达」**（风险 R4）。
- Apple 资产（签名器 dylib）从 `swcdn.apple.com` 按字节区间拉取并校验 sha256，缓存到 `os.UserCacheDir()/ipatool/sap/apple-assets-v2`（`internal/sap/assets/assets.go:199-205`）。
- 凭据后端由 `github.com/byteness/keyring` 决定（`pkg/keychain/keyring.go`）：macOS Keychain / Windows Credential Manager；文件后端需 `--keychain-passphrase` 等价物。**GUI 默认走 OS 安全存储**，仅在不可用时提示配置文件后端口令。
- **Windows 分发现状（供参考/供用户装 CLI 做对照测试）**：Scoop Main bucket 有 `ipatool` 2.7.0（hash 与官方一致，`extract_dir: bin`，`pre_install` 负责把带版本号的 exe 重命名为 `ipatool.exe`）；Chocolatey 有 `ipatool 2.7.0`；**WinGet 无清单**（`microsoft/winget-pkgs` 下 `manifests/m/majd` 为 404）。我们的 GUI 若要走 winget 分发，需自己提交清单。

### Performance
- 冷启动到可搜索 ≤ 1.5 s（缓存命中）；首次准备阶段允许数十秒到数分钟，必须可见。
- 进度事件 10 Hz 上限，UI 侧合并渲染，避免高频事件打满 IPC。
- 搜索/详情 API 往返 ≤ 2 s（P95，含 Apple 侧延迟）；列表虚拟滚动支持 1000+ 条历史版本。
- 下载吞吐不做人为限制，但并发固定 1；大文件（>4 GB）内存占用 O(64KB)（流式拷贝 + 10Hz 轮询，不缓存整包）。

### Security
- Apple ID 密码与 2FA 码仅经内存 + 系统安全存储，界面不回显、日志不落盘、数据库不保存。
- Cookie jar 文件权限 0700 目录 / 0600 文件；提示用户该文件等价于会话凭据。
- MCP helper 与 GUI 共享 Keychain 条目时，若触发系统授权弹窗，界面明确解释「允许一次/始终允许」的含义。
- 不做任何绕过 Apple 付费或 DRM 的行为；付费应用明确不支持（法律与合规边界）。
- 内部工具不采集遥测；日志仅本地，可一键清理。

### Integration
- **ipatool v2.7.0**：`github.com/majd/ipatool/v2/pkg/{appstore,keychain,http,util/operatingsystem,util/machine}`；`AppStore` 接口提供 `Login` / `AccountInfo` / `Revoke` / `Lookup` / `Search` / `OwnedApps` / `Purchase` / `Download` / `ReplicateSinf` / `ListVersions` / `GetVersionMetadata` / `Bag`。
- **MCP helper**：复用上游 `ipatool mcp`（v2.7.0 起，基于官方 `github.com/modelcontextprotocol/go-sdk/mcp` + `mcp.StdioTransport{}`）。实测握手 `protocolVersion 2025-06-18`、`serverInfo {name: "ipatool", version: "2.7.0"}`、6 个工具（`search_apps` / `download_app` / `list_app_versions` / `get_version_metadata` / `list_purchases` / `purchase_app`）；该命令会把日志强制重定向到 stderr（`cmd/common.go:104-106`），因此 stdout 是干净 JSON-RPC。
  **复用机制（已确认可行）**：`cmd.Execute() int` 内部只做 `rootCmd()` + cobra 的 `cmd.Execute()`（cmd/root.go:61-89），而 cobra 默认从 **`os.Args[1:]`** 取参。因此 helper 的 main 只需 `os.Args = append([]string{"ipatool", "mcp"}, tail...)` 再调 `cmd.Execute()`，即可 100% 复用上游 MCP 服务，无需自行实现协议。
  **必须独立进程的硬约束**：`cmd.Execute()` 出错路径会写包级 `dependencies.Logger`，`initWithCommand` 还会覆盖包级 `dependencies.{OS,Machine,CookieJar,Keychain,AppStore}`（cmd/common.go:97-120）。若在 GUI 进程内调用它，会污染 GUI 自己的库集成实例与日志输出。**结论：helper 必须是与 GUI 分离的独立二进制**，两者仅通过状态目录 + Keychain 条目共享会话。
- **MCP 客户端配置**：Claude Desktop 与 Cursor 的 `mcpServers` 键（路径见 Story 6），写入前备份、原子替换。
- **SQLite**：建议 `modernc.org/sqlite`（纯 Go，无 cgo）以保持交叉编译能力。

### Technology Stack
- 语言/运行时：Go 1.26.5（与上游一致）；Node + pnpm 仅用于前端构建。
- 桌面外壳：**Wails v3**（macOS WKWebView / Windows WebView2；Go 后端 ↔ 前端 IPC 与事件原生契合「后端已是 Go」的事实，且能避免引入 Rust 工具链）。备选：Tauri 2（需 sidecar 或 Rust 侧胶水，与 T1 决策冲突，故不采用）。
- 前端：Vite + TypeScript + Tailwind CSS，界面自绘为主（控制打包体积，避免重型组件库）。
- 依赖钉死：`ipatool v2.7.0`（记录 commit/tag）、`progressbar v3.13.1`、`modernc.org/sqlite`。
- 兼容性：macOS 13+（arm64/amd64）、Windows 10 1809+（WebView2 Runtime 需存在，安装包负责引导）。

---

## MVP Scope & Phasing

### Phase 1: MVP（用户已勾选的四项 + 必要支撑）
1. **核心链路**：登录（含 2FA 两阶段）→ 搜索/直查 → 详情 → 版本列表/元数据 → 下载（串行队列 + `.tmp` 续传）。
2. **精确下载进度**：Go 库集成 + `progressbar` 指针轮询 → 百分比/字节/剩余时间/速率。
3. **本地库**：SQLite 索引 + SHA-256 完整性/去重 + 文件缺失检测。
4. **MCP 一键注册**：`ipa-mcp` helper + 配置写入/还原 + 自检。
5. **支撑项（"必要"而非"可选"）**：首次准备向导（F7）、错误码→动作映射（Story 8）、平台选择器（iphone/ipad/tv/apple/watch/vision/mac）、mac + win 打包与启动自检。

**MVP Definition**：在 macOS 上完成「登录 → 搜索 → 选一个历史版本 → 下载到指定目录 → 在本地库看到带哈希的记录」全流程，且中途断网可续传、进度真实可见；在 Windows 上至少完成「登录 → 搜索 → 下载当前版本」全流程。无此闭环即不算 MVP。

### Phase 2: Enhancements
- **多账号 / 多区域隔离**（独立状态目录 + 独立 Keychain 条目或文件 keyring + 账号切换 UI），并把 Story 6 的共享策略改为「每账号一个 helper 实例」。
- 批量下载（一次入队多版本/多应用）与下载计划（低峰时段）。
- 导入既有 `~/.ipatool` 会话；CLI 回退模式（当库集成被上游破坏时切 sidecar 保命）。
- macOS 正式签名 + 公证；Windows 代码签名；自动更新器。
- 更新检查（本地已装版本 vs 商店最新版本）与 MCP 工具扩展（暴露登录/版本对比）。

### Future Considerations
- 安装到本地模拟器 / 真机（需额外工具链，且触达 re-sign 边界，需重新做合规判断）。
- 团队内共享的 IPA 制品库（含索引与权限）。
- 反向集成：把 GUI 的队列状态暴露为 MCP 资源。
- 选做：iOS/iPadOS 版本考古的可视化时间线、价格与授权变更历史追踪。

---

## Risk Assessment

| # | Risk | Probability | Impact | Mitigation Strategy |
|---|---|---|---|---|
| R1 | **Apple 停用账号**（上游 #584 报告 3 个账号被封；#627 报 `Account is disabled.`） | Med | High | 首次登录强告知；单账号串行；复用 cookie 降低登录频率；失败不自动重试登录；提供「登出并清理会话」入口；建议用户使用非主力账号 |
| R2 | **上游 `pkg/` 内部符号漂移**（无稳定 API 承诺，我们直接依赖内部实现） | High | High | 版本钉死（v2.7.0 + commit）；启动自检断言符号与错误类型；Phase 2 提供 CLI sidecar 回退模式；把升级上游做成一次受控任务 |
| R3 | **跨进程 Keychain ACL 提示**（GUI 写入的条目被 `ipa-mcp` helper 读取时被系统拦截） | Med | Med | 同一签名身份构建 GUI 与 helper；首次引导说明「始终允许」；备选：共享口令的文件 keyring（口令存 OS 安全存储） |
| R4 | **Windows 首次准备失败**（Unicorn 运行时要跑 `.7z` / `.pkg.tar.zst` 容器：x64 走 GitHub Release、arm64 走 msys2 镜像，属第三方可用性） | Low | Med | **风险已下调**：解压能力内嵌（`github.com/bodgit/sevenzip` 纯 Go）、可执行文件零外部 DLL 依赖（只导入 `kernel32.dll`），失败面收窄为「镜像不可达/限速」。缓解：准备阶段可见可重试 + 暴露原始错误与缓存路径 + 支持手工放置缓存目录；仍建议在 Windows 上用官方 CLI 做一次对照冒烟 |
| R5 | **进度语义偏差**（初始 `GetMax64()==1` 被误当总大小；超快完成时 `SecondsLeft` 抖动） | Low | Low | 已实测判据：`GetMax64() <= 1` → 不确定态（见附录 E）；`SecondsLeft` 在已下载字节数过低时（< 5%）不显示，改为展示速率与已下载量 |
| R6 | **风控与速率限制**（`2059` 暂不可用、`2034` 令牌过期） | Med | Med | 并发固定 1；退避重试 3 次；`2034` 后引导重登而非硬重试；不做批量轰炸式请求 |
| R7 | **共享状态目录的并发破坏**（GUI 与 CLI/MCP 同时写 cookie jar / keyring） | Low–Med | Med | 独占锁文件 + 检测外部 CLI 运行；MCP helper 只读会话、不写状态；文档明确「同一时刻只用一个入口」 |
| R8 | **凭据下落**（密码、cookie、`.tmp` 内的部分 IPA） | Low | High | 密码/2FA 永不落盘；cookie 文件 0600；日志脱敏；下载目录权限提示；提供「清除所有本地凭据」按钮 |
| R9 | **打包与分发摩擦**（macOS Gatekeeper、Windows SmartScreen、WebView2 缺失） | Med | Low–Med | 内部自用：mac 先 ad-hoc + 「仍要打开」，Win 提供免安装包；安装包检测 WebView2 并引导安装；公证/签名列 Phase 2 |
| R10 | **SQLite 依赖引入 cgo**（破坏交叉编译） | Low | Med | 强制使用 `modernc.org/sqlite`（纯 Go），CI/构建脚本断言 `CGO_ENABLED=0` 可编译 mac+win |
| R11 | **付费应用期望落差**（用户以为能买付费应用） | Med | Low | UI 在详情页与下载按钮处明确禁用并解释上游限制；MCP 工具说明同步该约束 |

---

## Dependencies & Blockers

**Dependencies**
- **ipatool v2.7.0**（上游，owner: majd）：库集成对象；v2.7.0 发布的 **7 个平台 tar.gz + 各自 `.sha256sum`** 可用（macOS arm64 `5ae100d4…`、Windows amd64 `b7296087…`、Windows arm64 `85ab35a2…` 均已实测校验一致）。**Windows 官方包存在**（仅 `.tar.gz`，内含带版本号的单个 console exe；Scoop 与 Chocolatey 已收录 2.7.0，WinGet 无清单）。上游无稳定 API 契约（见 R2）。
- **Apple 私有接口与资产**（`itunes.apple.com` / `buy.itunes.apple.com` / `init.itunes.apple.com/bag.xml` / `swcdn.apple.com`）：任何一方变更都会导致功能不可用，且不受我们控制。
- **Wails v3**：桌面外壳与 IPC/事件机制；需确认其 macOS/Windows 构建与免 cgo 的前端资源嵌入方式。
- **`github.com/schollz/progressbar/v3 v3.13.1`**：进度数据源；仅用只读 API（`GetMax64` / `State`）。
- **`modernc.org/sqlite`**：本地库；纯 Go 以维持 cgo-free。
- **MCP 客户端**（Claude Desktop / Cursor）：配置路径与 `mcpServers` 结构可能随版本变化 → 自检步骤兜底。
- **Unicorn 2.1.4 运行时资产**（PyPI / GitHub Release / msys2 镜像）：第三方可用性与格式（`.whl` / `.7z` / `.pkg.tar.zst`）。
- 构建环境：Go 1.26.5、Node + pnpm、Windows 机器（打包与冒烟）。

**Known Blockers**
- **B1（唯一未解，必须先解）**：Windows 上 `ipatool` 的**首次准备与下载**是否真能跑通尚未实测（本次只验证了「包能下、哈希对、exe 零外部依赖、解压能力内嵌」，未验证运行时行为；arm64 走的 msys2 `.pkg.tar.zst` 路径尤其需要真机确认）。→ 行动：在 Windows 机器上用官方 v2.7.0 二进制（或 `scoop install ipatool`）跑通 `auth login` → `search` → `download`，并观察 `%LOCALAPPDATA%`/`os.UserCacheDir()` 下 unicorn 缓存是否落盘。
- ~~**B2（影响 T4 精度）**：`progressbar.GetMax64()` 在 `ChangeMax64` 之前的行为需验证。~~ **已解（2026-10-16）**：探针直接跑 v3.13.1 确认 `GetMax64()` 初始为 `1`、`State()` 全零；`ChangeMax64` 后读取精确，且可二次增长（见附录 E）。不确定态判据定为 `GetMax64() <= 1`。该探针已固化为单元测试（含在启动自检中）。
- ~~**B3（影响 Story 6 可行性）**：`cmd.Execute()` 是否读 `os.Args[1:]`。~~ **已解（2026-10-16）**：`cmd/root.go:61-89` + cobra 默认行为确认可读 `os.Args`，helper 通过重写 `os.Args` 复用上游 MCP 服务；同时发现「helper 必须独立进程」的硬约束（见 Integration §MCP helper）。

---

## Appendix

### A. 错误码 → UI 动作映射（基于上游常量与已实测行为）

| 上游错误/常量 | 含义 | UI 文案与动作 |
|---|---|---|
| `ErrInvalidCredentials` (-5000) | 凭据无效 | 「Apple ID 或密码不正确」→ 重新输入；**不自动重试** |
| `ErrPasswordTokenExpired` (2034) | 密码令牌过期 | 自动重试（≤3 次）→ 仍失败则「请重新登录」 |
| `ErrSignInRequired` (2042) | 需登录 | 弹出登录面板 |
| `ErrLicenseNotFound` (9610) | 无该应用授权 | 「未购买或该版本已不可获取」→ 提供查看商店页 |
| `ErrLicenseAlreadyExists` (5002) | 已购买 | 「已购买，直接下载」 |
| `ErrDeviceVerificationFailed` (1008) | 设备验证失败 | 「设备验证失败」→ 建议重启应用/重试/检查系统时间 |
| `ErrTemporarilyUnavailable` (2059) | 服务暂不可用 | 「Apple 侧暂不可用，稍后自动重试」+ 手动重试按钮 |
| `ErrAuthCodeRequired` | 需要 2FA 验证码 | **切到验证码输入态并继续同一会话**（关键：不得判定为成功） |
| `Account is disabled.`（`CustomerMessage`，见 #627） | 账号被停用 | 「该账号已被 Apple 停用」→ 停止一切自动重试，建议更换账号 |
| 状态目录存在但 Keychain 无条目 | 未登录 | 「未登录」引导（不要暴露 `The specified item could not be found in the keyring` 原文，但可展开查看） |

### B. 关键源码锚点索引（供开发直接跳转）

| 关注点 | 位置 |
|---|---|
| CLI 装配（需复刻的胶水） | `cmd/common.go:108-118` |
| Keychain 服务名 `ipatool-auth.service` | `cmd/constants.go:6` |
| 状态目录解析（XDG 支持） | `cmd/state_directory.go:16-22` |
| MCP 命令强制日志转 stderr | `cmd/common.go:104-106` |
| 进度条创建条件与参数 | `cmd/download.go:105-122` |
| 2FA 非交互「假成功」 | `cmd/auth.go:84-88` |
| 登录重试与首次准备提示 | `cmd/auth.go:59-114` |
| `DownloadInput` / `DownloadOutput` | `pkg/appstore/appstore_download.go:23-31`、`:33` |
| `.tmp` 路径构造 | `pkg/appstore/appstore_download.go:119` |
| 输出路径语义（空/目录/字面） | `pkg/appstore/appstore_download.go:351-372` |
| 续传与 Range 请求 | `pkg/appstore/appstore_download.go:236` 起（`downloadFile`） |
| `ListVersions` | `pkg/appstore/appstore_list_versions.go:21` |
| `AppStore` 接口与 `NewAppStore` | `pkg/appstore/appstore.go:68` |
| `App` 结构（无文件大小字段） | `pkg/appstore/app.go:9-17` |
| 搜索入口（storefront 先决） | `pkg/appstore/appstore_search.go:28` |
| 私有接口常量 | `pkg/appstore/constants.go` |
| Storefront 映射（账号国家决定） | `pkg/appstore/storefront.go` |
| 设备身份（MAC/IOKit） | `pkg/util/machine/mac_address_darwin.go:14-39` |
| Keychain 抽象与 keyring 库 | `pkg/keychain/keychain.go`、`pkg/keychain/keyring.go` |
| Apple 资产运行时下载与缓存 | `internal/sap/assets/assets.go:199-205` |
| Unicorn 运行时缓存 | `internal/sap/unicorn/cache_runtime.go:38` |
| progressbar 只读 API | progressbar.go:578 / :583 / :598 / :670（v3.13.1） |

### C. Glossary

- **IPA**：iOS 应用安装包（此处为 App Store 下发的 **FairPlay 加密**版本）。
- **storefront**：Apple 区域店面 ID，由账号注册国家决定；上游无 `--country` 参数。
- **externalVersionID**：指定历史版本的标识，下载历史版本时必填（`get-version-metadata` 与 `download --external-version-id`）。
- **sinf**：Apple 的授权签名片段，上游 `ReplicateSinf` 可基于已购授权重建。
- **MCP**：Model Context Protocol，AI 客户端与工具之间的 stdio JSON-RPC 协议；上游 v2.7.0 提供 6 个工具。
- **SAP**：`internal/sap`，负责下载并仿真 Apple 的签名器（含 Unicorn 运行时与 Apple dylib 资产）。
- **首次准备**：首次登录/下载时下载 Apple 资产与 Unicorn 运行时的长耗时阶段（上游原文 `preparing authentication; the first login may take a few minutes`）。

### D. References

- 调研报告：[ipatool-GUI-调研报告.md](../ipatool-GUI-调研报告.md)（8 章 + 4 附录，含 CLI 契约实测、源码剖析、竞品扫描、方案对比与路线图）
- 上游仓库：[majd/ipatool](https://github.com/majd/ipatool)（MIT，v2.7.0，2026-10-09 发布）
- 相关 issue：[#584 账号封锁报告](https://github.com/majd/ipatool/issues/584)、[#627 Account is disabled](https://github.com/majd/ipatool/issues/627)、#498 `password token is expired`、#235 多账号、#206 家庭共享、#168 FIDO、#380 TestFlight、#345 分类搜索
- 技术依赖：[Wails v3](https://v3.wails.io/)、[schollz/progressbar](https://github.com/schollz/progressbar)、[modernc.org/sqlite](https://gitlab.com/cznic/sqlite)
- 上游实测基线：ipatool v2.7.0（CLI 契约实测、源码剖析与竞品扫描均基于该版本）

---

### E. 已实测的进度探针结果（progressbar v3.13.1，2026-10-16）

探针：`OptionSetWriter(io.Discard)` 建条 → 读 → `ChangeMax64(1000)` + `Set64(250)` → 读 → `ChangeMax64(4096)` + `Set64(3000)` → 读。原始输出：

```
BEFORE ChangeMax64: GetMax64=1 State={CurrentPercent:0 CurrentBytes:0 SecondsSince:6.7791e-05 SecondsLeft:0 KBsPerSecond:0}
AFTER: GetMax64=1000 CurrentBytes=250 CurrentPercent=0.2500 SecondsLeft=0.0 KBs=1183.71
GROWN: GetMax64=4096 CurrentBytes=3000 CurrentPercent=0.7324
```

可执行结论：
1. **不确定态判据**：`GetMax64() <= 1`（新建时为 `NewOptions64(1)` 的 1，`State()` 全零）——不要用「百分比为 0」判定，那与真实 0% 无法区分。
2. **结构化数值可用**：`CurrentBytes` 与 `CurrentPercent` 与 `Set64`/`ChangeMax64` 精确一致，**无需解析渲染文本**，`io.Discard` writer 完全抑制输出（stdout 干净）。
3. **总大小可动态增长**：`ChangeMax64` 可多次调用（模拟续传/Range 协商），轮询模型天然跟上，百分比单调不回退。
4. `KBsPerSecond` 需要足够的 `SecondsSince` 才有意义（探针中因瞬时完成而偏大）；UI 应在已下载量占比 > 5% 后再展示速率与剩余时间。

---

*This PRD was created through interactive requirements gathering with quality scoring to ensure comprehensive coverage of business, functional, UX, and technical dimensions.*
**Scope locked by earlier decisions**: 平台 = macOS + Windows；定位 = 自用工具（现以源码形式开源）；MVP = {核心链路, 精确下载进度(Go 库集成), 本地库(SQLite 索引与哈希), MCP 一键注册}；多账号/多区域隔离 → Phase 2。
