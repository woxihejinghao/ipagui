-- IPA-GUI 本地账本 schema v1
--
-- 迁移策略：用 PRAGMA user_version 记录版本号，Migration 逐个递增执行。
-- 不要把这一段拆成多个文件、也不要改写已发布的 DDL —— 用户的库里已经有数据。

CREATE TABLE IF NOT EXISTS library_entries (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id        INTEGER NOT NULL DEFAULT 0,
    bundle_id     TEXT    NOT NULL DEFAULT '',
    name          TEXT    NOT NULL DEFAULT '',
    version       TEXT    NOT NULL DEFAULT '',
    platform      TEXT    NOT NULL DEFAULT '',
    path          TEXT    NOT NULL,
    size_bytes    INTEGER NOT NULL DEFAULT 0,
    sha256        TEXT    NOT NULL DEFAULT '',
    downloaded_at TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL DEFAULT '',
    updated_at    TEXT    NOT NULL DEFAULT ''
);

-- 同一个文件路径只允许一条记录：重复下载同一个应用时走 upsert 而不是堆记录。
CREATE UNIQUE INDEX IF NOT EXISTS idx_library_path ON library_entries (path);
CREATE INDEX IF NOT EXISTS idx_library_app ON library_entries (app_id, bundle_id);

CREATE TABLE IF NOT EXISTS download_tasks (
    id            TEXT    PRIMARY KEY,
    app_id        INTEGER NOT NULL DEFAULT 0,
    bundle_id     TEXT    NOT NULL DEFAULT '',
    name          TEXT    NOT NULL DEFAULT '',
    platform      TEXT    NOT NULL DEFAULT '',
    version_id    TEXT    NOT NULL DEFAULT '',
    output_path   TEXT    NOT NULL DEFAULT '',
    state         TEXT    NOT NULL DEFAULT 'pending',
    bytes_done    INTEGER NOT NULL DEFAULT 0,
    bytes_total   INTEGER NOT NULL DEFAULT 0,
    error_code    TEXT    NOT NULL DEFAULT '',
    error_message TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL DEFAULT '',
    updated_at    TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_tasks_state ON download_tasks (state, created_at);
