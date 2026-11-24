package ipa

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMapErrorMapsStorefrontDiscoveryFailure 钉住 v2.8.1 新增的登录失败路径。
//
// 上游从 v2.8.1 起把店面表换成 availableStorefronts 动态发现，登录时会多一次
// 请求；失败时错误文本是 "failed to discover storefronts: ..."。这条不做映射就会
// 落进 CodeUnknown，界面只显示「操作失败」，用户不知道该等还是该换网络。
func TestMapErrorMapsStorefrontDiscoveryFailure(t *testing.T) {
	err := errors.New("failed to discover storefronts: storefront country code request failed: apple returned 502 Bad Gateway")

	mapped := MapError(err)
	if mapped.Code != CodeTemporarilyUnavailable {
		t.Fatalf("Code = %s，期望 %s", mapped.Code, CodeTemporarilyUnavailable)
	}

	if !mapped.Retryable {
		t.Error("店面发现失败是苹果侧/网络问题，应标记为可重试")
	}
}

// TestMapErrorMapsAuthenticationDialog 钉住 v2.8.1 的认证弹窗。
//
// 上游把 Apple 的对话框转成错误文本："<消息>: <解释> (<按钮>: https://*.apple.com/…)"。
// 这不是「失败」而是「需要用户去苹果页面处理」，必须给出可点的链接；
// 否则用户只会看到一句英文。
func TestMapErrorMapsAuthenticationDialog(t *testing.T) {
	err := errors.New("This Apple ID has been locked for security reasons: " +
		"Visit iforgot to unlock it. (Continue: https://iforgot.apple.com/password/verify/appleid)")

	mapped := MapError(err)
	if mapped.Code != CodeActionRequired {
		t.Fatalf("Code = %s，期望 %s", mapped.Code, CodeActionRequired)
	}

	if !strings.Contains(mapped.Hint, "https://iforgot.apple.com/password/verify/appleid") {
		t.Fatalf("Hint 里没有把链接提出来：%q", mapped.Hint)
	}
}

// TestMapErrorPrioritizesAuthenticationDialogOverMarkers 钉住判据顺序。
//
// Apple 的对话框文案里完全可能出现 "account is disabled" 这样的句子；
// 只要带可信链接，就该按「需要用户操作」处理，而不是按「账号停用」处理。
func TestMapErrorPrioritizesAuthenticationDialogOverMarkers(t *testing.T) {
	err := errors.New("Your account is disabled. (Continue: https://support.apple.com/en-us/HT204915)")

	mapped := MapError(err)
	if mapped.Code != CodeActionRequired {
		t.Fatalf("Code = %s，期望 %s（带可信链接的弹窗优先于 marker 判据）", mapped.Code, CodeActionRequired)
	}
}

// TestMapErrorIgnoresUntrustedDialogURL 确认只有 *.apple.com 的链接才会触发 action_required。
func TestMapErrorIgnoresUntrustedDialogURL(t *testing.T) {
	err := errors.New("Something went wrong (Continue: https://example.com/not-apple)")

	mapped := MapError(err)
	if mapped.Code == CodeActionRequired {
		t.Fatalf("非 apple.com 链接不应映射成 %s（Hint=%q）", CodeActionRequired, mapped.Hint)
	}
}

// TestErrormapMarkersStillExistInFork 盯住 marker 文案的上游侧。
//
// errormap 只能靠字符串匹配上游的未导出错误，上游一改文案映射就会静默失效。
// 这份 fork 由我们同步，所以直接把断言打在 fork 源码上。
func TestErrormapMarkersStillExistInFork(t *testing.T) {
	path := filepath.Join("..", "..", "third_party", "ipatool", "pkg", "appstore", "appstore_login.go")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 fork 的登录实现失败: %v", err)
	}

	if !strings.Contains(string(data), markerStorefrontDiscovery) {
		t.Errorf("%s 里找不到 %q：上游改了文案，errormap 的店面发现映射已失效", path, markerStorefrontDiscovery)
	}
}
