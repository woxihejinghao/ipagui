package ipa

import (
	"context"
	"errors"
	"net"
	"os"
	"regexp"
	"strings"
	"syscall"

	"github.com/byteness/keyring"
	"github.com/majd/ipatool/v2/pkg/appstore"
)

// Code 是给界面用的稳定错误码（可本地化、可分支）。
// 不要把上游的 error 字符串直接甩到界面上：它们随版本变，且对用户无意义。
type Code string

const (
	CodeNotLoggedIn            Code = "not_logged_in"
	CodeAuthCodeRequired       Code = "auth_code_required"
	CodeAuthCodeRejected       Code = "auth_code_rejected"
	CodeInvalidCredentials     Code = "invalid_credentials"
	CodeAccountDisabled        Code = "account_disabled"
	CodePasswordChanged        Code = "password_changed"
	CodePasswordTokenExpired   Code = "password_token_expired"
	CodeLicenseRequired        Code = "license_required"
	CodeLicenseAlreadyExists   Code = "license_already_exists"
	CodeSubscriptionRequired   Code = "subscription_required"
	CodeTemporarilyUnavailable Code = "temporarily_unavailable"
	CodePaidAppUnsupported     Code = "paid_app_unsupported"
	CodeAppNotFound            Code = "app_not_found"
	CodePassphraseRequired     Code = "keychain_passphrase_required"
	CodeNetwork                Code = "network"
	CodeDiskFull               Code = "disk_full"
	CodePermission             Code = "permission"
	CodeCanceled               Code = "canceled"
	CodeActionRequired         Code = "action_required"
	CodeUnknown                Code = "unknown"
)

// AppError 把一个底层错误翻译成「码 + 人话 + 建议动作 + 是否可重试」。
type AppError struct {
	Code      Code   `json:"code"`
	Message   string `json:"message"`
	Hint      string `json:"hint,omitempty"`
	Retryable bool   `json:"retryable"`
	cause     error
}

// Error 返回**给用户看**的文本：中文动作优先，不把错误码和上游英文原文甩到界面上。
// 诊断信息仍可通过 Unwrap() 拿到；未归类的错误没有更具体的中文说明，附上原始原因便于排查。
func (e *AppError) Error() string {
	text := e.Message
	if e.Hint != "" {
		text += "：" + e.Hint
	}

	if e.Code == CodeUnknown && e.cause != nil {
		text += "（底层错误：" + e.cause.Error() + "）"
	}

	return text
}

// Unwrap 保留错误链，调用方仍可用 errors.Is 判定上游哨兵错误。
func (e *AppError) Unwrap() error { return e.cause }

func appErr(code Code, message, hint string, retryable bool, cause error) *AppError {
	return &AppError{Code: code, Message: message, Hint: hint, Retryable: retryable, cause: cause}
}

// 上游的判据字符串。这些不是我们能改的接口，只能盯住——selftest.go 会断言它们仍然存在，
// 一旦上游改文案，启动自检就会失败，而不是让界面弹出一句看不懂的英文。
const (
	markerKeyringNotFound    = "The specified item could not be found in the keyring"
	markerAccountDisabled    = "account is disabled"
	markerPasswordChanged    = "Your password has changed."
	markerBadLogin           = "MZFinance.BadLogin.Configurator_message"
	markerLicenseNotFound    = "License not found"
	markerPaidAppUnsupported = "purchasing paid apps is not supported"
	markerNoUsableAuth       = "apple returned no usable authentication response"
	// v2.8.1 起登录会动态发现店面（availableStorefronts），失败时包成这句。
	markerStorefrontDiscovery = "failed to discover storefronts"
	markerVerificationFailed  = "apple did not complete verification"
)

// authDialogURLPattern 从上游认证弹窗错误里提取可信链接。
//
// v2.8.1 起 Apple 返回对话框时，错误文本形如：
//
//	This Apple ID has been locked for security reasons: ... (Continue: https://iforgot.apple.com/...)
//
// 上游只把 OK 按钮的 OpenURL 嵌进文本（且已校验必须是 https + *.apple.com），
// 这里再把 URL 抽出来给用户点。
var authDialogURLPattern = regexp.MustCompile(`https://[^\s()]*apple\.com[^\s()]*`)

