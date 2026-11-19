package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()

	path := filepath.Join(t.TempDir(), "nested", "ipa.db")

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q) 失败: %v", path, err)
	}

	t.Cleanup(func() { _ = store.Close() })

	return store
}

func TestOpenCreatesSchemaAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "ipa.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("首次 Open 失败: %v", err)
	}

	version, err := first.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("读取 schema 版本失败: %v", err)
	}

	if version != schemaVersion {
		t.Fatalf("schema 版本 = %d，期望 %d", version, schemaVersion)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("账本文件未创建: %v", err)
	}

	// 第二次打开必须是无操作的迁移，而不是报错或重建。
	second, err := Open(path)
	if err != nil {
		t.Fatalf("再次 Open 失败（迁移不幂等）: %v", err)
	}

	defer func() { _ = second.Close() }()

	again, err := second.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("再次读取 schema 版本失败: %v", err)
	}

	if again != schemaVersion {
		t.Fatalf("重开后的 schema 版本 = %d，期望 %d", again, schemaVersion)
	}
}

// 这条守的是 DSN 里的 _pragma 参数：写错了不会报错，只会静默用回默认日志模式，
// 一直到并发写出现 database is locked 才暴露。
func TestOpenEnablesWALAndBusyTimeout(t *testing.T) {
	store := openTestStore(t)

	var journalMode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("读取 journal_mode 失败: %v", err)
	}

	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q，期望 wal", journalMode)
	}

	var busyTimeout int
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("读取 busy_timeout 失败: %v", err)
	}

	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d，期望 5000", busyTimeout)
	}
}

func TestOpenRejectsFutureSchemaVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ipa.db")

	future, err := Open(path)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}

	// 伪造一个「由更新版本的程序写过」的库。
	if _, err := future.db.ExecContext(ctx, "PRAGMA user_version = 99"); err != nil {
		t.Fatalf("写入版本号失败: %v", err)
	}

	if err := future.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	if _, err := Open(path); err == nil {
		t.Fatal("打开更高 schema 版本的账本必须失败，否则旧程序会写坏新库")
	}
}

func TestUpsertEntryIsKeyedByPath(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	first, err := store.UpsertEntry(ctx, Entry{
		AppID:     123,
		BundleID:  "com.example.app",
		Name:      "示例",
		Version:   "1.0",
		Platform:  "iphone",
		Path:      "/tmp/a.ipa",
		SizeBytes: 10,
	})
	if err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}

	second, err := store.UpsertEntry(ctx, Entry{
		AppID:     123,
		BundleID:  "com.example.app",
		Name:      "示例",
		Version:   "2.0",
		Platform:  "iphone",
		Path:      "/tmp/a.ipa",
		SizeBytes: 20,
		SHA256:    "deadbeef",
	})
	if err != nil {
		t.Fatalf("二次写入失败: %v", err)
	}

	if first.ID != second.ID {
		t.Fatalf("同一 path 的两次写入产生了不同 id：%d vs %d", first.ID, second.ID)
	}

	if second.Version != "2.0" || second.SizeBytes != 20 || second.SHA256 != "deadbeef" {
		t.Fatalf("更新字段未生效: %+v", second)
	}

	entries, err := store.ListEntries(ctx, EntryFilter{})
	if err != nil {
		t.Fatalf("ListEntries 失败: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("记录数 = %d，期望 1（path 去重失效）", len(entries))
	}
}

