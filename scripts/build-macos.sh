#!/usr/bin/env bash
# 构建 macOS 版 IPA GUI（含 MCP helper），并把 helper 塞进 .app 包内。
#
# 用法：
#   scripts/build-macos.sh            # 默认 arm64
#   scripts/build-macos.sh amd64
#   CODESIGN_IDENTITY="Apple Development: xxx (XXXXXXXXXX)" scripts/build-macos.sh
#   CODESIGN_IDENTITY=<40 位 SHA-1> scripts/build-macos.sh   # 同名证书有歧义时用指纹
#
# 关于 CODESIGN_IDENTITY：macOS 的钥匙串授权（「IPA GUI 想要使用存储在钥匙串中的机密
# 信息」）是按**代码签名身份**记的。默认的 ad-hoc 签名（-）每次构建都会换一个 cdhash，
# 于是每次重建后点过的「始终允许」都作废、又要重新授权。换成一个稳定的证书身份
# （`security find-identity -v -p codesigning` 里挑一个）之后，「始终允许」就能长期有效。
#
# 关于 Developer ID：身份是「Developer ID Application」时，脚本会自动加上
# `--options runtime --timestamp`（公证的硬性要求）；这类签名需要联网取时间戳。
# 其它身份（Apple Development / ad-hoc）只做普通签名，因为开发证书不支持安全时间戳。
#
# 为什么需要这个脚本（而不是直接 wails build）：
#   1. `ipa-mcp` 是独立进程（ADR-6），必须和主程序放在一起，客户端配置里写的才是这个路径；
#      （GUI 里「一键接入」那张卡片已按用户要求删掉，配置需要自己写，见 architecture.md §8.11）
#   2. wails build 只会产出主程序，往 .app 里塞第二个二进制属于「改 bundle 内容」，
#      必须重新签名，否则 Gatekeeper 会拒；
#   3. 顺手把单测与自检跑在构建之前 —— 上游漂移时宁可构建失败，也不要发一个会写坏
#      状态目录的版本。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

ARCH="${1:-arm64}"
if [[ "$ARCH" != "arm64" && "$ARCH" != "amd64" ]]; then
    echo "不支持的架构: ${ARCH}（仅支持 arm64 / amd64；universal 需先把 helper 用 lipo 合成）" >&2
    exit 1
fi

VERSION="${VERSION:-0.1.0-dev}"
APP_NAME="IPAGUI"
HELPER="ipa-mcp"

export PATH="$HOME/go/bin:$PATH"
# macOS 必须开 cgo：系统钥匙串后端带 `cgo` 构建标签，
# CGO_ENABLED=0 会静默降级成文件后端（表现为突然要口令，而不是报错）。
export CGO_ENABLED=1

echo "==> 1/6 单测"
go test ./internal/...

echo "==> 2/6 静态检查"
go vet ./internal/... ./cmd/...

echo "==> 3/6 构建 helper（darwin/${ARCH}）"
go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" \
    -o "build/bin/$HELPER" ./cmd/ipa-mcp

echo "==> 4/6 构建应用（darwin/${ARCH}）"
wails build -platform "darwin/$ARCH" -ldflags "-X main.version=$VERSION"

APP="build/bin/$APP_NAME.app"
if [[ ! -d "$APP" ]]; then
    echo "没有找到 $APP —— wails build 的输出名可能变了" >&2
    exit 1
fi

echo "==> 5/6 把 helper 放进 app 包"
cp "build/bin/$HELPER" "$APP/Contents/MacOS/$HELPER"
chmod +x "$APP/Contents/MacOS/$HELPER"

echo "==> 6/6 重新签名并校验"
CODESIGN_IDENTITY="${CODESIGN_IDENTITY:--}"

# 默认参数：只覆盖已有签名。Developer ID 会在下面追加 runtime/timestamp。
SIGN_ARGS=(--force)
ENTITLEMENTS="$ROOT/scripts/macos-entitlements.plist"
if [[ "$CODESIGN_IDENTITY" == "-" ]]; then
    echo "    身份: ad-hoc（每次构建 cdhash 都变，钥匙串授权会反复问；设 CODESIGN_IDENTITY 可固化）"
