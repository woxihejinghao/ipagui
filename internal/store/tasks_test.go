package store

import (
	"context"
	"testing"
)

// SaveTask 是全量 upsert：ON CONFLICT 时把每一列都换成传进来的值。这条行为是
// DownloadService.Retry 必须显式带上原任务名的原因（它重建 Task 时如果漏了 Name，
// 任务名会被冲成空串，下载完成后写进本地库的那条也是空名字）。
// 这个测试守的是「全量覆盖」这个前提：一旦 SaveTask 改成局部更新，这段注释与
// Retry 里保名字的代码就都该重新审视。
func TestSaveTaskUpsertCoversEveryColumn(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	original, err := store.SaveTask(ctx, Task{
		ID:           "task-1",
		AppID:        414478124,
		BundleID:     "com.tencent.xin",
		Name:         "微信",
		Platform:     "iphone",
		OutputPath:   "/tmp/wechat.ipa",
		State:        TaskFailed,
		ErrorCode:    "network",
		ErrorMessage: "网络中断",
	})
	if err != nil {
		t.Fatalf("SaveTask 失败: %v", err)
	}

	if original.Name != "微信" {
		t.Fatalf("Name = %q，期望 微信", original.Name)
	}

	// 重排队列时不带 name / 错误码：全部被覆盖成空串。
	requeued, err := store.SaveTask(ctx, Task{
		ID:         "task-1",
		AppID:      414478124,
		BundleID:   "com.tencent.xin",
		Platform:   "iphone",
		OutputPath: "/tmp/wechat.ipa",
		State:      TaskPending,
	})
	if err != nil {
		t.Fatalf("SaveTask(重排) 失败: %v", err)
	}

	if requeued.State != TaskPending {
		t.Fatalf("State = %q，期望 pending", requeued.State)
	}

	if requeued.ErrorCode != "" || requeued.ErrorMessage != "" {
		t.Fatalf("错误码/错误文本 = %q/%q，期望被清空（否则界面上会一直挂着旧的失败原因）",
			requeued.ErrorCode, requeued.ErrorMessage)
	}

	if requeued.Name != "" {
		t.Fatalf("Name = %q，期望空串 —— 这正说明调用方必须自己把名字带回去", requeued.Name)
	}

	// 带上名字再重排一次：名字要能保住。
	kept, err := store.SaveTask(ctx, Task{
		ID:         "task-1",
		AppID:      414478124,
		BundleID:   "com.tencent.xin",
		Name:       "微信",
		Platform:   "iphone",
		OutputPath: "/tmp/wechat.ipa",
		State:      TaskPending,
	})
	if err != nil {
		t.Fatalf("SaveTask(带名字) 失败: %v", err)
	}

	if kept.Name != "微信" {
		t.Fatalf("Name = %q，期望 微信", kept.Name)
	}
}
