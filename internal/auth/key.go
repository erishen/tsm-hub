// Package auth 负责自制 Token Key 的签发与校验。
//
// 对外 Key 形如：sk-tr-7f3c...（48 位十六进制随机串）。
// 库里只保存 sha256 明文摘要，明文仅在创建接口返回一次。
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Prefix 是对外 Key 的固定前缀，便于 CI/密钥扫描工具识别与回收。
const Prefix = "sk-tr-"

// Generate 生成一个新的明文 Key 及其摘要信息。
func Generate() (plaintext string, hash string, display string, err error) {
	buf := make([]byte, 24) // 48 个 hex 字符
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("entropy: %w", err)
	}
	body := hex.EncodeToString(buf)
	plaintext = Prefix + body
	hash = Hash(plaintext)
	display = Prefix + body[:4] + "…" + body[len(body)-4:]
	return plaintext, hash, display, nil
}

// Hash 计算明文 Key 的 sha256 摘要。
func Hash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// Extract 从 Authorization: Bearer xxx 或 x-api-key 头里取出明文 Key。
func Extract(header string) string {
	v := strings.TrimSpace(header)
	if v == "" {
		return ""
	}
	// 注意先整体 trim，所以 "Bearer " 到这里已变成 "Bearer"，
	// 必须按前缀匹配再取余下部分，否则会把 scheme 本身当成 key。
	if len(v) >= 6 && strings.EqualFold(v[:6], "bearer") {
		return strings.TrimSpace(v[6:])
	}
	return v
}

// ConstantTimeEqual 常量时间比较，避免时序侧信道。
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ---------- Admin token ----------

// AdminToken 解析管理台口令：优先显式参数，其次环境变量 LLM_ROUTER_ADMIN_TOKEN。
func AdminToken(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return os.Getenv("LLM_ROUTER_ADMIN_TOKEN")
}

// ---------- 会话（管理台登录） ----------

// Session 是一个轻量内存会话，登录成功后下发 token，默认 12 小时有效。
type Session struct {
	mu     sync.Mutex
	ttl    time.Duration
	tokens map[string]time.Time
	secret string
}

// NewSession 创建会话管理器。
func NewSession(ttl time.Duration) *Session {
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	return &Session{ttl: ttl, tokens: map[string]time.Time{}}
}

// Issue 签发一个会话 token（内容即随机串 + admin token 摘要，避免泄露原口令）。
func (s *Session) Issue(adminToken string) string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	tok := hex.EncodeToString(buf)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[tok] = time.Now().Add(s.ttl)
	return tok
}

// Valid 判断会话 token 是否仍有效。
func (s *Session) Valid(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.tokens[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.tokens, tok)
		return false
	}
	return true
}

// Revoke 注销会话。
func (s *Session) Revoke(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, tok)
}

// JSON 是一个小工具，统一错误响应格式。
func JSON(status int, v any) (int, any) { return status, v }

// ErrResponse 是统一错误体。
type ErrResponse struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// MarshalErr 便于测试。
func MarshalErr(e ErrResponse) []byte {
	b, _ := json.Marshal(e)
	return b
}