// MapError 是唯一的错误翻译入口：所有对外 API 的错误都必须经过它。
func MapError(err error) *AppError {
	if err == nil {
		return nil
	}

	var mapped *AppError
	if errors.As(err, &mapped) {
		return mapped
	}

	switch {
	case errors.Is(err, context.Canceled):
		return appErr(CodeCanceled, "操作已取消", "", false, err)

	case errors.Is(err, context.DeadlineExceeded):
		return appErr(CodeNetwork, "网络请求超时", "请检查网络或代理设置后重试", true, err)

	case errors.Is(err, ErrPassphraseRequired):
		return appErr(CodePassphraseRequired, "需要设置凭据口令",
			"此平台没有可用的系统凭据库，ipatool 会退回到加密文件存储，需要你设置一个口令", false, err)

	case errors.Is(err, appstore.ErrAuthCodeRequired):
		return appErr(CodeAuthCodeRequired, "需要双重认证验证码",
			"请输入 Apple ID 收到的 6 位验证码后重试", false, err)

	case errors.Is(err, appstore.ErrPasswordTokenExpired):
		return appErr(CodePasswordTokenExpired, "登录凭据已过期",
			"请重新登录（App Store 令牌已失效）", true, err)

	case errors.Is(err, appstore.ErrLicenseRequired):
		return appErr(CodeLicenseRequired, "该应用需要先获取许可",
			"开启「自动获取免费应用许可」后重试", true, err)

	case errors.Is(err, appstore.ErrLicenseAlreadyExists):
		return appErr(CodeLicenseAlreadyExists, "许可已存在", "", false, err)

	case errors.Is(err, appstore.ErrSubscriptionRequired):
		return appErr(CodeSubscriptionRequired, "该应用需要有效订阅",
			"当前账号没有可用于该应用的订阅", false, err)

	case errors.Is(err, appstore.ErrTemporarilyUnavailable):
		return appErr(CodeTemporarilyUnavailable, "App Store 暂时不可用",
			"稍后重试；若持续出现，可能是账号被风控", true, err)

	case errors.Is(err, appstore.ErrAppNotFound):
		return appErr(CodeAppNotFound, "没有找到该应用",
			"确认 App ID 或 Bundle ID 是否正确", false, err)

	case errors.Is(err, keyring.ErrKeyNotFound):
		return appErr(CodeNotLoggedIn, "尚未登录", "请先登录 Apple ID", false, err)

	case errors.Is(err, syscall.ENOSPC):
		return appErr(CodeDiskFull, "磁盘空间不足", "请清理磁盘后重试（已下载的分片会被保留）", false, err)

	case errors.Is(err, os.ErrPermission):
		return appErr(CodePermission, "没有文件访问权限",
			"换一个输出目录，或检查该目录的权限", false, err)
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return appErr(CodeNetwork, "网络请求失败", "请检查网络或代理设置后重试", true, err)
	}

	// 字符串判据兜底：上游把这些信息放在 Error 的 Metadata 里（未导出类型，
	// 无法类型断言），只有 Error() 文本能拿到。
	text := err.Error()

	// 上游会把 HTTP 状态写进错误文本（SAP 签名器初始化时尤其常见：
	// "exchange SAP setup message: apple returned 502 Bad Gateway"）。
	// 这类错误与账号、密码无关，是苹果侧的临时故障，必须提示「稍后重试」而不是「操作失败」。
	if status, ok := transientHTTPStatus(text); ok {
		if status == "429" {
			return appErr(CodeTemporarilyUnavailable, "Apple 暂时限制了登录请求（HTTP 429）",
				"请求过于频繁，请等几分钟再试；不要在多个工具或设备上同时登录同一个 Apple ID", true, err)
		}

		return appErr(CodeTemporarilyUnavailable, "Apple 服务暂时不可用（HTTP "+status+"）",
			"这是苹果侧的临时故障，与账号密码无关；请稍后重试，若持续出现请检查网络或代理设置", true, err)
	}

	switch {
	case strings.Contains(text, markerStorefrontDiscovery):
		return appErr(CodeTemporarilyUnavailable, "无法获取 App Store 店面信息",
			"登录时要向苹果查询账号所在区域，这一步失败了；请检查网络或代理后重试", true, err)

	// 有可信链接就说明不是「失败」，而是需要用户去苹果页面处理（解锁账号、重置密码等）。
	// 这条必须排在 marker 判据之前：Apple 的对话框文案里也可能带 "account is disabled" 之类的句子。
	case authDialogURLPattern.MatchString(text):
		return appErr(CodeActionRequired, "Apple 要求在继续前先处理一个问题",
			"请在浏览器打开："+authDialogURLPattern.FindString(text)+"，处理完再回来重试", false, err)

	case strings.Contains(text, markerKeyringNotFound):
		return appErr(CodeNotLoggedIn, "尚未登录", "请先登录 Apple ID", false, err)

	case strings.Contains(text, markerAccountDisabled):
		return appErr(CodeAccountDisabled, "Apple 报告该账号已停用",
			"下载已购内容通常仍可用；登录/购买可能被 Apple 风控拦截，建议改用其他账号或稍后再试", false, err)

	case strings.Contains(text, markerPasswordChanged):
		return appErr(CodePasswordChanged, "Apple ID 密码已变更",
			"请重新登录以刷新凭据", true, err)

	case strings.Contains(text, markerLicenseNotFound):
		return appErr(CodeLicenseRequired, "该应用需要先获取许可",
			"开启「自动获取免费应用许可」后重试", true, err)

	case strings.Contains(text, markerPaidAppUnsupported):
		return appErr(CodePaidAppUnsupported, "不支持购买付费应用",
			"ipatool 只能获取免费应用或你已购内容的许可", false, err)

	case strings.Contains(text, markerVerificationFailed):
		return appErr(CodeAuthCodeRejected, "验证码没有通过",
			"Apple 没有完成这次验证：验证码可能已过期、已经用过，或者这一轮登录耗时太久（首次登录要几十秒）。"+
				"请重新点「登录」取得新验证码，收到后立刻输入", true, err)

	case strings.Contains(text, markerBadLogin), strings.Contains(text, "invalid credentials"):
		return appErr(CodeInvalidCredentials, "Apple ID 或密码不正确",
			"请重新输入；若已开启双重认证，还需要填写验证码", false, err)

	case strings.Contains(text, markerNoUsableAuth):
		return appErr(CodeInvalidCredentials, "Apple 未返回可用的登录结果",
			"请稍后重试，或换一个网络环境", true, err)
	}

	return appErr(CodeUnknown, "操作失败", "", false, err)
}

