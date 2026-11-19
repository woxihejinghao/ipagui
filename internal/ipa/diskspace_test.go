package ipa

import (
	"path/filepath"
	"testing"
)

// 批量下载前的空间预检靠它：查不到空间应该报错，而不是返回 0 让界面以为磁盘满了。
func TestFreeSpace(t *testing.T) {
	available, err := FreeSpace(t.TempDir())
	if err != nil {
		t.Fatalf("查询临时目录的可用空间失败：%v", err)
	}

	if available <= 0 {
		t.Fatalf("可用空间 = %d，应该大于 0", available)
	}
}

// 下载目录常常还没创建（默认 ~/Downloads/IPA 也可能是新的）：
// 这时要往上找到存在的目录再问，而不是直接报错。
func TestFreeSpaceWalksUpToExistingAncestor(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "还没有", "创建的", "IPA")

	available, err := FreeSpace(nested)
	if err != nil {
		t.Fatalf("上级目录存在时应该能查到空间：%v", err)
	}

	if available <= 0 {
		t.Fatalf("可用空间 = %d，应该大于 0", available)
	}
}

func TestFreeSpaceRejectsEmptyPath(t *testing.T) {
	if _, err := FreeSpace(""); err == nil {
		t.Fatal("空路径应该报错")
	}
}
