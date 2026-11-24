# 架构设计：IPA-GUI

> 状态：**Phase 1 施工图**（可开工）
> 上游基线：[majd/ipatool](https://github.com/majd/ipatool) **v2.8.1**（MIT，2026-11-12）
> 构建链接：`third_party/ipatool`（v2.8.1 + FairPlay sinf 修复），说明见 `third_party/ipatool/FORK.md`
> 需求来源：[ipatool-gui-prd.md](./ipatool-gui-prd.md) · 调研依据：内部调研报告（未随仓库发布）
> 目标平台：**macOS + Windows**（源码开源于 GitHub，不提供预编译包）

---

## 0. 已实证的前提（不是推测）

这些结论都已在本机跑过，架构长在它们上面：

| 结论 | 证据 |
|---|---|
| 外部模块**可以**编译并运行期调用 `pkg/appstore` | 探针 `/tmp/ipatool-lib-probe`：`go build` 通过 darwin/arm64 + windows/amd64 + windows/arm64；运行后 `NewAppStore` 构造成功、`MacAddress()` 读到真实硬件 MAC、`AccountInfo()` 返回基线未登录错误串 |
| 上游 `pkg/` 里 import 的 `internal/sap` 不会阻挡我们 | Go 的 internal 规则只约束**直接 importer 路径**，而 `pkg/appstore` 自身在 `github.com/majd/ipatool/v2/` 子树内 |
| 签名器不需要 cgo | 走 `github.com/ebitengine/purego` 动态加载 `libunicorn.2.dylib` / `libunicorn.dll`；全库无 `import "C"` |
| 进度可结构化获取 | 持有 `*progressbar.ProgressBar`，轮询 `GetMax64()` + `State()`（`CurrentBytes`/`CurrentPercent`/`SecondsLeft`/`KBsPerSecond`） |
| Windows 凭据**不走** Credential Manager | 见 ADR-3 |
| 交叉编译 Windows 可行 | `GOOS=windows GOARCH=amd64/arm64 go build` 实测通过 |

---

## 1. 架构决策记录（ADR）

### ADR-1：后端**嵌入 Go 库**，不用 sidecar 包装 CLI

**决策**：直接 `import github.com/majd/ipatool/v2/pkg/{appstore,keychain,http,util/{operatingsystem,machine}}`，与之同进程。

**理由**：MVP 要求「精确下载进度」，而 CLI 只在 `interactive` 时创建进度条、且只渲染到 stdout 文本——没有结构化进度可解析。唯一的结构化入口是库里的 `DownloadInput.Progress`。同时库调用省掉进程间状态同步。

**代价**：直接依赖上游 `pkg/`（非稳定 API）。缓解见 §6。

### ADR-2：界面框架用 **Wails v2.16.0**（不用 v3）

**决策**：Wails v2 稳定版 + 系统 WebView；Go 后端 + React 前端。

**理由**：
1. **v3 尚未 GA** —— `go list -m -versions github.com/wailsapp/wails/v3` 最高只有 `v3.0.0-beta.30`。PRD 里「Wails v3 已 GA」的表述是**错的，本文更正之**。
2. v3 的官方模板基于 `Taskfile`，依赖本机未安装的 `task`；v2 的 CLI 无额外依赖。
3. MVP 只需要「单窗口 + 服务绑定 + 事件推进度」，v2 的 `Bind` + `runtime.EventsEmit` 完全覆盖。
4. 定位是自用工具，稳定性优先于新特性；开源后仍按「单人维护、稳定性优先」取舍。

**约束**：
- macOS 构建**必须开 cgo**（Wails 需要，且 `byteness/keyring` 的 macOS 钥匙串后端带 `//go:build darwin && cgo` 标签——CGO_ENABLED=0 会**静默降级**成文件后端）。§5 的自检会断言这一点。
- Windows 侧是纯 Go（go-webview2），可从 macOS 交叉编译。

### ADR-3：凭据后端**沿用上游的 AllowedBackends**（Windows 上是文件后端）

**决策**：`AllowedBackends = [KeychainBackend, SecretServiceBackend, FileBackend]`，与 `cmd/common.go` 逐字一致。

**关键事实（读 `byteness/keyring@v1.9.0` 源码确认）**：
- `KeychainBackend` 只在 `keychain_macos.go`（`darwin && !ios && cgo`）和 `keychain_ios.go` 注册；
- `WinCredBackend` 只在 `wincred.go`（`//go:build windows`）注册，**但上游的白名单里没有它**；
- `FileBackend` 无条件注册。

⇒ **macOS 落到系统钥匙串；Windows 落到 state 目录下的加密文件，因而需要一个「文件口令」。** 这与 PRD T7 原先写的「Windows Credential Manager」不符，本文更正。

**为什么不把 WinCredBackend 加进白名单**：那样 Windows 上 GUI 用 Credential Manager、而官方 CLI 与 MCP helper（复用上游装配）仍用文件后端，两边看不到彼此的登录态，**直接打断「GUI 登录过 → CLI/MCP 可用」这条 MVP 承诺**。保持上游一致优先。

**Windows 口令的 UX 方案**（Phase 1 采用 a）：
- a) GUI 首次运行让用户设置一次文件口令，之后把该口令存进 **Windows Credential Manager**（用**另一个** service 名，例如 `ipagui.filepassphrase`，仅作为「口令的保险箱」）。ipatool 自己的凭据仍存文件后端，**CLI 共享不受影响**。
- b) 每次启动提示输入（最安全，最烦）。
- c) 环境变量 `IPAGUI_KEYCHAIN_PASSPHRASE`（CI/脚本用）。

### ADR-4：SQLite 用 `modernc.org/sqlite`（纯 Go）

**决策**：纯 Go 驱动，不用 `mattn/go-sqlite3`。

**理由**：`mattn` 需要 cgo，会让 Windows 交叉编译从 macOS 变成不可行。我们要的只是本地账本，性能无关紧要。

### ADR-5：与官方 CLI **共享**状态目录（`~/.ipatool`）

沿用 PRD T3 的 MVP 决策：状态目录 = `$HOME/.ipatool`（若 `$XDG_STATE_HOME` / `$XDG_DATA_HOME` 为绝对路径则优先），0700，内含 cookie jar（`cookies`）与（文件后端时的）keyring 文件；keychain service 名硬编码为 `ipatool-auth.service`（该常量在 `cmd/constants.go`，**不在 pkg 里**，外部无法引用，只能复刻字面量）。

**代价**：与 CLI 并发访问。约束：GUI 内下载并发固定 1；检测到外部 CLI 占用时提示而非硬锁。

### ADR-6：MCP 走**独立进程** helper

**决策**：`cmd/ipa-mcp` 是独立二进制；GUI 只负责写/还原客户端配置 + 可用性自检。（**后半句的「GUI 负责写入配置」已按用户要求删掉，见 §8.11：helper 仍是独立二进制，但客户端配置要手动写**。）

**理由**：`cmd.Execute()` 会初始化**包级 `dependencies`**（`cmd/common.go:var dependencies = Dependencies{}`）并重定向全局 logger；在 GUI 进程内调用会污染 GUI 自己的日志与依赖状态，且 cobra 默认读 `os.Args`。helper 通过重写 `os.Args` 后调用 `cmd.Execute()` 复用上游 MCP 实现（含其「logger 走 stderr」处理）。

---

## 2. 分层与目录

```
IPA-GUI/
├── main.go                    Wails 入口：装配依赖 → 绑定服务 → 起窗口
├── app.go                     应用生命周期（OnStartup/OnShutdown）、事件发射器
├── go.mod                     module github.com/milan/ipagui   (go 1.25.0)
│
├── internal/
│   ├── ipa/                   ★ 框架无关核心：ipatool 集成层（不 import wails）
│   │   ├── deps.go            复刻 cmd/common.go 的装配（Deps 结构 + NewDeps）
│   │   ├── statedir.go        复刻 cmd/state_directory.go（未导出，必须自己实现）
│   │   ├── keychain.go        复刻 newKeychain + 口令提供策略（ADR-3）
│   │   ├── session.go         会话：Login/AccountInfo/Logout，登录态缓存
│   │   ├── progress.go        进度桥：轮询 *progressbar.ProgressBar → 结构化快照
│   │   ├── errormap.go        上游错误 → 稳定的错误码 + 中文文案 + 可执行动作
│   │   ├── download.go        串行下载队列、.tmp 续传语义、任务状态机
│   │   ├── catalog.go         搜索 / 版本考古 / 元数据（含 App 字段补全）
│   │   ├── mcp.go             写/还原 Claude Desktop、Cursor 配置
│   │   ├── selftest.go        启动自检（§6 的「把上游漂移变成会失败的断言」）
│   │   └── settings.go        设置读写（JSON，落在用户配置目录）
│   │
│   ├── store/                 SQLite 本地库账本（modernc.org/sqlite）
│   │   ├── store.go           打开/迁移/CRUD
│   │   └── schema.sql         表结构（含 schema_version）
│   │
│   ├── services/              Wails 绑定层（薄壳：DTO 转换 + 事件发射）
│   │   ├── auth.go            AuthService
│   │   ├── catalog.go         CatalogService
│   │   ├── download.go        DownloadService
│   │   ├── library.go         LibraryService
│   │   ├── mcp.go             MCPService
│   │   └── settings.go        SettingsService
│   │
│   └── dto/                   前后端共享的数据结构（Wails 会生成 TS 模型）
│
├── cmd/ipa-mcp/main.go        MCP helper 独立二进制
│
├── frontend/                  Vite + React + TS（Wails 生成 wailsjs/ 绑定）
│
└── docs/                      本文件 + PRD
```

**边界规则（硬性）**：
1. `internal/ipa` **不得** import `wailsapp/wails/*` —— 保证核心可被单测与 CLI 复用。
2. `internal/services` 不写业务逻辑，只做 DTO 转换 + 事件发射 + 调 `ipa`。
3. 前端**不**直接碰文件系统/系统 API，一律走服务。
4. 只有 `main.go` / `app.go` / `internal/services` 允许 import Wails。

---

## 3. 关键数据流

### 3.1 登录（含 2FA 两阶段）

上游的陷阱：非交互模式下需要 2FA 时，`auth login` 只打一条 info 就 `return nil` —— **退出码 0 且没有 `success` 字段**。GUI 绝不能靠退出码判断登录成功。

```
用户填 email/password (+ authCode)
  → ipa.Session.Login(email, password, authCode)
      → AppStore.Login(LoginInput{...})
          ├─ 成功 → 缓存 Account，落库，emit auth:state{loggedIn:true}
          └─ errors.Is(err, appstore.ErrAuthCodeRequired)
                 → 返回 ErrCodeAuthCodeRequired（前端切到「输入验证码」态，保留 email/password）
```

**不要**在这里做重试循环——2FA 的第二阶段由用户提供验证码后再调一次。

### 3.2 下载（进度桥）

```
StartDownload(req)
  → 校验：必须显式传**绝对**输出路径（上游 output 为空会落到进程工作目录 Getwd()）
  → 归一化：目录 → filepath.Join(dir, packageFileName) ；文件 → 按字面路径
  → 入串行队列（并发固定 1）
  → 建 progressbar（OptionSetWriter(io.Discard)，绝不写 stdout）
  → 起 goroutine：10Hz 读 GetMax64()/State() → emit download:progress
  → AppStore.Download(DownloadInput{Progress: bar, OutputPath: 绝对路径, ...})
      ├─ 成功 → 记录 LibraryEntry（大小/哈希/耗时）→ emit download:state{done}
      └─ 失败 → 保留 .tmp（下次自动续传）→ errormap → emit download:state{failed, code}
```

**不确定性判据**：进度条初始 `GetMax64() == 1`（`NewOptions64(1, ...)`），要等 HTTP 响应头到达才 `ChangeMax64(total)`。因此 `GetMax64() <= 1` = 分母未知，前端显示「准备中/不确定态」而不是 0%。

**续传语义**（上游行为，不可违背）：同一 destination 命中 `<dest>.tmp` 会带 `Range: bytes=<size>-` 续传；服务端返回 200（不支持 Range）时上游会 `Truncate(0)` 重下。∴ **失败时绝不删除 `.tmp`**；「重新下载」= 显式删 `.tmp` 再入队。

### 3.3 本地库账本

下载成功后写入 `library_entries`（app_id、bundle_id、name、version、platform、文件路径、大小、sha256、下载时间、来源任务 id）。哈希按需计算（大文件后台算，避免阻塞完成事件）。

---

## 4. 前后端契约

### 4.1 服务 API（Wails 绑定）

| 服务 | 方法 | 说明 |
|---|---|---|
| `AuthService` | `Login(email, password, authCode) → LoginResult` | `code=auth_code_required` 表示要验证码 |
| | `AccountInfo() → Account` | 未登录返回 `code=not_logged_in` |
| | `Logout() → void` | 退出本应用（置登出标记，不再回读钥匙串；凭据仍保留，重新登录即恢复） |
| | `Revoke() → void` | 删除本机钥匙串里的凭据（与 CLI / MCP helper 共用）；**不向 Apple 撤销令牌**，单独二次确认 |
| `CatalogService` | `Search(term, limit, platform) → SearchResult` | 结果里的 `artworkUrl` / `updatedAt` / `fileSizeBytes` 来自 Apple 公开目录接口（§8 表 #10）。三者都描述**当前商店版本** |
| | `ListVersions(appID, bundleID, platform) → VersionList` | |
| | `GetVersionMetadata(appID, bundleID, versionID, platform) → VersionMetadata` | 版本标识 → 显示版本 + 发布时间 |
| | `ListPurchases(page, limit, platform) → OwnedAppsPage` | 账号的**已购清单**（Apple 的购买历史，`page` 从 1 起，上限 100/页）。搜索结果里的 `purchasedAt` 永远是空的 —— 已购判定只能靠它（§8.5） |
| `CatalogService` | `OpenInAppStore(appID) → void` | 用 `macappstore://apps.apple.com/app/id<ID>` 把商店页拉起来（付费应用「先去买」最省事的入口），打不开则退回 https 链接 |
| `DownloadService` | `Start(req) → TaskID` | `req.OutputPath` 必须绝对路径；留空时文件名是 `<App 名称>_<显示版本号>.ipa`（指定历史版本时是 `<App 名称>_<版本标识>.ipa`）。名称取不到时退回 `<bundleId>`；两个不同 App 同名时后者追加 `-<appID>`（§8.19） |
| | `List() → []Task` / `Cancel(id)` / `Retry(id)` / `Remove(id)` | |
| | `RevealInFileManager(id)` | |
| | `FreeSpace() → bytes` | 下载目录所在磁盘的剩余空间（批量下载前预检；查不出来的平台返回错误，界面就跳过预检） |
| `LibraryService` | `List(filter) → []Entry` / `Delete(id)` / `Reveal(id)` | |
| | `Hash(id) → sha256` | 本机现算，不代表真实性（界面上有声明） |
| | `Artwork(id) → data: URI` | 从那个 IPA 里的 `iTunesArtwork` 读，是**该历史版本**的图标（§8 表 #11） |
| `MCPService` | `Status() → MCPStatus` | 探测客户端配置存在性与 helper 路径 |
| | `Register(client)` / `Unregister(client)` | client ∈ `claude` / `cursor` |
| `SettingsService` | `Get()` / `Save(s)` / `Preflight() → PreflightReport` | |

### 4.2 事件契约（后端 → 前端）

| 事件名 | payload | 频率 |
|---|---|---|
| `download:progress` | `{taskId, phase, current, total, percent, bytesPerSec, etaSeconds}` | 10Hz（`total<=0` 表示分母未知） |
| `download:state` | `{taskId, state, message}` | 状态迁移时 |
| `auth:state` | `{loggedIn, account, code, message}` | 登录态变化时 |
| `log:line` | `{level, message, time}` | 按需 |

### 4.3 错误分类（`errormap.go` 的产出）

统一形状：`{code, message(中文), retryable, action}`。

| code | 触发 | 前端建议动作 |
|---|---|---|
| `not_logged_in` | keyring 里没有账号（基线串 `failed to get account: failed to get item: The specified item could not be found in the keyring`） | 引导去登录 |
| `auth_code_required` | `appstore.ErrAuthCodeRequired` | 显示验证码输入框 |
| `password_token_expired` | `appstore.ErrPasswordTokenExpired` | 自动重试一次，再失败则要求重新登录 |
| `license_required` | `appstore.ErrLicenseRequired` | 询问是否获取免费 license 后重下 |
| `account_disabled` | CustomerMessage 含 `Your account is disabled.` | 明确告知（见调研报告 #627：Apple 侧风控，非本工具可修） |
| `keychain_passphrase_required` | 文件后端且未提供口令 | 弹出设置口令 |
| `disk_space` / `permission` / `network` / `unknown` | 本地与网络错误 | 按 retryable 决定是否给重试按钮 |

---

## 5. 构建与验证

```bash
# 依赖
go mod tidy

# 纯 Go 核心自检（不需要 GUI、不需要登录）
go test ./internal/ipa/ -run TestPreflight -v

# macOS 原生运行（必须 cgo）
CGO_ENABLED=1 wails dev

# macOS 打包
CGO_ENABLED=1 wails build -platform darwin/universal

# Windows 交叉编译（纯 Go 路径）
CGO_ENABLED=0 wails build -platform windows/amd64

# 只验核心与 helper 的跨平台编译（快）
GOOS=windows GOARCH=amd64 go build ./internal/... ./cmd/...
```

