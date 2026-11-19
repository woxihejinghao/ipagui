package ipa

import (
	"fmt"
	"sync"

	"github.com/majd/ipatool/v2/pkg/appstore"
)

// Account 是**可以外发给前端**的账号视图。
//
// 刻意只保留非敏感字段：appstore.Account 里的 Password / PasswordToken /
// DirectoryServicesID 等价于会话凭据，绝不能进 JSON 传给 WebView。
type Account struct {
	Email      string `json:"email"`
	Name       string `json:"name"`
	StoreFront string `json:"storeFront"`
}

// LogoutReason 说明这次登出是怎么来的（界面文案据此区分「手动退出」与「凭据过期」）。
type LogoutReason string

const (
	// LogoutManual 是用户在本应用里主动点了「退出登录 / 切换账号」。
	LogoutManual LogoutReason = "manual"
	// LogoutExpired 是检测到 App Store 令牌（password token）已失效，被动登出。
	LogoutExpired LogoutReason = "expired"
)

// Session 管理登录态。
//
// 登录态有两份存储（都由上游负责写）：
//  1. 状态目录里的 cookies 文件（juju/persistent-cookiejar）；
//  2. 系统凭据库里的 keychain 条目（key = "account"）。
//
// 我们额外在内存里缓存一份，避免每次操作都读一次凭据库。
type Session struct {
	deps *Deps

	mu      sync.RWMutex
	account *appstore.Account

	// loggedOut 记录「用户在本应用里主动退出登录」。
	//
	// 与 Forget 的区别：Forget 只是丢掉缓存，下一次读取会从钥匙串重新加载；
	// loggedOut 会让 Credentials 继续拒绝读钥匙串，直到重新 Login（或 Revoke）。
	// 没有它，「退出登录」按完刷新一下状态就会被钥匙串里的凭据「复活」。
	loggedOut bool

	// logoutReason 记录登出原因（manual / expired），仅供界面文案与前端分支用。
	logoutReason LogoutReason
}

func NewSession(deps *Deps) *Session {
	return &Session{deps: deps}
}

// Credentials 返回**完整**账号（含 password token），仅供后端调用上游 API。
// 不要把它序列化给前端。
func (s *Session) Credentials() (appstore.Account, error) {
	s.mu.RLock()

	if s.loggedOut {
		reason := s.logoutReason
		s.mu.RUnlock()

		if reason == LogoutExpired {
			return appstore.Account{}, appErr(CodeNotLoggedIn, "登录凭据已过期", "请重新登录后继续", false, nil)
		}

		return appstore.Account{}, appErr(CodeNotLoggedIn, "已退出登录", "重新登录后即可继续", false, nil)
	}

	cached := s.account
	s.mu.RUnlock()

	if cached != nil {
		return *cached, nil
	}

	output, err := s.deps.Store.AccountInfo()
	if err != nil {
		return appstore.Account{}, MapError(err)
	}

	account := output.Account

	s.mu.Lock()
	s.account = &account
	s.mu.Unlock()

	return account, nil
}

// Info 返回脱敏后的账号信息。
func (s *Session) Info() (Account, error) {
	account, err := s.Credentials()
	if err != nil {
		return Account{}, err
	}

	return redact(account), nil
}

// Login 登录。
//
// 注意两点上游行为：
//  1. appstore.LoginInput 没有 Context —— 首次登录可能要下载 Apple 资产并解签名，
//     耗时数分钟，且**无法中途取消**。界面必须用「进行中」状态而不是可取消的进度。
//  2. AuthCode 必须是 6 位数字，否则上游在本地就报错（"2FA code must contain
//     exactly six digits"）。界面应做同样的前置校验，别把用户送到网络往返里失败。
func (s *Session) Login(email, password, authCode string) (Account, error) {
	if email == "" {
		return Account{}, appErr(CodeInvalidCredentials, "请填写 Apple ID", "", false, nil)
	}

	output, err := s.deps.Store.Login(appstore.LoginInput{
		Email:    email,
		Password: password,
		AuthCode: authCode,
	})
	if err != nil {
		return Account{}, MapError(err)
	}

	s.mu.Lock()
	account := output.Account
	s.account = &account
	s.loggedOut = false
	s.logoutReason = ""
	s.mu.Unlock()

	// 换号收尾：剪掉其它账号的商店 cookie。
	//
	// 上游 Login 只写钥匙串 + 让 Apple 的响应覆盖**同名** cookie，而 mz_at*-<dsid>
	// 的名字里带着账号 ID，旧账号那一组永远不会被覆盖 —— 它会跟着之后每个商店
	// 请求一起发出去，Apple 可能据此按旧账号处理（表现为换号后下载仍走旧账号）。
	// 清理失败不影响登录本身：最坏情况是退回原来的行为。
	//
	// DSID 为空时不剪：pruneAccountCookies 约定空值 = 删全部，那会把刚登录写入的
	// 新 cookie 一起清掉（真实登录必定带 DSID，这里只是防御）。
	if account.DirectoryServicesID != "" {
		_, _ = pruneAccountCookies(s.deps.Jar, account.DirectoryServicesID)
	}

	return redact(account), nil
}

