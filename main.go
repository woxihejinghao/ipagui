package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"github.com/milan/ipagui/internal/services"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	runtime := services.NewRuntime(version)

	app := NewApp(runtime)

	bindings := []interface{}{
		app,
		services.NewEnvService(runtime),
		services.NewAuthService(runtime),
		services.NewCatalogService(runtime),
		services.NewDownloadService(runtime),
		services.NewLibraryService(runtime),
		services.NewSettingsService(runtime),
	}

	err := wails.Run(&options.App{
		Title:     "IPA GUI",
		Width:     1180,
		Height:    820,
		MinWidth:  940,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		// 亮色主题：窗口底色必须和 style.css 里的 --bg 一致，
		// 否则页面加载完成前会闪一下深色/白底，和界面拼不起来。
		BackgroundColour: &options.RGBA{R: 246, G: 247, B: 249, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		OnBeforeClose:    app.beforeClose,
		Mac: &mac.Options{
			TitleBar: mac.TitleBarHiddenInset(),
			// 固定 Aqua（亮色）：系统处于深色模式时，原生标题栏/交通灯/滚动条
			// 不能跟着变黑，否则会和白底界面割裂。
			Appearance:           mac.NSAppearanceNameAqua,
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			About: &mac.AboutInfo{
				Title:   "IPA GUI",
				Message: "App Store IPA 下载工具，基于 ipatool v2.8.0 + FairPlay sinf 修复（fork）",
			},
		},
		Windows: &windows.Options{
			Theme: windows.Light,
		},
		Bind: bindings,
	})
	if err != nil {
		println("启动失败:", err.Error())
	}
}
