# ipatool fork（上游 main `3b02b4c` + sinf 修复）

本目录是 GUI 构建时实际链接的 ipatool 源码，不是上游 `github.com/majd/ipatool/v2` 的原样副本。

## 来源

- 仓库：<https://github.com/majd/ipatool>
- 基线：上游 `main` 的 `3b02b4c55576562f2a55284cda9db9a4411d3067`（2026-11-17）
  —— 上游**尚未**为这些提交打 tag，最新 tag 仍是 `v2.8.1`
- 上一版基线：`v2.8.1`（`3e84853`，2026-11-12）；更早是 `v2.8.0`（`72c47d5`，2026-11-08）
- 本地改动共五处（见下）

v2.8.0 是一次认证链路的整体升级：新增本地 Anisette / machine provisioning
（`internal/storeauth`）、把登录与商店请求改由模拟的 Apple Store 框架执行
（`internal/store`）、SAP 签名器迁移到 macOS 27 资产并支持 arm64 模拟。
直接原因见上游发布说明「Restored App Store authentication」。

v2.8.1 是 v2.8.0 之后的 10 个 commit（60 文件，+3961/−394，`go.mod` 未变）：

| commit | 内容 |
| --- | --- |
| `0d99fc2` | 店面动态发现：删掉硬编码 `storeFronts` map，改走 `availableStorefronts` API 并缓存 |
| `8563232` | 认证服务器对话框：Apple 返回 dialog 时转成错误，可信 `apple.com` OpenURL 附在错误文本里 |
| `536d7ba` | 支持 percent-encoded URL 查询串 |
| `f137223` | restore image 下载改用浏览器兼容 UA（**本 fork 之前手抄的补丁，上游已吸收**） |
| `e0e2908` | 大文件并行下载（>128 MB + 有 ETag 时 4 路 Range，暂存目录 `<目标>.ipatool-parts`） |
| `8771e57` | 下架 App 恢复：`sendDelistedDownload`，redownload 失败后回退 ent/download，绕过 catalog 无 offer |
| `9df35a6` | auth state 未 provisioning 时提示重新登录 |
| `e4b0aae` | unicorn 运行时内存上限改为全平台生效（`engine_config.go`） |
| `2cfd33f` | 复用不可变的 emulator image 准备结果（`machimage/preparation_cache.go`） |
| `3e84853` | 资产下载/缓存重构：读前缓存、只保留模拟器用的架构、缓存目录 `arm64-v4` → `arm64-v5`（带 v4 迁移） |

v2.8.1 之后上游 `main` 又落了 6 个 commit（31 文件，+2293/−349，`go.mod` 未变；
没有新 tag，所以本目录的记录按 commit 走）：

| commit | 日期 | 内容 |
| --- | --- | --- |
| `2029022` | 11-12 | unicorn 并发请求下不再提前超时（`internal/sap/unicorn/clock.go`） |
| `6194bf4` | 11-12 | 读取 IPA 内版本元数据时预读（read-ahead） |
| `b352566` | 11-12 | 加固版本元数据的 range 读取 |
| `5a6dfe0` | 11-17 | 购买时恢复账号 storefront |
| `d69faf8` | 11-17 | 购买时接受 Apple 更新的 App Store 条款（`internal/store/terms`） |
| `3b02b4c` | 11-17 | storefront 改为从「带作用域的 bag」解析（`appstore_bag.go` / `account_storefront.go`） |

这 6 个 commit 对 fork 的影响：店面发现从「全局 `availableStorefronts` 列表 +
参数化 URL」变成「先读 bag 里的 storefront 元数据，拿不到再查 bag 广告的列表 URL」。
本地第 5 处补丁（发现重试）因此换了落点，见「修复内容 5」。

## 修复内容

### 1. FairPlay 回退（`pkg/appstore/appstore_download_product.go`）

- `ent/download` 响应如果没有携带任何 FairPlay 数据（移动端 `sinf` / macOS `dpInfo`），不再直接接受；
- 继续走 `volumeStore` / `redownload` / `updateProduct` 回退链路，避免产出“二进制已加密但没有有效 sinf”的 IPA。