func TestListEntriesFiltersAndDeletes(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	seed := []Entry{
		{BundleID: "com.example.alpha", Name: "Alpha", Platform: "iphone", Path: "/tmp/a.ipa", Version: "1.0"},
		{BundleID: "com.example.beta", Name: "Beta", Platform: "ipad", Path: "/tmp/b.ipa", Version: "2.0"},
		{BundleID: "com.example.gamma", Name: "Gamma", Platform: "iphone", Path: "/tmp/c.ipa", Version: "3.0"},
	}
	for _, entry := range seed {
		if _, err := store.UpsertEntry(ctx, entry); err != nil {
			t.Fatalf("seed 失败: %v", err)
		}
	}

	byQuery, err := store.ListEntries(ctx, EntryFilter{Query: "BETA"})
	if err != nil {
		t.Fatalf("按关键词过滤失败: %v", err)
	}

	if len(byQuery) != 1 || byQuery[0].Name != "Beta" {
		t.Fatalf("关键词过滤结果不对: %+v", byQuery)
	}

	byPlatform, err := store.ListEntries(ctx, EntryFilter{Platform: "iphone"})
	if err != nil {
		t.Fatalf("按平台过滤失败: %v", err)
	}

	if len(byPlatform) != 2 {
		t.Fatalf("平台过滤记录数 = %d，期望 2", len(byPlatform))
	}

	if err := store.DeleteEntry(ctx, byQuery[0].ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	if _, err := store.GetEntry(ctx, byQuery[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后再取应得 ErrNotFound，实际: %v", err)
	}

	if err := store.DeleteEntry(ctx, byQuery[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除应得 ErrNotFound，实际: %v", err)
	}
}

func TestSaveTaskPreservesCreatedAt(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	created, err := store.SaveTask(ctx, Task{
		ID:         "task-1",
		Name:       "示例",
		State:      TaskPending,
		OutputPath: "/tmp/a.ipa",
	})
	if err != nil {
		t.Fatalf("首次保存任务失败: %v", err)
	}

	updated, err := store.SaveTask(ctx, Task{
		ID:         "task-1",
		Name:       "示例",
		State:      TaskDone,
		OutputPath: "/tmp/a.ipa",
		BytesDone:  100,
	})
	if err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}

	if updated.State != TaskDone || updated.BytesDone != 100 {
		t.Fatalf("任务状态未更新: %+v", updated)
	}

	if updated.CreatedAt != created.CreatedAt {
		t.Fatalf("created_at 被覆盖了：%q → %q", created.CreatedAt, updated.CreatedAt)
	}
}

func TestRecoverInterruptedTasks(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	states := map[string]TaskState{
		"running":  TaskRunning,
		"pending":  TaskPending,
		"done":     TaskDone,
		"failed":   TaskFailed,
		"canceled": TaskCanceled,
	}
	for id, state := range states {
		if _, err := store.SaveTask(ctx, Task{ID: id, State: state}); err != nil {
			t.Fatalf("seed %s 失败: %v", id, err)
		}
	}

	affected, err := store.RecoverInterruptedTasks(ctx)
	if err != nil {
		t.Fatalf("恢复中断任务失败: %v", err)
	}

	if affected != 2 {
		t.Fatalf("受影响行数 = %d，期望 2（只该动 pending/running）", affected)
	}

	for id, want := range map[string]TaskState{
		"running":  TaskFailed,
		"pending":  TaskFailed,
		"done":     TaskDone,
		"failed":   TaskFailed,
		"canceled": TaskCanceled,
	} {
		task, err := store.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("读回 %s 失败: %v", id, err)
		}

		if task.State != want {
			t.Fatalf("%s 状态 = %q，期望 %q", id, task.State, want)
		}
	}
}

// 用公开的已知向量（sha256("abc")）验证，而不是用同一条代码路径算出期望值 ——
// 否则实现里的偏移/编码错误会被自己的计算抵消掉，断言就是空转的。
func TestHashFileMatchesKnownVector(t *testing.T) {
	path := filepath.Join(t.TempDir(), "abc.bin")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}

	got, err := HashFile(context.Background(), path)
	if err != nil {
		t.Fatalf("HashFile 失败: %v", err)
	}

	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Fatalf("sha256 = %q，期望 %q", got, want)
	}
}

func TestHashFileHonorsCanceledContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(path, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := HashFile(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消的 ctx 应返回 context.Canceled，实际: %v", err)
	}
}

func TestSaveTaskRejectsEmptyID(t *testing.T) {
	store := openTestStore(t)

	if _, err := store.SaveTask(context.Background(), Task{}); err == nil {
		t.Fatal("空 id 的任务必须被拒绝")
	}
}

// 确保 Open 真的建了父目录（用户在设置里填了一个还不存在的路径是常见输入）。
func TestOpenCreatesParentDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "c")
	store, err := Open(filepath.Join(dir, "ipa.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}

	defer func() { _ = store.Close() }()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("父目录未创建: %v", err)
	}

	if !info.IsDir() {
		t.Fatal("父路径不是目录")
	}
}

// sql.ErrNoRows 必须被翻译成 ErrNotFound，而不是泄漏驱动错误：
// 上层要用 errors.Is(err, store.ErrNotFound) 区分「没有」和「读失败」。
func TestNotFoundTranslation(t *testing.T) {
	store := openTestStore(t)

	_, err := store.GetEntry(context.Background(), 42)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际: %v", err)
	}

	if errors.Is(err, sql.ErrNoRows) {
		t.Fatal("不应把 sql.ErrNoRows 直接暴露给上层（应用层不该依赖数据库驱动）")
	}
}
