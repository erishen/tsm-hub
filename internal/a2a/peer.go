// Package a2a 实现 tsm-hub 对 lume 集群的 A2A 接入：
//
//   - A2A 标准面：GET /.well-known/agent-card.json（公开发现）+ POST /a2a
//     （JSON-RPC：message/send / tasks/get / tasks/cancel），任务落盘可追溯；
//   - peer 签名面：GET /peer/health + POST /peer/invoke，HMAC-SHA256 认证，
//     协议与 lume-core/lib/peer.lume 字节级一致（同一套凭据与算法）。
//
// 设计原则与 lume-a2a 同源：
//   * 凭据缺失 = 整面关闭（fail-closed），绝不放行；
//   * 不认识的 capability 不猜、不兜底转发 → 回 rejected / input-required；
//   * 签名串、scope 拼接、密钥派生公式**一字不改** —— 任何一侧改动都会让
//     全舰队同时 401，表现为"密钥配错"，排查成本极高。
package a2a

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mode 是实际生效的签名凭据方案。优先级 derived > shared > off。
type Mode string

const (
	ModeOff      Mode = "off"      // 未配任何凭据，peer 面关闭
	ModeShared   Mode = "shared"   // PEER_SHARED 单密钥（无法防冒充）
	ModeDerived  Mode = "derived"  // PEER_ROOT + 按节点派生（防冒充）
)

// Peer 持有签名凭据与节点注册表。
// 注册表文件每次验证时热加载（mtime 变化即重读），与 lume 的
// .data/peers-identity.json 行为一致：撤销一个节点不用重启。
type Peer struct {
	Name     string // 本节点名（出站签名用；tsm-hub 作为被调方主要用于对账/展示）
	shared   string // PEER_SHARED
	root     string // PEER_ROOT
	registry string // registry 文件路径（{"peers":[...]}）
	skewSecs int    // 时间窗（默认 120）

	mu       sync.RWMutex
	known    map[string]bool
	regMTime time.Time
	regErr   error
}

type registryFile struct {
	Peers []string `json:"peers"`
}

// New 创建 Peer。skewSecs <= 0 时用默认 120。
func New(name, shared, root, registry string, skewSecs int) *Peer {
	if skewSecs <= 0 {
		skewSecs = 120
	}
	p := &Peer{
		Name:     name,
		shared:   shared,
		root:     root,
		registry: registry,
		skewSecs: skewSecs,
		known:    map[string]bool{},
	}
	p.reload()
	return p
}

// Mode 返回当前生效的凭据方案（不读注册表，只看配置）。
func (p *Peer) Mode() Mode {
	if p.root != "" {
		return ModeDerived
	}
	if p.shared != "" {
		return ModeShared
	}
	return ModeOff
}

// Enabled 报告 peer 签名面是否开启。
func (p *Peer) Enabled() bool { return p.Mode() != ModeOff }

// reload 在注册表文件 mtime 变化时重读。失败不阻塞验证（保留上次结果）。
func (p *Peer) reload() {
	if p.registry == "" {
		return
	}
	fi, err := os.Stat(p.registry)
	if err != nil {
		p.mu.Lock()
		p.regErr = err
		p.mu.Unlock()
		return
	}
	p.mu.RLock()
	same := p.regMTime.Equal(fi.ModTime())
	p.mu.RUnlock()
	if same {
		return
	}
	raw, err := os.ReadFile(p.registry)
	if err != nil {
		p.mu.Lock()
		p.regErr = err
		p.mu.Unlock()
		return
	}
	var rf registryFile
	if err := json.Unmarshal(raw, &rf); err != nil {
		p.mu.Lock()
		p.regErr = err
		p.mu.Unlock()
		return
	}
	known := make(map[string]bool, len(rf.Peers))
	for _, n := range rf.Peers {
		if n = strings.TrimSpace(n); n != "" {
			known[n] = true
		}
	}
	p.mu.Lock()
	p.known = known
	p.regMTime = fi.ModTime()
	p.regErr = nil
	p.mu.Unlock()
}

// Known 报告某节点是否在注册表里。
func (p *Peer) Known(node string) bool {
	p.reload()
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.known[node]
}

// registryWarning 返回注册表加载问题（仅报告，不影响验证 —— 与
// peer.lume 的 config_warnings 同思路：静默改行为比问题本身更难查）。
func (p *Peer) registryWarning() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.regErr != nil {
		return "peer registry unreadable: " + p.regErr.Error()
	}
	return ""
}

