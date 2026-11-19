package services

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
)

// OpenInAppStore 在系统商店里打开这个 App 的页面。
//
// 为什么需要它：付费应用上游的 Purchase 只支持免费应用，买不了 —— 用户能做的
// 只有「去 App Store 里买，再回来下载」。把这一步做成一个按钮，别让人自己去搜。
func (s *CatalogService) OpenInAppStore(appID int64) error {
	if err := s.runtime.EnsureReady(); err != nil {
		return err
	}

	if appID <= 0 {
		return errors.New("缺少应用 ID，没法在 App Store 里定位它")
	}

	deepLink, webLink := appStoreLinks(appID)

	// macOS 优先用 macappstore:// 深链，直接落在 App Store 的该 App 页面。
	if runtime.GOOS == "darwin" {
		if err := exec.Command("open", deepLink).Run(); err == nil {
			return nil
		}
	}

	var command *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", webLink)
	case "windows":
		// start 的第一个参数是窗口标题占位，不能省。
		command = exec.Command("cmd", "/c", "start", "", webLink)
	default:
		command = exec.Command("xdg-open", webLink)
	}

	if err := command.Start(); err != nil {
		return fmt.Errorf("打开 App Store 失败：%w", err)
	}

	return nil
}

// appStoreLinks 给出「深链」和「网页」两种形态（分开是为了能单测，不必真的开商店）。
func appStoreLinks(appID int64) (string, string) {
	return fmt.Sprintf("macappstore://apps.apple.com/app/id%d", appID),
		fmt.Sprintf("https://apps.apple.com/app/id%d", appID)
}
