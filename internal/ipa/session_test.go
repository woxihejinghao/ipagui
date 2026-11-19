package ipa

import (
	"testing"

	"github.com/majd/ipatool/v2/pkg/appstore"
)

// fakeStore 只实现 Session 实际用到的方法；其余方法通过嵌入的 nil 接口
// 保留（被调用会 panic，提醒测试不要越界）。
type fakeStore struct {
	appstore.AppStore

	account appstore.Account
	revoked bool
}

func (f *fakeStore) AccountInfo() (appstore.AccountInfoOutput, error) {
	return appstore.AccountInfoOutput{Account: f.account}, nil
}

func (f *fakeStore) Login(appstore.LoginInput) (appstore.LoginOutput, error) {
	return appstore.LoginOutput{Account: f.account}, nil
}

func (f *fakeStore) Revoke() error {
	f.revoked = true

	return nil
}

// TestLogoutStopsKeychainReadback 钉住「退出登录」的真正语义：
// 置登出标记之后，Credentials 不能再从钥匙串把账号读回来 ——
// 否则界面刷新一下状态就「复活」了（这正是加标记前的行为）。
func TestLogoutStopsKeychainReadback(t *testing.T) {
	deps := newTestDeps(t)
	store := &fakeStore{account: appstore.Account{Email: "tester@example.com", Name: "Tester", StoreFront: "143441-1"}}
	deps.Store = store

	session := NewSession(deps)

	if _, err := session.Credentials(); err != nil {
		t.Fatalf("未登出时读凭据失败: %v", err)
	}

	session.Logout()

	if !session.LoggedOut() {
		t.Fatal("Logout 之后 LoggedOut() 应为 true")
	}

	if _, err := session.Credentials(); !IsNotLoggedIn(err) {
		t.Fatalf("Logout 之后应返回 not_logged_in，实际: %v", err)
	}

	// Forget 只丢缓存，不解除登出标记：令牌过期的重试路径不能把用户「登回去」。
	session.Forget()

	if _, err := session.Credentials(); !IsNotLoggedIn(err) {
		t.Fatalf("Forget 不应解除登出标记，实际: %v", err)
	}
}

// TestLoginClearsLogoutMark 确保重新登录是解除登出标记的唯一正常入口。
func TestLoginClearsLogoutMark(t *testing.T) {
	deps := newTestDeps(t)
	store := &fakeStore{account: appstore.Account{Email: "tester@example.com"}}
	deps.Store = store

	session := NewSession(deps)
	session.Logout()

	if _, err := session.Login("tester@example.com", "password", ""); err != nil {
		t.Fatalf("登录失败: %v", err)
	}

	if session.LoggedOut() {
		t.Fatal("登录成功后 LoggedOut() 应为 false")
	}
}

// TestRevokeClearsLogoutMarkAndStore 确认 Revoke 走的是存储层删除，
// 并且把登出标记一并清掉（下一次状态读取交给钥匙串的真实结果）。
func TestRevokeClearsLogoutMarkAndStore(t *testing.T) {
	deps := newTestDeps(t)
	store := &fakeStore{account: appstore.Account{Email: "tester@example.com"}}
	deps.Store = store

	session := NewSession(deps)
	session.Logout()

	if err := session.Revoke(); err != nil {
		t.Fatalf("Revoke 失败: %v", err)
	}

	if !store.revoked {
		t.Fatal("Revoke 应委托给存储层")
	}

	if session.LoggedOut() {
		t.Fatal("Revoke 之后 LoggedOut() 应为 false")
	}
}