**「构建成功」不等于「能跑」**，所以 Phase 1 的验收必须包含：
1. `go build ./...` 在 darwin 本地通过；
2. `GOOS=windows GOARCH=amd64 go build ./internal/... ./cmd/...` 通过；
3. `ipa.Selftest()` 在真实用户环境跑通（见 §6）；
4. 前端 `pnpm build` 通过；
5. `wails build` 产出可启动的 `.app`，并**实际启动截图**确认不是白屏。

---

## 6. 上游漂移的护栏（最重要的一条）

我们直接依赖上游 `pkg/` 的内部符号。上游一次不兼容改动，**默认表现是静默失效**（比如 `progressbar` 换掉字段、错误类型改名）。所以 `internal/ipa/selftest.go` 在启动时断言：

1. **装配自检**：状态目录可写（0700）、cookie jar 可打开、keychain 能打开。
2. **凭据后端自检**：macOS 上必须拿到钥匙串后端（若拿到文件后端 → 说明构建时 cgo 被关掉了 → **报错而不是静默降级**）；Windows 上必须是文件后端且口令可用。
3. **符号自检**：`appstore.NewAppStore` 能构造；错误类型集合存在且 `errors.Is` 生效（对 `ErrAuthCodeRequired` / `ErrPasswordTokenExpired` / `ErrLicenseRequired` 各做一次 `errors.Is` 反射式断言）。
4. **进度 API 自检**：`progressbar` 的 `GetMax64()` / `State()` 在运行期可用（编译期已保证，这里断言语义：新条 `GetMax64() <= 1`）。
5. **版本自检**：运行期从 `debug.ReadBuildInfo()` 读上游 ipatool 的版本，与 `ExpectedUpstreamVersion` 比对。
   ⚠️ 实测：`go test` 二进制里 `info.Deps` 是**空的**（`len(Deps)==0`，已用最小探针证实），所以这条断言在测试里只能拿到 warn —— 为此把比对逻辑抽成纯函数 `evaluateUpstreamVersion` 单测，另外用一条读 `go.mod` 的测试兜住版本钉死。

自检失败 → GUI 显示「环境不可用」页并给出具体哪一条失败，**不允许带着坏掉的前提继续跑**。

---

## 7. 待办与未决

| 项 | 状态 |
|---|---|
| `wails` CLI 需与库版本一致 | ✅ 已装 v2.16.0（`go install …@v2.16.0`），与库目标版本一致 |
| Windows 真机运行时行为（首次准备 / 登录 / 下载） | **未验**（B1）。macOS 机器上跑不了 exe |
| Windows arm64 的 `libunicorn.dll` 是否依赖 `libwinpthread` | **未验**。msys2 包把它列为依赖，但 `purego` 动态 `LoadLibrary` 时才暴露 |
| macOS 公证 / Windows 代码签名 | 当前跳过：发布形式为源码，不提供预编译包 |
| 多账号 / 多区域隔离 | 排 Phase 2（用户已确认降级） |
| 账本「版本」列只存一个值 | 指定历史版本 → 存 Apple 的版本标识（一串数字）；下载最新 → 存 App Store 的显示版本号（`8.0.50`）。同一个 App 下这两次下载在账本里长得不一样，所以版本列表的「本地」列要按两种口径各对一次（见 §8.2；**显示版本号那一口径只能用在最新版本行上**，否则会把所有历史版本都标成「已下载」—— §8.3.3 踩过）。要让二者都精确，得给 `library_entries` 加独立列，属 schema 改动 |
| 真实账号的端到端链路（搜索 → 版本 → 元数据 → 下载 → 账本） | **下载腿已真机跑通**：用户自己点的 QQ（`com.tencent.mqq`）`bytes_done = bytes_total = 647724400`，18:44:30 → 18:45:45 完成，账本与磁盘都有（§8.4 开头）。搜索/版本/元数据见本表上行。仍未在真机上点过的是**本次新加的搜索页下载提示条**（按无头预览 12 状态 + 3 处故障注入验的，§8.4） |
| 图标 / 更新时间 / 体积的真实端点响应 | **搜索富化已验真机**（§8.3.1：`search_apps` 返回真实 `artworkUrl` 与 `fileSizeBytes`，并因此抓出 §8 表 #13/#14 两个错；§8.3.3：`updatedAt` = `2026-09-29T01:36:22Z`）。**本地库图标未验真机** —— 只在无头预览里用桩 IPA 走过 |
| 真机上版本列表 / 版本元数据 | **已验证**。`list_app_versions` **6.9s** 返回（微信 500+ 个版本标识，`latestVersionId` = `891813653`）；`get_version_metadata` 用真实版本 ID **7.7–10.1s** 返回 `{"displayVersion":"8.0.79","releaseDate":"2026-09-23T14:25:16Z"}`；不存在的版本 ID 约 **40s** 后报错，不会永远挂着 |
| 「是否已购」的信息来源 | 只能来自账号的已购清单（`CatalogService.ListPurchases` → Apple 购买历史，实测 55 个 App / 3.1s）。Apple 的公开目录接口（`/lookup`、`/search`）返回的 44 个字段里**没有任何 `purchase` 字段**，所以凭搜索响应判「已购」必然判错（§8.5 修的就是这个 bug）。拉不到已购列表时界面显示「已购未确认」而**不**显示「未购买」，也不拦下载 |
| 下载体验这一批改动（A–E） | **无头预览验过，真实下载没整体跑过**。文件名带版本号、完成后算 sha256、`revealAfterDownload` 生效、失败自动重试这四项都在 `internal/services/download.go` 的同一条 `execute` 路径上，靠单测（命名/错误分类/账本版本）+ 20 个预览状态覆盖；但「一次真实下载确实产出了 `com.tencent.mqq_9.3.75.ipa` 且账本里有 sha256」**没验**——那要真下一份几百 MB 的包（§8.6） |
| 钥匙串授权跨构建复用 | **未验**。已把签名身份做成可配的 `CODESIGN_IDENTITY`（见 §8 表 #17），但「换成 Apple Development 证书 → 点一次『始终允许』 → 重新构建后不再问」这条链路没有在真实点击下走过 |
| `FreeSpace` 的 Windows 分支 | **未验**。darwin 分支在真机上跑过（批量确认框里显示得出 64.0 GB），Windows 走 `kernel32.GetDiskFreeSpaceExW`，只验过 `GOOS=windows` 交叉编译通过 |
| 重复下载提示的分工 | 正在下/排队中的同版本：按钮直接禁用并把状态写在按钮上（「下载中 43%」/「排队中…」），不弹窗；账本里已有同一份：`window.confirm` 问一句、可跳过（AC2）。后端**没有**硬去重 —— 绕过界面直接调 `Start`（含 MCP helper）仍会重复下载同一版本；AC 只要求「提示 + 可跳过」，故不加硬拦截。队列那段提示保留作竞态兜底（见 §8.4） |

---

## 8. 实测修正记录（与本文档早期判断不一致的地方）

搭建过程中被实测推翻或收紧的判断，逐条记在这里，避免以后又按旧结论写代码。

| # | 早期判断 | 实测结论 |
|---|---|---|
| 1 | macOS 构建可不带 cgo | **不行**。`byteness/keyring` 的 macOS 实现带 `//go:build darwin && !ios && cgo`，`CGO_ENABLED=0` 会静默换成文件后端 —— 凭据存不进的钥匙串，且与官方 CLI 不共享登录态。`Selftest` 因此把「macOS 必须拿到 keychain 后端」写成硬断言。 |
| 2 | 上游 ipatool 要求 go 1.26.5 | 实测 `go.mod` 是 **`go 1.25.0`**，无 toolchain 行。我们的 `go.mod` 取 1.25.0 为下限。 |
| 3 | Wails v3 已 GA | **仍为 beta**（当时最高 `v3.0.0-beta.30`，且 v3 模板依赖未安装的 `task`）。选 v2.16.0。 |
| 4 | 上游状态目录里 keyring 用 Keychain 后端即可 | 上游白名单是 `[Keychain, SecretService, File]`，**没有 WinCred**。所以 Windows 上（Keychain 未注册）必然落到文件后端，需要口令。想用 Windows 凭据管理器就得改白名单，那会**打断**与官方 CLI/MCP helper 的登录态共享，因此不改（ADR-3）。 |
| 5 | 下载的 `OutputPath` 可以给目录 | **必须给完整文件名**。给目录时上游用响应里的 `bundleShortVersionString` 拼名字，我们在下载前算不出 `<dest>.tmp` 在哪，续传判断与「删除半成品」都会指错路径 —— 属静默失效，故在 `Download` 里显式拒绝目录。 |
| 6 | MCP helper 应由上游 `cmd.Execute()` 提供 | **不可行**。`cmd.Execute()` 会初始化上游包级 `dependencies` 并重定向全局 logger，在 GUI 进程内调用会污染 GUI 自身状态。改为独立进程 `cmd/ipa-mcp`（ADR-6）。 |
| 7 | MCP 工具集照抄上游 6 个工具即可 | 上游 6 个工具**不含任何登录工具**。我们的 helper 提供 7 个工具（含 `account_status`、`lookup_app`），但**仍然故意不提供登录工具**：密码一旦作为工具参数传入就会进入模型上下文。 |
| 8 | MCP 的下载可以接受任意绝对路径 | **不能**。工具参数由模型生成、而模型会被页面内容影响（提示注入），一个能写任意路径的下载工具等于把任意文件写入交给模型。落点钉死在用户选定的下载目录内，并用「解析后仍在目录内」的几何判据校验。 |
| 9 | 付费应用（`App.Price > 0`）在搜索页一律禁用下载（PRD Story 8 AC3 的字面要求） | **按已购/未购区分**。上游只有 `Purchase` 限免费应用；`Download` 对已购付费应用是正常路径——这正是 ipatool 最常见的用法（把自己买过的 App 重下回来），一律禁用等于把「已购付费 App 下不了」这个真实缺口做进界面。故：未购付费（`purchasedAt` 为空）→ 禁用并说明「未购买」；已购 → 允许下载。 |
| 10 | 搜索结果里的图标与体积「上游已经有了，取一下就行」 | **上游拿不到**。`pkg/appstore/app.go:9-17` 的 `App` 只声明 7 个字段（trackId / bundleId / trackName / version / price / purchaseDate / platforms），JSON 解码时其余 37 项（含 `artworkUrl512`、`fileSizeBytes`）被直接丢掉，且那是上游内部结构体，改不了。我们按同一批 app id 再问一次公开接口：`internal/ipa/artwork.go` 的 `GET /lookup?id=…`，并按 §8 表 #12 带上商铺头。实测「微信」在 `entity=software` 下一次搜索返回 44 个字段（`fileSizeBytes` = 967153664 ≈ 922 MB、`averageUserRating`、`genres`、`minimumOsVersion`、`screenshotUrls` 等），我们只取图标与体积两项 —— 其余字段没有对应的界面位置，多取只会多一份要维护的契约。 |
| 11 | 本地库的图标也去问 Apple | **问 Apple 只会拿到「当前商店版本」的图标**，而本地库里每一份是**某个历史版本**，图标与版本必须严格一致才有考古意义。上游下载时已经把那个版本的图标写进包里：`appstore_download.go` 从购买响应的 `songList[].artworkURL` 取图（:222），`applyPatches` 把它写成 zip 条目 `iTunesArtwork`（:419-426）。所以 `ReadIPArtwork` 直接读本地那个 IPA，离线可读、版本自洽（顺带也就没有「商铺不对导致全网无图」的问题）。 |
| 12 | 公开接口按 `country=<两位国家码>` 取商铺就够，用账号的商铺数字 `143524-2,34` 也能塞进去 | **`country=143524` 直接被拒**（返回 gzip 错误体）；而 `country` 需要 ISO 两位码，上游那个「商铺数字 → 国家码」的映射表在 `pkg/appstore/storefront.go:8` 是**未导出**的（约 110 项），我们抄不了也不该抄。实测真正的解：公开接口认 **`X-Apple-Store-Front` 头**，直接把账号的原始商铺串塞进去即可 —— 不带该头时 Apple 按美国商铺回答（`trackViewUrl` 变 `/us/`），带上 `143524-2,34` 则返回 `/am/` 的条目。于是不需要任何映射表。 |
| 17 | 「macOS 每次都要钥匙串授权，是我们没处理好」 | **是签名身份的问题，不是读凭据的代码有问题**。上游 `cmd/common.go` 的 keyring 配置是 `KeychainTrustApplication: true`，落到 macOS 就是 ACL 里没有预信任的程序（`byteness/keyring@v1.9.0/keychain_macos.go:208-222` → `byteness/go-keychain@v0.1.0/macos.go:39-56`，`TrustedApplications: nil` → `SecAccessCreate(label, NULL)`），**任何程序读这个条目都要问一次**；而授权是按**代码签名身份**记的，`scripts/build-macos.sh` 原来固定 ad-hoc（`--sign -`，`Signature=adhoc`、`TeamIdentifier=not set`），**每次构建 cdhash 都变** ⇒ 点过的「始终允许」次次作废。此外同一个 `ipatool-auth.service` 条目有三个二进制在访问：`.app` 主程序、包内 `ipa-mcp`（独立签名）、以及用户自己装的官方 `ipatool` CLI（ADR-5 的共享登录态）。修法：`scripts/build-macos.sh` 支持 `CODESIGN_IDENTITY`（默认仍是 ad-hoc，签名前先 `security find-identity` 校验身份存在），换一个稳定证书后「始终允许」即可长期有效 |
| 15 | 「剩余空间要么引 `golang.org/x/sys`，要么自己写 syscall」 | **不引新依赖**：按 build tag 用标准库分平台实现 —— darwin/linux 用 `syscall.Statfs`（`Bavail * Bsize`，不是 `Bfree`：保留块不算可用），windows 用 `syscall.NewLazyDLL("kernel32.dll")` 的 `GetDiskFreeSpaceExW`。三目标交叉编译都要过，而 `x/sys` 不是直接依赖；查不出来的平台返回错误、界面就诚实地说「查不到，这次不预检」 |
| 16 | 「无头预览里写断言和真机一样」 | 预览环境自己有三个坑：① `evaluate` 的代码是写在模板字符串里的，正则 `/[0-9]{9}/` 若写成 `/\d{9}/` 会被模板字符串把 `\d` 吃成 `d`（与 §8.4 的 `\n` 同源）；② 桩的 `List()` 若返回**同一个数组引用**，React 认为状态没变、**不重渲染**，「新增任务后列表刷新」就永远测不到（真实现每次都返回新切片）；③ 提示条只有一条，后一个动作的 notify 会把前一条顶掉，「删半成品成功」这类中间态必须在下一个动作之前抓走 |
| 13 | 「体积就是个数字，字段声明成 `int64` 就行」 | **`/lookup` 返回的 `fileSizeBytes` 是带引号的字符串**（实测 `"967153664"`）。声明成 `int64` 时 `json.Unmarshal` 对整个响应报错，而富化是「失败就什么都不补」——于是**图标也一起消失**：界面上一张图都没有，接口却明明返回了 `artworkUrl512`。这个坏法是**真机跑 `search_apps` 才暴露的**：当时单元测试的桩数据用的是数字形态，所以 6 条测试全绿。修法：`byteCount` 自定义类型同时吃字符串与数字（含 `"1234.0"` 这种带小数），并把一次解整片改成**逐条解码** —— 一条怪数据只丢它自己。回归断言已把桩数据改成字符串形态（§8.3）。 |
| 14 | 「拿不到商铺号就别问了」（富化在 `storeFront == ""` 时提前返回） | **删掉这个提前返回**。凭据里没有商铺号时，搜索本身也是按美国商铺走（`countryCodeFromStoreFront("")` → `us`），所以不带商铺头再问一次目录接口拿到的图标与搜索结果来自同一商铺；而提前返回的效果是「凭据里少一个字段 → 界面上所有图标消失」，一个纯装饰项不该有这种静默失效。 |

### 8.1 已完成的验证（可复现）

```bash
go test ./...                                        # internal/ipa + internal/store 全绿
GOOS=windows GOARCH=amd64 go build ./internal/... ./cmd/...   # Windows 交叉编译通过（含 modernc sqlite）
go build -o /tmp/selfcheck ./cmd/selfcheck           # 环境自检（5 条断言）
go build -o /tmp/ipa-mcp ./cmd/ipa-mcp              # MCP helper
/tmp/ipa-mcp --selftest                             # 非 MCP 模式自检
```

已验证的**承重**结论（不是「编译过了」）：

- 自检的版本断言做过**故障注入**：把 `ExpectedUpstreamVersion` 改成 `v9.9.9` → 真的报 `✗ upstream_version` 且退出码 1；还原 → 退出码 0。
- MCP helper 的 stdio 实测干净：`initialize` 返回 `protocolVersion 2025-06-18`、`serverInfo {ipa-gui, 0.1.0-test}`；`tools/list` 返回 7 个工具且注解正确；**stdout 每一行都能被 `json.loads` 解析**、**stderr 0 字节**。
- 未登录态实测走的是结构化分支：`account_status` 返回 `structuredContent.loggedIn=false`（若 `IsNotLoggedIn` 失配，这里会变成 `isError` 的工具错误）。
- 文件名拼接守的是**几何判据**：`../../etc/passwd`、`..\..\windows`、`/etc/passwd`、含 `\x00` 的输入拼进目录后 `filepath.Rel` 都不以 `..` 开头。

