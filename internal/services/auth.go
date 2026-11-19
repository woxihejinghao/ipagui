package services

import (
	"github.com/milan/ipagui/internal/ipa"
)

// AuthService 是登录态的绑定层。
type AuthService struct {
	runtime *Runtime
}

// NewAuthService 构造登录服务。
func NewAuthService(runtimeInstance *Runtime) *AuthService {
	return &AuthService{runtime: runtimeInstance}
}

// AuthStatus 是给前端的登录态快照。
type AuthStatus struct {
	LoggedIn   bool   `json:"loggedIn"`
	Email      string `json:"email,omitempty"`
	Name       string `json:"name,omitempty"`
	StoreFront string `json:"storeFront,omitempty"`
	Message    string `json:"message,omitempty"`
	// LoggedOut 表示「用户在本应用里主动退出过」，用来区分「从未登录」与「刚退出」。
	LoggedOut bool `json:"loggedOut"`
	// Expired 表示「因为 App Store 令牌失效被自动登出」，与主动退出区分开：
	// 界面要告诉用户重新登录，而不是「已退出登录（本机凭据仍保留）」。
	Expired bool `json:"expired"`
}

// Status 查询登录态。未登录不是错误，而是 LoggedIn=false。
func (s *AuthService) Status() (AuthStatus, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return AuthStatus{}, err
	}

	session := s.runtime.Catalog().Session()

	// 用户主动退出 / 凭据过期被自动退出：都不再回读钥匙串，否则刷新一下状态就「复活」了。
	if session.LoggedOut() {
		status := AuthStatus{LoggedIn: false, LoggedOut: true}

		if session.LoggedOutReason() == ipa.LogoutExpired {
			status.Expired = true
			status.Message = "登录凭据已过期，请重新登录"
		} else {
			status.Message = "已退出登录（本机凭据仍保留）"
		}

		return status, nil
	}

	account, err := session.Info()
	if err != nil {
		if ipa.IsNotLoggedIn(err) {
			return AuthStatus{LoggedIn: false, Message: "尚未登录"}, nil
		}

		return AuthStatus{}, err
	}

	return AuthStatus{
		LoggedIn:   true,
		Email:      account.Email,
		Name:       account.Name,
		StoreFront: account.StoreFront,
		Message:    "已登录",
	}, nil
}

// Login 登录 Apple 账号。
//
// 首次登录可能要几分钟（上游会下载并校验 Apple 的签名所需资产），前端应当给出
// 「准备中」提示而不是超时放弃。需要 2FA 时会返回 code=auth_code_required，
// 前端据此显示验证码输入框并用同一个方法重试（authCode 传六位数字）。
//
// 注意：本方法**不**把密码写进任何日志或事件。
func (s *AuthService) Login(email, password, authCode string) (ipa.Account, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return ipa.Account{}, err
	}

	s.runtime.Emit("auth:state", map[string]any{
		"loggedIn": false,
		"phase":    "authenticating",
		"message":  "正在登录（首次登录要下载 macOS 27 系统资产，可能需要较久）……",
	})

	account, err := s.runtime.Catalog().Session().Login(email, password, authCode)
	if err != nil {
		s.runtime.Emit("auth:state", map[string]any{
			"loggedIn": false,
			"phase":    "failed",
			"message":  "登录失败",
		})

		return ipa.Account{}, err
	}

	// 登录成功可能意味着换了账号：上一个账号的已购清单不能再显示。
	s.invalidateOwned("login")

	s.runtime.Emit("auth:state", map[string]any{
		"loggedIn":   true,
		"phase":      "done",
		"email":      account.Email,
		"name":       account.Name,
		"storeFront": account.StoreFront,
		"message":    "登录成功",
	})

	return account, nil
}

// Logout 退出本应用的登录态：只清内存缓存并阻止再次回读钥匙串。
//
// 不动钥匙串里的凭据（那是 Revoke 的事），所以重新登录不需要重新下载签名器；
// 但钥匙串本身也不会被清掉，本机的官方 CLI / MCP helper 仍可继续使用。
func (s *AuthService) Logout() error {
	if err := s.runtime.EnsureReady(); err != nil {
		return err
	}

	s.runtime.Catalog().Session().Logout()

	// 退出后已购清单属于已退出的那个账号，作废掉；重新登录会重拉。
	s.invalidateOwned("logout")

	s.runtime.Emit("auth:state", map[string]any{"loggedIn": false, "phase": "logged_out", "message": "已退出登录（本机凭据仍保留）"})

	return nil
}

// SwitchAccount 退出当前账号并进入「等待登录新账号」状态，供界面做账号切换。
//
// 行为上等价于 Logout（清内存登录态 + 置登出标记 + 作废已购缓存 + 广播事件），
// 单独一个方法是为了让事件里带得出语义（phase=switching），前端据此把登录表单
// 展开并给出「请登录新账号」的提示。
//
// 刻意**不动**钥匙串：官方 CLI / MCP helper 与 GUI 共用同一份凭据，切回旧账号
// 不需要重新下载签名器；也**不清** cookie —— 新账号登录成功时 Session.Login 会
// 按新 DSID 剪掉旧账号的 mz_at*（见 internal/ipa/cookies.go）。
func (s *AuthService) SwitchAccount() (AuthStatus, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return AuthStatus{}, err
	}

	s.runtime.Catalog().Session().Logout()

	// 已购清单属于刚退出的那个账号，切号后必须作废（内存 + 磁盘 + 前端）。
	s.invalidateOwned("switch")

	s.runtime.Emit("auth:state", map[string]any{
		"loggedIn": false,
		"phase":    "switching",
		"message":  "已退出当前账号，请登录新账号",
	})

	return AuthStatus{
		LoggedIn:  false,
		LoggedOut: true,
		Message:   "已退出登录（本机凭据仍保留）",
	}, nil
}

// Revoke 删除本机保存的登录凭据 —— 破坏性操作，界面上必须二次确认。
//
// 注意：上游 v2.8.0 的实现只是删 account / kbsync 条目（不删新增的
// anisette-v4 设备身份），不会向 Apple 撤销令牌；
// 其他机器上的会话不受影响。本应用额外会把状态目录 cookie jar 里的
// 账号 cookie（mz_at*-<dsid> / X-Dsid）一并清掉，避免换号后旧账号继续被使用。
func (s *AuthService) Revoke() error {
	if err := s.runtime.EnsureReady(); err != nil {
		return err
	}

	if err := s.runtime.Catalog().Session().Revoke(); err != nil {
		return err
	}

	s.invalidateOwned("revoke")

	s.runtime.Emit("auth:state", map[string]any{"loggedIn": false, "phase": "revoked", "message": "本机凭据已清除"})

	return nil
}

// invalidateOwned 作废已购清单缓存，并通知前端把本地那份也丢掉。
//
// 只清后端缓存不够：前端还有一个模块级 cache（frontend/src/owned.ts，按平台存
// 整份清单），不主动通知的话切号后已购页会一直拿着上一个账号的清单。
func (s *AuthService) invalidateOwned(reason string) {
	if s.runtime.Catalog() == nil {
		return
	}

	s.runtime.Catalog().InvalidateOwned()
	s.runtime.Emit("purchases:invalidated", map[string]any{"reason": reason})
}
