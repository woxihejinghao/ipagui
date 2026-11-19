//go:build !darwin && !linux && !windows

package ipa

import "errors"

// freeSpaceAt 在不认识的平台上只能承认查不了 —— 界面会把「查不到」当成
// 「不做预检」，而不是当成「空间为 0」。
func freeSpaceAt(path string) (int64, error) {
	return 0, errors.New("这个平台还不支持查询磁盘空间")
}