v2.8.1 自带的 `downloadPackagePlatform` 只在 macOS 平台校验 `sinf` / `dpInfo`，
iOS 平台仍然依赖这处回退，所以补丁必须保留。两处插入点在 v2.8.1 里上下文未变，
可以原样重放。

**为什么只在主链路判定**：下架 App 恢复（`sendDelistedDownload`）走的是另一条 ent 分支。
本 fork 曾经也在那条路径上报错（`ent/download fallback returned no FairPlay data`），
但上游把「ent 成功返回、Items[0].Sinfs 为空」当成合法结果（`appstore_kbsync_test.go`
与 `appstore_delisted_download_test.go` 里 25 个用例都这么期望），下架恢复本来就是个
「能拿到什么算什么」的兜底。所以 2026-11-26 同步上游 main 时撤掉了那处报错：
缺 sinf 的事实通过 `DownloadOutput.Sinfs` 为空传到 GUI（`SinfCount = 0`），由上层展示，
而不是把一次可用的兜底结果变成错误。`internal/ipa/ipa_test.go` 的
`TestForkRetainsSinfFallback` 现在断言「定义 1 + 主链路 1」两处调用。

**上游测试 fixture**：上游把「成功响应没有 sinf」当合法成功，与这处补丁直接冲突，
`go test ./pkg/appstore` 会因此多出期望之外的请求。本地在 `appstore_kbsync_test.go`
的三处成功 fixture 上补了 `Sinfs: testFairPlaySinfs()`（辅助函数在本地新增的
`pkg/appstore/fork_helpers_test.go`），表达「真实成功响应是带 sinf 的」。同步上游时要
一并重放这三处 fixture。

### 2. 苹果 CDN User-Agent 修复（`internal/sap/assets/aea.go`）

v2.8.0 首次登录会从 `updates.cdn-apple.com` 下载 macOS 27 恢复镜像，但资产下载器用的是
裸 `&http.Client{}`，Go 默认 UA `Go-http-client/1.1` 会被苹果 CDN **直接 403**：

```text
prepare Apple authentication: load anisette assets: read pinned kernel collection:
read remote image range: server returned 403 Forbidden
```

**上游已在 v2.8.1（PR #653 / `f137223`）修复**，实现为
`restoreImageUserAgent = "Mozilla/5.0"` + `http_range_test.go` 回归测试，
本 fork 现在**直接使用上游实现**，不再手改 `http_range.go`。

剩下唯一的本地改动在 `aea.go`：FCS key 请求也带上同一个 UA。实测
`wkms-public.apple.com` 对 Go UA 也返回 200，属于本地防御性改动，**不打算上游化**。

### 3. 内置 Apple Root CA 回退（`internal/storeauth/apple_root_embedded.go`、`http_client.go`）

Anisette 的 machine provisioning 打的是 `https://gsa.apple.com/grandslam/MidService/…`，
该服务端证书链的根是老的「Apple Root CA」（2006-04-25 签发、SHA-1 自签名，2035-02-09 到期），
Windows 证书库与 Go 的 `crypto/x509` 都不认：

```text
prepare Apple authentication: install Apple provisioning root: download Apple root: HTTP 403
```

上游的补救是下载 `https://www.apple.com/appleca/AppleIncRootCertificate.cer` 再加进
本次连接的 `RootCAs`，但**该地址现在取不到了**：2026-11-24 实测直连、
走 HTTP 代理、换 `User-Agent` 全是 HTTP 403（`https://www.apple.com/certificateauthority/AppleRootCA.cer`
同样 403）。于是首次登录在任何一台 Windows 机器上都会停在上面这行错误。

本补丁把该证书内置（`apple_root_embedded.go`），下载失败时作为回退：

- `installAppleRoot` 先照旧调用 `downloadAppleRoot`（原逻辑原样搬到独立函数）；
- 下载报错则改用内置证书，并**仍然**过一遍上游的 `validAppleRoot`
  （CN = `Apple Root CA`、O = `Apple Inc.`、CA:TRUE、KeyUsageCertSign、自签名、在有效期内）；
