package ipa

import (
	"context"
	"errors"
	"testing"

	"github.com/majd/ipatool/v2/pkg/appstore"
)

// expiredStore 模拟「钥匙串里的令牌已失效」：Search 一律返回上游的哨兵错误。
type expiredStore struct {
	appstore.AppStore

	account appstore.Account
	calls   int
}

func (f *expiredStore) AccountInfo() (appstore.AccountInfoOutput, error) {
	return appstore.AccountInfoOutput{Account: f.account}, nil
}

func (f *expiredStore) Search(appstore.SearchInput) (appstore.SearchOutput, error) {
	f.calls++

	return appstore.SearchOutput{}, appstore.ErrPasswordTokenExpired
}

// TestExpiredCredentialLogsOut 钉住「检测到凭据过期就退出登录」：
// 错误照常返回给界面（用户看得到原因），但登录态必须已经退掉，
// 并且只通知一次 —— 否则界面上会连闪好几条同样的提示。
func TestExpiredCredentialLogsOut(t *testing.T) {
	deps := newTestDeps(t)
	store := &expiredStore{account: appstore.Account{
		Email:               "tester@example.com",
		DirectoryServicesID: "11111111111",
		StoreFront:          "143441-1",
	}}
	deps.Store = store

	services := NewServices(deps)

	notified := 0
	services.SetSessionExpiredHandler(func() { notified++ })

	_, err := services.Search(context.Background(), "wechat", 5, "iphone")
	if err == nil {
		t.Fatal("凭据过期时 Search 应该返回错误")
	}

	if !IsCredentialExpired(err) {
		t.Fatalf("错误码应为 password_token_expired，实际: %v", err)
	}

	if !services.Session().LoggedOut() {
		t.Fatal("凭据过期后应自动退出登录")
	}

	if reason := services.Session().LoggedOutReason(); reason != LogoutExpired {
		t.Fatalf("登出原因 = %q，期望 %q", reason, LogoutExpired)
	}

	if notified != 1 {
		t.Fatalf("过期通知次数 = %d，期望 1", notified)
	}

	// 第二次请求：此时已经登出，Credentials 直接返回 not_logged_in，
	// 不会再打到上游，也不应重复通知。
	_, secondErr := services.Search(context.Background(), "wechat", 5, "iphone")
	if !IsNotLoggedIn(secondErr) {
		t.Fatalf("登出后应返回 not_logged_in，实际: %v", secondErr)
	}

	if notified != 1 {
		t.Fatalf("重复请求不应重复通知，实际通知 %d 次", notified)
	}

	if store.calls != 1 {
		t.Fatalf("登出后不应再请求上游，实际调用 %d 次", store.calls)
	}
}

// TestManualLogoutIsNotExpired 确认「手动退出」不会被误标成过期：
// 两者的界面文案不同（一个说凭据保留，一个要求重新登录）。
func TestManualLogoutIsNotExpired(t *testing.T) {
	deps := newTestDeps(t)
	deps.Store = &expiredStore{account: appstore.Account{Email: "tester@example.com"}}

	session := NewSession(deps)
	session.Logout()

	if session.LoggedOutReason() != LogoutManual {
		t.Fatalf("手动退出的原因 = %q，期望 %q", session.LoggedOutReason(), LogoutManual)
	}

	// Credentials 的消息文案也要区分开：过期时说「已过期」，而不是「已退出登录」。
	if _, err := session.Credentials(); err == nil || !hasMessage(err, "已退出登录") {
		t.Fatalf("手动退出的错误 = %v，期望「已退出登录」", err)
	}
}

// TestExpiredLogoutCredentialsMessage 确认过期登出后，后续操作给出的是
// 「凭据已过期」而不是普通的「已退出登录」。
func TestExpiredLogoutCredentialsMessage(t *testing.T) {
	deps := newTestDeps(t)
	deps.Store = &expiredStore{account: appstore.Account{Email: "tester@example.com"}}

	session := NewSession(deps)
	session.LogoutExpired()

	if session.LoggedOutReason() != LogoutExpired {
		t.Fatalf("登出原因 = %q，期望 %q", session.LoggedOutReason(), LogoutExpired)
	}

	_, err := session.Credentials()
	if !IsNotLoggedIn(err) {
		t.Fatalf("应返回 not_logged_in，实际: %v", err)
	}

	if !hasMessage(err, "登录凭据已过期") {
		t.Fatalf("错误 = %v，期望「登录凭据已过期」", err)
	}
}

// hasMessage 只看 AppError.Message（Error() 会额外附上 Hint，不适合直接比字符串）。
func hasMessage(err error, want string) bool {
	var appError *AppError
	if !errors.As(err, &appError) {
		return false
	}

	return appError.Message == want
}

// TestLoginClearsExpiredMark 确认重新登录会清掉过期标记（否则成功登录后
// 界面还会一直提示「已过期」）。
func TestLoginClearsExpiredMark(t *testing.T) {
	deps := newTestDeps(t)
	deps.Store = &fakeStore{account: appstore.Account{
		Email:               "tester@example.com",
		DirectoryServicesID: "11111111111",
	}}

	session := NewSession(deps)
	session.LogoutExpired()

	if _, err := session.Login("tester@example.com", "password", ""); err != nil {
		t.Fatalf("登录失败: %v", err)
	}

	if session.LoggedOut() || session.LoggedOutReason() != "" {
		t.Fatalf("登录成功后应清掉登出/过期标记，实际 reason=%q", session.LoggedOutReason())
	}
}