### 8.2 2026-10-16：补齐的验收项与「无头预览」验证法

这一轮把 PRD 里能靠前端收口的验收项补上（`SearchView` / `EnvView` / `LibraryView` + 两处后端小改）：

- **Story 1 AC1** 未登录态的**风险告知**：说明 Apple 会把这登录当风险行为（上游 #584 / #627）、只调官方接口、不复用他人账号、单账号串行、凭据只进钥匙串。未确认时这张警示卡常驻在登录表单上方；登录表单照旧渲染，但首次点「登录」会先弹一个带**完整风险文本**的系统确认框，取消则不放行并把告知拉回展开态，确认才写 `localStorage["ipagui.risk-acknowledged"]` 继续登录 —— 也就是说「没读过就登进去」这条路走不通。确认过之后卡片收成一行「你已确认过第三方登录的风险。再看一遍」。
- **Story 1 AC5** 未登录时显示引导文案（`AuthStatus.message`）而不是原始错误。
- **Story 7 AC1/AC3** `services.EnvironmentInfo` 新增 `cacheDir`（`os.UserCacheDir()`），「环境」卡片显示首次准备缓存目录并说明「已有 apple-assets-v2 与 unicorn/2.1.4 时会跳过」。
- **Story 3 AC2** 版本列表每行一个「查询」，调 `GetVersionMetadata` 显示「显示版本 · 发布时间」，并提供「查询全部显示版本」（串行请求，Apple 侧有限流）。
- **Story 3 AC4** 版本列表新增「本地」列，按版本标识与本地库交叉引用；因为**下载最新时账本存的是显示版本**，所以查到显示版本后会再对一次（`matchEntry`，`localEntry` 只是渲染层包一层）。
- **Story 5 AC2** 重复下载前给提示、且提示可以「跳过」：点下载时先查队列（同 `bundleId + platform + versionId` 且 `pending/running` → 「这个版本已经在下载队列里了（任务 xxxxxxxx，下载中）」），再查账本（有记录 → 「已存在（哈希一致）：账本里这一份的 sha256 是 …」＋文件在不在磁盘上＋路径；没算过哈希就说「还没算过 sha256，没法比对内容」）。确认框取消 = 真的不下（`Start` 不被调用），确定 = 照常下载。判断本身出错只提示一句、不拦下载。
- **Story 5 AC4** 本地库顶部注明「哈希只用于本地校验、不代表真实性、不能当正版证据」。
- **Story 8 AC3** 付费应用门禁，口径见 §8 表 #9。
- **账本版本号**：`DownloadService` 记 `ExternalVersionID` 为空时用登录后 `ListVersions` 拿到的 `App.Version`（显示版本号）填 `version`，否则账本里「下载最新」永远是空版本（见 §8 表 #9 同段的 `ledgerVersion`）。

验证分两层（当时没有可用的真人账号，且桌面处于**锁屏**，GUI 自动化点不动）：

1. **编译层**：`npx tsc --noEmit`、`vite build`、`bash scripts/build-macos.sh`（6 步全过，含 `go test` / `go vet` 与签名校验）。
2. **界面层**：`frontend/preview.html` 在真实浏览器里伪造 `window.go` / `window.runtime`（默认 `vite build` 只以 `index.html` 为入口，这个文件不进 app 包），再用无头 Chrome + CDP 逐页断言 `document.body.innerText` 并截图：

```bash
cd frontend && npx vite --port 5199 --strictPort   # 一个终端里起着
node scripts/preview-ui.mjs                        # 另一个终端：10 个状态，断言不过就退出码 1
```

`scripts/preview-ui.mjs` 用 Node 22 自带的 `WebSocket` + CDP 驱动无头 Chrome，逐状态断言 `document.body.innerText`（弹窗类断言则把 `window.confirm` 换成记录桩，把文案与「`Start` 被调了几次」一起当文本喂给断言）。10 个状态：搜索页初始 → 搜索结果（免费/付费门禁）→ 历史版本列表 → 版本元数据 + 本地交叉引用 → 付费门禁明细 → 下载队列（进行中）→ 本地库（哈希声明）→ 未登录风险告知 → 重复下载提示（账本里已有）→ 重复下载提示（队列里正在下）。

测到的（截图 `/tmp/ipagui-shots/`）：搜索「微信」命中 1 条；版本列表查 `857109923` 得到 `8.0.50 · 2024/6/18 08:00:00` 后「本地」列出现「已下载」+ 路径；未购付费 App 的「下载最新」是禁用态并显示「不能下载：未购买」，已购的可用；下载队列显示 `201 MB / 460 MB · 43.7%`；未登录态渲染出完整风险告知（5 条）且首次点「登录」会被确认框拦住（这一步的断言是把 `AuthService.Status` 换成未登录桩，再点顶栏「刷新状态」）；重复下载提示两条分支都出现，且「跳过」那次 `Start` 调用数为 0、「仍然下载」那次为 1；本地库渲染出哈希声明与两行（含后端给的「文件已不在磁盘上」）。

**故障注入**：
- `ledgerVersion` 的单测（`internal/services/download_test.go`）把分支改成「一律返回外部版本标识」后，只有「下载最新」那条子测试变红（另两条仍绿——说明只有它真守这个分支），还原后全绿。
- 重复下载守卫：把 `if (!(await confirmDuplicate(app, versionID)))` 短路成 `if (false)` 后，步骤 09 / 10 全红（提示文案消失、`Start` 调用数从 0/1 变成 3），还原后 10 步全绿。
- 构建产物里真的带上了新界面：`python3` 按字节查 `build/bin/ipagui.app/Contents/MacOS/ipagui`，`已存在（哈希一致）` 命中 1 次（`grep -ac` 在 Mach-O 上数不出多字节模式，别用它下结论）。

诚实边界：以上是**伪造绑定**下的界面验证，不是真实 Apple 调用。真实账号的搜索 → 版本 → 元数据 → 下载 → 账本链路仍未跑过，需要在解锁的会话里点一遍（或等自动化）。

### 8.3 2026-10-16：图标与体积（搜索 + 本地库）

新增 `internal/ipa/artwork.go`（公开目录接口客户端 + 「从 IPA 里读图标」）与 `LibraryService.Artwork`，`AppView` 增加 `artworkUrl` / `fileSizeBytes`（`omitempty`）。背景与选型见 §8 表 #10 / #11 / #12。（`updatedAt` 是后来用户要的，见 §8.3.3。）

界面上的三处用法：

- **搜索结果**：「应用」列变成图标 + 名称，「版本」列下面显示体积（只有一个版本有体积：Apple 只给当前商店版本，历史版本不提供 —— 显示为「体积未知」，不编数字）。
- **版本面板**：标题左侧显示该 App 图标，并把「当前商店版本的体积」写进说明文字。
- **本地库**：每行左侧是**那个版本自己的图标**，顺序读取（一份一份来 —— 每个请求要开一个 GB 级文件，并发等于同时开一堆），文件已丢失的行直接跳过并显示占位块；读不到包里 `iTunesArtwork` 的行退化成占位块 + 「重取图标」按钮，不整行报错。
- **重复下载确认框**里也带上体积（下之前知道要下多大）。

验证（全部实测，命令见 §8.1 与 §8.2 的两段）：

- `internal/ipa/artwork_test.go` 6 条单测：商铺头确实发出去了（服务端断言 `X-Apple-Store-Front`）、非 200 报错、富化失败时视图不被破坏、按 `trackId` 合并、`ReadIPArtwork` 三种输入（有图标 / 没图标 / 根本不是 zip）、data URI 前缀。
- **故障注入**（3 处，全部确认变红后还原全绿）：去掉商铺头 → `TestStoreMetadataLookupSendsStoreFront` 红；`views[index].ArtworkURL = item.ArtworkURL` 改成不写回 → `TestEnrichAppViewsMergesByTrackID` 红；`if file.Name != iTunesArtworkName` 改成 `if true` → `TestReadIPArtwork` 红（读到了 `Info.plist` 的字节，说明判据守的是**内容**不只是「成功」）。
- 无头预览扩到 10 个状态（步骤 02 / 05 / 07 增加 DOM 断言：`table img.icon` 的个数、占位块个数、`img.src` 前缀是 `data:image/png;base64`、以及错误分支的占位块 title 与「重取图标」）。**故障注入** 3 处同样确认变红：搜索行不传 `artworkUrl` → 02/05 红；`sizeText` 一律返回「体积未知」→ 02/05 红；本地库不发起图标请求 → 07 红。
- 产物核对：python 按字节查 `build/bin/ipagui.app/Contents/MacOS/ipagui`（34.5 MB），`X-Apple-Store-Front` / `artworkUrl512` / `iTunesArtwork` / `重取图标` / `体积未知` 均命中。

诚实边界：**本地库的图标仍然只在无头预览里验过**（桩 IPA / 桩 data URI），包里的 `iTunesArtwork` 那条路还没拿真实下载到的 IPA 走一遍。

#### 8.3.1 真机跑了一次搜索 —— 抓到两个只有真机才会露的错

上面那套东西做完时，搜索路径的真实端点响应是「未验」。后来用**已登录的钥匙串会话**直接驱动产物里的 MCP helper（只读工具，不需要重输密码）跑了一次真实搜索，结论如下。

命令形状（`python3` 逐行写 JSON-RPC 到 helper 的 stdin）：

```
build/bin/ipagui.app/Contents/MacOS/ipa-mcp
  → initialize / notifications/initialized
  → tools/call {"name":"search_apps","arguments":{"term":"微信","limit":2,"platform":"iphone"}}
```

**第一次跑：真实数据回来了，但 `artworkUrl` 与 `fileSizeBytes` 都是 `null`。**

```
{"id":414478124,"bundleId":"com.tencent.xin","name":"WeChat","version":"8.0.79","artworkUrl":null,"fileSizeBytes":null}
```

而同一时刻用 `curl` 打同一个接口，`artworkUrl512` 与 `fileSizeBytes` 都在。两个原因（§8 表 #13 / #14）：

1. `fileSizeBytes` 是**字符串** `"967153664"`，声明成 `int64` 导致整个响应解码失败；
2. 富化在 `storeFront == ""` 时提前返回（凭据里这个字段是可以为空的）。

修完后重跑同一命令，两个字段都到位，而且是本次真实账号商铺（`143524-2,34` → 亚美尼亚 `am`，所以名字是 `WeChat` 而不是「微信」，与 `country=AM` 的 `curl` 结果一致）：

```
{"id":414478124,"name":"WeChat","version":"8.0.79",
 "artworkUrl":"https://is1-ssl.mzstatic.com/image/thumb/Purple221/v4/61/70/2e/…/AppIcon-0-0-1x_U007epad-0-1-0-sRGB-0-85-220.png/512x512bb.jpg",
 "fileSizeBytes":967153664}          # ≈ 922 MB
{"id":333206289,"name":"Alipay - Simplify Your Life","version":"12.12.30",
 "fileSizeBytes":514912256}          # ≈ 491 MB
```

**故障注入**（证明那条回归断言真的在承重）：把 `FileSizeBytes` 改回 `int64`，`TestStoreMetadataLookupSendsStoreFront` 应声变红，报的正是真机看到的现象 ——

```
--- FAIL: TestStoreMetadataLookupSendsStoreFront
    artwork_test.go:64: 图标 URL = ""
    artwork_test.go:68: 体积 = 0, 期望 967153664（字符串形态也要能解）
```

还原后全绿。教训写在 §8 表 #13 里：**桩数据的形态也是契约的一部分** —— 桩里把字符串写成数字，就等于把唯一能抓住这个 bug 的机会丢掉了。

诚实边界：`list_app_versions` / `get_version_metadata` 这两条只读腿也已经真机跑通（见 §8.3.2）；下载那条腿（数百 MB，且会写用户磁盘）没有用户明确点头不会自己跑。

#### 8.3.2 一次「卡住」的假警报 —— 量具自己的毛病

真机验完搜索之后，顺手去跑另外两条只读腿，**一个请求都没回来**，等了 4 分多钟。当时的结论是「`list_app_versions` / `get_version_metadata` 在真机上会挂住」，甚至据此给 `GetVersionMetadata` 加了一个 60s 的 `context.WithTimeout`（理由写的是「上游没超时，界面会永远显示查询中」）。

那个结论是错的。换成一个**只发一次**调用的脚本，同一天同一台机器：

| 调用 | 结果 |
|---|---|
| `list_app_versions`（微信） | **6.9s** 返回，500+ 个版本标识 |
| `get_version_metadata`（真实版本 ID `891813653`） | **7.7s** 返回 `8.0.79 / 2026-09-23T14:25:16Z` |
| `get_version_metadata`（不存在的版本 ID `857109923`） | **40.4s** 后报错，不是挂住 |

第三行是**对照实验**：用**改动前**的二进制跑同一个输入，也是 40.4s 报错。既然改前改后一样，那个 60s 超时就什么都没修 —— 已回滚（`versionMetadataTimeout` / `storeCtx` 在 `internal/ipa/catalog.go` 里已不存在）。

根因在我的临时 Python 量具：`select.select([proc.stdout], ...)` 之后调 `readline()`。`subprocess` 拿到的 `TextIOWrapper` 有内部缓冲 —— 数据一旦被 `readline()` 之前的某次读取吞进缓冲区，`select` 就再也看不见它（fd 上没有新字节），而 `readline()` 又在那儿等更多数据，于是**看上去**就是对方不回话。同一份脚本一次发多个请求时更容易触发（并发 + 缓冲）。

教训两条，都是关于「别相信自己的量具」：

1. 报「卡住」之前，必须用**改动前的同一二进制 + 完全相同的输入**跑一次对照。这次的对照只花了 40s，就把一个通向「多加一段没必要的超时逻辑 + 一条错误的文档结论」的分叉堵住了。
2. 别对 `subprocess` 的 `TextIOWrapper`（或任何带缓冲的 reader）用 `select` 判可读。要在 stdio 上判超时，要么在**线程/队列**里读，要么直接对 fd 做非阻塞读，要么像 §8.1 那样用真正会退出的一次性调用。

#### 8.3.3 版本号后面加「更新时间」

需求来自用户（原话：「版本号后面 增加更新时间」）。`AppView` 增加 `updatedAt`，搜索结果的「版本」列在版本号下面多一行「更新 2026/9/29」（列头改成「版本 · 更新时间 / 体积」），版本面板的说明文字也带上当前商店版本的更新时间。

字段来源与语义：Apple 公开接口的 `currentVersionReleaseDate`（`2026-09-29T01:36:22Z`，实测值，见 §8.1 的 curl）。它与 `version`（`8.0.79`）是配套的一对 —— `version` 说「现在是哪个版本」，`updatedAt` 说「这个版本什么时候上线的」。同样是**当前商店版本**的属性，历史版本没有（与 §8.3 的体积同一条边界）。

- 后端：`internal/ipa/artwork.go` 的 `storeMetadata` 增加 `CurrentVersionReleaseDate string \`json:"currentVersionReleaseDate"\``，富化时写进 `AppView.UpdatedAt`；`catalog.go` 的 `AppView` 增加 `UpdatedAt string \`json:"updatedAt,omitempty"\``。
- 前端：`api.ts` 新增 `formatDate()`（只到「天」：列表里不需要 `2026/9/29 09:36:22` 那么细；解析不了就把原值原样返回，宁可显示得丑也别显示「无效日期」）。
- 断言：`TestEnrichAppViewsMergesByTrackID` 的桩数据加了 `currentVersionReleaseDate` 并断言它落到视图上（`只返回了 id=2，id=1 不该被写上值` 那条同时扩成三个字段）。无头预览步骤 02 增加 `"updatedCells":1` / `"updateMatches":true`。

**验证时踩到的坑（值得记）**：日期断言如果写死 `更新 2026/9/29`，就悄悄依赖了跑它的机器时区 —— 换台 TZ 不同的机器就会红/绿乱跳，而它看上去仍然是「通过了」。改成在页面里**用同一条 ISO 串现算期望值**再和 DOM 实际渲染的文本比。这不是自证：后端少给 `updatedAt` 时 DOM 会是「更新 —」，`updateMatches` 就是 `false`。

**故障注入**：把 `更新 {formatDate(app.updatedAt)}` 改成 `更新 {formatDate(undefined)}`（元素还在、数据流断了）→ 步骤 02 变红，且报的正是 `少了 ["\"updateMatches\":true"]` —— 而 `"updatedCells":1` 仍然通过，说明两条断言分工正确：一条守「有这个东西」，一条守「它真的来自后端」。还原后 10/10 全绿。

**顺手抓到的另一个缺陷（看截图才发现的，断言当时全绿）**：版本列表里**四个历史版本全都显示「已下载」，而且都指向同一份文件**（`com.tencent.xin_857109923.ipa`）。