- 内置证书的来源是 `gsa.apple.com` TLS 握手中服务端发出的链（自签名末端），
  不是第三方镜像，SHA-1 自签名无法用 `CheckSignatureFrom` 复核，这个校验只能靠上述字段 + 来源。

**不打算上游化**：上游更可能的做法是换掉这个历史根，而不是内置一份证书。
`notBefore` 之前的场景不会触发这条路径，所以本补丁只影响「装不上根就登录不了」的环境。

### 4. 认证响应追踪（`pkg/appstore/appstore_login_trace.go`、`appstore_login.go`）

登录失败时上游只抛一句英文，HTTP 状态、苹果返回的 `customerMessage`、
这一轮究竟带着哪套机器头（`X-Apple-I-MD` / `-M` / `-RINFO`）全被丢掉，
国内网络下排查 2FA 失败无从下手。

设置 `IPATOOL_AUTH_TRACE=<日志文件路径>` 后，每次认证往返往该文件追加一行 JSON
（次数、是否带验证码、HTTP 状态、`failureType`、`customerMessage`、AMD 动作、
`X-Dsid` 有无、机器头有无、错误文本）。**默认关闭，不设环境变量没有任何开销。**

刻意不记录任何凭据：密码、验证码、`passwordToken`、cookie、机器头的值都不写，
只写「有没有」和长度。**不打算上游化**（上游有自己的日志设施）。

### 5. 店面发现重试（`pkg/appstore/storefront.go`、`storefront_discovery_test.go`）

店面发现是搜索 / 登录 / 下载的共同前置，而它只是个只读的公共查询。苹果侧抖一下
（5xx / 429 / 连接被重置）就让「搜索」直接报错，看起来像功能坏了。本地给它 3 次
快速重试（300ms、600ms 退避）：

- `discoverAvailableStorefronts` 外层循环，把「发请求 + 判定 + 解码」抽到
  `fetchAvailableStorefronts`，返回 `(map, retryable, error)`；
- 可重试：网络层错误、429、5xx；**不重试**：4xx 与解码 / 内容问题（重试结果不会变）；
- 仍然不缓存失败——用完次数还是错就把错误往上抛，`storefrontListLoaded` 保持 false，
  下一次调用重新去问苹果；
- 错误文本带上状态码（`available storefronts request failed (HTTP 403)`），否则界面
  只能看到「失败了」而分不清 403 还是 503。

落点随上游 main 变了：v2.8.1 时期是硬编码 `availableStorefrontsURL` 的静态参数，
main 起改成 bag 广告的 URL，参数化变成 `discoverAvailableStorefronts(endpoint string)`。
**不打算上游化**（重试策略属于客户端偏好）。

## 为什么放在仓库里

Go 的模块路径必须与 `go.mod` 一致。上游模块路径仍是
`github.com/majd/ipatool/v2`，用本地 `replace` 指向本目录：

```text
replace github.com/majd/ipatool/v2 => ./third_party/ipatool
```

## 同步方式

```bash
# 基线是上游 main，不是 tag（上游还没为这些提交打 tag）
git clone https://github.com/majd/ipatool.git
cd ipatool
git checkout 3b02b4c

# 只导出 GUI 需要的模块文件（保留本目录的 FORK.md）
git archive 3b02b4c go.mod go.sum LICENSE pkg internal \
  | tar -x -C /path/to/IPA-GUI/third_party/ipatool
```

同步后必须重放上面的补丁（**五处**，见「修复内容」；其中第 1 处还包括
`appstore_kbsync_test.go` 三处成功 fixture 的 `Sinfs` 与新增的
`pkg/appstore/fork_helpers_test.go`），然后：

```bash
cd /path/to/IPA-GUI
go mod tidy          # v2.8.0 起新增 go-macho / go-apfs / lzfse / purego 等依赖
go build ./...
go vet ./internal/... ./cmd/...
go test ./internal/... ./cmd/...
go run ./cmd/selfcheck
```

