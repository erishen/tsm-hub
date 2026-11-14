// a2a 包测试：
//  1. 静态 HMAC 向量 —— 与 Lume 的 hmac_sha256（hex 输出）+ identity.lume 的
//     key_for() 派生公式做字节级对齐锚点（向量独立用 Python 计算）；
//  2. peer 验签行为 —— 错误签名 / 未登记节点 / 时间窗外的**必须被拒**
//     （lume-a2a 的 fleet-verify 第 6 条原话：只测"正确签名能过"等于什么都没测）；
//  3. A2A JSON-RPC 行为 —— 未知能力不猜（rejected / input-required）、
//     多轮续发参数合并、tasks/get 与 cancel 的错误码；
//  4. peer/invoke 白名单 —— 未知名直接拒。
package a2a

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestStaticVectors 纯公式对齐：派生密钥与签名拼接的字节级语义。
// 与 Lume 的 hmac_sha256（hex）+ identity.lume key_for() 一致。
//
//	root = "ROOT-SECRET-2026"
//	derivedKey(node) = hex(hmac_sha256(root, "peer:" + node))
//	derivedKey("tsm-hub") = 2b4bdf78ba7c03833ef6d40358d359001743d399365c72f173642dd7af55abdb
//	derivedKey("crm")     = 664b6d34f3bdbed4b7b45a3c3df8690d169856bb5b2f9506b583b98ecde2c3d4

const (
	staticRoot   = "ROOT-SECRET-2026"
	staticShared = "SHARED-KEY"
	fxTs         = "1762200000" // 固定 ts，只用于拼公式向量（不参与时间窗校验）
	fxNonce      = "abc123"
	invBody      = `{"tool":"tsm.overview"}`

	keyTSM  = "2b4bdf78ba7c03833ef6d40358d359001743d399365c72f173642dd7af55abdb"
	keyCRM  = "664b6d34f3bdbed4b7b45a3c3df8690d169856bb5b2f9506b583b98ecde2c3d4"
	sigGET  = "aeda436ed3aff5b32e5ea3388257f221a84a9ea3e450c57e0720824d47ed4ab1" // tsm-hub, GET /peer/health
	sigPOST = "9bc6a86eaaec9cb0201eb9efcdc517b864322319bdedd1d7bbf18b782b2f4c3f" // tsm-hub, POST /peer/invoke + invBody
	sigSHRD = "80962b75e4e90d600d7289cd43aaa2409edd9b0e94bbdc38e8759bbb156493ec" // shared key, tsm-hub, GET /peer/health
	sigCRM  = "a2ddd44f082c572f84f0bba78ae13f22756a4da343ec4e63a9d89697aa54165e" // crm derived key, crm, GET /peer/health
)

func TestStaticVectors(t *testing.T) {
	// 派生密钥公式与 identity.lume key_for() 一致。
	peer := New("tsm-hub", "", staticRoot, "", 0)
	if got := peer.deriveKey("tsm-hub"); got != keyTSM {
		t.Fatalf("deriveKey(tsm-hub) = %s, want %s", got, keyTSM)
	}
	if got := peer.deriveKey("crm"); got != keyCRM {
		t.Fatalf("deriveKey(crm) = %s, want %s", got, keyCRM)
	}

	// 各模式/节点的 GET / POST 签名向量（固定 ts，只锚公式，不锚时钟）。
	if got := signWith(keyTSM, "tsm-hub", fxTs, fxNonce, scopeGET("/peer/health")); got != sigGET {
		t.Fatalf("GET sig = %s, want %s", got, sigGET)
	}
	if got := signWith(keyTSM, "tsm-hub", fxTs, fxNonce, scopePOST("/peer/invoke", invBody)); got != sigPOST {
		t.Fatalf("POST sig = %s, want %s", got, sigPOST)
	}
	if got := signWith(staticShared, "tsm-hub", fxTs, fxNonce, scopeGET("/peer/health")); got != sigSHRD {
		t.Fatalf("shared sig = %s, want %s", got, sigSHRD)
	}
	if got := signWith(keyCRM, "crm", fxTs, fxNonce, scopeGET("/peer/health")); got != sigCRM {
		t.Fatalf("crm sig = %s, want %s", got, sigCRM)
	}
}

