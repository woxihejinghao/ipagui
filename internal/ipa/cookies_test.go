package ipa

import (
	"net/http"
	"net/url"
	"sort"
	"testing"
	"time"

	cookiejar "github.com/juju/persistent-cookiejar"
	"github.com/majd/ipatool/v2/pkg/appstore"
)

var testCookieURL = &url.URL{Scheme: "https", Host: "apple.com", Path: "/"}

// seedCookies 往 jar 里塞一批「像 Apple 登录后留下的」cookie。
func seedCookies(t *testing.T, jar *cookiejar.Jar, cookies ...*http.Cookie) {
	t.Helper()

	jar.SetCookies(testCookieURL, cookies)
}

func cookieNames(jar interface{ AllCookies() []*http.Cookie }) []string {
	cookies := jar.AllCookies()

	names := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		names = append(names, cookie.Name)
	}

	sort.Strings(names)

	return names
}

func accountCookie(name, value string) *http.Cookie {
	// 必须带 Expires：juju 的 jar 只把持久 cookie 写进 cookies 文件，
	// 而 Apple 下发的 mz_at* 正好是两年有效的持久 cookie。
	return &http.Cookie{
		Name:    name,
		Value:   value,
		Path:    "/",
		Domain:  "apple.com",
		Secure:  true,
		Expires: time.Now().Add(24 * time.Hour),
	}
}

// TestPruneAccountCookiesKeepsCurrentAccount 钉住换号修复的核心行为：
// 登录新账号后，只保留当前 DSID 的 mz_at*，旧账号那一组必须从 jar 里消失，
// 而设备级 cookie（itspod 等）不受影响。
func TestPruneAccountCookiesKeepsCurrentAccount(t *testing.T) {
	deps := newTestDeps(t)
	jar := deps.Jar

	seedCookies(t, jar,
		accountCookie("mz_at_ssl-11111111111", "old-ssl"),
		accountCookie("mz_at0_fr-11111111111", "old-fr"),
		accountCookie("mz_at_ssl-22222222222", "new-ssl"),
		accountCookie("mz_at0_fr-22222222222", "new-fr"),
		accountCookie("X-Dsid", "11111111111"),
		accountCookie("itspod", "3"),
	)

	removed, err := pruneAccountCookies(jar, "22222222222")
	if err != nil {
		t.Fatalf("pruneAccountCookies 失败: %v", err)
	}

	if removed != 3 {
		t.Fatalf("删除条目数 = %d，期望 3（旧账号 2 个 mz_at* + 失配的 X-Dsid）", removed)
	}

	assertNames(t, cookieNames(jar), []string{
		"itspod",
		"mz_at0_fr-22222222222",
		"mz_at_ssl-22222222222",
	})
}

// TestPruneAccountCookiesClearsAllOnRevoke 确认「清除本机凭据」会把所有账号
// cookie 一起删掉（keepDSID 为空），但不会误删设备级 cookie。
func TestPruneAccountCookiesClearsAllOnRevoke(t *testing.T) {
	deps := newTestDeps(t)
	jar := deps.Jar

	seedCookies(t, jar,
		accountCookie("mz_at_ssl-11111111111", "ssl"),
		accountCookie("X-Dsid", "11111111111"),
		accountCookie("itspod", "3"),
	)

	removed, err := pruneAccountCookies(jar, "")
	if err != nil {
		t.Fatalf("pruneAccountCookies 失败: %v", err)
	}

	if removed != 2 {
		t.Fatalf("删除条目数 = %d，期望 2", removed)
	}

	assertNames(t, cookieNames(jar), []string{"itspod"})
}

// TestPruneAccountCookiesPersists 确认剪枝会落盘：不 Save 的话重启后旧账号
// cookie 会从 cookies 文件里被读回来，bug 依旧。
func TestPruneAccountCookiesPersists(t *testing.T) {
	deps := newTestDeps(t)
	jar := deps.Jar

	seedCookies(t, jar,
		accountCookie("mz_at_ssl-11111111111", "old"),
		accountCookie("mz_at_ssl-22222222222", "new"),
	)

	if _, err := pruneAccountCookies(jar, "22222222222"); err != nil {
		t.Fatalf("pruneAccountCookies 失败: %v", err)
	}

	// 重新打开同一个 jar 文件，模拟下一次启动。
	reopened, err := NewDeps(Options{StateDir: deps.StateDir, Passphrase: StaticPassphrase("test-passphrase")})
	if err != nil {
		t.Fatalf("重新打开依赖失败: %v", err)
	}

	assertNames(t, cookieNames(reopened.Jar), []string{"mz_at_ssl-22222222222"})
}

// TestLoginPrunesPreviousAccountCookies 是端到端的一条：Session.Login 成功后
// 会自动按新账号 DSID 剪掉旧账号的 cookie。
func TestLoginPrunesPreviousAccountCookies(t *testing.T) {
	deps := newTestDeps(t)
	deps.Store = &fakeStore{account: appstore.Account{
		Email:               "new@example.com",
		DirectoryServicesID: "22222222222",
	}}

	seedCookies(t, deps.Jar,
		accountCookie("mz_at_ssl-11111111111", "old"),
		accountCookie("mz_at_ssl-22222222222", "new"),
	)

	session := NewSession(deps)

	if _, err := session.Login("new@example.com", "password", ""); err != nil {
		t.Fatalf("登录失败: %v", err)
	}

	assertNames(t, cookieNames(deps.Jar), []string{"mz_at_ssl-22222222222"})
}

// TestLoginWithoutDSIDKeepsCookies 防御一条边界：DSID 为空时不能把刚登录写入的
// cookie 当成「其它账号的」全部删掉。
func TestLoginWithoutDSIDKeepsCookies(t *testing.T) {
	deps := newTestDeps(t)
	deps.Store = &fakeStore{account: appstore.Account{Email: "some@example.com"}}

	seedCookies(t, deps.Jar, accountCookie("mz_at_ssl-22222222222", "new"))

	session := NewSession(deps)

	if _, err := session.Login("some@example.com", "password", ""); err != nil {
		t.Fatalf("登录失败: %v", err)
	}

	assertNames(t, cookieNames(deps.Jar), []string{"mz_at_ssl-22222222222"})
}

func assertNames(t *testing.T, got, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("cookie = %v，期望 %v", got, want)
	}

	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("cookie = %v，期望 %v", got, want)
		}
	}
}
