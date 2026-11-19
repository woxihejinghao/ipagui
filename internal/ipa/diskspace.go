package ipa

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FreeSpace 返回 path 所在文件系统还能写入多少字节。
//
// path 不必存在（下载目录常常还没创建）：一路往上找到第一个存在的目录再问，
// 因为「/a/b/c 不存在」不代表问不了它将来会落在哪个磁盘上。
func FreeSpace(path string) (int64, error) {
	probe, err := existingAncestor(path)
	if err != nil {
		return 0, err
	}

	return freeSpaceAt(probe)
}

// existingAncestor 找到 path 自己或它最近的、真实存在的上级目录。
func existingAncestor(path string) (string, error) {
	if path == "" {
		return "", errors.New("路径为空")
	}

	current := filepath.Clean(path)

	for {
		if _, err := os.Stat(current); err == nil {
			return current, nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("找不到可用的上级目录：%s", path)
		}

		current = parent
	}
}
