package ipa

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/majd/ipatool/v2/pkg/appstore"
)

// fakeDownloadStore 只实现 Download 路径用到的方法。
// 其余方法通过嵌入的 nil 接口保留（被调用会 panic，提醒测试不要越界）。
type fakeDownloadStore struct {
	appstore.AppStore

	account        appstore.Account
	output         appstore.DownloadOutput
	replicated     bool
	replicateInput appstore.ReplicateSinfInput
}

func (f *fakeDownloadStore) AccountInfo() (appstore.AccountInfoOutput, error) {
	return appstore.AccountInfoOutput{Account: f.account}, nil
}

func (f *fakeDownloadStore) Lookup(appstore.LookupInput) (appstore.LookupOutput, error) {
	return appstore.LookupOutput{App: appstore.App{ID: 1, BundleID: "com.example.app"}}, nil
}

func (f *fakeDownloadStore) Download(appstore.DownloadInput) (appstore.DownloadOutput, error) {
	return f.output, nil
}

func (f *fakeDownloadStore) ReplicateSinf(input appstore.ReplicateSinfInput) error {
	f.replicated = true
	f.replicateInput = input

	return nil
}

// TestDownloadReplicatesSinf 钉住下载后的收尾动作：
// Apple 的 sinf 是下载响应单独下发的，必须在 Download 返回前写回包内，
// 否则 GUI 产出的 IPA 与 CLI 不一致，离线安装会缺许可证。
func TestDownloadReplicatesSinf(t *testing.T) {
	deps := newTestDeps(t)

	destination := filepath.Join(t.TempDir(), "App.ipa")
	store := &fakeDownloadStore{
		account: appstore.Account{Email: "tester@example.com"},
		output: appstore.DownloadOutput{
			DestinationPath: destination,
			Sinfs:           []appstore.Sinf{{ID: 1, Data: []byte("sinf")}},
		},
	}
	deps.Store = store

	services := NewServices(deps)

	result, err := services.Download(t.Context(), DownloadRequest{
		BundleID:   "com.example.app",
		OutputPath: destination,
	}, nil)
	if err != nil {
		t.Fatalf("Download 失败: %v", err)
	}

	if result.SinfCount != 1 {
		t.Fatalf("SinfCount = %d，期望 1", result.SinfCount)
	}

	if !store.replicated {
		t.Fatal("Download 成功后必须调用 ReplicateSinf")
	}

	if store.replicateInput.PackagePath != destination {
		t.Errorf("ReplicateSinf.PackagePath = %q，期望 %q", store.replicateInput.PackagePath, destination)
	}

	if len(store.replicateInput.Sinfs) != 1 {
		t.Errorf("ReplicateSinf.Sinfs 数量 = %d，期望 1", len(store.replicateInput.Sinfs))
	}
}

// TestPartialSizeCountsParallelParts 钉住「半成品」的统计口径。
//
// 上游 v2.8.1 起，>128 MB 的包走 4 路 Range 并行下载，分片写在
// `<tmp>.ipatool-parts/` 目录里。只统计 .tmp 会把「已经下了几百 MB」显示成
// 「半成品 0 B」，清理时也会把目录留在磁盘上。
func TestPartialSizeCountsParallelParts(t *testing.T) {
	tmpPath := filepath.Join(t.TempDir(), "App.ipa.tmp")

	if size, ok := PartialSize(tmpPath); ok || size != 0 {
		t.Fatalf("两者都不存在时应返回 (0, false)，实际 (%d, %v)", size, ok)
	}

	if err := os.WriteFile(tmpPath, make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}

	if size, ok := PartialSize(tmpPath); !ok || size != 100 {
		t.Fatalf("只有 .tmp 时应为 100，实际 (%d, %v)", size, ok)
	}

	parts := tmpPath + partialPartsSuffix
	if err := os.MkdirAll(filepath.Join(parts, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}

	for name, length := range map[string]int{"0.part": 50, "1.part": 30} {
		if err := os.WriteFile(filepath.Join(parts, name), make([]byte, length), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if size, ok := PartialSize(tmpPath); !ok || size != 180 {
		t.Fatalf("应当把并行分片算进来（100+50+30，跳过子目录），实际 (%d, %v)", size, ok)
	}
}

// TestResetPartialRemovesPartsDirectory 确认「重新下载」会把暂存目录一起删掉。
func TestResetPartialRemovesPartsDirectory(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "App.ipa")
	tmpPath := destination + ".tmp"
	parts := tmpPath + partialPartsSuffix

	if err := os.WriteFile(tmpPath, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(parts, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := ResetPartial(destination); err != nil {
		t.Fatalf("ResetPartial 失败: %v", err)
	}

	for _, path := range []string{tmpPath, parts} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s 应被删除，实际 err=%v", path, err)
		}
	}
}

// TestDownloadClearsStalePartsDirectory 钉住下载前的自愈动作。
//
// 上游用 os.Mkdir 建暂存目录：进程被强杀留下的 `<tmp>.ipatool-parts/` 会让
// 下一次下载直接报 "failed to create download staging directory"。
func TestDownloadClearsStalePartsDirectory(t *testing.T) {
	deps := newTestDeps(t)

	destination := filepath.Join(t.TempDir(), "App.ipa")
	parts := destination + ".tmp" + partialPartsSuffix

	if err := os.MkdirAll(parts, 0o700); err != nil {
		t.Fatal(err)
	}

	deps.Store = &fakeDownloadStore{
		account: appstore.Account{Email: "tester@example.com"},
		output:  appstore.DownloadOutput{DestinationPath: destination},
	}

	if _, err := NewServices(deps).Download(t.Context(), DownloadRequest{
		BundleID:   "com.example.app",
		OutputPath: destination,
	}, nil); err != nil {
		t.Fatalf("Download 失败: %v", err)
	}

	if _, err := os.Stat(parts); !os.IsNotExist(err) {
		t.Errorf("下载前应清掉残留的暂存目录，实际 err=%v", err)
	}
}