根因是 §8.3 加的那个「显示版本号回退对」用错了地方：`matchEntry(…, displayVersion)` 的本意是接住「下载最新时账本记的是显示版本号」这种情况（§7 表「账本『版本』列只存一个值」），但它在**每一行**都被传了 `versionList.app.version`（当时是 `8.0.50`），于是每个还没查元数据的行都拿 `8.0.50` 去账本里对，全都命中最新那一次下载 —— 用户其实只有最新那一份，界面却告诉他四个版本都有。

修法：**只有「问的就是最新版本」时才允许用显示版本号回退**。渲染层 `localEntry()` 先判 `id === versionList.latestVersionId`；`confirmDuplicate()` 增加 `latestVersionID` 参数（「下载最新」传空串即视为最新，版本面板传列表里的最新标识），不满足就不传显示版本号。已查到元数据的行仍按 `map[displayVersion]` 对 —— 那是精确的（显示版本号唯一对应一次构建），与「假设这行就是最新」不是一回事。

无头预览步骤 03 因此加了 `"downloadedBadges":1` 与 `"lastRowCell":"—"`：**故障注入**（把 `isLatest` 写成 `true`，即恢复旧行为）→ 步骤 03 报 `少了 ["\"downloadedBadges\":1","\"lastRowCell\":\"—\""]`；还原后 10/10 全绿。

这条值得单独记一笔：**断言只守你想到要守的东西**。这个坏法在 10/10 全绿的状态下躺了好几轮，是「把截图当断言看」才发现的 —— DOM 断言（第 N 列里有「已下载」）和「第 N 列里的东西是不是对的」是两件事。

### 8.4 2026-10-16：搜索页的下载反馈（点了「下载」之后要看得见）

需求来自用户（原话：「搜索列表 点击下载以后 没有开始 购买/下载的 交互 不知道已经开始下载了」）。

**先查账本，确认不是故障**：`download_tasks` 里躺着一行 QQ（`com.tencent.mqq`，18:44:30 → 18:45:45，`bytes_done = bytes_total = 647724400`，state=done），`library_entries` 里那一条的「版本」列记的是显示版本号 `9.3.75`。也就是说那一次点下去**确实下完了**（75 秒），用户看不到的只是过程 —— 这是反馈缺失，不是下载失败。

缺的反馈具体是什么：点击后只有一条 6 秒就消失的 toast（`已加入下载队列：…`），搜索页其余部分**什么都不变**。第一次下载还要先跟 App Store 协商许可，那几秒连字节都没有（`ipa.Progress.Indeterminate`，见 `internal/ipa/progress.go`），看上去更像「点了没反应」。上游/我们的事件里**没有**独立的「购买」阶段（`PollProgressUntil` 只用 `download` 一个阶段，见 `internal/ipa/download.go`），所以界面不能假装有购买进度。

- 新增 `frontend/src/DownloadFeed.tsx`：搜索结果上方常驻的下载提示条。每条显示 名称 / `版本 <id>`（或「最新版本」）、进度条与 `正在下载 · 43.7% · 201 MB / 460 MB · 8.0 MB/s · 剩约 4 秒`；排队中写「排队中 —— 前面的任务结束后会自动开始」；许可协商那一段写「已开始：正在向 App Store 请求下载（免费应用会先获取许可）…」（indeterminate 进度条，而不是编一个百分比）；刚跑完的那一单**留在条上**显示「下载完成：<路径>」/「下载失败：<原因>」/「已取消（半成品 .tmp 保留）」，并给 取消 / 打开位置 / 去下载页三个按钮。
- `SearchView` 订阅 `download:progress` 与 `download:state`；挂载时用 `DownloadService.List()` 把**已经在跑的任务**也拉进提示条（别的标签页启动的、或刷新后仍在跑的）；点 Start 之后直接把返回的 `store.Task` 放进条里，不等事件。
- 结果行与版本面板的下载按钮：同应用 + 同平台 + 同版本的下载**正在跑或排队**时，按钮文字变成「下载中 43%」/「排队中…」并禁用，`title` 说明原因。
- 顺手修：`.row .spacer` 之前**没有 CSS**（只有 `.topbar .spacer` 有 `flex:1`），所以各视图 `.row` 里的 `<span className="spacer" />` 一直是空的、不起作用；给 `.row .spacer` 补上 `flex: 1`。

**设计选择（与 Story 5 AC2 的分工）**：账本里已有同一份（带 sha256）时**弹窗问一句**、可跳过（AC2，预览步骤 09 守这条）；**正在下/排队中**的版本则不弹窗，直接把状态写在按钮上并禁用。为什么队列那段提示还留着：`disabled` 是 React 上一帧算出来的，可能已经过期（另一个窗口或 MCP helper 刚启动了同一单），那种竞态下 `confirmDuplicate` 里的「这个版本已经在下载队列里了（任务 xxx）」仍然会拦一下。

**验证**：`scripts/preview-ui.mjs` 12 个状态全绿（新增 11「点击下载后立刻有反馈」、12「进度事件推进 → 下载完成」；02 改成断言「正在下的那一行显示状态而不是『下载最新』」，10 改成断言「同版本正在下时按钮禁用且显示状态」）。预览桩的 `EventsOn` 从空转的 no-op 换成**真事件总线**并暴露 `window.__emit` / `window.__preview.finish()` —— 否则「下载中 / 已完成」这些状态根本测不到。

**故障注入（三处，各打掉一个机制，都只让对应的步骤变红）**：① 不渲染 `<DownloadFeed>` → 02 / 11 / 12 红；② 点 Start 后不把新任务并进 `taskList` → 只有 11 红（少了「排队中 —— 前面的任务结束后会自动开始。」「排队中…」）；③ 不监听 `download:state` → 只有 12 红（少了「下载完成：」「打开位置」「下载最新」）。还原后 12/12 全绿。

**诚实边界**：速率 / 剩余时间 / 百分比都来自 `ipa.Progress` 的真实字段（无头预览里是用 `__emit` 灌进去的桩值）；「购买、许可协商」没有独立事件，那段时间界面只能给 indeterminate + 文字说明。

---

### 8.5 2026-10-16：看得见「应用是否已购」（并修掉一个把已购付费应用挡住的 bug）

需求来自用户（原话：「能否看到应用是否已经购」）。

**先量数据**（用产物里的 helper 跑只读工具，见 §8.3.1 的方法）：
- `list_purchases`（`platform=iphone`）→ `totalCount = 55`，**3.1s** 返回 55 条，每条都有 `name` / `bundleId` / `purchasedAt`（如 `Island Pocket` / `xyz.wulonglin.PinToIsland` / `2026-10-14T01:37:35Z`）。
- 同一个 App 用公开目录接口问一次：`GET /lookup?bundleId=com.revenuecat.mobile`（已购）→ **44 个字段，含 `purchase` 的字段一个都没有**。对照上游源码：`App.PurchaseDate` 只由购买历史那条路填 —— `appstore_owned_apps.go` 里解析二进制 plist 的 `asdp` 键（4 字节大端 Unix 时间戳，`:509`），搜索/查找那条路根本不经过它。

**因此找到一个真 bug**：搜索页的付费门禁原来是 `canDownload(app) = app.free || Boolean(app.purchasedAt)`，而搜索结果的 `purchasedAt` **恒为空** ⇒ 只要 App 是付费的，界面一律显示「不能下载：未购买」并把按钮禁用，**哪怕账号真的买过它**（那正是 ipatool 最有用的场景：重下已购的老版本）。旧断言（无头预览步骤 05）当时是绿的 —— 因为桩数据在搜索响应里塞了 `purchasedAt`，**桩的形态和真实端点的形态不一致，断言就成了摆设**。

改法（三态，不是两态）：
- 新增 `frontend/src/owned.ts`：`loadOwned(platform, force)` 按平台缓存已购列表（`PAGE_SIZE = 100`、`MAX_PAGES = 20`，边翻页边按 `totalCount` 收敛；同一平台的并发调用共享一个 in-flight promise），`findOwned` 先按 `id` 再按 `bundleId` 兜底，`ownedState(index, app, hasList)` 返回 **`owned: true | false | null`** —— `null` 表示「拉不到列表，无法判断」，**不能当成未购买**（那会把已购买的付费应用误判成买不到）。
- `frontend/src/PurchasedView.tsx` + 新标签页「已购」（`App.tsx` 的 `TabID` 加 `purchased`，TABS 里排在「搜索」之后）：表列为 应用 / 购买时间 / 当前版本 · 体积 / 操作，行尾「版本 · 下载」把该 App 的版本面板打开 —— 实现方式是回搜索页：`App.tsx` 用 `versionRequest = {app, platform, id}` 把请求递过去（`id: Date.now()` 做去重键），`SearchView` 收到后切平台、拉已购、`openVersions(app, platform)`。（**这条链路已按用户要求删除，见 §8.9**）
- 搜索页价格列改成四态：`已购` 绿徽标 + 购买时间（来自已购列表）／`免费`／付费未购（价格徽标 + 「不能下载：未购买」）／付费但列表没拉到（「已购未确认」，`title` 给出失败原因）。门禁与按钮 `title` 都改走 `canDownload(app, owned)` / `blockedReason(app, owned)`。

**验证**：`scripts/preview-ui.mjs` **14 个状态全绿**（新增 13「已购页」、14「从已购页点『版本 · 下载』→ 切到搜索页并打开版本面板」；步骤 05 改成断言 `已购` / `2023/11/11` / `"ownedBadge":1` / `"enabled":[true,false,true]`）。预览桩同步改成**真实形态**：`apps` 里的搜索结果**不再带** `purchasedAt`，购买信息只放在新的 `ownedApps` 里（`ListPurchases` 桩按 `(page, limit, platform)` 翻页返回），所以「已购」标记与那个 `2023/11/11` 只可能来自已购列表。

**故障注入（三处，都只让对应断言变红）**：① 价格格改成不看已购列表（`ownedState(null, app, false)`）→ 05 少 `["2023/11/11","不能下载：未购买","\"ownedBadge\":1"]`；② 门禁退回旧实现（`Boolean(app.purchasedAt) || app.free`，即那个 bug 本身）→ 05 少 `["\"enabled\":[true,false,true]"]`；③ 已购页不跳版本面板 → 14 少 `["选择版本 · Pages","100000001"]`。还原后 14/14 全绿。

**诚实边界**：真机上只验过 **MCP helper 的 `list_purchases`**（55 条 / 3.1s，见上）；GUI 的「已购」标签页本身只在无头预览里用桩数据走过，没有在真机上点过。helper 的工具参数是下划线风格（`list_purchases` 用 `page`/`limit`/`platform`），与 GUI 绑定的位置参数不是一套东西。`Store.RecoverInterruptedTasks`（`internal/store/tasks.go:169`，启动时把上次退出留下的 pending/running 标成 failed「应用退出导致下载中断，可重试」，由 `internal/services/runtime.go:87` 调用）保证提示条不会出现永远「正在下载」的幽灵任务；提示条只把本次会话真的看到的收尾结果留在上面（挂载时只并入还在跑的）。

### 8.6 2026-10-16：把「下载 / 购买」这条路上的摩擦点磨掉（A–E）

**起因**：用户提问「为了方便下载 购买 还有哪些可以优化的」。我先按代码逐条核实（不靠印象），列出 15 项，用户全选 A–E。

**A 购买入口**：搜索结果与版本面板的付费未购行增加「去 App Store 买」，走 `CatalogService.OpenInAppStore` 的 `macappstore://apps.apple.com/app/id<ID>` 深链（打不开退回 https）；版本面板多一个「我已经买好了，重新检查」（`owned.refresh(platform)`）；搜索框现在**直接吃 App Store 链接或纯数字 ID**（`parseAppTarget`：`/apps.apple.com/…/id(\d+)/` 与 `^(id)?\d{6,}$`），命中就 `Lookup` 一次并直接打开版本面板 —— 这比「先复制 bundle id、再粘进来搜」少两步。

**B 可靠性**：下载文件名从 `<bundleId>_latest.ipa` 改成**带显示版本号**（`<bundleId>_9.3.75.ipa`，`ipa.NamedDownloadFileName`；指定历史版本时仍用版本标识）；`execute` 完成时用 `store.HashFile` 算 sha256 写进账本（失败只记日志，不影响下载结果）；`revealAfterDownload` 这个原本**只有界面没有实现**的设置项现在真的会弹出 Finder；`internal/ipa/download.go` 的 3 次重试只覆盖许可与口令过期，故外面加了一层**网络错误自动重试 2 次、间隔 5 秒**（`retryableDownloadError`：`network` / `temporarily_unavailable` / `net.Error` 超时 / `io.ErrUnexpectedEOF`，取消不算），重试前发一条 `download:state` 说明「正在自动重试」；下载页的「从头下载」拆成**删半成品**（`ResetPartial`，不是任务动作，`.tmp` 删掉后点「续传/重试」就是从零开始）与 **清理半成品**（`done` 行上，清残留 .tmp）。

**C 已购页**：每行加「下载最新」（下载中/排队中的同 App 会被跳过并说明），工具栏加「批量下载本页（N）」—— 先算**体积合计**（只算有 `fileSizeBytes` 的）+ 查**磁盘剩余空间**（`DownloadService.FreeSpace`），一次 `window.confirm` 报清楚再逐个 `Start`；`batchTargets` 只含**本地库没有**的 App，所以重复批量不会重复下载。

**D 本地库 / 下载页**：本地库的「文件已丢失」行加「重新下载」（账本版本是 7 位以上数字 → 当历史版本重下；否则说明「账本记的显示版本是 X，只能下最新」，`LibraryView.redownload`）；下载页加「重试全部失败（N）」一次重排所有失败任务。

**E 列表便利**：版本标识可**复制**（`copyVersionID` → `navigator.clipboard`）；版本列表可勾「只看没下载的」；搜索结果行提前显示**本地已有 / 本地丢失**徽标（`ledger` 按 bundleId 建索引），不用点开版本就知道下过没有。

**验证**：`scripts/preview-ui.mjs` 从 14 个状态扩到 **20 个**（15 链接搜索直接到版本面板、16 本地徽标 + 只看没下载的 + 复制、17 已购页批量下载要先报体积与剩余空间、18 本地库重新下载、19 删半成品 + 重试全部失败、20 macappstore 深链）。**故障注入 3 处**（`parseAppTarget` 直接返回 null → 15/16 红；批量确认里去掉剩余空间那行 → 17 红；`LibraryView.redownload` 直接 return → 18 红），还原后 20/20 全绿。`gofmt` 干净、`go test ./...` 绿（新增 `internal/ipa/naming_test.go`、`internal/ipa/diskspace_test.go`、`internal/services/appstore_test.go`，`TestRetryableDownloadError` 9 条），`tsc --noEmit` 绿。

**顺带修掉一个「重试会丢任务名」的旧缺陷**：`DownloadService.Retry` 重建任务时 `Name` 传的是空串，而 `Store.SaveTask`（`internal/store/tasks.go:79-110`）是**全量 upsert**（`ON CONFLICT(id) DO UPDATE SET` 把每一列都换成传进来的值），于是重试一次就把任务名冲掉 —— 下载页只剩 bundleId，下载完成后写进本地库的那条也是空名字（`internal/services/download.go:231` 的 `Name: task.Name`）。修法是 `Retry` 先 `GetTask` 取回原名再带上；并新增 `internal/store/tasks_test.go`（`TestSaveTaskUpsertCoversEveryColumn`）把「全量覆盖」这个前提钉住：不带 name 时必须被清空、带上时必须保住。故障注入（删掉 `name = excluded.name` 那一行）→ 测试报 `Name = "微信"，期望空串`，还原后绿。

**诚实边界**：真实下载**没有**为了这轮改动重跑过 —— 「文件名带版本号」「完成时算 sha256」「revealAfterDownload」「自动重试」四项都在同一条 `execute` 上，靠单测与预览状态覆盖，但「真下一次、账本里出现 sha256」这条没验（要几百 MB 与用户点头）；`FreeSpace` 的 Windows 分支只过交叉编译；预览里的 `macappstore://` 是桩（记录调用 id），真机点一次才会知道商店会不会拉起来。

### 8.7 2026-10-19：把「已下载」并成「下载」页的二级视图

**起因**：用户提问「下载 和 本地账本模块是不是重复了」。按代码核实：两表职责其实不同（`download_tasks` 是过程/队列，`library_entries` 是结果清单、按 `path` 唯一 upsert、被搜索/已购/已下载三处消费），但界面上确实有两处重复：顶层「本地库」页签和「下载」页签并列，而 `done` 任务又永远不会自动清理，于是同一份 IPA 在「下载」里（done 行）和「本地库」里各出现一次。用户选了「已下载 改为下载列表的二级页面」（原话：「合并成一个模块,或者已下载 可以改为下载列表的二级页面」），保留两表。

