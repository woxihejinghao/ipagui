package services

import (
	"context"
	"io"
	"testing"

	"github.com/milan/ipagui/internal/ipa"
)

// 账本里记哪个版本号，决定了本地库那一列可不可读：
//   - 指定历史版本时必须是 Apple 的版本标识，SearchView 的「已下载」交叉引用按它匹配；
//   - 下载最新时没有版本标识，只能是 App Store 返回的显示版本号（8.0.50 这种），
//     否则用户最常走的那条路会在账本里留下一个空版本。
func TestLedgerVersion(t *testing.T) {
	service := NewDownloadService(nil)

	service.mu.Lock()
	service.labels["latest"] = "8.0.50"
	service.mu.Unlock()

	cases := []struct {
		name            string
		taskID          string
		externalVersion string
		expected        string
	}{
		{name: "指定历史版本时记 Apple 的版本标识", taskID: "latest", externalVersion: "857109923", expected: "857109923"},
		{name: "下载最新时记显示版本号", taskID: "latest", externalVersion: "", expected: "8.0.50"},
		{name: "没有记录的任务退回空串而不是 panic", taskID: "unknown", externalVersion: "", expected: ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := service.ledgerVersion(testCase.taskID, testCase.externalVersion)
			if got != testCase.expected {
				t.Fatalf("ledgerVersion(%q, %q) = %q, want %q",
					testCase.taskID, testCase.externalVersion, got, testCase.expected)
			}
		})
	}
}

// timeoutError 模拟「网络读超时」这类错误（net.Error 接口的最小实现）。
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// 自动重试只对「再试一次有意义」的错误生效：
// 网络抖动值得重试；没登录 / 没许可 / 磁盘满重试一百次也一样，白等 15 秒更糟。
func TestRetryableDownloadError(t *testing.T) {
	cases := []struct {
		name     string
		failure  error
		expected bool
	}{
		{name: "网络错误", failure: &ipa.AppError{Code: ipa.CodeNetwork}, expected: true},
		{name: "临时不可用", failure: &ipa.AppError{Code: ipa.CodeTemporarilyUnavailable}, expected: true},
		{name: "没登录", failure: &ipa.AppError{Code: ipa.CodeNotLoggedIn}, expected: false},
		{name: "没许可", failure: &ipa.AppError{Code: ipa.CodeLicenseRequired}, expected: false},
		{name: "磁盘满", failure: &ipa.AppError{Code: ipa.CodeDiskFull}, expected: false},
		{name: "用户取消", failure: context.Canceled, expected: false},
		{name: "传输断在半路", failure: io.ErrUnexpectedEOF, expected: true},
		{name: "读超时", failure: timeoutError{}, expected: true},
		{name: "没有错误", failure: nil, expected: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := retryableDownloadError(testCase.failure); got != testCase.expected {
				t.Fatalf("retryableDownloadError(%v) = %v, want %v", testCase.failure, got, testCase.expected)
			}
		})
	}
}
