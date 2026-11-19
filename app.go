package main

import (
	"context"

	"github.com/milan/ipagui/internal/services"
)

// version 由构建脚本用 -ldflags "-X main.version=..." 注入。
var version = "0.1.0-dev"

// App 是 Wails 的宿主对象。
//
// 它只做三件事：持有 Runtime、把 Wails 的生命周期回调转给 Runtime、不做任何业务逻辑。
type App struct {
	runtime *services.Runtime
}

// NewApp 构造宿主对象。
func NewApp(runtime *services.Runtime) *App {
	return &App{runtime: runtime}
}

// startup 由 Wails 在窗口创建后调用。
//
// 关键：这里不返回错误、也不做重活。环境装配（凭据后端、SQLite）已经在 NewRuntime
// 里做完了，失败也被收进 Runtime 里 —— GUI 必须能启动，然后如实显示「环境不可用」，
// 而不是启动失败留一个白屏。
func (a *App) startup(ctx context.Context) {
	a.runtime.Startup(ctx)
}

// shutdown 由 Wails 在退出时调用。
func (a *App) shutdown(ctx context.Context) {
	a.runtime.Shutdown(ctx)
}

// beforeClose 在窗口关闭前触发。目前不做拦截：任务状态已落 SQLite，
// 下次启动会被标成「中断」而不是凭空消失。
func (a *App) beforeClose(ctx context.Context) bool {
	return false
}