**改法（只动界面与文案，数据层零改动）**：
- 新 `frontend/src/DownloadCenter.tsx`：二级胶囊 `队列 / 已下载`（`SUBTABS`），分别渲染 `DownloadsView` 与 `LibraryView`；`LibraryView` 的 `onQueued` 回调在「重新下载」入队成功后自动切回队列视图。
- `frontend/src/App.tsx`：`TabID` 去掉 `library`，`TABS` 去掉「本地库」，`downloads` 渲染 `DownloadCenter`。
- `frontend/src/DownloadsView.tsx`：`done` 任务从主表移出，折叠进 `<details className="card done-fold">`（summary「已完成（N）—— 任务记录，默认收起；文件与哈希看「已下载」」），主表只留 pending/running/failed/canceled；行渲染抽成 `renderRow(task)` 复用。这样「队列」真的只表示过程，「已下载」只表示结果。
- `frontend/src/LibraryView.tsx`：标题改为「已下载的 IPA」，重下提示改「已加入下载队列：… —— 进度在「下载 · 队列」里看」。
- `frontend/src/style.css`：新增 `.subtabs / .subtab / .subtab.active`（胶囊，和顶部页签在视觉上分层）与 `.done-fold / .done-fold summary`。
- `SearchView` / `PurchasedView` 里所有「本地库」指路文案改成「下载 → 已下载」。
- `scripts/preview-ui.mjs`：新增 `clickSubTab(label)`（先点顶部「下载」，再点 `.subtabs` 里的标签），步骤 07 / 18 改点「已下载」、步骤 19 点「队列」。

**验证**：`npm run build` 通过；无头预览 20/20 全绿（截图在 `/tmp/ipagui-shots/`），确认顶部只剩 搜索 / 已购 / 下载 / 设置（当时的「环境与账号」页签后来并进设置，见 §8.8），下载页内是「队列 / 已下载」胶囊，队列页 done 任务收进折叠区。

**没做的（刻意留着）**：两表仍未合并（`app_id/bundle_id/name/platform/version/path/bytes` 在两边重复存）；`done` 任务仍全量留在 SQLite，只是界面折叠；`library_entries` 仍没有 `source_task_id` 列（`§3.3` 与 PRD F5 里「来源任务 id / 任务状态」的表述仍是旧设计残留）。这些若要收，属于数据层迁移，另开一轮。

**诚实边界**：新入口结构只在无头预览里点过，没有在真机 Wails 窗口里点过；`done-fold` 的展开/收起只是浏览器原生 `<details>` 行为，没做状态记忆（切页回来会重新收起）。

### 8.8 2026-10-19：把「环境与账号」并进「设置」，删掉环境清单与启动自检面板

**起因**：用户要求「环境与账号模块 和 设置模块合并，移除环境与账号模块的 环境、自检信息显示」。原来的「环境与账号」页签里其实只有「账号」卡片需要操作，剩下两块（环境路径清单、`EnvService.Preflight` 自检面板）是排错用的只读信息，不值得单独占一个顶层页签。

**改法**：
- 新 `frontend/src/AccountSection.tsx`：把原 `EnvView.tsx` 里的账号卡片（登录 / 两步验证码 / 退出 / 撤销令牌 + 首次登录风险告知）原样抽出，保留 `localStorage` 风险确认与登录中「正在准备签名器」提示（缓存路径仍读 `env.cacheDir`）。
- `frontend/src/SettingsView.tsx`：Props 增 `env / auth / refresh`，在「下载设置」上方渲染 `<AccountSection />`；「设置」页现在是 账号 → 下载设置 → 凭据后端与文件口令 → MCP 接入（**最后这块在 §8.11 里被删了**）。
- `frontend/src/App.tsx`：`TabID` 去掉 `env`，`TABS` 去掉「环境与账号」，`settings` 把 `env / auth / refreshEnv` 传下去；顶栏的版本徽标、「环境未就绪」徽标、登录邮箱与「刷新状态」保留（它们不属于那个页签）。
- 删除 `frontend/src/EnvView.tsx`：环境路径表（版本 / 状态目录 / 账本数据库 / 首次准备缓存 / 默认下载目录 / 当前平台 / 初始化错误）与「启动自检」卡片（`EnvService.Preflight` 的 checkline 列表）不再有 UI 入口。
- `scripts/preview-ui.mjs` 步骤 08：`clickTab("环境与账号")` → `clickTab("设置")`，断言从「首次准备缓存」换成「下载设置」（同时证明账号与设置确实在同一页）。

**验证**：`npm run build` 通过（tsc + vite）；无头预览 20/20 全绿（截图在 `/tmp/ipagui-shots/08.png`：设置页顶部就是账号卡片，风险告知完整）。`tsc` 能过也说明没有残留对已删 `EnvView` 的引用。

**补删（2026-10-19，用户「不要了」）**：既然前端不再调用，`EnvService.Preflight` 也一并删掉 —— `internal/services/env.go` 的 `Preflight()`、`internal/ipa/catalog.go` 里没人调用的 `Services.Preflight` 转发、生成绑定 `frontend/wailsjs/go/services/EnvService.{js,d.ts}` 的 `Preflight`、`frontend/wailsjs/go/models.ts` 里的 `ipa.Report` / `ipa.Check`、`frontend/preview.html` 的 `Preflight` 桩全部移除。`ipa.Preflight` / `ipa.Report`（`internal/ipa/selftest.go`、`catalog.go` 的 `Services` 之外）仍保留，因为 `ipa-mcp --selftest`（`cmd/ipa-mcp/main.go:52`）、`cmd/selfcheck`（`cmd/selfcheck/main.go:46`）和 `internal/ipa/ipa_test.go` 的漂移断言还要用；`ipa.Report` 不再出现在 Wails 绑定层。

**没做的（刻意留着）**：环境信息没有挪到任何「关于/诊断」入口，属于直接砍掉；`EnvService.Info` 仍保留（顶栏版本徽标与登录提示还要读 `version` / `cacheDir`）。

**诚实边界**：只在无头预览里点过「设置」页，没有在真机 Wails 窗口里点过；删掉自检面板后，上游 ipatool 漂移的告警就只剩构建期的 `go test ./internal/...`（`internal/ipa/ipa_test.go` 的 `ExpectedUpstreamVersion` 检查）与安装包构建脚本兜底。

### 8.9 2026-10-19：已购页每行只留「下载最新」（移除「版本 · 下载」）

**起因**：用户要求「已购页面的列表移除 版本*下载 的操作按钮，仅保留下载最新」。已购页每行原来两个按钮（「版本 · 下载」跳回搜索页打开版本面板、「下载最新」直接入队），而这一页的主要用途就是「把买过的东西重新下回来」，多一个跳页按钮反而让行尾变拥挤。

**改法**：
- `frontend/src/PurchasedView.tsx:323-332` 行尾只留一个 `<button className="primary">下载最新</button>`；操作列表头宽度 `230 → 130`；表下说明改成「想重下老版本：到『搜索』页查到这个 App，点它那行的『版本』」，不再指向已不存在的按钮（**这段说明在 §8.10 里又改了一次 —— 搜索页那个「版本」按钮也被删了**）。
- 连带删掉这条死链路（按钮没了，producer 就只剩 `null`）：`PurchasedView` 的 `onOpenVersions` prop、`App.tsx` 的 `versionRequest` state 与 `frontend/src/SearchView.tsx` 里「收到 versionRequest → 切平台 + 打开版本面板」的 effect（`SearchView` 的 Props 也去掉了这两个可选字段）。`SearchView` 的版本面板入口仍有两条：结果行上的「版本」按钮、以及搜索框直接吃 App Store 链接 / 数字 ID（`parseAppTarget`）。
- `scripts/preview-ui.mjs`：步骤 13 的期望值 `版本 · 下载` → `下载最新`；步骤 14 从「点『版本 · 下载』跳搜索页」改成「已购页每行只有一个动作按钮」——断言 `actions` 二维数组每行都是 `["下载最新"]` 且 `hasVersionButton:false`。

**验证**：`tsc --noEmit` 通过；`npm run build` 通过（`frontend/dist/assets/index-CC3hPHkp.js` 里已无 `版本 · 下载` 字样）；无头预览 **20/20 全绿**。**故障注入**：把 `版本 · 下载` 按钮临时加回行尾 → 只有步骤 14 变红（少 `actions` 与 `hasVersionButton` 两条），步骤 13 仍绿（它只验「下载最新」在），还原后恢复 20/20。这说明新的断言确实钉住了「按钮被移除」这件事，而不是碰巧通过。

**诚实边界**：只在无头预览（`/tmp/ipagui-shots/13.png`、`14.png`）里验过，没有在真机 Wails 窗口里点过；老版本入口当时**只剩**「搜索」页（这条也在 §8.10 里被推翻：连搜索页的版本入口一起删了），已购页不再能一键到版本面板 —— 这是这次取舍的代价，如果以后发现「已购页看不到老版本」太绕，该做的是在已购页内嵌版本列表（复用 `openVersions` 那套逻辑），而不是把跳页按钮加回来。

### 8.10 2026-10-19：搜索页移除「版本」按钮 → 版本面板整体下线

**起因**：用户先问「搜索列表中的 版本功能现在还有用吗」。回答里列了它的真实成本（真机实测：微信 `ListVersions` **6.9 s** 返回 **500+ 个版本标识**；`GetVersionMetadata` **7.7–10.1 s/个**；不存在的版本 ID 约 **40 s** 才报错 —— 所以「查询全部显示版本」对 500+ 版本等于跑一个多小时），并建议保留；用户仍然要求「移除这个按钮」，确认对象是搜索列表行尾那个「版本」。

**改法**：
- `frontend/src/SearchView.tsx` 结果行只剩 `<button className="primary">下载最新</button>`（操作列 `210 → 130`）。版本面板既然没有任何入口了，就整块下线：`versionList` / `versionTarget` / `chosenVersion` / `meta` / `metaBusy` / `downloaded` / `onlyMissing` 七个 state、`openVersions` / `loadMeta` / `loadAllMeta` / `fetchDownloaded` / `loadDownloaded` / `copyVersionID` / `metaCell` / `localEntry` / `localCell`、监听 `library:changed` 刷新版本列表的 effect，以及顶层 `matchEntry` 的「显示版本号回退匹配」全部删掉。`confirmDuplicate(app)` 现在只处理「下载最新」一种情况：直接拿 `app.version`（显示版本号）去账本里找 entry，不再有 `versionID` / `latestVersionID` 参数。
- `parseAppTarget`（搜索框吃 App Store 链接 / 纯数字 ID）保留，只是命中后不再顺手打开版本面板，而是 `Lookup` 之后把结果当成「只有一条搜索结果」渲染出来 —— 贴链接的用途变成「直接查到这一个 App」。
- **后端一行没动**：`CatalogService.ListVersions` / `GetVersionMetadata` 仍在，因为 `ipa-mcp` 的 `list_app_versions` / `get_version_metadata`（`internal/ipa/mcp.go:260-267`）还要用；`LibraryView` 的「重新下载」也仍然按账本里的版本标识下历史版本（`internal/services/download.go:116` 的 `VersionID` 就是 `ExternalVersionID`）。
- `frontend/src/PurchasedView.tsx` 的说明文字跟着改：不再指路「去搜索页点『版本』」，改成「要指定历史版本只能用 MCP / 命令行（`download --external-version-id`），界面里不提供版本列表」。
- `scripts/preview-ui.mjs`：删掉版本面板专属的旧 03 / 04 / 15 / 16，新增 03「搜索结果行只剩『下载最新』」（断言每行按钮数组 + `hasVersionButton:false`）与 04「链接只出一条结果」；09「重复下载提示」改用「某付费软件」驱动（微信那行正在下、按钮已禁用，点不到弹窗）；10 不再需要「关闭」面板；17–20 重编号为 15–18，现在共 **18 个状态**（原文档里「20/20」的说法随之作废）。

**验证**：`tsc --noEmit` 通过；`npm run build` 通过（`frontend/dist/assets/index-PCcDX258.js`，902 行的 `SearchView.tsx` 缩到 545 行）；无头预览 **18/18 全绿**。**故障注入**：把 `<button>版本</button>` 临时加回结果行 → 只有 03 / 04 变红（03 少 `"actions":[["下载中 43%"]]` 与 `"hasVersionButton":false`，04 少 `"hasVersionButton":false`），其余 16 步全绿，还原后恢复 18/18 —— 断言确实钉住了「这个按钮被移除」。

**诚实边界**：界面里从此**没有任何**版本列表入口，要下历史版本只能走 MCP（`list_app_versions` → `download` 带 `external_version_id`）或命令行；只在无头预览（`/tmp/ipagui-shots2/03.png`、`04.png`）里点过，没有在真机 Wails 窗口里点过。

### 8.11 2026-10-19：设置页移除「MCP 接入」模块（连后端一起删）

**起因**：用户要求「设置中心的 mcp 模块移除」。确认范围时选了「连后端一起删（彻底移除模块）」——这张卡片是 `MCPService`（把 `ipa-mcp` helper 写进 Claude Desktop / Cursor 配置、可还原、可自检）唯一的调用方，只删 UI 会留下 400 行没人调的代码。

**改法**：
- `frontend/src/SettingsView.tsx`（299 → 212 行）：删掉「MCP 接入」整张卡片（标题右侧「一键全部接入」、客户端表格「接入 / 移除」、状态徽标 已接入 / 参数已变 / 未接入 / helper 未找到 / error、底部 helper 路径）、`targets` state、`register()` / `unregister()`，`load()` 的 `Promise.all` 从三项收回两项（不再调 `MCPService.Status()`）。设置页现在只有三块：账号 → 下载设置 → 凭据后端与文件口令。
- **后端整体删除**：`internal/services/mcp.go`（404 行，含 `mcpServerName = "ipa-gui"`、`mcpTarget`、`MCPService` / `MCPStatus`、`helperBinaryPath`、注册/还原/自检与原子写配置）删掉，`main.go` 的 `services.NewMCPService(runtime)` 绑定一起去掉。
- 生成物与桩：`frontend/wailsjs/go/services/MCPService.{js,d.ts}` 删除、`frontend/wailsjs/go/models.ts` 的 `MCPStatus` 删除、`frontend/src/api.ts` 的 `MCPService` 导入与再导出删除、`frontend/preview.html` 的 `MCPService` 桩删除。
- `scripts/preview-ui.mjs` 步骤 08 增加两条断言：`"cards":["账号","下载设置","凭据后端与文件口令"]`（设置页确实只剩三块）与 `"hasMCP":false`（整页文本里不再出现 MCP / 一键全部接入）——这同时把「账号并进设置」和「MCP 卡片被删」钉在一起。
- 文档与构建脚本：`docs/ipatool-gui-prd.md` 的产品闭环句、Story 6、F6 都加了「GUI 侧已按用户要求下线，helper 保留、配置手写」的现状注；`scripts/build-macos.sh` / `scripts/build-windows.ps1` 里「界面才能在『设置 → MCP 接入』里注册它」的注释改成「配置需手写」。
- **`ipa-mcp` 本身一行没动**：`cmd/ipa-mcp` 仍随包分发（`scripts/build-macos.sh:49`、`scripts/build-windows.ps1:36`），`internal/ipa/mcp.go` 的 `NewMCPServer` 与七个工具（`account_status` / `search_apps` / `lookup_app` / `list_app_versions` / `get_version_metadata` / `list_purchases` / `download_app`）全部保留。

**验证**：`gofmt -l` 干净、`go build ./...`、`go vet ./...` 通过；`tsc --noEmit`、`npm run build` 通过（`frontend/dist/assets/index-D6UP5BVI.js`）；无头预览 **18/18 全绿**。**故障注入**：把 `<div className="card"><h3>MCP 接入</h3><button>一键全部接入</button></div>` 临时加回设置页末尾 → **只有步骤 08 变红**（少 `"cards":[...]` 与 `"hasMCP":false` 两条），其余 17 步全绿，还原后恢复 18/18。

**诚实边界**：只在无头预览（`/tmp/ipagui-shots3/08.png`）里看过设置页，没有在真机 Wails 窗口里点过。删掉之后**没有任何界面入口**能帮你把 helper 写进客户端配置，要接 MCP 只能手改 `claude_desktop_config.json` / `~/.cursor/mcp.json` 里的 `mcpServers`；如果以后要恢复「一键接入」，`git` 里没有历史（本仓库不是 git），只能从 §8.11 上面这段描述重写，或者从备份 `*.ipagui.bak` 之类的想法反推 —— 动手前先想清楚这一点。

### 8.12 2026-10-19：修正「退出登录 / 撤销令牌」的语义与文案

**起因**：用户问「设置中心的退出登录和撤销令牌的功能区别」。核对代码与上游 ipatool v2.7.0 后发现两处名不副实：

1. `AuthService.Logout` 只调 `Session.Forget()` 清内存缓存，紧接着前端 `refresh()` → `Status()` → `Credentials()` 又从钥匙串把账号读回来，界面仍显示已登录 —— 「退出」等于没退出。
2. `Session.Revoke()` → 上游 `appstore.Revoke()`（`pkg/appstore/appstore_revoke.go:11`）只删钥匙串里的 `account` 与 `kbsync` 条目，**不发任何网络请求**；弹窗却写着「向 Apple 撤销登录令牌……所有使用该账号的工具都会掉线」。事实是：本机共用同一份钥匙串的官方 CLI / MCP helper 会掉线，其他机器不受影响，Apple 侧会话不受影响。（`cookies` 文件里的账号 cookie 原先也不会被删 —— 这一点已在 §8.21 修掉。）

