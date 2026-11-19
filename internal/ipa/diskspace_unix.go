//go:build darwin || linux

package ipa

import (
	"fmt"
	"syscall"
)

// freeSpaceAt 用 statfs 问可用块。用 Bavail（普通用户可用的块）而不是 Bfree：
// 后者包含 root 保留的那部分，报给用户会偏乐观。
func freeSpaceAt(path string) (int64, error) {
	var stat syscall.Statfs_t

	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("查询磁盘空间失败：%w", err)
	}

	return int64(stat.Bavail) * int64(stat.Bsize), nil
}
