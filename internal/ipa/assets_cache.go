package ipa

import (
	"fmt"
	"os"
	"path/filepath"
)

// Apple 登录时上游要把一份 SAP 资产缓存写到用户缓存目录
// （`<UserCacheDir>/ipatool/sap/apple-assets-*`，见 third_party/ipatool/internal/sap/assets）。
//
// 首次登录没有缓存时，上游会从 Apple 的 macOS 27 恢复镜像里**按区段**读取所需内容
// （不是把十几 GB 的镜像整包下下来），解压到临时目录、校验、再落盘一份 compact 版本。
// 磁盘峰值 ≈ 临时解压产物 + 落盘缓存：
//
//	legacyFiles（解压出来的 universal 版）  174_733_872 B ≈ 167 MiB
//	requiredFiles（v5 落盘缓存）             78_249_168 B ≈  75 MiB
//
// 合计 ≈ 242 MiB。数字取自 v2.8.1 的 fileSpec 表，上游换版本会变，所以这里
// 只用于「空间够不够」的判断，并留出 ~60 MiB 余量。
const (
	appleAssetCacheBytes = 82 << 20  // 落盘缓存本身
	appleAssetPeakBytes  = 300 << 20 // 首次准备期间的峰值（解压 + 落盘 + 余量）
)

// appleAssetCacheRoot 返回上游写 SAP 资产缓存的父目录（`<UserCacheDir>/ipatool/sap`）。
func appleAssetCacheRoot() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("找不到用户缓存目录：%w", err)
	}

	return filepath.Join(root, "ipatool", "sap"), nil
}

// AssetCacheReady 报告 SAP 资产缓存是否已经就绪。
//
// 只要 `apple-assets-*` 下有任何一个非空目录就算就绪：上游 v2.8.1 会自动把
// 旧的 `-v4` 缓存迁移成 `-v5`，我们不需要（也不应该）在这里硬编码版本号。
func AssetCacheReady() bool {
	root, err := appleAssetCacheRoot()

	return err == nil && assetCacheReadyIn(root)
}

// assetCacheReadyIn 是 AssetCacheReady 的纯目录判据（便于测试）。
func assetCacheReadyIn(root string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}

	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) < len("apple-assets-") ||
			entry.Name()[:len("apple-assets-")] != "apple-assets-" {
			continue
		}

		files, err := os.ReadDir(filepath.Join(root, entry.Name()))
		if err != nil {
			continue
		}

		for _, file := range files {
			if info, infoErr := file.Info(); infoErr == nil && info.Size() > 0 {
				return true
			}
		}
	}

	return false
}

// EnsureAssetCacheSpace 在首次登录前检查缓存目录所在磁盘的剩余空间。
//
// 空间不足时上游会在 provisioning 中途失败，报的是
// "cache Apple SAP assets: ... no space left on device" —— 既没有说清
// 「要多少」，也把失败点埋在一串英文里。这里提前拦下来，给一个可操作的提示。
//
// 缓存已存在、或查不到磁盘信息时不阻断：交给上游自己的错误链路处理。
func EnsureAssetCacheSpace() error {
	if AssetCacheReady() {
		return nil
	}

	root, err := appleAssetCacheRoot()
	if err != nil {
		return nil
	}

	free, err := FreeSpace(root)
	if err != nil {
		return nil
	}

	return assetCacheSpaceError(free)
}

// assetCacheSpaceError 是 EnsureAssetCacheSpace 的纯判据（便于测试）。
func assetCacheSpaceError(free int64) error {
	if free >= appleAssetPeakBytes {
		return nil
	}

	return appErr(CodeDiskFull, "磁盘空间不足，无法准备 Apple 资产",
		fmt.Sprintf("首次登录要向 Apple 读取并缓存 macOS 27 资产：需要约 %d MB 可用空间（解压峰值 + 约 %d MB 落盘缓存），当前约 %d MB，位于用户缓存目录",
			appleAssetPeakBytes>>20, appleAssetCacheBytes>>20, free>>20), false, nil)
}
