//go:build windows

package ipa

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceExW = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// freeSpaceAt 走 kernel32 的 GetDiskFreeSpaceExW：Go 标准库在 Windows 上没有
// statfs 的等价物，而为了这一项引入 golang.org/x/sys 不划算。
func freeSpaceAt(path string) (int64, error) {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}

	var available, total, totalFree uint64

	ok, _, callErr := getDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(pointer)),
		uintptr(unsafe.Pointer(&available)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if ok == 0 {
		return 0, fmt.Errorf("查询磁盘空间失败：%w", callErr)
	}

	return int64(available), nil
}