**改法**：
- `internal/ipa/session.go`：新增 `loggedOut` 标记与 `Logout()` / `LoggedOut()`；`Credentials()` 在标记置位时直接返回 `not_logged_in`，不再回读钥匙串；`Login()` 成功后清标记；`Revoke()` 改为清缓存 + 清标记（原先借 `Forget()`）。`Forget()` 保持「只丢缓存」且**不**解除登出标记 —— 否则下载路径里「令牌过期 → Forget → 重读凭据」的重试会把用户悄悄登回去。
- `internal/services/auth.go`：`Logout()` 改调 `Session.Logout()`；`AuthStatus` 新增 `loggedOut` 字段；`Status()` 在 `LoggedOut()` 时直接返回 `{loggedIn:false, loggedOut:true, message:"已退出登录（本机凭据仍保留）"}`，省掉一次无谓的钥匙串读取；`Revoke()` 的注释与事件文案改成「本机凭据已清除」。
- `frontend/src/AccountSection.tsx`：退出登录的 toast 改为「已退出登录（本机凭据仍保留，重新登录即可继续）」；「撤销令牌」按钮改名「清除本机凭据」（`title` 注明不会向 Apple 撤销令牌）；二次确认改成「删除本机保存的登录凭据（系统钥匙串），与官方 ipatool CLI / MCP helper 共用的凭据也会一起登出……不会通知 Apple，其他机器上的会话不受影响，状态目录里的 cookies 也不会被删除」。
- 新增 `internal/ipa/session_test.go`：`fakeStore`（嵌入 `appstore.AppStore`，只实现 `AccountInfo` / `Login` / `Revoke`）钉住三条语义 —— Logout 后 `Credentials` 返回 `not_logged_in`、`Forget` 不解除标记、`Login` / `Revoke` 清除标记。

**验证**：`gofmt -l` 干净；`go build ./...`、`go vet ./...`、`go test ./...` 全绿（新增 3 条测试）；`tsc --noEmit`、`npm run build` 通过（`frontend/dist/assets/index-Dn6XDYxA.js`）；无头预览 **18/18 全绿**（`/tmp/ipagui-shots-auth/08.png`）。

**诚实边界**：预览里的 `Logout` / `Revoke` 是 `preview.html` 的桩，所以**这两颗按钮本身没有在界面上点过**，只跑了单元测试与构建；测试里的 `fakeStore` 绕过了真实钥匙串，「退出后重新登录」依赖 `Login()` 清标记这条路径（由 `TestLoginClearsLogoutMark` 覆盖），但真实钥匙串的读写没有在自动化里复现。登出标记只活在当前进程里：**重启应用后仍会自动从钥匙串恢复登录**（因为凭据没删）——这是刻意的，真正的「清除」是另一颗按钮；如果以后要「记住退出」，得把标记落到设置库里。

### 8.13 2026-10-20：已购列表从「前端翻页」改成「后端一次拿全 + 缓存 + 后台刷新」

**起因**：用户问「已购数据只能一次性获取吗，现在有点慢」。查代码后结论是**慢的原因不是一次性拿全，而是每翻一页都把整份历史重拉一遍**：前端 `frontend/src/owned.ts` 从第 1 页翻到 `apps.length >= totalCount`，而上游 `pkg/appstore/appstore_owned_apps.go` 的 `OwnedApps` **每次调用**都会 `fetchOwnedApps` 走两个 storefront（`34` / `13`）各一遍 login → update → items，再在内存里切出请求的那一页。所以：

- 页数 N = 全量抓取 N 次。这个账号 **359 个已购** → 4 页，实测串行 **28.3s**（每页还多一次 `/lookup` 补图标）；
- 而且缓存只在 WebView 的内存里：界面窗口一关、或者切走再切回来（`PurchasedView` 卸载即丢），下次又从 0 开始。

**改法**（后端为主，前端只负责显示）：
- 新增 `internal/ipa/owned.go`：
  - `OwnedAppsList{Platform, TotalCount, FetchedAt, Cached, Refreshing, Warning, Apps}` 是**整份**清单（不是一页）。
  - `Services.ListAllOwnedApps(ctx, platform, OwnedAppsOptions{Force, OnRefresh})`：90s 内的进程内缓存直接返回；更旧的先立刻返回旧清单（`Cached:true`）同时起一个后台刷新，拉到了用 `OnRefresh` 回调（绑定层广播 `purchases:updated`）；
  - `fetchOwnedList`：第 1 页串行（只有它知道 `totalCount`，才能算页数），其余页按 `ownedPageConcurrency = 3` 并发（`parallelFor`），结果按页号落位后拼接，再按 app id 去重（拉取途中 `totalCount` 变了会让相邻页重叠）；图标/体积按每批 100 个 id 并发补（一条 `/lookup` 带太多 id 会被 Apple 拒掉）；
  - **任何一页失败都整体失败**，不返回半份清单 —— 半份清单会让搜索页把没拉到的应用判成「未购买」（§8.5 那个 bug 的翻版）。刷新失败时退回旧清单并带 `Warning`，让界面显式说「这是上一次拉的」；
  - 磁盘缓存 `~/Library/Application Support/ipagui/owned-<platform>.json`（`ownedCacheVersion` 带版本号、按账号邮箱校验：换号后旧缓存自动作废；14 天过期；tmp+rename 原子写；写失败静默，缓存只是加速）。
- `internal/services/catalog.go` 新增绑定 `CatalogService.ListAllPurchases(platform, force)`；老的 `ListPurchases(page, limit)` 保留（`ipa-mcp` 的 `list_purchases` 还用它），但注释写明界面不要再用它翻页。
- 前端：`owned.ts` 删掉 `PAGE_SIZE` / `MAX_PAGES` / `fetchAll` 的翻页逻辑，只发一次 `ListAllPurchases`；模块加载时订阅 `purchases:updated` 更新缓存并通知订阅者（`onOwnedUpdated`）。`PurchasedView` 表头多一个「· 后台更新中…」，刷新失败时**保留旧行**并显示「这是上一次拉到的清单 + 重试」，而不是像以前那样把整页清空；`SearchView` 同样订阅事件，刚买的应用不必再手动点「重新检查」。
- `frontend/preview.html` 补 `ListAllPurchases` 桩（老的分页桩留着，注释说明界面不再调用）。

**验证**（真机 + 真实账号，359 个已购）：
- 冷启动（删掉磁盘缓存）：**19.4s** 一次拿全 359 条，`cached=false`、无 warning；改造前的等价串行路径是 **28.3s**。
- 同进程再要一次：**2.7µs**（内存缓存）；新进程读磁盘缓存：**2.25ms**（`cached=true`）。注意首次 `Credentials()` 本身在新进程里要 **5.46s**（Apple 的 account info 网络调用），界面启动时 `AuthService.Status()` 已经付过这笔，所以「点开已购页」这一下现在是毫秒级。
- 并发分页安全性：同一账号 4 页并发连跑 **4 次**，每次都是 359 条、页边界正确（100/100/100/59）、去重后 359，**没有出现串会话或掉登录**（`go run` 探针，跑完即删）。
- 单元测试 `internal/ipa/owned_test.go`（7 条，`-race` 干净）：55 个 App 只问一次 Apple、缓存命中不再问、强制刷新真的再问、150 个必须正好 2 页、第二页失败整体失败、刷新失败保留旧清单 + Warning、重启吃磁盘缓存、换账号不吃别人的缓存、平台之间不串。
- `go test ./internal/...`、`go vet`、`tsc`、`npm run build`、无头预览 **18/18 全绿**。

**诚实边界**：
- **第一次**（或磁盘缓存过期后）仍然是 19s，只是不再每页重来；真要更快得改上游（`OwnedApps` 没有「拿全」的入口，`limit > 100` 会被 `normalizeOwnedAppsInput` 直接拒）。
- 并发拉页依赖 Apple DAAP 对同一账号并发请求的容忍度：本地连跑 4 次干净，但这**不是** Apple 的承诺，风控收紧时可能表现为 401/掉登录；真出现就把 `ownedPageConcurrency` 调回 1（退回 28s 但不并发）。
- 缓存意味着「刚买的应用最多晚 90s 出现」（或等后台刷新事件）。这是刻意的取舍：界面上的「未购买」结论永远来自**完整**清单，而完整清单不会因为缓存变旧而变成假的 —— 只是不新，且界面会把「不新」说出来。
- 磁盘缓存里存的是 `AppView`（含名字/id/购买时间），过期 14 天；换账号即作废，但**不加密**（和 `settings.json`、`library.db` 同一个目录、同一权限级别）。
- 无头预览里 `ListAllPurchases` 是桩，所以「后台更新中…」这条文案没有在预览里被断言过；只在真机上看过（真机这次是先有磁盘缓存、再后台刷新的路径）。

### 8.14 2026-11-10：升级上游到 v2.8.0（恢复 Apple ID 登录）+ 登录错误可见化

**起因**：真机登录在「首次准备签名器」阶段失败：`exchange SAP setup message: apple returned 502 Bad Gateway`，界面显示成 `unknown: 操作失败 (…)`。上游 v2.8.0（2026-11-08）的发布说明第一条正是「Restored App Store authentication with local Anisette generation and provisioning」。

**改法**：
- `third_party/ipatool` 基线从 `v2.7.0` 升到 **`v2.8.0`**（`72c47d5`）；重新落地唯一的本地补丁：`ent/download` 响应没有 `sinf`/`dpInfo` 时继续走 `volumeStore` / `redownload` / `updateProduct` 回退（v2.8.0 的 `downloadPackagePlatform` 只在 macOS 校验，iOS 仍需要这处回退）。说明见 `third_party/ipatool/FORK.md`。
- v2.8.0 的登录链路：`Login` → `nativeLogin` → 本地 Anisette provisioning（钥匙串新增 `anisette-v4`）→ 带 `X-Apple-AMD` 头调用 `legacyLogin`；请求改由 `internal/store` 模拟的 Store 框架执行，SAP 签名器使用 macOS 27 资产。`LoginInput` / `ErrAuthCodeRequired` / `NewAppStore` 等公开 API 未变，GUI 装配层无需改。
- 依赖：Go `1.26.5`（GOTOOLCHAIN=auto 自动切换）、`progressbar` `v3.19.1`（钉版断言同步更新）、新增 go-macho / go-apfs / lzfse / purego 等。
- `internal/ipa/errormap.go`：新增 `transientHTTPStatus`，识别 `apple returned 502`、`status code: 503`、`HTTP 502` 等文本，映射成「Apple 服务暂时不可用（HTTP 502）+ 稍后重试」；`AppError.Error()` 改为中文说明 + 建议，不再把错误码与上游英文原文甩给用户（诊断信息仍可 `Unwrap()`）。
- `frontend/src/AccountSection.tsx`：登录表单改为 `<form>`（Enter 可提交）；账号/密码为空时不再禁用按钮，而是常驻显示「请填写 Apple ID / 密码」；后端失败同时写 toast 与表单下方的常驻错误。toast 保留，用于登录耗时期间切到其他标签页的场景。

**验证**：
- `go build ./...`、`go test ./...`、`go vet ./...` 全绿；`go run ./cmd/selfcheck` 与打包后的 `ipa-mcp --selftest` 均报 `v2.8.0+sinf-fix`。
- 2026-11-10 用 Developer ID 指纹重新签名并启动；**尚未用真实账号完成一次 v2.8.0 登录**（需要用户本人账号与 2FA），这是本次升级唯一未验证的环节。

**诚实边界**：
- 502 是否随 v2.8.0 消失，只能在真实登录里确认；本次只保证了调用链、依赖与构建全部对齐上游。
- 上游 `Revoke()` 会删 `account` / `kbsync`，但**不删** `anisette-v4` 设备身份；界面「清除本机凭据」的文案没有宣称删除它。
- `docs/ipatool-gui-prd.md` 里的 v2.7.0 事实（CLI 行为、官方产物哈希、MCP 工具清单）保留为调研时的历史记录，未随本次升级改写。

**追加（同日真机验证）**：升级后真实登录暴露了上游 v2.8.0 的第二个问题——首次登录要下载 macOS 27 恢复镜像，而资产下载器使用裸 `&http.Client{}`，苹果 CDN 对 Go 默认 UA `Go-http-client/1.1` 直接返回 **403**，界面表现为 `操作失败（底层错误：prepare Apple authentication: load anisette assets: read pinned kernel collection: read remote image range: server returned 403 Forbidden）`。用 curl 对同一个 Range 请求实测：curl / 空 UA → 206，Go UA → 403。fork 已在 `internal/sap/assets/http_range.go`（两处 Range 请求）与 `aea.go`（FCS key 请求）显式设置 `restoreImageUserAgent`，并用临时探针确认 `openRemoteFile` 成功读到 26.6 GB 的镜像大小；探针随即删除。

同时把登录提示改成实话：首登需要下载**十几 GB**的系统资产并本地解包，可能耗时十几分钟以上；**下载过程不保存进度**，失败要从头再来；成功后缓存在 `<UserCacheDir>/ipatool/sap/apple-assets-macos-27-26A434-arm64-v4`，之后登录才快。

> **§8.26 更正**：这里的「十几 GB」是当时按恢复镜像大小估的，不准确 —— 上游只从 14 GB 镜像里**按区段读取**所需内容，镜像从不落地；真实磁盘峰值 ≈ 300 MB（解压中间产物 + 落盘缓存）。

### 8.15 2026-11-18：切换账号后已购页不刷新（缓存没有账号维度）

**起因**：用户反馈「切换账号以后，已购页面没有重新刷新数据」。

**根因**（三层缓存都没有「账号」这一维度的失效动作）：
- 前端 `frontend/src/owned.ts` 的模块级 `cache` 只按 platform 分键。`loadOwned(platform, false)` 只要命中且没有 `warning` 就直接返回，**根本不发请求**；而 `PurchasedView` 的 `useEffect` 只依赖 `[load, platform]`，登录/退出不会让它重跑 —— 所以切号后已购页、搜索页的「已购」标记都是上一个账号的。
- 后端 `internal/ipa/owned.go` 的槽位虽然按 `account` 校验（换号后读取会作废），但只有「有人来拉」才会触发；前端不请求，后端就没机会刷新。
- 另外还有两个隐蔽的口子：① 拉取途中切号，返回的旧清单仍会被写进缓存；② 后端在途的后台刷新完成后会 `purchases:updated` 推一份**上一个账号**的清单上来。

**改法**：
- `internal/ipa/owned.go`：新增 `Services.InvalidateOwned()` → `ownedCache.invalidate()`：清空内存槽、删除 `owned-*.json` 磁盘缓存，并自增 `gen`；`refreshOwnedAsync` 起跑时记下 `gen`，结果落地前用 `current(gen)` 比对，期间被作废就直接丢弃（不写缓存、不落盘、不回调）。
- `internal/services/auth.go`：`Login` 成功、`Logout`、`Revoke` 之后统一调 `invalidateOwned(reason)` —— 作废后端缓存并广播 `purchases:invalidated`（只清后端不够，前端还有一份模块级缓存）。
- `frontend/src/owned.ts`：新增 `resetOwned()`（`generation++`、清 `cache`/`inflight`、通知 `onOwnedInvalidated` 订阅者）并在模块加载时订阅 `purchases:invalidated`；`loadOwned` 记下发起时的 `generation`，对不上就不写缓存（结果仍返回，但调用方用 `peekOwned` 比对会拒绝采用它）。
- `frontend/src/PurchasedView.tsx`：订阅 `onOwnedInvalidated` → 清空当前索引并重新拉（非 force：后端缓存已作废，正常请求就会真的去问 Apple）；`load` 里加 `peekOwned(target) !== index` 判断，避免在途结果覆盖新账号的显示。
- `frontend/src/SearchView.tsx`：订阅 `onOwnedInvalidated` → `owned = null`（回到「已购未确认」，而不是继续拿旧账号的结论），下一次搜索/下载判定时重拉。

**验证**：`go build ./...`、`go vet ./internal/...`、`go test ./internal/ipa/ ./internal/services/`、`tsc --noEmit` 全绿；新增 `TestInvalidateOwnedDropsMemoryAndDiskCache`（作废后同账号必须重新问 Apple、磁盘文件已清）。

**诚实边界**：切换账号原本就依赖「先退出再登录」（`AuthService.Login` 成功后会按新账号 DSID 剪掉 cookie jar 里其它账号的授权 cookie，见 §8.21），本次只保证这条路径上的缓存失效；若上游 CLI / MCP 在外部改了账号，本应用不会感知（要等下次本应用登录或退出）。

### 8.16 2026-11-18：已购页不再显示「本地已有」，每次点击都重新下载

**起因**：用户要求「已经下载过的，购买记录那里不需要加上已下载过的记录，每次点击都支持重新下载」。

**改法**（只动 `frontend/src/PurchasedView.tsx`，后端不变）：
- 删掉 `ledger`（按平台拉本地库、bundle id → `LibraryEntry` 的映射）与它带来的三处行为：
  1. 行内「本地已有 / 本地丢失」徽标；
  2. `startLatest` 里「已下载里已经有 X 了（版本 …），还要再下一份吗？」的确认弹窗 —— 现在点一下就进队列；
  3. `batchTargets`（只下本地没有的）—— 「批量下载本页」改成把**这一页全部**应用依次入队，按钮上的数量就是本页数量，确认框里写明「已经下过的也会重新下一份」。
- 保留 `activeTaskFor`：同一个 App 已经在 `pending` / `running` 时仍然跳过并提示去队列看，避免排两遍（这和「下过也能再下」不冲突 —— 已完成的旧任务不影响）。
- 卡片说明文案改成「这一页不看本地文件：已经下过的应用也能再点『下载最新』重新下一份」。

