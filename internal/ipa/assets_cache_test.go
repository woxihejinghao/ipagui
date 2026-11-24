package ipa

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAssetCacheReadyIn 钉住「缓存是否已就绪」的判据。
//
// 这个判据决定要不要在登录前做磁盘预检：判错成「未就绪」只会多一次空间检查；
// 判错成「已就绪」会让空间不足的用户在 provisioning 中途失败。所以只认
// `apple-assets-*` 下**有非空文件**的目录，空目录/无关目录都不算。
func TestAssetCacheReadyIn(t *testing.T) {
	root := t.TempDir()

	if assetCacheReadyIn(root) {
		t.Fatal("空目录不应报告缓存就绪")
	}

	if assetCacheReadyIn(filepath.Join(root, "does-not-exist")) {
		t.Fatal("目录不存在时不应报告缓存就绪")
	}

	// 无关目录不算。
	if err := os.MkdirAll(filepath.Join(root, "ipatool"), 0o700); err != nil {
		t.Fatal(err)
	}

	if assetCacheReadyIn(root) {
		t.Fatal("非 apple-assets-* 目录不应报告缓存就绪")
	}

	// 空目录不算。
	empty := filepath.Join(root, "apple-assets-macos-27-26A434-arm64-v5")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}

	if assetCacheReadyIn(root) {
		t.Fatal("空缓存目录不应报告缓存就绪")
	}

	// 有文件就算就绪（v4 旧目录同样有效：上游会自动迁移成 v5）。
	legacy := filepath.Join(root, "apple-assets-macos-27-26A434-arm64-v4")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(legacy, "CoreFP"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if !assetCacheReadyIn(root) {
		t.Fatal("有非空文件的 apple-assets-* 目录应报告缓存就绪")
	}
}

// TestAssetCacheSpaceError 钉住登录前的空间阈值与文案。
func TestAssetCacheSpaceError(t *testing.T) {
	if err := assetCacheSpaceError(appleAssetPeakBytes); err != nil {
		t.Fatalf("刚好够 %d MB 时不应报错：%v", appleAssetPeakBytes>>20, err)
	}

	err := assetCacheSpaceError(64 << 20)
	if err == nil {
		t.Fatal("剩 64 MB 时应报磁盘空间不足")
	}

	mapped, ok := err.(*AppError)
	if !ok {
		t.Fatalf("错误应当是 *AppError，实际 %T", err)
	}

	if mapped.Code != CodeDiskFull {
		t.Errorf("Code = %s，期望 %s", mapped.Code, CodeDiskFull)
	}

	if mapped.Retryable {
		t.Error("空间不足需要用户先清理磁盘，不应标记为可重试")
	}
}
