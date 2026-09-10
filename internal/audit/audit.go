// Package audit 实现管理操作审计日志：记录所有写操作（创建/修改/删除）的
// 时间、操作类型、对象类型、对象 ID、详情、操作者 IP，用于合规追溯。
//
// 存储：SQLite（data/audit.db），纯 Go 实现（modernc.org/sqlite），无 cgo。
// 保留策略：默认保留 90 天，可通过 settings.audit_retention_days 配置；
// 启动时和每次写入时清理过期记录。
package audit

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Log 是一条审计日志记录。
type Log struct {
	ID        int64     `json:"id"`
	TS        time.Time `json:"ts"`
	Action    string    `json:"action"`     // create / update / delete / toggle / clear / login
	Object    string    `json:"object"`     // provider / route / key / mcp / settings / usage / memory / fastpath
	ObjectID  string    `json:"object_id"`  // 对象 ID（provider id / route model / key id 等）
	Detail    string    `json:"detail"`     // 操作详情（JSON 或描述，不含敏感信息）
	Operator  string    `json:"operator"`   // 操作者标识（admin token 前缀或 "admin"）
	ClientIP  string    `json:"client_ip"`  // 客户端 IP
	UserAgent string    `json:"user_agent"` // 客户端 User-Agent（截断）
}

// Store 是审计日志的 SQLite 持久化存储。
type Store struct {
	db            *sql.DB
	retentionDays int
}

// Open 打开/创建审计日志库并建表。retentionDays <= 0 时使用默认 90 天。
func Open(dataDir string, retentionDays int) (*Store, error) {
	path := filepath.Join(dataDir, "audit.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create audit dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open audit db: %w", err)
	}
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA synchronous=NORMAL")
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS audit_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts TEXT NOT NULL,
		action TEXT NOT NULL,
		object TEXT NOT NULL,
		object_id TEXT NOT NULL DEFAULT '',
		detail TEXT NOT NULL DEFAULT '',
		operator TEXT NOT NULL DEFAULT '',
		client_ip TEXT NOT NULL DEFAULT '',
		user_agent TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create audit table: %w", err)
	}
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_log(ts)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_object ON audit_log(object, object_id)`)

	if retentionDays <= 0 {
		retentionDays = 90
	}
	s := &Store{db: db, retentionDays: retentionDays}
	s.purge()
	return s, nil
}

func (s *Store) SetRetentionDays(days int) {
	if days > 0 {
		s.retentionDays = days
	}
}

// Record 记录一条审计日志。
func (s *Store) Record(action, object, objectID string, detail any, operator, clientIP, userAgent string) {
	if s == nil || s.db == nil {
		return
	}
	detailStr := ""
	if detail != nil {
		switch v := detail.(type) {
		case string:
			detailStr = v
		default:
			if b, err := json.Marshal(detail); err == nil {
				detailStr = string(b)
			}
		}
	}
	if len(detailStr) > 2000 {
		detailStr = detailStr[:2000] + "…"
	}
	if len(userAgent) > 200 {
		userAgent = userAgent[:200]
	}
	detailStr = maskSensitive(detailStr)

	ts := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`INSERT INTO audit_log (ts, action, object, object_id, detail, operator, client_ip, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, action, object, objectID, detailStr, operator, clientIP, userAgent)
	if err != nil {
		log.Printf("[audit] record failed: %v", err)
	}
	go s.purge()
}

// Query 查询审计日志，按时间倒序。
func (s *Store) Query(object, action, objectID string, limit, offset int) ([]Log, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("audit store not initialized")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	where := []string{"1=1"}
	args := []any{}
	if object != "" {
		where = append(where, "object = ?")
		args = append(args, object)
	}
	if action != "" {
		where = append(where, "action = ?")
		args = append(args, action)
	}
	if objectID != "" {
		where = append(where, "object_id LIKE ?")
		args = append(args, "%"+objectID+"%")
	}
	whereStr := strings.Join(where, " AND ")

	var total int
	err := s.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM audit_log WHERE %s", whereStr), args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.db.Query(fmt.Sprintf(`SELECT id, ts, action, object, object_id, detail, operator, client_ip, user_agent
		FROM audit_log WHERE %s ORDER BY id DESC LIMIT ? OFFSET ?`, whereStr), append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	logs := []Log{}
	for rows.Next() {
		var l Log
		var tsStr string
		if err := rows.Scan(&l.ID, &tsStr, &l.Action, &l.Object, &l.ObjectID, &l.Detail, &l.Operator, &l.ClientIP, &l.UserAgent); err != nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
			l.TS = t
		}
		logs = append(logs, l)
	}
	return logs, total, nil
}

func (s *Store) purge() {
	if s == nil || s.db == nil || s.retentionDays <= 0 {
		return
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -s.retentionDays).Format(time.RFC3339Nano)
	res, err := s.db.Exec("DELETE FROM audit_log WHERE ts < ?", cutoff)
	if err != nil {
		log.Printf("[audit] purge failed: %v", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("[audit] purged %d expired records (retention: %d days)", n, s.retentionDays)
	}
}

func (s *Store) Close() error {
	if s != nil && s.db != nil {
		return s.db.Close()
	}
	return nil
}

// maskSensitive 脱敏字符串中的敏感信息。
func maskSensitive(s string) string {
	result := s
	fields := []string{"api_key", "apiKey", "token", "password", "secret"}
	for _, field := range fields {
		prefix := `"` + field + `":"`
		for {
			idx := strings.Index(result, prefix)
			if idx < 0 {
				break
			}
			start := idx + len(prefix)
			end := strings.Index(result[start:], `"`)
			if end < 0 {
				break
			}
			result = result[:start] + "***" + result[start+end:]
		}
	}
	// 脱敏 sk- 开头的 key
	parts := strings.Split(result, "sk-")
	for i := 1; i < len(parts); i++ {
		j := 0
		for j < len(parts[i]) && isAlnum(parts[i][j]) {
			j++
		}
		if j >= 20 {
			parts[i] = "***" + parts[i][j:]
		}
	}
	return strings.Join(parts, "sk-")
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
