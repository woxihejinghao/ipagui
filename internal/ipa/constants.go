// Package ipa 是「框架无关」的 ipatool 集成层。
//
// 硬性约束：本包**不得** import github.com/wailsapp/wails/*，
// 这样核心逻辑可以被单元测试、CLI、乃至 MCP helper 复用。
//
// 上游 [majd/ipatool] v2.8.0 的 pkg/ 是公开可用的，但 cmd/ 下有几个**未导出**
// 的装配函数（prepareStateDirectory / newCookieJar / newKeychain）和几个只存在于
// cmd/constants.go 的常量，外部无法引用。本包把它们逐字复刻，并在 selftest.go 里
// 对「复刻是否仍与上游行为一致」做运行期断言。
package ipa

import "github.com/majd/ipatool/v2/pkg/appstore"

// 以下三个常量定义在 cmd/constants.go（不在 pkg/ 下），外部模块无法引用，
// 只能复刻字面量。必须与上游逐字一致，否则无法与官方 CLI 及 `ipatool mcp`
// 共享登录态（共享状态目录 + 同一个 Keychain service 名）。
const (
	ConfigDirectoryName = ".ipatool"
	CookieJarFileName   = "cookies"
	KeychainServiceName = "ipatool-auth.service"
)

// AccountKey 是 keychain 中账号条目的 key。
// 上游 pkg/appstore/appstore_account_info.go 里用的是字面量 "account"。
const AccountKey = "account"

// PlatformChoices 是与 CLI `--platform` 等价的取值集合（CLI 在 cmd 层做别名归一，
// 库层的 appstore.ParsePlatform 也接受别名，这里给出 GUI 下拉框的规范值）。
var PlatformChoices = []string{"iphone", "ipad", "appletv", "watchos", "visionos", "macos"}

// Platform 是上游平台枚举的别名，避免调用方到处 import 上游包。
type Platform = appstore.Platform

// ParsePlatform 直接转发上游实现，保证别名（ios/ipados/tvos/…）行为一致。
func ParsePlatform(value string) (Platform, error) { return appstore.ParsePlatform(value) }