// deriveKey 与 identity.lume 的 key_for() 完全一致：
//
//	peer_key(node) = hmac_sha256(PEER_ROOT, "peer:" + node)
//
// Lume 的 hmac_sha256 输出 hex（builtins_crypt.c），这里用 hex 编码对齐。
func (p *Peer) deriveKey(node string) string {
	m := hmac.New(sha256.New, []byte(p.root))
	m.Write([]byte("peer:" + node))
	return hex.EncodeToString(m.Sum(nil))
}

// SigningString 是待签名字符串。两种模式共用同一串（与 peer.lume 一致）：
//
//	node + "\n" + ts + "\n" + nonce + "\n" + scope
//
// ⚠️ 不能改（包括加版本前缀）：它已经出现在所有在跑的节点之间。
func SigningString(node, ts, nonce, scope string) string {
	return node + "\n" + ts + "\n" + nonce + "\n" + scope
}

// scopeGET / scopePOST 与 peer.lume 的 verify_get / verify_post 同口径：
// GET 的 scope 是 "GET " + path（path 不含 query）；POST 把 body 拼进去。
func scopeGET(path string) string        { return "GET " + path }
func scopePOST(path, body string) string { return "POST " + path + "\n" + body }

// verifyResult 是入站校验结果。
type verifyResult struct {
	OK   bool
	Err  string // 失败原因（措辞与 peer.lume 对齐，便于两侧排障对照）
	Node string // 通过验证后，声称的调用方节点名
}

// Verify 校验一个带签名 query（node/ts/nonce/sig）的请求。
// method/scope 由调用方按 GET / POST 语义给出。
func (p *Peer) Verify(r *http.Request, method, scope string) verifyResult {
	mode := p.Mode()
	if mode == ModeOff {
		return verifyResult{Err: "peer disabled: PEER_SHARED / PEER_ROOT not set"}
	}
	q := r.URL.Query()
	node := q.Get("node")
	ts := q.Get("ts")
	nonce := q.Get("nonce")
	sig := q.Get("sig")
	if node == "" || ts == "" || nonce == "" || sig == "" {
		return verifyResult{Err: "missing auth params (node/ts/nonce/sig)"}
	}
	tsI, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || tsI <= 0 {
		return verifyResult{Err: "bad ts"}
	}
	drift := time.Now().Unix() - tsI
	if drift < 0 {
		drift = -drift
	}
	if int(drift) > p.skewSecs {
		return verifyResult{Err: fmt.Sprintf("ts outside window (%ds)", drift)}
	}

	// 待验密钥：derived 按**声称的 node** 派生（拿自己的 key 冒充别人必然对不上）；
	// shared 用共享密钥（无法防冒充，保留向后兼容）。
	var key string
	switch mode {
	case ModeDerived:
		if !p.Known(node) {
			return verifyResult{Err: "unknown peer '" + node + "' (not in registry)"}
		}
		key = p.deriveKey(node)
	default:
		key = p.shared
	}
	expect := signWith(key, node, ts, nonce, scope)
	if !constEq(expect, sig) {
		return verifyResult{Err: "bad signature for peer '" + node + "'"}
	}
	return verifyResult{OK: true, Node: node}
}

// signWith 与 peer.lume 的 sign() 一致：hex(hmac_sha256(key, signing_string))。
func signWith(key, node, ts, nonce, scope string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(SigningString(node, ts, nonce, scope)))
	return hex.EncodeToString(m.Sum(nil))
}

// constEq 常量时间比较，避免按字节提前退出泄漏签名前缀。
func constEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return hmac.Equal([]byte(a), []byte(b))
}

// signedQuery 出站工具：给 host+path(+body) 生成带签名的完整 URL，
// 供 tsm-hub 主动调 lume 节点（对账 / 反向探测）使用。
func (p *Peer) signedQuery(host, path, body string) string {
	mode := p.Mode()
	if mode == ModeOff {
		return ""
	}
	name := p.Name
	if name == "" {
		name = "tsm-hub"
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := strconv.FormatInt(time.Now().UnixNano(), 10)
	scope := scopeGET(path)
	if body != "" {
		scope = scopePOST(path, body)
	}
	key := p.shared
	if mode == ModeDerived {
		key = p.deriveKey(name)
	}
	sig := signWith(key, name, ts, nonce, scope)
	return "http://" + host + path +
		"?node=" + name + "&ts=" + ts + "&nonce=" + nonce + "&sig=" + sig
}

var errInvalidRegistryFormat = errors.New("peer registry must be JSON with a \"peers\" array")

// WriteRegistry 在首次部署时生成注册表文件（幂等：文件已存在则不覆盖）。
// 返回是否实际写入。
func WriteRegistry(path string, peers []string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	rf := registryFile{Peers: peers}
	raw, err := json.MarshalIndent(rf, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