想在 fork 目录里单独跑上游自带的测试（`go test ./pkg/...`）需要先生成 mock ——
上游的 `pkg/*/mock_*.go` 是 `go generate` 产物，**不提交到仓库**，而本目录只导出
`pkg` 与 `internal`。做法：把本目录复制到临时位置，在那里跑

```bash
go generate ./pkg/...   # 生成 *_mock.go（不要提交回本目录）
go test ./pkg/appstore/
```

验证补丁是否真的还在：`internal/ipa/ipa_test.go` 的 `TestForkRetainsSinfFallback`
按符号断言主链路判定还在；`TestForkRetainsStorefrontRetry` 同类型地盯第 5 处补丁。

## v2.8.1 对 GUI 的影响

- **并行下载**：>128 MB 的包会走 4 路 Range，暂存目录是 `<目标>.ipatool-parts`。
  上游用 `os.Mkdir` 建这个目录，崩溃残留会让下一次下载直接失败，所以 GUI 在下载前
  清一次；`internal/ipa/download.go` 的 `PartialSize` / `ResetPartial` 已经把
  `.tmp` 与该目录一起统计和删除（界面上的「半成品」「删半成品」都算总大小）。
- **资产缓存换目录**：`apple-assets-macos-27-26A434-arm64-v4` → `-v5`。v5 只是更小的
  compact 表示（v4 缓存 167 MB、v5 缓存 75 MB），且上游会**直接复用已有 v4 缓存并
  迁移成 v5**，不重新下载；只有全新缓存才走网络。首次准备的磁盘峰值约 300 MB
  （解压中间产物 167 MB + 落盘缓存 75 MB + 余量），网络量是恢复镜像里的相关区段，
  不是 14 GB 的整包（整包从不在本地落地）。
  GUI 侧：`internal/ipa/assets_cache.go` 在登录前做空间预检（不足直接报 `disk_full`，
  并说明要多少、剩多少），登录页文案也换成了准确数字。
- **店面动态发现**：`loginStorefront` 不再查硬编码表，手机号账号登录时多一次
  `availableStorefronts` 请求；失败会让登录直接失败（错误文本
  `failed to discover storefronts: ...`）。`Account.StoreFront` 的格式未变。
- **认证对话框**：Apple 返回 dialog 时错误文本形如
  `消息: 解释 (Continue: https://...)`。GUI 的 `internal/ipa/errormap.go` 会把其中的
  `*.apple.com` 链接提取出来，映射成 `CodeActionRequired`（提示用户去浏览器处理）。
- 登录走 `nativeLogin`：首次登录会做本地 Anisette provisioning，并在钥匙串新增
  `anisette-v4` 条目（设备身份，不是账号凭据）。上游 `Revoke()` **不会**删除它。
- Go 版本要求 `go 1.26.5`；GUI 侧 `progressbar` 必须与上游同为 `v3.19.1`
  （`internal/ipa/ipa_test.go` 有钉版断言）。
- `LoginInput` / `ErrAuthCodeRequired` / `NewAppStore(Args{...})` / `DownloadOutput{Sinfs}`
  等公开 API 未变，GUI 的 `internal/ipa` 不需要改装配逻辑。
- 升级 ipatool 时，先确认 GUI 的 `internal/ipa/ipa_test.go` 中钉住的
  `progressbar` / `keyring` / `cookiejar` 版本仍然一致，再同步本目录；
  `internal/ipa/selftest.go` 的 `ExpectedUpstreamVersion` / `LocalForkVersion`
  也要跟着改，否则启动自检会报「上游版本漂移」。

## 许可与分发

本目录是 MIT 许可下的 ipatool 派生物：上游版权归 Majd Alfhaily，许可全文随上游源码保留在
同目录的 `LICENSE`（不得删除）。本仓库对它的改动（上述 FairPlay 回退、FCS key UA、
内置 Apple Root CA、认证响应追踪）同样以 MIT 授权，随本仓库整体分发。根目录 `NOTICE`
记录了完整的归属关系、补丁清单，以及 `internal/store/profile/data_generated.go`
属于上游生成数据这一事实。
