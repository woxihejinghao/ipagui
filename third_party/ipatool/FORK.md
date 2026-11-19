# ipatool fork（v2.8.0 + sinf 修复）

本目录是 GUI 构建时实际链接的 ipatool 源码，不是上游 `github.com/majd/ipatool/v2 v2.8.0` 的原样副本。

## 来源

- 仓库：<https://github.com/majd/ipatool>
- 基线：上游 `v2.8.0`（`72c47d538b726df397fbf17bb3af809d69ef51b7`，2026-11-08）
- 本地修复：`pkg/appstore/appstore_download_product.go` 的 FairPlay 回退（原先 cherry-pick 自 fork `woxihejinghao/ipatool` 的 `760fed0`，已按 v2.8.0 重新落地）

v2.8.0 是一次认证链路的整体升级：新增本地 Anisette / machine provisioning
（`internal/storeauth`）、把登录与商店请求改由模拟的 Apple Store 框架执行
（`internal/store`）、SAP 签名器迁移到 macOS 27 资产并支持 arm64 模拟。
直接原因见上游发布说明「Restored App Store authentication」。

## 修复内容

### 1. FairPlay 回退（`pkg/appstore/appstore_download_product.go`）

- `ent/download` 响应如果没有携带任何 FairPlay 数据（移动端 `sinf` / macOS `dpInfo`），不再直接接受；
- 继续走 `volumeStore` / `redownload` / `updateProduct` 回退链路，避免产出“二进制已加密但没有有效 sinf”的 IPA。

v2.8.0 自带的 `downloadPackagePlatform` 只在 macOS 平台校验 `sinf` / `dpInfo`，
iOS 平台仍然依赖这处回退，所以补丁必须保留。

### 2. 苹果 CDN User-Agent 修复（`internal/sap/assets/http_range.go`、`aea.go`）

v2.8.0 首次登录会从 `updates.cdn-apple.com` 下载 macOS 27 恢复镜像，但资产下载器用的是
裸 `&http.Client{}`，Go 默认 UA `Go-http-client/1.1` 会被苹果 CDN **直接 403**：

```text
prepare Apple authentication: load anisette assets: read pinned kernel collection:
read remote image range: server returned 403 Forbidden
```

实测（2026-11-10）同一个 Range 请求：`curl/8.x` → 206、空 UA → 206、
`Go-http-client/1.1` → 403。

**上游已在 2026-11-09 通过 PR #653（`f1372232`）修复**，实现为
`restoreImageUserAgent = "Mozilla/5.0"` + `http_range_test.go` 回归测试。
本 fork 已与上游实现对齐（包含测试）；`aea.go` 的 FCS key 请求也带上同一个 UA，
但实测 `wkms-public.apple.com` 对 Go UA 也返回 200，属于本地防御性改动，**不打算上游化**。

## 为什么放在仓库里

Go 的模块路径必须与 `go.mod` 一致。上游模块路径仍是
`github.com/majd/ipatool/v2`，用本地 `replace` 指向本目录：

```text
replace github.com/majd/ipatool/v2 => ./third_party/ipatool
```

## 同步方式

```bash
git clone https://github.com/majd/ipatool.git
cd ipatool
git checkout v2.8.0

# 只导出 GUI 需要的模块文件
git archive v2.8.0 go.mod go.sum LICENSE pkg internal \
  | tar -x -C /path/to/IPA-GUI/third_party/ipatool
```

同步后必须重放上面的 FairPlay 回退补丁，然后：

```bash
cd /path/to/IPA-GUI
go mod tidy          # v2.8.0 新增 go-macho / go-apfs / lzfse / purego 等依赖
go test ./internal/...
go run ./cmd/selfcheck
```

## v2.8.0 对 GUI 的影响

- 登录走 `nativeLogin`：首次登录会做本地 Anisette provisioning，并在钥匙串新增
  `anisette-v4` 条目（设备身份，不是账号凭据）。上游 `Revoke()` **不会**删除它。
- 首次登录需要下载 macOS 27 的 Store/SAP 资产，耗时比以前更长。
- Go 版本要求 `go 1.26.5`；GUI 侧 `progressbar` 必须与上游同为 `v3.19.1`
  （`internal/ipa/ipa_test.go` 有钉版断言）。
- `LoginInput` / `ErrAuthCodeRequired` / `NewAppStore(Args{...})` 等公开 API 未变，
  GUI 的 `internal/ipa` 不需要改装配逻辑。
- 升级 ipatool 时，先确认 GUI 的 `internal/ipa/ipa_test.go` 中钉住的
  `progressbar` / `keyring` / `cookiejar` 版本仍然一致，再同步本目录。

## 许可与分发

本目录是 MIT 许可下的 ipatool 派生物：上游版权归 Majd Alfhaily，许可全文随上游源码保留在
同目录的 `LICENSE`（不得删除）。本仓库对它的改动（上述 FairPlay 回退与 Apple CDN UA 修复）
同样以 MIT 授权，随本仓库整体分发。根目录 `NOTICE` 记录了完整的归属关系、补丁清单，以及
`internal/store/profile/data_generated.go` 属于上游生成数据这一事实。