**验证**：`tsc --noEmit`、`npm run build` 通过；`PurchasedView` 里已无 `LibraryService` / `ledger` 引用。后端与已购清单语义（`Cached` / `Refreshing` / `Warning`）未动。

### 8.17 2026-11-18：已下载页移除「算 sha256」「图标」按钮；修掉 macOS 上确认框恒为 false

**起因**：用户反馈「已下载那里无法移除记录」，随后要求「已下载列表移除算 sha256、获取图标的按钮」。

**根因（确认框）**：Wails v2 在 macOS 的 `WKWebView` 只实现了 `WKUIDelegate` 的 `runOpenPanel`，没有实现 `runJavaScriptAlertPanel` / `runJavaScriptConfirmPanel`。WebKit 在缺少这两个 delegate 时按「用户点了取消」处理，于是 **`window.confirm()` 在 macOS 上恒返回 `false`**：
- 「移除记录」点确定后直接 `return`，界面毫无反应（用户看到的就是「无法移除」）；
- 同样坏掉的还有「清凭据」「重新下载」「批量下载」「清理半成品」「重试失败任务」以及搜索页的两处重复下载确认。

**改法**：
- `internal/services/env.go` 新增 `EnvService.Confirm(title, message) (bool, error)`：走 `runtime.MessageDialog` 的原生问答框（按钮「确定 / 取消」），窗口上下文未就绪时返回错误。
- `frontend/wailsjs/go/services/EnvService.{js,d.ts}` 补上 `Confirm`（与 Wails 生成格式一致，重跑 `wails generate` 会覆盖成同样的内容）。
- `frontend/src/api.ts` 新增 `confirmDialog(message, title)`：调 `EnvService.Confirm`，失败时退回 `window.confirm`（Windows 上可用）。
- 把所有 9 处 `window.confirm` 换成 `await confirmDialog(...)`：`AccountSection`（2）、`DownloadsView`（2）、`LibraryView`（2）、`SearchView`（2）、`PurchasedView`（1）。
- `frontend/preview.html` 的 `EnvService` 桩补 `Confirm: async () => true`，无头预览不被阻塞。

**已下载页简化**（按用户要求）：
- 删掉每行的「算 sha256」按钮与其 `hash()`、以及「图标 / 重取图标」按钮与其 `reloadIcon()`；行内操作只剩「打开位置」「重新下载」「移除记录」。
- 删掉「哈希只用来做本地校验」说明卡片（它解释的就是那个被移除的按钮）。
- 保留：列表里自动从 IPA 包读取图标（`loadIcon` 顺序加载、`library:changed` 自动刷新）、「文件」列里已有的 sha256 前 12 位（账本里存过的值）、后端 `LibraryService.Hash`（MCP/命令行仍可用）。
- `SearchView` 重复下载确认里的「下完可以在『下载 → 已下载』里点『算 sha256』对照」改为「对照 sha256 前几位」。

**验证**：`go build ./...`、`go vet ./internal/services/`、`go test ./internal/...`、`tsc --noEmit`、`npm run build` 全绿；前端已无 `window.confirm` 直接调用（`grep -rn "window.confirm" src/` 只剩 `api.ts` 里的回退分支）。

### 8.18 2026-11-18：重新构建 + Developer ID 签名 + 公证，产物放桌面

**构建**：`CODESIGN_IDENTITY="Developer ID Application: <YOUR_NAME> (<TEAM_ID>)" ./scripts/build-macos.sh arm64`（`VERSION` 默认 `0.1.0-dev`，`CFBundleShortVersionString=0.1.0`）。脚本内跑完单测/vet/前端编译，产物 `build/bin/IPAGUI.app`（含 helper `ipa-mcp`），带 hardened runtime 与 secure timestamp。

**为什么用指纹**：钥匙串里同名证书可能有多张（旧 G1 与新 G2），按名字签名会报 ambiguous；此时改用证书的 40 位 SHA-1 指纹作为 `CODESIGN_IDENTITY` 即可。

**公证**：`ditto -c -k --keepParent` 打包 → `xcrun notarytool submit --key <AuthKey.p8> --key-id <KEY_ID> --issuer <ISSUER>` → `Accepted` → `xcrun stapler staple` → `spctl` 报 `source=Notarized Developer ID`。

**踩坑**：第一次 submit 在「Initiating connection」阶段 `HTTPClientError.connectTimeout`，虽然拿到了 submission id，但 Apple 侧永远停在 `In Progress`（上传实际中断）。重新 `ditto` 打包并重提一次即 21 秒通过 —— 判断成功与否看有没有 `Successfully uploaded file`。

**产物**：桌面 `IPAGUI.app`（约 77M，含 helper `ipa-mcp`）与同版本 zip（约 33M，已 staple，可直接分发）。

### 8.19 2026-11-18：下载文件名改用 App 名称

**起因**：用户要求「下载 ipa 包的命名使用 app 的名称」。此前是 `<bundleId>_<版本>.ipa`（`com.tencent.mqq_9.3.75.ipa`），在访达里完全看不出是哪个 App，中文应用尤其明显。

**改法**：
- `internal/ipa/download.go` 新增 `DownloadFileName(appID, appName, bundleID, versionID, displayVersion)`：主名取 App 名称，名称缺失退回 bundle id，再退回 `app-<id>`；版本优先级不变（Apple 版本标识 > 显示版本号 > `latest`）。`DefaultDownloadFileName` 变成它的「无名称」包装，`NamedDownloadFileName` 删除。
- `sanitizeSegment` 加固：① 除 `/ \ : \0` 外，把 Windows 不允许的 `* ? " < > |` 也换成 `_`（项目同时构建 windows-amd64，同一个下载目录搬过去不能炸）；② 按**字符**而不是字节截断到 120（原来按字节切会切断中文，产出非法 UTF-8 文件名）；③ 末尾的点/空格去掉（Windows 不允许）。
- `internal/services/download.go`：`Start` 改用 `DownloadFileName`；新增 `avoidClobber` —— 文件名以名称为主之后，两个不同 App 同名（例如都叫 Notes）会落到同一路径，而账本按 path 有唯一索引，`UpsertEntry` 会**改写另一条记录**。所以仅在「目标路径已被另一个 bundle id 占用」时追加 `-<appID>`；同一 App 重下仍然覆盖自己那份。
- `internal/ipa/mcp.go` 的 `download_app` 同步改用 App 名称（MCP 侧没有账本避让逻辑，冲突概率低，先记录为已知边界）。

**验证**：`go test ./internal/ipa/`（新增 `TestDownloadFileName` 7 例 + `TestDownloadFileNameTruncatesByRune`）、`go vet`、`go build ./...` 全绿；`DefaultDownloadFileName` 的路径逃逸测试（`..`、反斜杠、绝对路径、`\x00`）仍通过。

**诚实边界**：账本里已有的旧记录仍是 bundle id 命名，重新下载会产生一份名称命名的**新文件**（旧文件与旧记录不会自动改名/清理）。

### 8.20 2026-11-18：搜索页支持查看并下载历史版本

**起因**：用户要求「在搜索页面支持查看历史版本 并且支持下载历史版本」。之前按用户要求把版本入口整块删掉了（§8.10），历史版本只能走 MCP。

**改法**（只动前端 + 预览桩，后端 `ListVersions` / `GetVersionMetadata` / `Download` 本来就在）：
- 搜索页每行操作列变成两个按钮：「下载最新」+「版本」（列宽 130 → 180）。
- 新增历史版本面板（原生 `<dialog>`，`<dialog className="modal">` + `showModal()`，与登录验证码弹窗同一套样式）：
  - `CatalogService.ListVersions(appId, bundleId, platform)` 拿 `latestVersionId` + `versionIdentifiers`，列表先渲染出来；
  - `GetVersionMetadata` 逐版本补显示版本号与发布时间（**限 3 并发**）：上游没有批量接口，一个常更新的 App 几十个版本，全量并发只会更慢且容易被限流。单条失败只标那一行「元数据没查到」，不影响其它行；
  - 最新那一版打「最新」徽标；每行同时显示显示版本号与 Apple 版本标识（下载记录里两种都有）。
- 下载历史版本：`DownloadService.Start({ externalVersionId: versionId })`，走的是与「下载最新」完全相同的队列/进度/重试链路。
- 版本感知的重复检查 `confirmVersionDuplicate`：先看队列里有没有同一 `(bundleId, platform, versionId)` 的 pending/running 任务，再看账本里有没有这一版 —— 账本里历史版本记的是**版本标识**、最新版记的是**显示版本号**，两种都算命中。
- 付费未购仍然禁用（`canDownload` 不变），按钮 title 会给原因。
- 已购页**没加**版本入口：用户只要求搜索页。
- 预览桩 `frontend/preview.html`：`EnvService.Confirm` 改成可被脚本覆写的钩子 `window.__preview.confirm`（默认 `() => true`），因为确认框已经改成 Go 侧原生对话框（§8.17），`window.confirm` 在预览脚本里不再拦得住；同时把账本样例路径改成新的「App 名称」命名（§8.19）。

**验证**：`tsc --noEmit`、`npm run build`、`go build ./...`、`go vet`、`go test ./internal/...` 全绿；无头预览 **20/20** —— 新增 04b（面板列出 4 个版本、1 个「最新」、4 个下载按钮、显示版本号陆续补齐）与 04c（点第二行下载 8.0.49，任务进队列、该行按钮变「排队中…」），03/04 改为断言「下载最新 + 版本」两个按钮。

**诚实边界**：真实账号下没跑过 —— `list-versions` 对「只在某区域/某平台可下载」的 App 返回什么形状没验；历史版本体积 Apple 不提供，面板里不显示体积。

### 8.21 2026-11-19：换号后下载仍可能走旧账号（cookie jar 里旧账号的 `mz_at*` 从不清理）

**起因**：用户反馈「设置中心退出旧账号、登录新账号以后，搜索页面还是可能会用旧账号进行下载」。

**根因（有实测数据）**：Apple 的商店授权 cookie 按账号命名 —— `mz_at_ssl-<dsid>` / `mz_at0_fr-<dsid>`，有效期约两年。名字里带 DSID 意味着**换号登录不会覆盖旧账号那一对**：

- 上游 `Login` 只写钥匙串 + 让响应覆盖**同名** cookie；
- 上游 `Revoke()` 只删钥匙串的 `account` / `kbsync`，不碰 cookie jar；
- 本应用原来的 `Session.Logout()` 也只清内存。

于是持久 jar 里会累积多个账号的 `mz_at*`，而 `third_party/ipatool/internal/store/backend.go` 会把 jar 里**所有** cookie 塞进每个商店请求的 `Cookie` 头 —— `X-Dsid` 已经是新账号，但旧账号的 `mz_at_ssl-<旧DSID>` 也一起发出去，Apple 可能据此按旧账号处理。实测本机 `~/.ipatool/cookies` 同时存在多个不同 DSID 的 `mz_at*` 对（最早与最新相隔约三个月），而 `X-Dsid` 只有一个。

**改法**：
- 新增 `internal/ipa/cookies.go`：`pruneAccountCookies(jar, keepDSID)` 遍历 `jar.AllCookies()`，删掉 `mz_at*-<dsid>`（DSID ≠ 当前）与失配的 `X-Dsid`，然后 `jar.Save()` 落盘（不 Save 的话下次启动会从文件读回来）。`keepDSID == ""` 表示删全部账号 cookie。**不动**设备/商店级 cookie（`itspod`、`pldfltcid`、`vrep`、无 DSID 的 `mz_at0_fr` 等）—— 后者每次登录都会被同名覆盖，属于当前账号，删了只会让会话少一块。
- `internal/ipa/deps.go`：`Deps` 新增 `Jar *cookiejar.Jar`（`AllCookies` / `RemoveCookie` / `Save` 不在 `http.CookieJar` 接口上）。
- `internal/ipa/session.go`：`Login` 成功后按新账号 `DirectoryServicesID` 剪一次（DSID 为空时不剪，避免误删刚写入的 cookie）；`Revoke` 在删钥匙串成功后把账号 cookie 一起清掉 —— 「清除本机凭据」名副其实。
- `internal/ipa/catalog.go`：新增 `Services.PruneStaleAccountCookies()`；`internal/services/runtime.go` 在装配完成后调用一次，让**升级前**留下的旧账号 cookie 在启动时就失效，不必等用户重新登录。
- `frontend/src/AccountSection.tsx`：「清除本机凭据」的二次确认文案从未删 cookies 改为会清账号 cookie（设备身份保留）。

**验证**：`go vet ./internal/...`、`go test ./internal/...` 全绿；新增 `internal/ipa/cookies_test.go` 5 条（保留当前账号 / Revoke 全清 / 落盘持久 / Login 端到端 / 空 DSID 边界）。在真实 `~/.ipatool/cookies` 的**临时副本**上跑真实代码：17 → 剪掉 8 条（4 个旧账号 × 2）→ 剩 9 条（当前账号一对 + `X-Dsid` + 5 个设备/会话 cookie），未触碰用户真实文件。

**诚实边界**：登录**前**没有清 cookie（只在登录成功后剪），所以登录请求本身仍可能带着旧账号的 `mz_at*`；实测登录靠 email/password/2FA，不受这些 cookie 影响，暂不改。退出登录那一刻**已经在跑**的下载任务会继续用当时取到的旧账号凭据跑完（`ipa.Download` 只在 `passwordToken` 过期时重读凭据）—— 若要求「换号必打断下载」，需要另加「Logout/Login 时取消运行中任务」。外部 CLI / MCP 直接改 cookie 文件时本应用不会感知（jar 是进程内副本）。

### 8.22 2026-11-19：增加「切换账号」入口

**起因**：用户要求「增加账号切换功能」。之前换号只能手动两步：设置页点「退出登录」→ 回到登录表单，再输入另一个 Apple ID；顶部栏也看不出「当前这个账号可以换掉」。

**改法**：
- 后端新增 `AuthService.SwitchAccount()`（`internal/services/auth.go`）：语义与 `Logout()` 一致（`Session.Logout()` + `invalidateOwned("switch")` + `purchases:invalidated`），但事件里带 `phase: "switching"` 并返回退出后的 `AuthStatus`，前端据此直接展开登录表单、省一次 `Status()` 往返。**不动钥匙串**（官方 CLI / MCP helper 仍可用），**不清 cookie** —— 新账号登录成功时 `Session.Login` 已会按新 DSID 剪掉旧账号的 `mz_at*`（§8.21）。
- 新增 `frontend/src/account.ts` 的 `switchAccount(currentEmail)`：把「二次确认 + 调后端」收成一处，顶部栏与设置页共用，避免两处行为/文案不同步。确认框说明四件事：凭据保留可切回、已购按新账号重拉、进行中的下载不中断、CLI/MCP 共用同一份凭据。
- `frontend/src/App.tsx`：顶部栏在账号名右侧加「切换账号」（未登录时显示「登录」跳到设置页）；切换成功后自动跳设置页。
- `frontend/src/AccountSection.tsx`：账号卡片加「切换账号」按钮（在「退出登录」左侧，`busy` 时禁用）；切换成功后显示 `.notice.good` 提示、清空表单并把光标放进 Apple ID 输入框。**刻意不预填旧邮箱** —— 切号要输的是新账号，预填旧值容易让人以为「没切成功」。
- `frontend/wailsjs/go/services/AuthService.{js,d.ts}`：手工补 `SwitchAccount` 绑定（与生成器格式一致；下次 `wails build` 会重新生成同样的声明）。
- `frontend/preview.html`：`AuthService` 桩改成可变登录态（`previewAuth` / `previewLoggedOut()`），否则预览里点切换后 `Status` 仍返回已登录，界面看起来「点了没反应」。
- `scripts/preview-ui.mjs`：新增两条 —— `07b 顶部栏切换账号`（点按钮 → 断言顶部变「未登录」+ 按钮变「登录」+ 内容区出现密码框）、`07c 设置页切换账号`（先把桩恢复成已登录 → 点卡片按钮 → 断言出现绿色提示、Apple ID 框为空、光标已经落进去）。
- `frontend/src/AccountSection.tsx`：有 `switchNotice` 时不再重复渲染 `auth.message`（否则「已退出登录」会出现两遍）。

**验证**：`tsc --noEmit`、`npm run build`（产物含 `SwitchAccount`）、`go vet ./internal/...`、`go test ./internal/...` 全绿；无头预览 **22/22**（新增 07b / 07c，截图 `/tmp/ipagui-shots-switch4/07b.png` 与 `07c.png`）。

**诚实边界**：
- **不是「多账号保存 / 免密切换」**：密码与 2FA 不落盘是既定约束（PRD R8、§8.12），所以切换仍需输入新账号密码；上游钥匙串只有单个 `account` 条目，保存多份凭据还会与官方 CLI 的登录态互相踩。
- 切换**不打断**进行中的下载：任务会沿用开始那一刻的账号跑完（与「退出登录」一致，见 §8.21 边界）。
- 切换后未登录期间，`AuthService.Status()` 返回 `loggedOut: true`；重启应用会因凭据仍在而恢复原账号（这是既有语义，真删除用「清除本机凭据」）。

### 8.23 2026-11-19：检测到登录凭据过期就自动退出登录

