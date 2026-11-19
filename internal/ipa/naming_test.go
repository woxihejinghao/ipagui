package ipa

import (
	"strings"
	"testing"
)

// 文件名以 App 名称为主（用户要求）：以前是 bundle id，在访达里看不出是哪个 App。
// 但版本仍要能自报：`_latest.ipa` 被下次下载默默覆盖之后什么也说不清。
// 账号段拼在最后：换号后同一个 App 可能要再下一份，不带账号就会互相覆盖。
func TestDownloadFileName(t *testing.T) {
	cases := []struct {
		name           string
		appName        string
		bundleID       string
		versionID      string
		displayVersion string
		account        string
		expected       string
	}{
		{
			name: "中文名称 + 显示版本号 + 账号", appName: "微信",
			bundleID: "com.tencent.xin", displayVersion: "8.0.80",
			account:  "hejinghao@changzhi.top",
			expected: "微信_8.0.80_hejinghao@changzhi.top.ipa",
		},
		{
			name: "指定历史版本时用 Apple 的版本标识", appName: "QQ",
			bundleID: "com.tencent.mqq", versionID: "857109923", displayVersion: "9.3.75",
			account:  "tester@example.com",
			expected: "QQ_857109923_tester@example.com.ipa",
		},
		{
			name: "名称缺失时退回 bundle id", appName: "",
			bundleID: "com.tencent.mqq", displayVersion: "9.3.75",
			account:  "tester@example.com",
			expected: "com.tencent.mqq_9.3.75_tester@example.com.ipa",
		},
		{
			name: "名称与 bundle 都没有时用 appID", appName: "", bundleID: "",
			account:  "tester@example.com",
			expected: "app-42_latest_tester@example.com.ipa",
		},
		{
			name: "没有账号（未登录 / 旧调用点）时退回旧格式", appName: "微信",
			bundleID: "com.tencent.xin", displayVersion: "8.0.80",
			expected: "微信_8.0.80.ipa",
		},
		{
			name: "账号里不能有路径分隔符", appName: "Notes",
			bundleID: "com.apple.notes", displayVersion: "1.0",
			account: "../../etc/passwd", expected: "Notes_1.0_etc_passwd.ipa",
		},
		{
			name: "账号用姓名时可以带中文", appName: "Notes",
			bundleID: "com.apple.notes", displayVersion: "1.0",
			account: "何静豪", expected: "Notes_1.0_何静豪.ipa",
		},
		{
			name: "名称里的路径分隔符要被收敛", appName: "../../etc/passwd",
			bundleID: "com.x.app", displayVersion: "1.0",
			expected: "etc_passwd_1.0.ipa",
		},
		{
			name: "Windows 不允许的字符也要收敛", appName: `A/B:C*D?E"F<G>H|I`,
			bundleID: "com.x.app", displayVersion: "1.0",
			expected: "A_B_C_D_E_F_G_H_I_1.0.ipa",
		},
		{
			name: "名称以点结尾时不能留下尾点", appName: "Notes.  ",
			bundleID: "com.apple.notes", displayVersion: "1.0",
			expected: "Notes_1.0.ipa",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := DownloadFileName(
				42, testCase.appName, testCase.bundleID,
				testCase.versionID, testCase.displayVersion, testCase.account,
			)
			if got != testCase.expected {
				t.Fatalf("DownloadFileName(%q, %q, %q, %q) = %q, want %q",
					testCase.appName, testCase.versionID, testCase.displayVersion,
					testCase.account, got, testCase.expected)
			}
		})
	}
}

// 按字节截断会把中文切成半个字（非法 UTF-8），必须按字符截。
func TestDownloadFileNameTruncatesByRune(t *testing.T) {
	longName := strings.Repeat("中", 200)
	got := DownloadFileName(1, longName, "com.x.app", "", "1.0", "")

	suffix := "_1.0.ipa"
	base := strings.TrimSuffix(got, suffix)
	if base == got {
		t.Fatalf("文件名没有版本后缀: %q", got)
	}

	if len([]rune(base)) != maxFileNameSegment {
		t.Fatalf("按字符截断后应是 %d 个字符，实际 %d", maxFileNameSegment, len([]rune(base)))
	}

	if !strings.HasPrefix(longName, base) {
		t.Fatal("截断后的名字不是原名的前缀")
	}
}

// TestDownloadFileNameTruncatesAccount 确认账号段自己也有长度上限：
// email 再长也不能把文件名的额度吃光（App 名那一段已经占了不少）。
func TestDownloadFileNameTruncatesAccount(t *testing.T) {
	longAccount := strings.Repeat("a", 200) + "@example.com"
	got := DownloadFileName(1, "微信", "com.tencent.xin", "", "8.0.80", longAccount)

	const prefix = "微信_8.0.80_"
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("账号段应接在版本后面: %q", got)
	}

	label := strings.TrimSuffix(strings.TrimPrefix(got, prefix), ".ipa")
	if len([]rune(label)) != maxAccountSegment {
		t.Fatalf("账号段应按字符截到 %d，实际 %d: %q", maxAccountSegment, len([]rune(label)), label)
	}
}