// writeRegistry 写一份 Lume 形状的注册表 {"peers":[...]}。
func writeRegistry(t *testing.T, dir string, peers ...string) string {
	t.Helper()
	p := filepath.Join(dir, "peers-identity.json")
	raw, _ := json.Marshal(map[string]any{"peers": peers})
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// signedRequest 构造带签名 query 的请求（模拟 Lume 的 signed_url() 出站形态：
// Lume 读不到自定义请求头，认证参数只能走 query）。
func signedRequest(method, url, node, ts, nonce, sig, body string) *http.Request {
	var r *http.Request
	var err error
	if body != "" {
		r, err = http.NewRequest(method, url, strings.NewReader(body))
	} else {
		r, err = http.NewRequest(method, url, nil)
	}
	if err != nil {
		panic(err)
	}
	q := r.URL.Query()
	q.Set("node", node)
	q.Set("ts", ts)
	q.Set("nonce", nonce)
	if sig != "" {
		q.Set("sig", sig)
	}
	r.URL.RawQuery = q.Encode()
	return r
}

func TestPeerVerifyDerived(t *testing.T) {
	dir := t.TempDir()
	reg := writeRegistry(t, dir, "crm", "scanner")
	peer := New("tsm-hub", "", staticRoot, reg, 120)
	ts, nonce := dynTs(), "testnonce"

	// 正确签名 → 过。
	good := signWith(peer.deriveKey("crm"), "crm", ts, nonce, scopeGET("/peer/health"))
	r := signedRequest("GET", "http://x/peer/health", "crm", ts, nonce, good, "")
	v := peer.Verify(r, "GET", scopeGET("/peer/health"))
	if !v.OK {
		t.Fatalf("expected ok, got: %s", v.Err)
	}

	// 未登记节点 → 拒绝（签名格式合法也要先过注册表 —— 这是身份可信层，不是 bug）。
	ghost := signWith(peer.deriveKey("ghost"), "ghost", ts, nonce, scopeGET("/peer/health"))
	r = signedRequest("GET", "http://x/peer/health", "ghost", ts, nonce, ghost, "")
	v = peer.Verify(r, "GET", scopeGET("/peer/health"))
	if v.OK || !strings.Contains(v.Err, "unknown peer") {
		t.Fatalf("ghost node should be rejected, got: %+v", v)
	}

	// 错误签名 → 拒绝。
	r = signedRequest("GET", "http://x/peer/health", "crm", ts, nonce, strings.Repeat("0", 64), "")
	v = peer.Verify(r, "GET", scopeGET("/peer/health"))
	if v.OK || !strings.Contains(v.Err, "bad signature") {
		t.Fatalf("wrong sig must be rejected, got: %+v", v)
	}

	// 冒充：用 crm 的密钥给 scanner 签 → 必拒（derived 模式存在的意义：
	// 拿自己的 key 冒充别人，签名必然对不上）。
	forged := signWith(peer.deriveKey("crm"), "scanner", ts, nonce, scopeGET("/peer/health"))
	r = signedRequest("GET", "http://x/peer/health", "scanner", ts, nonce, forged, "")
	v = peer.Verify(r, "GET", scopeGET("/peer/health"))
	if v.OK || !strings.Contains(v.Err, "bad signature") {
		t.Fatalf("impersonation must fail, got: %+v", v)
	}

	// 时间窗外 → 拒（旧 ts 即使签名正确也不放行 —— 防重放是时间窗级别）。
	oldTs := strconv.FormatInt(time.Now().Unix()-3600, 10)
	oldSig := signWith(peer.deriveKey("crm"), "crm", oldTs, nonce, scopeGET("/peer/health"))
	r = signedRequest("GET", "http://x/peer/health", "crm", oldTs, nonce, oldSig, "")
	v = peer.Verify(r, "GET", scopeGET("/peer/health"))
	if v.OK || !strings.Contains(v.Err, "ts outside window") {
		t.Fatalf("stale ts must be rejected, got: %+v", v)
	}
}

func TestPeerVerifySharedAndDisabled(t *testing.T) {
	peer := New("tsm-hub", staticShared, "", "", 120)
	if peer.Mode() != ModeShared {
		t.Fatalf("mode = %s, want shared", peer.Mode())
	}
	ts, nonce := dynTs(), "testnonce"
	good := signWith(staticShared, "tsm-hub", ts, nonce, scopeGET("/peer/health"))
	r := signedRequest("GET", "http://x/peer/health", "tsm-hub", ts, nonce, good, "")
	v := peer.Verify(r, "GET", scopeGET("/peer/health"))
	if !v.OK {
		t.Fatalf("shared mode: expected ok, got: %s", v.Err)
	}

	// 无凭据 → fail-closed（整面关闭，不是放行）。
	off := New("tsm-hub", "", "", "", 0)
	v = off.Verify(r, "GET", scopeGET("/peer/health"))
	if v.OK || !strings.Contains(v.Err, "peer disabled") {
		t.Fatalf("no-credential peer must be fail-closed, got: %+v", v)
	}

	// 缺参数 → 拒。
	r2 := signedRequest("GET", "http://x/peer/health", "", "", "", "", "")
	v = peer.Verify(r2, "GET", scopeGET("/peer/health"))
	if v.OK || !strings.Contains(v.Err, "missing auth params") {
		t.Fatalf("missing params must be rejected, got: %+v", v)
	}
}

// TestMergedArgs 覆盖多轮续发的参数合并语义（空串不覆盖旧值）。
func TestMergedArgs(t *testing.T) {
	prev := map[string]any{"name": "alva", "industry": "IT"}
	// 本轮只补一个字段，其余传空串（客户端续发习惯：只填缺的那几个）。
	merged := mergedArgs(prev, map[string]any{"industry": "", "count": 3.0})
	if merged["name"] != "alva" {
		t.Fatalf("empty string must not overwrite old value, got %v", merged["name"])
	}
	if merged["industry"] != "IT" {
		t.Fatalf("empty string must not overwrite, got %v", merged["industry"])
	}
	if merged["count"] != 3.0 {
		t.Fatalf("new value should land, got %v", merged["count"])
	}
	// 非空串覆盖。
	merged2 := mergedArgs(prev, map[string]any{"industry": "Finance"})
	if merged2["industry"] != "Finance" {
		t.Fatalf("non-empty should overwrite, got %v", merged2["industry"])
	}
}

// TestA2ARPC 走完整 HTTP 链路（/a2a JSON-RPC），对照 lume-a2a 的验收语义。
func TestA2ARPC(t *testing.T) {
	dir := t.TempDir()
	ts := NewTaskStore(dir, "tsm-hub")
	caps := NewCapabilities()
	caps.Register(Capability{Name: "demo.greet"}, func(args map[string]any) DispatchResult {
		if argStr(args, "name") == "" {
			return DispatchResult{State: StateInputRequired, Note: "请指定 name"}
		}
		return completedArtifact(map[string]any{"hello": argStr(args, "name")})
	})
	srv := &Server{Node: "tsm-hub", Caps: caps, Tasks: ts}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent-card.json", srv.HandleAgentCard)
	mux.HandleFunc("/a2a", srv.HandleA2A)
	h := httptest.NewServer(mux)
	defer h.Close()

	// 1) Agent Card 公开可发现，skills 里能看到登记过的能力。
	resp, err := http.Get(h.URL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatal(err)
	}
	cardRaw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var card map[string]any
	if err := json.Unmarshal(cardRaw, &card); err != nil {
		t.Fatalf("card not json: %v", err)
	}
	skills := card["skills"].([]any)
	if len(skills) != 1 {
		t.Fatalf("skills = %v", skills)
	}
	if !strings.Contains(card["url"].(string), "/a2a") {
		t.Fatalf("card url = %v", card["url"])
	}

	// 2) 不指定 capability → input-required（不猜，让客户端明确指定），
	//    且 status.message 里要说清缺什么（追问要带候选清单）。
	body := `{
		"jsonrpc":"2.0","id":1,"method":"message/send",
		"params":{"message":{"role":"user","parts":[{"kind":"text","text":"hi"}]}}}`
	resp, err = postJSON(h.URL+"/a2a", body)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var rpc struct {
		Result struct {
			Status struct {
				State   string `json:"state"`
				Message *struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"message"`
			} `json:"status"`
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &rpc); err != nil {
		t.Fatalf("rpc: %v\n%s", err, raw)
	}
	if rpc.Result.Status.State != StateInputRequired {
		t.Fatalf("state = %s, want input-required", rpc.Result.Status.State)
	}
	if rpc.Result.Status.Message == nil || !strings.Contains(rpc.Result.Status.Message.Parts[0].Text, "demo.greet") {
		t.Fatalf("input-required must name the missing capability: %+v", rpc.Result.Status.Message)
	}
	taskID := rpc.Result.ID

	// 3) 带 taskId 续发，补齐 name → completed。
	body = `{
		"jsonrpc":"2.0","id":2,"method":"message/send",
		"params":{"id":"` + taskID + `",
		  "message":{"role":"user","parts":[{"kind":"text","text":"retry"}],
		             "taskId":"` + taskID + `"},
		  "metadata":{"capability":"demo.greet","args":{"name":"alva"}}}}`
	resp, err = postJSON(h.URL+"/a2a", body)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	var rpc2 struct {
		Result struct {
			Status struct {
				State string `json:"state"`
			} `json:"status"`
			Artifacts []struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"artifacts"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &rpc2); err != nil {
		t.Fatalf("rpc2: %v\n%s", err, raw)
	}
	if rpc2.Result.Status.State != StateCompleted {
		t.Fatalf("resume state = %s, want completed\n%s", rpc2.Result.Status.State, raw)
	}
	if len(rpc2.Result.Artifacts) == 0 || !strings.Contains(rpc2.Result.Artifacts[0].Parts[0].Text, `"hello":"alva"`) {
		t.Fatalf("artifact = %+v", rpc2.Result.Artifacts)
	}

	// 4) tasks/get → 取回完整任务（含 capability 与最终状态）。
	body = `{"jsonrpc":"2.0","id":3,"method":"tasks/get","params":{"id":"` + taskID + `"}}`
	resp, _ = postJSON(h.URL+"/a2a", body)
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), `"capability":"demo.greet"`) ||
		!strings.Contains(string(raw), `"state":"completed"`) {
		t.Fatalf("tasks/get = %s", raw)
	}

	// 5) tasks/get 不存在的任务 → -32001。
	body = `{"jsonrpc":"2.0","id":4,"method":"tasks/get","params":{"id":"deadbeef"}}`
	resp, _ = postJSON(h.URL+"/a2a", body)
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), "-32001") {
		t.Fatalf("want -32001, got %s", raw)
	}

	// 6) 已终结任务 cancel → -32002（任务不可变性）。
	body = `{"jsonrpc":"2.0","id":5,"method":"tasks/cancel","params":{"id":"` + taskID + `"}}`
	resp, _ = postJSON(h.URL+"/a2a", body)
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), "-32002") {
		t.Fatalf("want -32002, got %s", raw)
	}

	// 7) 未知 capability → rejected（名字存在但没注册，追问也没用）。
	body = `{
		"jsonrpc":"2.0","id":6,"method":"message/send",
		"params":{"message":{"role":"user","parts":[{"kind":"text","text":"x"}]},
		  "metadata":{"capability":"no.such.cap"}}}`
	resp, _ = postJSON(h.URL+"/a2a", body)
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), `"state":"rejected"`) || !strings.Contains(string(raw), "no.such.cap") {
		t.Fatalf("unknown capability must be rejected: %s", raw)
	}

	// 8) 未知 method → -32601。
	body = `{"jsonrpc":"2.0","id":7,"method":"teleport","params":{}}`
	resp, _ = postJSON(h.URL+"/a2a", body)
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), "-32601") {
		t.Fatalf("want -32601, got %s", raw)
	}

	// 9) GET /a2a（浏览器直接访问）→ 302 重定向到管理台能力盘页，
	//    避免 405 死胡同（协议 JSON-RPC 只走 POST，Lume 客户端不受影响）。
	// 注意：默认 http.Client 会跟随重定向（跟到测试 mux 里没有的 /a2a-panel → 404），
	// 所以这里用不跟随重定向的 client 来断言第一跳的 302。
	noRedirect := &http.Client{CheckRedirect: func(r *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err = noRedirect.Get(h.URL + "/a2a")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/a2a-panel" {
		t.Fatalf("GET /a2a = %d %q, want 302 /a2a-panel", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp.Body.Close()
}

func TestPeerInvokeWhitelist(t *testing.T) {
	dir := t.TempDir()
	// 本节点名 tsm-hub 登记在注册表里（对端视角：调用方是 tsm-hub）。
	reg := writeRegistry(t, dir, "tsm-hub")
	peer := New("crm", "", staticRoot, reg, 120)
	caps := NewCapabilities()
	caps.Register(Capability{Name: "tsm.overview"}, func(args map[string]any) DispatchResult {
		return completedArtifact(map[string]any{"ok": true})
	})
	srv := &Server{Node: "tsm-hub", Caps: caps, Tasks: NewTaskStore(dir, "tsm-hub"), PEER: peer}

	// 正确签名 → 通过。
	ts, nonce := dynTs(), "testnonce"
	body := `{"tool":"tsm.overview","arguments":{}}`
	sig := signWith(peer.deriveKey("tsm-hub"), "tsm-hub", ts, nonce, scopePOST("/peer/invoke", body))
	req := peerInvokeRequest(t, "tsm-hub", ts, nonce, sig, body)
	w := httptest.NewRecorder()
	srv.HandlePeerInvoke(w, req)
	if raw := w.Body.String(); !strings.Contains(raw, `"ok":true`) {
		t.Fatalf("signed invoke should pass, got %s", raw)
	}

	// 未登记的 tool → 拒（白名单语义：只信"清单里有"）。
	body = `{"tool":"no.such","arguments":{}}`
	sig = signWith(peer.deriveKey("tsm-hub"), "tsm-hub", ts, nonce, scopePOST("/peer/invoke", body))
	req = peerInvokeRequest(t, "tsm-hub", ts, nonce, sig, body)
	w = httptest.NewRecorder()
	srv.HandlePeerInvoke(w, req)
	if raw := w.Body.String(); !strings.Contains(raw, `unknown tool 'no.such'`) {
		t.Fatalf("unknown tool must be rejected: %s", raw)
	}

	// 缺签名 → fail-closed，错误文案与 Lume 侧一致（unauthorized + missing auth params）。
	req = peerInvokeRequest(t, "tsm-hub", ts, nonce, "", body)
	w = httptest.NewRecorder()
	srv.HandlePeerInvoke(w, req)
	if raw := w.Body.String(); !strings.Contains(raw, "unauthorized") || !strings.Contains(raw, "missing auth params") {
		t.Fatalf("missing sig must be rejected: %s", raw)
	}

	// 未配凭据的 Server → 整面 fail-closed。
	srvOff := &Server{Node: "tsm-hub", Caps: caps, Tasks: NewTaskStore(dir, "tsm-hub"), PEER: nil}
	req = peerInvokeRequest(t, "tsm-hub", ts, nonce, sig, body)
	w = httptest.NewRecorder()
	srvOff.HandlePeerInvoke(w, req)
	if raw := w.Body.String(); !strings.Contains(raw, "peer disabled") {
		t.Fatalf("no-credential server must fail-closed: %s", raw)
	}

	// relay → 410 Gone（多跳转发已退役，端点保留并说明原因）。
	// 请求打到 /peer/relay（handler 按 r.URL.Path 算 scope），签名要对
	// 本次 body + 本次路径重算（POST 签名含 body）。
	relayBody := `{}`
	relaySig := signWith(peer.deriveKey("tsm-hub"), "tsm-hub", ts, nonce, scopePOST("/peer/relay", relayBody))
	req2, err := http.NewRequest("POST", "http://x/peer/relay", strings.NewReader(relayBody))
	if err != nil {
		t.Fatal(err)
	}
	req2.URL.RawQuery = "node=tsm-hub&ts=" + ts + "&nonce=" + nonce + "&sig=" + relaySig
	w = httptest.NewRecorder()
	srv.HandlePeerRelay(w, req2)
	if w.Code != http.StatusGone {
		t.Fatalf("relay status = %d, want 410 (body=%q)", w.Code, w.Body.String())
	}
	if raw := w.Body.String(); !strings.Contains(raw, "relay 已退役") {
		t.Fatalf("relay body should explain, got %s", raw)
	}
}

func TestTaskStoreIsolationAndPersist(t *testing.T) {
	dir := t.TempDir()
	// 两个"节点"共用同一数据目录：任务必须按节点名隔离（lume-a2a 实测踩过的坑：
	// 不隔离时 node-b 的 tasks/get 能把 node-a 的任务查出来）。
	tsA := NewTaskStore(dir, "node-a")
	tsB := NewTaskStore(dir, "node-b")

	ta := newTask("node-a", "", "cap.x", "hello", 0)
	if err := tsA.save(ta); err != nil {
		t.Fatal(err)
	}
	if _, err := tsB.load(ta.ID); err == nil {
		t.Fatalf("node-b must not see node-a's task (directory isolation)")
	}
	if _, err := tsA.load(ta.ID); err != nil {
		t.Fatalf("node-a should load its own task: %v", err)
	}

	// 路径穿越防护：id 是外部输入（tasks/get），含 ../ 必须被拒。
	if _, err := tsA.load("../etc/passwd"); err == nil {
		t.Fatalf("path traversal must be rejected")
	}
	if p := tsA.path("a/b"); p != "" {
		t.Fatalf("task id with slash must be rejected, got %s", p)
	}

	// 重启恢复：新建 store 能读回落盘任务。
	tsC := NewTaskStore(dir, "node-a")
	if got, err := tsC.load(ta.ID); err != nil {
		t.Fatalf("restore after restart: %v", err)
	} else if got.Status.State != StateSubmitted {
		t.Fatalf("restored state = %s", got.Status.State)
	}
}

// ── helpers ─────────────────────────────────────────────────────────

// dynTs 返回当前 unix 秒的字符串，保证验签测试落在时间窗内。
func dynTs() string {
	return strconv.FormatInt(time.Now().Unix(), 10)
}

// peerInvokeRequest 构造 /peer/invoke 请求（签名走 query，Lume 出站同款形态）。
func peerInvokeRequest(t *testing.T, node, ts, nonce, sig, body string) *http.Request {
	t.Helper()
	r, err := http.NewRequest("POST", "http://x/peer/invoke", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	q := r.URL.Query()
	if node != "" {
		q.Set("node", node)
	}
	if ts != "" {
		q.Set("ts", ts)
	}
	if nonce != "" {
		q.Set("nonce", nonce)
	}
	if sig != "" {
		q.Set("sig", sig)
	}
	r.URL.RawQuery = q.Encode()
	return r
}

func postJSON(urlStr, body string) (*http.Response, error) {
	return http.Post(urlStr, "application/json", strings.NewReader(body))
}
