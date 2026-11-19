package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// TaskState 是下载任务的状态。
type TaskState string

const (
	TaskPending  TaskState = "pending"
	TaskRunning  TaskState = "running"
	TaskDone     TaskState = "done"
	TaskFailed   TaskState = "failed"
	TaskCanceled TaskState = "canceled"
)

// IsTerminal 报告状态是否已经结束（不会再变化）。
func (s TaskState) IsTerminal() bool {
	switch s {
	case TaskDone, TaskFailed, TaskCanceled:
		return true
	default:
		return false
	}
}

// Task 是一条下载任务记录。
//
// 它是「队列」的持久化形态：应用重启后，非终态的任务会被标成可重试，
// 而不是凭空消失 —— 这正是用户能对着一份清单点「重试」的前提。
type Task struct {
	ID           string    `json:"id"`
	AppID        int64     `json:"appId"`
	BundleID     string    `json:"bundleId"`
	Name         string    `json:"name"`
	Platform     string    `json:"platform"`
	VersionID    string    `json:"versionId"`
	OutputPath   string    `json:"outputPath"`
	State        TaskState `json:"state"`
	BytesDone    int64     `json:"bytesDone"`
	BytesTotal   int64     `json:"bytesTotal"`
	ErrorCode    string    `json:"errorCode"`
	ErrorMessage string    `json:"errorMessage"`
	CreatedAt    string    `json:"createdAt"`
	UpdatedAt    string    `json:"updatedAt"`
}

const taskColumns = `id, app_id, bundle_id, name, platform, version_id, output_path,
	state, bytes_done, bytes_total, error_code, error_message, created_at, updated_at`

func scanTask(scanner interface{ Scan(...any) error }) (Task, error) {
	var task Task

	err := scanner.Scan(
		&task.ID, &task.AppID, &task.BundleID, &task.Name, &task.Platform,
		&task.VersionID, &task.OutputPath, &task.State, &task.BytesDone,
		&task.BytesTotal, &task.ErrorCode, &task.ErrorMessage,
		&task.CreatedAt, &task.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, ErrNotFound
		}

		return Task{}, fmt.Errorf("读取任务失败: %w", err)
	}

	return task, nil
}

// SaveTask 按 id 插入或更新任务。
//
// created_at 只在首次插入时写入：ON CONFLICT 分支刻意不更新它，
// 这样「任务是什么时候建的」不会被后续状态刷新覆盖。
func (s *Store) SaveTask(ctx context.Context, task Task) (Task, error) {
	if task.ID == "" {
		return Task{}, errors.New("任务 id 不能为空")
	}

	if task.State == "" {
		task.State = TaskPending
	}

	row := s.db.QueryRowContext(ctx, `
		INSERT INTO download_tasks (
			id, app_id, bundle_id, name, platform, version_id, output_path,
			state, bytes_done, bytes_total, error_code, error_message,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			app_id        = excluded.app_id,
			bundle_id     = excluded.bundle_id,
			name          = excluded.name,
			platform      = excluded.platform,
			version_id    = excluded.version_id,
			output_path   = excluded.output_path,
			state         = excluded.state,
			bytes_done    = excluded.bytes_done,
			bytes_total   = excluded.bytes_total,
			error_code    = excluded.error_code,
			error_message = excluded.error_message,
			updated_at    = excluded.updated_at
		RETURNING `+taskColumns,
		task.ID, task.AppID, task.BundleID, task.Name, task.Platform,
		task.VersionID, task.OutputPath, task.State, task.BytesDone,
		task.BytesTotal, task.ErrorCode, task.ErrorMessage, now(), now(),
	)

	return scanTask(row)
}

// GetTask 按 id 取任务。
func (s *Store) GetTask(ctx context.Context, id string) (Task, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM download_tasks WHERE id = ?`, id)

	return scanTask(row)
}

// ListTasks 列出任务（新建的排前面）。
func (s *Store) ListTasks(ctx context.Context) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskColumns+` FROM download_tasks ORDER BY created_at DESC, rowid DESC`)
	if err != nil {
		return nil, fmt.Errorf("查询任务失败: %w", err)
	}

	defer func() { _ = rows.Close() }()

	tasks := []Task{}
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}

		tasks = append(tasks, task)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历任务失败: %w", err)
	}

	return tasks, nil
}

// DeleteTask 删除一条任务记录。
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM download_tasks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除任务失败: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("确认删除结果失败: %w", err)
	}

	if affected == 0 {
		return ErrNotFound
	}

	return nil
}

// RecoverInterruptedTasks 把上次退出时卡在 pending/running 的任务标成 failed。
//
// 为什么不是自动继续下载：下载进程随进程结束而中止，账本里的 bytes_done 只是快照。
// 标成可重试的失败态，把「要不要接着下」交给用户，比静默恢复更可预期。
func (s *Store) RecoverInterruptedTasks(ctx context.Context) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE download_tasks
		SET state = ?, error_code = ?, error_message = ?, updated_at = ?
		WHERE state IN (?, ?)`,
		TaskFailed, "interrupted", "应用退出导致下载中断，可重试", now(),
		TaskPending, TaskRunning,
	)
	if err != nil {
		return 0, fmt.Errorf("恢复中断任务失败: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("确认恢复结果失败: %w", err)
	}

	return int(affected), nil
}
