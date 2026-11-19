package ipa

import (
	"net/http"
	"regexp"
	"strings"

	cookiejar "github.com/juju/persistent-cookiejar"
)

// accountCookiePattern 匹配 Apple 商店按账号（DSID）命名的授权 cookie：
// `mz_at_ssl-17541436626`、`mz_at0_fr-700036474` 等。
//
// 名字里带 DSID 意味着**换号时旧账号的那一对不会被同名覆盖**：它们会一直留在
// 持久 cookie jar 里（有效期约两年），并被塞进之后每个商店请求的 Cookie 头。
// 实测本机 `~/.ipatool/cookies` 同时存在 5 个不同 DSID 的 mz_at* 条目 ——
// 这就是「设置中心退出旧账号、登录新账号后，搜索页下载仍可能走旧账号」的根因。
var accountCookiePattern = regexp.MustCompile(`^mz_at[0-9a-zA-Z_]*-\d+$`)

// accountDSIDCookieName 是商店用来标记「当前会话属于哪个账号」的 cookie。
// 它没有 DSID 后缀，登录时会被同名覆盖；这里仍然校验一次，避免 jar 里出现
// 多个 domain/path 变体时把旧账号的那个带出去。
const accountDSIDCookieName = "X-Dsid"

// pruneAccountCookies 从持久 cookie jar 里删掉「不属于 keepDSID」的账号 cookie，
// 返回删除的条目数。keepDSID 为空表示删掉**全部**账号 cookie（Revoke 用）。
//
// 只动账号相关的条目（mz_at*-<dsid> 与 X-Dsid），设备/商店级别的 cookie
// （itspod、pldfltcid、vrep 等）保持原样：它们与账号无关，删了只会让 Apple
// 多下发一次，不会改变「用哪个账号」这件事。
//
// 删除后必须 Save：juju 的 jar 默认只在 Load/Save 时机落盘，不 Save 的话本进程
// 内存里干净了，但下次启动会从文件里把旧账号 cookie 又读回来。
func pruneAccountCookies(jar *cookiejar.Jar, keepDSID string) (int, error) {
	if jar == nil {
		return 0, nil
	}

	removed := 0

	for _, cookie := range jar.AllCookies() {
		if !isStaleAccountCookie(cookie, keepDSID) {
			continue
		}

		jar.RemoveCookie(cookie)
		removed++
	}

	if removed == 0 {
		return 0, nil
	}

	return removed, jar.Save()
}

// isStaleAccountCookie 判断一条 cookie 是否属于「应该被剪掉」的账号条目。
func isStaleAccountCookie(cookie *http.Cookie, keepDSID string) bool {
	if cookie == nil {
		return false
	}

	if cookie.Name == accountDSIDCookieName {
		if cookie.Value == "" {
			return false
		}

		return keepDSID == "" || cookie.Value != keepDSID
	}

	if !accountCookiePattern.MatchString(cookie.Name) {
		return false
	}

	if keepDSID == "" {
		return true
	}

	return !strings.HasSuffix(cookie.Name, "-"+keepDSID)
}