// transientHTTPStatus 从上游错误文本里识别「过一会儿再试」的 HTTP 状态。
//
// 上游没有导出这些错误的类型，只有文本可判（SAP 协议层用的是 response.Status，
// 例如 "502 Bad Gateway"；目录接口用的是 "status code: 502"）。
//
// 返回的状态码用于把 429（限流）与 5xx（服务端故障）区分开，两者给用户的
// 建议不一样：前者要等一等，后者是苹果临时故障。
func transientHTTPStatus(text string) (string, bool) {
	for _, status := range []string{"429", "500", "502", "503", "504"} {
		if strings.Contains(text, "apple returned "+status) ||
			strings.Contains(text, "status code: "+status) ||
			strings.Contains(text, "HTTP "+status) {
			return status, true
		}
	}

	markers := []struct {
		marker string
		status string
	}{
		{"Too Many Requests", "429"},
		{"Internal Server Error", "500"},
		{"Bad Gateway", "502"},
		{"Service Unavailable", "503"},
		{"Gateway Timeout", "504"},
	}

	for _, item := range markers {
		if strings.Contains(text, item.marker) {
			return item.status, true
		}
	}

	return "", false
}

// IsNotLoggedIn 判定「未登录」，界面用它决定是否跳转登录页。
func IsNotLoggedIn(err error) bool {
	mapped := MapError(err)

	return mapped != nil && mapped.Code == CodeNotLoggedIn
}