// Logout 是本应用内的「退出登录」：丢掉内存缓存并置位登出标记。
//
// 它不动钥匙串里的凭据（那是 Revoke 的事），所以是可逆的；但也不会像 Forget
// 那样下一次读取就把账号从钥匙串读回来 —— 标记会一直生效，直到重新 Login。
func (s *Session) Logout() {
	s.logout(LogoutManual)
}

// LogoutExpired 是「检测到凭据过期」时的被动登出。
//
// 与 Logout 行为一致，只把原因写成 expired，让 Status 能告诉界面「不是用户退的，
// 是令牌失效了」，而不是一句含糊的「已退出登录」。
func (s *Session) LogoutExpired() {
	s.logout(LogoutExpired)
}

func (s *Session) logout(reason LogoutReason) {
	s.mu.Lock()
	s.account = nil
	s.loggedOut = true
	s.logoutReason = reason
	s.mu.Unlock()
}

// Revoke 删除本机保存的登录凭据（对应 CLI 的 `auth revoke`）。
//
// 上游 v2.8.0 的实现只是删掉钥匙串里的 account 与 kbsync 缓存条目
// （**不删** v2.8.0 新增的 anisette-v4 设备身份），
// **不会**向 Apple 发请求，也不删状态目录里的 cookies 文件 ——
// 本方法在它之外额外把账号 cookie（mz_at*-<dsid> / X-Dsid）从 jar 里清掉，
// 这是「换号后仍在用旧账号」的根因。别的机器、别处拷贝走的令牌都不受影响。
func (s *Session) Revoke() error {
	if err := s.deps.Store.Revoke(); err != nil {
		return MapError(err)
	}

	// 上游 Revoke 只删钥匙串里的 account / kbsync，**不碰** cookie jar。
	// 账号 cookie 同样是本机会话凭据，「清除本机凭据」时一并删掉，否则换号后
	// 旧账号的 mz_at* 还会被继续发送（剪枝失败也只影响这一次操作）。
	_, _ = pruneAccountCookies(s.deps.Jar, "")

	s.mu.Lock()
	s.account = nil
	s.loggedOut = false
	s.logoutReason = ""
	s.mu.Unlock()

	return nil
}

// Forget 丢弃内存缓存（不影响磁盘上的登录态）。
// 用在「检测到外部 CLI 改了登录态」或「令牌过期需要重新读取」时。
// 它不会解除 Logout 置的登出标记 —— 那个只能靠 Login / Revoke 解除。
func (s *Session) Forget() {
	s.mu.Lock()
	s.account = nil
	s.mu.Unlock()
}

// LoggedOut 报告用户是否在本应用里主动退出过（状态文案用）。
func (s *Session) LoggedOut() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.loggedOut
}

// LoggedOutReason 返回登出原因；未登出时为空串。
func (s *Session) LoggedOutReason() LogoutReason {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.loggedOut {
		return ""
	}

	return s.logoutReason
}

// IsLoggedIn 探一次登录态，不返回错误细节。
func (s *Session) IsLoggedIn() bool {
	_, err := s.Credentials()

	return err == nil
}

// AccountLabel 返回「当前登录账号」的可读标识，用于下载文件名里的账号段。
//
// email 优先（唯一、能直接对应到 Apple ID），为空时退回姓名；
// 未登录或读失败返回空串 —— 调用方拼文件名时要把空串当成「不加这一段」。
func (s *Session) AccountLabel() string {
	info, err := s.Info()
	if err != nil {
		return ""
	}

	if info.Email != "" {
		return info.Email
	}

	return info.Name
}

func redact(account appstore.Account) Account {
	return Account{
		Email:      account.Email,
		Name:       account.Name,
		StoreFront: account.StoreFront,
	}
}

// ValidateAuthCode 复刻上游的 6 位数字校验，供界面在提交前拦截。
func ValidateAuthCode(code string) error {
	if code == "" {
		return nil
	}

	if len(code) != 6 {
		return fmt.Errorf("验证码必须是 6 位数字")
	}

	for _, r := range code {
		if r < '0' || r > '9' {
			return fmt.Errorf("验证码必须是 6 位数字")
		}
	}

	return nil
}
