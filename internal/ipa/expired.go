package ipa

// IsCredentialExpired 判断一个错误是否表示「钥匙串里的 App Store 令牌已失效」。
//
// 上游在多个位置抛 appstore.ErrPasswordTokenExpired（下载、搜索、已购、购买都可能），
// MapError 会把它统一翻译成 CodePasswordTokenExpired。这里只看翻译后的码 ——
// 直接比对哨兵错误会漏掉被包装过的那些。
func IsCredentialExpired(err error) bool {
	mapped := MapError(err)

	return mapped != nil && mapped.Code == CodePasswordTokenExpired
}

// SetSessionExpiredHandler 注册「凭据过期导致自动登出」的回调。
//
// 为什么要回调：ipa 包不允许 import wails（见 constants.go 的硬性约束），
// 而「已自动退出登录」必须广播给界面。事件发送留在 services 层，这里只负责通知。
func (s *Services) SetSessionExpiredHandler(handler func()) {
	s.expiredMu.Lock()
	s.expiredHandler = handler
	s.expiredMu.Unlock()
}

// mapError 是 Services 内所有上游错误的统一收口（代替裸的 MapError）。
//
// 除了翻译错误码，它还承担一件事：**检测到凭据过期就把本应用的登录态退掉**。
// 不做的话界面会停在「已登录」，而后每一个操作都报同一句「令牌已失效」，
// 用户只能自己猜到要去设置页点退出再重新登录。
func (s *Services) mapError(err error) *AppError {
	mapped := MapError(err)
	if mapped == nil {
		return nil
	}

	if mapped.Code == CodePasswordTokenExpired {
		s.expireSession()
	}

	return mapped
}

// expireSession 把「凭据过期」落成一次登出：清内存账号、作废已购缓存、通知上层。
//
// 幂等：已经登出过就不再重复通知。并发的两个请求可能同时拿到过期错误
// （下载 goroutine 与界面请求），没有这层判断就会连发两次事件、前端闪两次 toast。
func (s *Services) expireSession() {
	if s.session.LoggedOut() {
		return
	}

	s.session.LogoutExpired()
	s.InvalidateOwned()

	s.expiredMu.Lock()
	handler := s.expiredHandler
	s.expiredMu.Unlock()

	if handler != nil {
		handler()
	}
}
