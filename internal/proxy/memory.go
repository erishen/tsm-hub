package proxy

// 会话记忆持久化：remember/recall 从进程内存迁移到 SQLite（data/memory.db），
// 网关重启后记忆保留；按 keyID 隔离命名空间（ns = "mem:<keyID>:<key>"）。
//
// SQLite 采用 modernc.org/sqlite（纯 Go 实现，无 cgo，跨平台编译稳定）。

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// memoryStore 是 remember/recall 的 SQLite 持久化存储。
type memoryStore struct {
	db *sql.DB
}

// openMemory 打开/创建记忆库并建表。
func openMemory(path string) (*memoryStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create memory dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open memory db: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS memory (
		ns TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("init memory db: %w", err)
	}
	return &memoryStore{db: db}, nil
}

// Set 写入/覆盖一条记忆。
func (m *memoryStore) Set(ns, value string) error {
	_, err := m.db.Exec(
		`INSERT INTO memory(ns, value, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(ns) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		ns, value, time.Now().Format(time.RFC3339))
	return err
}

// Get 取回一条记忆。
func (m *memoryStore) Get(ns string) (string, bool) {
	var v string
	err := m.db.QueryRow(`SELECT value FROM memory WHERE ns = ?`, ns).Scan(&v)
	if err != nil {
		return "", false
	}
	return v, true
}

// List 返回全部记忆条目（按 ns 排序）。
func (m *memoryStore) List() ([]map[string]string, error) {
	rows, err := m.db.Query(`SELECT ns, value FROM memory ORDER BY ns`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]string, 0)
	for rows.Next() {
		var ns, v string
		if err := rows.Scan(&ns, &v); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{"ns": ns, "value": v})
	}
	return out, rows.Err()
}

// Clear 清空全部记忆。
func (m *memoryStore) Clear() error {
	_, err := m.db.Exec(`DELETE FROM memory`)
	return err
}

// Close 关闭数据库。
func (m *memoryStore) Close() error {
	if m == nil || m.db == nil {
		return nil
	}
	return m.db.Close()
}
