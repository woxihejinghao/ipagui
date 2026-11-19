package ipa

import (
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