**起因**：用户要求「如果检测到登录凭据已过期 应该退出登录」。之前的表现是：上游返回 `ErrPasswordTokenExpired`（App Store 的 password token 失效）后，错误只是被翻译成「登录凭据已过期」抛给界面，登录态仍停在「已登录」—— 用户会看到每一个操作都报同一句话，只能自己猜到要去设置页点「退出登录」再重新登录。

**改法**：
- 新增 `internal/ipa/expired.go`：
  - `IsCredentialExpired(err)`：只看 `MapError` 之后的 `CodePasswordTokenExpired`（直接比对哨兵错误会漏掉被包装过的）；
  - `Services.mapError(err)`：Services 内所有上游错误的统一收口，替换了 13 处裸 `MapError`（catalog / download / owned）。翻译之外多做一件事 —— 判定过期就调 `expireSession()`。包级的 `MapError` 保留给非 Services 场景（`ResetPartial`、`parallelFor`、`Session` 自身）。
  - `expireSession()`：`Session.LogoutExpired()` + `InvalidateOwned()` + 回调上层。**幂等**：已经登出就不再通知（并发的两个请求会同时拿到过期错误，否则前端会连闪两条 toast）。
  - `SetSessionExpiredHandler()`：ipa 包禁止 import wails，事件发送留给 services 层。
- `internal/ipa/session.go`：新增 `LogoutReason`（`manual` / `expired`）与 `LoggedOutReason()`、`LogoutExpired()`；`Login` / `Revoke` 清空原因。`Credentials()` 在过期登出后返回「登录凭据已过期 / 请重新登录后继续」，与手动退出的「已退出登录」区分开。
- `internal/services/auth.go`：`AuthStatus` 新增 `Expired bool`；`Status()` 在 `LoggedOut()` 时按 `LoggedOutReason()` 给不同 `message`（重启后前端也能知道上次是被过期登出的）。
- `internal/services/runtime.go`：装配时注册 handler，广播 `auth:state{phase:"expired", expired:true, message:"登录凭据已过期，请重新登录"}` 与 `purchases:invalidated{reason:"expired"}`（后端已购缓存由 ipa 层自己作废）。
- 前端：
  - `App.tsx` 订阅 `auth:state`：`phase === "expired"` 时 toast 提示、`refreshEnv()`、跳设置页 —— 用户直接看到登录表单；
  - `AccountSection.tsx`：`auth.expired` 时显示红色 notice「App Store 令牌已过期，当前账号已自动退出……」，并与「切换账号」的绿色提示互斥（过期优先）；
  - `wailsjs/go/models.ts`：`AuthStatus` 补 `expired`。
- `frontend/preview.html`：新增 `__previewExpire()`（登录态标成 expired）；`scripts/preview-ui.mjs` 新增 `07d 凭据过期` —— 先恢复已登录，再 `__emit("auth:state", {phase:"expired"})`，断言顶部变未登录、页面跳设置、内容区出现红色提示、toast 有「登录凭据已过期」。

**验证**：`go vet ./internal/...`、`go test ./internal/...` 全绿（新增 `internal/ipa/expired_test.go` 4 条：过期即登出且只通知一次、登出后不再请求上游、手动退出/过期登出的文案区分、重新登录清标记）；`tsc --noEmit`、`npm run build` 通过；无头预览 **23/23**（新增 07d，截图 `/tmp/ipagui-shots-expired2/07d.png`）。

**诚实边界**：
- 只对**上游明确返回**的 `ErrPasswordTokenExpired` 生效。`account_disabled`、密码变更、`invalid_credentials` **不会**自动登出 —— 那些不是「重新登录就好」的问题，贸然退出只会让用户以为是自己掉线了。
- 判定依赖 `MapError` 的错误码；上游若改错误类型，`errormap.go` 的 marker 常量与 `selftest` 会先红（这是刻意的，比静默失效好）。
- `loggedOut` 标记仍只活在进程内：重启应用会从钥匙串恢复成「已登录」，第一次操作再报过期、再登出一次。
- 进行中的下载在过期后不会立刻停：`ipa.Download` 先按既有逻辑 `Forget` + 重读凭据重试一次，最终失败才登出并写任务失败原因。

### 8.24 2026-11-19：精简界面上的说明文案与重复小标题

**起因**：用户要求「减少界面中无意义的提示文案、小标题」。实际翻一遍界面，冗余主要来自三类：与 tab / 子 tab 重复的卡片标题、两三句一段的实现解释、以及「只装一个按钮的空卡片」。

**删/改清单**：
- 顶部栏：`刷新状态` → `刷新`。
- 搜索页：删卡片标题「搜索 App Store」（tab 已是「搜索」）；删搜索框下那段「结果按账号所在区域返回（…storefront…换区只能换账号）。搜索本身也要先登录。」——保留的信息挪到「区域」标签的 `title`；版本面板删掉「显示版本号是一个版本一次请求查出来的…」整段，版本列数字与「查询中…」已自明；错误 notice 去掉「版本列表没拉到」小标题，只留错误文本 + 重试。
- 下载 · 队列：删标题「下载队列」（tab「下载」+ 子 tab「队列」已经说了两遍）与三句说明；刷新 / 重试按钮从独立的空卡片移进列表卡片顶部；空状态压成一句；折叠项标题从「已完成（N）—— 任务记录，默认收起；文件与哈希看「已下载」」改为「已完成（N）」。
- 下载 · 已下载：删标题「已下载的 IPA」与「这是下载账本，不是文件夹扫描…」两句说明；工具栏（过滤 / 平台 / 刷新）并入列表卡片。
- 已购：删标题「已购应用」与三句说明（其中「历史版本只能用 MCP / 命令行」已经过时，§8.20 已能在搜索页下载历史版本）；工具栏 + 过滤框 + 表格合并成一张卡片；删掉 stale / error notice 里「清单本身是完整的…」「常见原因：…」两段解释，保留标题、错误文本与重试；过滤框 placeholder 去掉「（只筛这一页数据，不再请求 Apple）」。
- 搜索页下载提示条（DownloadFeed）：删「同一时刻只跑一个下载，其余排队等待。」。
- 设置页：凭据卡片标题「凭据后端与文件口令」→「凭据后端」；删「只支持免费 App 的『获取许可』…」独立一行（挪进复选框 `title`）；删底部「注意：Windows 上…macOS 上…」（与后端返回的 `passphrase.hint` 重复）；「文件后端口令（保存进系统凭据库，不落明文）」→「文件后端口令」，括号内容挪进 `title`。
- 账号卡片：未登录说明压成「未登录：搜索与下载都需要先登录。」；登录中的资产下载说明从五行压成两行；登录前的「首次登录要下载 macOS 27 系统资产…」压成一句。

**保留的**：第三方登录风险告知（合规）、删除/覆盖类操作的二次确认、错误文本与重试入口、下载进度与失败原因、所有 `title` 里的操作性说明（tooltip 不占版面）。

**同步改动**：`scripts/preview-ui.mjs` 里依赖旧文案的断言一并更新（01 改为断言账号名/区域/条数/搜索；02 改断言「打开下载页」；08 的卡片刻表改「凭据后端」；13 去掉「已购应用」「这一页不看本地文件」；11/12 用「打开下载页」按钮定位提示条，不再靠那句说明）。

**验证**：`tsc --noEmit`、`npm run build`（bundle 275.0 → 274.6 kB）、`go build ./...`、`go vet ./internal/...`、`go test ./internal/...` 全绿；无头预览 **23/23**（截图 `/tmp/ipagui-shots-trim5/`）。

**诚实边界**：只做了「删冗余」，没有重排版面（字号、间距、配色未动）；卡片合并后已购 / 已下载 / 队列三页都变成「一张卡片 = 工具栏 + 列表」，如果以后某页工具栏继续变长，需要重新考虑分区。

**第二轮（同一诉求继续往下做）**：
- 设置页：把「期望后端 / 实际后端 / 一致」收进默认收起的 `<details>凭据后端自检</details>` —— 那是排障信息，普通用户只需要知道「要不要口令」；`passphrase.hint` 仍常驻。
- 账号卡片：风险告知 5 条各压成一行（要点与合规含义不变）；「你已确认过第三方登录的风险。再看一遍」→「已确认登录风险。再看一遍」。
- 已购页：`共 4 个 · 拉取于 11:00:22` → `共 4 个 · 11:00 更新`（秒级时间戳没有意义，时间挪进 `title` 说明是上次拉取时间）；体积缺失从「体积未知」改成「—」，与搜索页表格的占位一致。
- 已下载页：「文件」列不再显示 sha256 前 12 位（§8.17 已删掉「算 sha256」按钮，这串前缀既点不动也读不懂），完整哈希挪进该单元格的 `title`，悬停可见。

验证同上（`/tmp/ipagui-shots-trim7/`，预览 23/23，bundle 274.5 kB）。

### 8.25 2026-11-19：下载文件名拼上账号段（`<App>_<版本>_<账号>.ipa`）

**起因**：用户要求「再拼接上账号名称」。之前同一个 App 换号后再下一次，路径完全一样（`<App>_<版本>.ipa`），`avoidClobber` 又只在**不同 bundle id** 撞名时才触发 —— 结果就是新账号那份直接覆盖旧账号那份，账本里也只剩一条记录。

**改法**：
- `internal/ipa/download.go`：`DownloadFileName` 增加第 6 个参数 `account`，拼在版本段之后；账号段单独限长 `maxAccountSegment = 60`（email 可能很长，不能把整个文件名的额度吃光），并与其他外部输入一样过 `sanitizeSegment`。
- `internal/ipa/session.go`：新增 `Session.AccountLabel()` —— email 优先（唯一、能直接对应 Apple ID），为空退回姓名，未登录/读失败返回空串（此时不拼这一段，旧的 `<App>_<版本>.ipa` 仍然成立）。它读的是内存缓存，不会额外碰钥匙串。
- 两个调用点都接上：`internal/services/download.go`（GUI 的所有下载入口都传空 `outputPath`，命名统一在这里决定）与 `internal/ipa/mcp.go`（MCP 的 `download_app`）。
- 前端显示适配（文件名变长后被 `shortPath` 从中间省略，恰恰会把版本号吃掉）：
  - `frontend/src/api.ts` 的 `shortPath` 改为「先保住文件名，目录部分超长才省略；文件名自己超长才头尾各留一截」；
  - 下载队列页与搜索页下载提示条的路径也改成 `shortPath(…, 64)`（完整路径放 `title`）。

**现在的格式**：

```
<下载目录>/<App 名称>_<版本>_<账号>.ipa
微信_8.0.80_hejinghao@changzhi.top.ipa      ← 下最新版
QQ_857109923_tester@example.com.ipa         ← 下历史版本（版本段是 Apple 版本标识）
Notes_1.0_何静豪.ipa                        ← 账号没有 email 时用姓名
微信_8.0.80.ipa                             ← 未登录 / 旧调用点（不拼账号段）
```

**测试**：`internal/ipa/naming_test.go` 的用例表补了 `account` 字段并新增 4 条 —— 中文名 + email、账号里的路径分隔符被收敛、账号用中文姓名、账号段按字符截断；`gofmt`/`go vet`/`go test ./internal/...` 全绿。预览桩的路径同步成带账号段（`frontend/preview.html`），09 用例的确认框断言随之更新；无头预览 **23/23**（截图 `/tmp/ipagui-shots-accountname3/`）。

**诚实边界**：账本里的**旧记录不会改名** —— 同一次换号后重新下载会按新格式生成新文件，于是本地可能同时存在新旧两份（这正是要区分账号的代价，`已下载` 页会各列一行）。账号段用 email 时文件名偏长，窄窗口下路径会被 `shortPath` 压成 `…/`；`avoidClobber` 的 `-<appID>` 后缀仍然只在「不同 App 同名」时追加。

### 8.26 2026-11-24：升级上游 ipatool 到 v2.8.1（保留 sinf 修复）

**起因**：上游在 v2.8.0 之后连发 10 个 commit 并打了 `v2.8.1`（60 文件，+3961/−394）。逐文件比对确认：`go.mod`、`cmd/`、README 均未变（CLI 表面接口与依赖零变化），改动集中在下载链路、店面发现、SAP/模拟器与资产缓存。

**改法**：
- `third_party/ipatool` 基线从 `v2.8.0`（`72c47d5`）换成 **`v2.8.1`**（`3e84853`），导出方式同前（`git archive` 只带 `go.mod`/`go.sum`/`LICENSE`/`pkg`/`internal`）。
- 本地补丁仍只有**两个文件级改动**，但 FairPlay 回退这次需要覆盖 **两条** ent 分支：
  1. `pkg/appstore/appstore_download_product.go` 的 FairPlay 回退 —— `ent/download` 响应没带 `sinf`/`dpInfo` 时不接受，继续走 `volumeStore` → `redownload` → `updateProduct`；v2.8.1 新增的下架恢复 `sendDelistedDownload` 里那条 ent 回退也套上同一判定（无 FairPlay 数据 → 按回退失败处理）；
  2. `internal/sap/assets/aea.go` 的 FCS key UA（防御性）。
- `http_range.go` 的 UA 补丁**已由上游 v2.8.1（PR #653）吸收**，本地不再手改；`http_range_test.go` 与上游逐字节一致。
- 版本常量：`internal/ipa/selftest.go` 的 `ExpectedUpstreamVersion` → `v2.8.1`、`LocalForkVersion` → `v2.8.1+sinf-fix`，`ipa_test.go` 的钉版断言注释同步。

**验证**：`go mod tidy` 无 `go.sum` 变化；`go build ./...`、`go vet ./internal/... ./cmd/...`、`go test ./internal/... ./cmd/...` 全绿；`go run ./cmd/selfcheck` 报 `上游 ipatool: v2.8.1+sinf-fix` 且五项检查全部 ✓。另新增护栏测试 `internal/ipa/ipa_test.go::TestForkRetainsSinfFallback`：按符号断言 fork 里存在 `hasDownloadFairPlayData` 且 ent/download 分支确实接入了判定，防止下次整目录覆盖时补丁静默丢失。

**v2.8.1 带来的行为变化与本次配套修改**：
- >128 MB 的包会走 4 路并行 Range 下载，暂存目录为 `<目标>.ipatool-parts`。上游用 `os.Mkdir` 建它，崩溃残留会让下一次下载直接报 `failed to create download staging directory`。已处理：`internal/ipa/download.go` 新增 `partialPartsSuffix` / `removeStaleParts`（下载前清理），`PartialSize` 把 `.tmp` 与暂存目录**一起**统计，`ResetPartial` 一并删除；界面文案不再特指 `.tmp`。护栏：`download_test.go` 的 `TestPartialSizeCountsParallelParts` / `TestResetPartialRemovesPartsDirectory` / `TestDownloadClearsStalePartsDirectory`。
- 资产缓存目录 `apple-assets-macos-27-26A434-arm64-v4` → `-v5`（v5 只是更小的 compact 表示）。已处理：确认上游会**复用已有 v4 缓存并迁移**（不重新下载），只有全新缓存才走网络；首次准备的磁盘峰值约 300 MB（解压中间产物 167 MB + 落盘 75 MB + 余量），网络量是恢复镜像里的相关区段、不是 14 GB 整包。新增 `internal/ipa/assets_cache.go`：`AssetCacheReady` 判定缓存是否就绪（只看 `apple-assets-*` 下有无非空文件，不硬编码版本号），`Session.Login` 前调 `EnsureAssetCacheSpace` 预检空间（不足直接报 `disk_full`，并说清要多少/剩多少）。护栏：`assets_cache_test.go`。
  同次修正了登录页的错误文案：原先写「要下载十几 GB 系统资产」——实际是从镜像里按区段读、磁盘峰值约 300 MB，已改成准确表述。
- 登录店面从硬编码表改成 `availableStorefronts` 动态发现并缓存，手机号账号登录多一次请求，失败会让登录直接失败（`failed to discover storefronts: ...`）。已处理：映射为 `CodeTemporarilyUnavailable`（可重试，提示检查网络/代理）。
- 认证服务器返回 dialog 时错误文本变成 `消息: 解释 (Continue: https://...)`。已处理：`authDialogURLPattern` 提取 `*.apple.com` 链接，映射为新增的 `CodeActionRequired`，并优先于 `account is disabled` 等 marker（Apple 对话框文案里完全可能带这些句子）。护栏：`internal/ipa/errormap_test.go`（含「marker 文案仍在 fork 里」的上游侧断言）。
- 新增 `sendDelistedDownload`（下架 App 恢复）在 ent 分支直接返回响应、不经过 sinf 判定。已处理：fork 的补丁扩展到这条路径（无 FairPlay 数据→按回退失败处理，错误为 `ent/download fallback returned no FairPlay data`），`TestForkRetainsSinfFallback` 改为断言两处调用（定义 1 + 主链路 1 + 下架恢复 1）。

**诚实边界**：本次只做了编译/单测/自检级别的验证，**没有用真实 Apple ID 跑一次 v2.8.1 的登录与下载**；并行下载、店面发现、下架恢复三条新路径都未在真实网络下走过。资产磁盘阈值（`appleAssetPeakBytes = 300 MB`）是按上游 `fileSpec` 的 size 之和算的，不是实测峰值；缓存迁移（v4→v5）也没有在真实机器上走过一次完整流程。