else
    echo "    身份: $CODESIGN_IDENTITY"

    # 证书不在钥匙串里的话 codesign 会失败得很晚，这里先检查，报错更直白。
    MATCHES=$(security find-identity -v -p codesigning | grep -cF -- "$CODESIGN_IDENTITY" || true)
    if [[ "${MATCHES:-0}" -eq 0 ]]; then
        echo "找不到代码签名身份：$CODESIGN_IDENTITY（用 security find-identity -v -p codesigning 看可用的）" >&2
        exit 1
    fi

    # 同名证书（例如旧 G1 与新 G2 各一张）会让 codesign 报 ambiguous 而不是挑一张用。
    # 与其在最后一步抛一句晦涩的报错，不如在这里把候选列出来。
    if [[ "$MATCHES" -gt 1 ]]; then
        echo "身份名有歧义：\"$CODESIGN_IDENTITY\" 匹配到 $MATCHES 张证书。" >&2
        echo "  要么删掉过期的同名证书，要么改用 SHA-1 指纹作为 CODESIGN_IDENTITY：" >&2
        security find-identity -v -p codesigning | grep -F -- "$CODESIGN_IDENTITY" >&2 || true
        exit 1
    fi

    # Developer ID 是给「App Store 之外分发」用的：必须带加固运行时与安全时间戳，
    # 否则 notarytool 会以缺少 hardened runtime / 无时间戳为由拒绝。Apple Development
    # 不支持 --timestamp（会直接报错），所以这两个参数只在 Developer ID 身份上加。
    if security find-identity -v -p codesigning | grep -F -- "$CODESIGN_IDENTITY" | grep -q "Developer ID Application"; then
        SIGN_ARGS+=(--options runtime --timestamp)
        echo "    类型: Developer ID（加固运行时 + 安全时间戳，可送公证）"
    else
        echo "    类型: 开发/本地签名（仅本机使用，不带安全时间戳）"
    fi
fi

# 不用 --deep：--deep 对嵌套代码的重签行为已于 macOS 15 起被 Apple 弃用，且不会可靠地
# 把 runtime/timestamp 带给包内第二个可执行文件。正确顺序是「由内到外」：先签
# Contents/MacOS/ipa-mcp，再签 .app 本身（外层签名会封住所有资源与内层代码）。
sign_target() {
    local target="$1"
    local sign_args=("${SIGN_ARGS[@]}")
    if [[ -f "$ENTITLEMENTS" ]]; then
        sign_args+=(--entitlements "$ENTITLEMENTS")
    fi
    if ! codesign "${sign_args[@]}" --sign "$CODESIGN_IDENTITY" "$target"; then
        echo "签名失败: $target" >&2
        if [[ "$CODESIGN_IDENTITY" != "-" ]] && printf '%s\n' "${SIGN_ARGS[@]}" | grep -q -- "--timestamp"; then
            echo "  Developer ID 签名需要联网向 Apple 取安全时间戳；离线或 TSA 不可达时会失败。" >&2
        fi
        exit 1
    fi
}

sign_target "$APP/Contents/MacOS/$HELPER"
sign_target "$APP"
codesign --verify --deep --strict --verbose=2 "$APP"
codesign --display --verbose=2 "$APP" 2>&1 | grep -E "Identifier|Authority|TeamIdentifier|Timestamp|flags" || true

echo
echo "ok: $APP"
echo "helper: $APP/Contents/MacOS/$HELPER"
echo
echo "接下来可以："
echo "  open $APP                       # 启动"
echo "  $APP/Contents/MacOS/$HELPER --selftest    # 单独跑 helper 的自检"

# vite 的 emptyOutDir 会删掉 frontend/dist/.gitkeep；补回去，避免 git status 变脏。
# （仓库只跟踪这个占位文件，因为它满足 main.go 的 //go:embed all:frontend/dist）
touch frontend/dist/.gitkeep
