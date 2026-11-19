// Package store 是 IPA GUI 的本地账本（SQLite）。
//
// 它只负责持久化：已下载的 IPA 清单（library_entries）与下载任务（download_tasks）。
// 刻意不依赖 internal/ipa —— 账本是数据层，不该被上游 ipatool 的 API 形状绑住；
// 需要苹果侧数据时由调用方把它转换成本包的 Entry/Task。
//
// 驱动用 modernc.org/sqlite（纯 Go）：Windows 交叉编译时不需要 cgo，
// 这与 ADR-1 选 Wails+Go 库、ADR-4 选纯 Go SQLite 的理由是同一条。
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // 注册 database/sql 驱动名 "sqlite"
)

//go:embed schema.sql
var schemaFS embed.FS

// schemaVersion 是最新 schema 版本，必须与 schema.sql 的 DDL 同步递增。
const schemaVersion = 1

// Store 是账本句柄。并发安全由 database/sql 的连接池保证。
type Store struct {
	db   *sql.DB
	path string
}

// Open 打开（必要时创建）账本。
//
// WAL 模式 + busy_timeout 是为了容忍「同一台机器上第二个实例」的情况：
// 我们不假设自己是唯一进程，宁可让写操作排队也不要立刻报 database is locked。
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("账本路径不能为空")
	}

	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析账本路径失败: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return nil, fmt.Errorf("创建账本目录失败: %w", err)
	}

	dsn := "file:" + absolute +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开账本失败: %w", err)
	}

	// SQLite 的写是串行的：把连接数压到 1，让写操作在 Go 侧排队而不是在
	// 文件锁上互相打转（读操作依然走同一个连接，Phase 1 的并发量完全够用）。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	store := &Store{db: db, path: absolute}

	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()

		return nil, err
	}

	return store, nil
}

// Close 关闭账本。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}

	return s.db.Close()
}

// Path 返回账本的绝对路径。
func (s *Store) Path() string { return s.path }

// SchemaVersion 返回当前库里的 schema 版本。
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	return s.userVersion(ctx)
}

func (s *Store) userVersion(ctx context.Context) (int, error) {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("读取 schema 版本失败: %w", err)
	}

	return version, nil
}

// migrate 把库升到 schemaVersion。
//
// 幂等：重复调用不会重复建表（DDL 全是 IF NOT EXISTS），也不会重复灌数据。
func (s *Store) migrate(ctx context.Context) error {
	current, err := s.userVersion(ctx)
	if err != nil {
		return err
	}

	if current > schemaVersion {
		return fmt.Errorf("账本 schema 版本 %d 高于本程序支持的 %d，请升级 IPA GUI", current, schemaVersion)
	}

	if current == schemaVersion {
		return nil
	}

	ddl, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("读取内置 schema 失败: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始迁移事务失败: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, string(ddl)); err != nil {
		return fmt.Errorf("执行 schema 迁移失败: %w", err)
	}

	// PRAGMA 不接受占位符，schemaVersion 是编译期常量，直接格式化安全。
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("写入 schema 版本失败: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交迁移失败: %w", err)
	}

	return nil
}

// now 返回统一的时间戳格式（本地时区 RFC3339），便于人类直接读库排查。
func now() string { return time.Now().Format(time.RFC3339) }

// HashFile 计算文件的 sha256（流式，不把整个 IPA 读进内存）。
//
// 大文件（IPA 动辄几百 MB）必须可取消：ctx 取消时立刻返回。
func HashFile(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开文件失败: %w", err)
	}

	defer func() { _ = file.Close() }()

	hasher := sha256.New()

	buf := make([]byte, 1<<20) // 1 MiB
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		n, readErr := file.Read(buf)
		if n > 0 {
			if _, err := hasher.Write(buf[:n]); err != nil {
				return "", fmt.Errorf("计算哈希失败: %w", err)
			}
		}

		if errors.Is(readErr, io.EOF) {
			break
		}

		if readErr != nil {
			return "", fmt.Errorf("读取文件失败: %w", readErr)
		}
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}
