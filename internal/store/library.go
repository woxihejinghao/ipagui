package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound 表示记录不存在。
var ErrNotFound = errors.New("记录不存在")

// Entry 是「已经下载到本地的一个 IPA」。
type Entry struct {
	ID           int64  `json:"id"`
	AppID        int64  `json:"appId"`
	BundleID     string `json:"bundleId"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	Platform     string `json:"platform"`
	Path         string `json:"path"`
	SizeBytes    int64  `json:"sizeBytes"`
	SHA256       string `json:"sha256"`
	DownloadedAt string `json:"downloadedAt"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

// EntryFilter 是 ListEntries 的过滤条件（零值 = 不过滤）。
type EntryFilter struct {
	// Query 会在 name / bundle_id / version 上做子串匹配（大小写不敏感）。
	Query string
	// Platform 精确匹配。
	Platform string
}

const entryColumns = `id, app_id, bundle_id, name, version, platform, path,
	size_bytes, sha256, downloaded_at, created_at, updated_at`

func scanEntry(scanner interface{ Scan(...any) error }) (Entry, error) {
	var entry Entry

	err := scanner.Scan(
		&entry.ID, &entry.AppID, &entry.BundleID, &entry.Name, &entry.Version,
		&entry.Platform, &entry.Path, &entry.SizeBytes, &entry.SHA256,
		&entry.DownloadedAt, &entry.CreatedAt, &entry.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Entry{}, ErrNotFound
		}

		return Entry{}, fmt.Errorf("读取账本记录失败: %w", err)
	}

	return entry, nil
}

// UpsertEntry 按 path 插入或更新一条记录，返回落库后的完整记录。
//
// 为什么按 path 去重：同一个 IPA 被重下时，账本应该更新那一条（版本、大小、哈希），
// 而不是堆出一串指向同一个文件的记录。
func (s *Store) UpsertEntry(ctx context.Context, entry Entry) (Entry, error) {
	if entry.Path == "" {
		return Entry{}, errors.New("账本记录的 path 不能为空")
	}

	timestamp := now()
	if entry.DownloadedAt == "" {
		entry.DownloadedAt = timestamp
	}

	row := s.db.QueryRowContext(ctx, `
		INSERT INTO library_entries (
			app_id, bundle_id, name, version, platform, path,
			size_bytes, sha256, downloaded_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			app_id        = excluded.app_id,
			bundle_id     = excluded.bundle_id,
			name          = excluded.name,
			version       = excluded.version,
			platform      = excluded.platform,
			size_bytes    = excluded.size_bytes,
			sha256        = excluded.sha256,
			downloaded_at = excluded.downloaded_at,
			updated_at    = excluded.updated_at
		RETURNING `+entryColumns,
		entry.AppID, entry.BundleID, entry.Name, entry.Version, entry.Platform, entry.Path,
		entry.SizeBytes, entry.SHA256, entry.DownloadedAt, timestamp, timestamp,
	)

	return scanEntry(row)
}

// GetEntry 按 id 取一条记录。
func (s *Store) GetEntry(ctx context.Context, id int64) (Entry, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+entryColumns+` FROM library_entries WHERE id = ?`, id)

	return scanEntry(row)
}

// ListEntries 按过滤条件列出记录（最近下载的排前面）。
func (s *Store) ListEntries(ctx context.Context, filter EntryFilter) ([]Entry, error) {
	query := `SELECT ` + entryColumns + ` FROM library_entries`
	conditions := []string{}
	args := []any{}

	if filter.Query != "" {
		conditions = append(conditions, `(
			LOWER(name)      LIKE ? OR
			LOWER(bundle_id) LIKE ? OR
			LOWER(version)   LIKE ?
		)`)
		pattern := "%" + strings.ToLower(filter.Query) + "%"
		args = append(args, pattern, pattern, pattern)
	}

	if filter.Platform != "" {
		conditions = append(conditions, "platform = ?")
		args = append(args, filter.Platform)
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	query += " ORDER BY downloaded_at DESC, id DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询账本失败: %w", err)
	}

	defer func() { _ = rows.Close() }()

	entries := []Entry{}
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}

		entries = append(entries, entry)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历账本失败: %w", err)
	}

	return entries, nil
}

// DeleteEntry 删除一条记录（只删账本，不动磁盘上的文件）。
func (s *Store) DeleteEntry(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM library_entries WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除账本记录失败: %w", err)
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
