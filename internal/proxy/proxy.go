// Package proxy 实现 OpenAI 兼容的转发层：
//
//   - 请求侧：校验自制 Key 后，把 Authorization 换成上游 provider 的 Key；
//   - 路由侧：按路由表挑候选，失败自动 failover 到下一个；
//   - 响应侧：非流式原样回传，流式按 SSE 逐帧转发并采集 usage。
package proxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/erishen/tsm-hub/internal/quota"
	"github.com/erishen/tsm-hub/internal/router"
	"github.com/erishen/tsm-hub/internal/skills"
	"github.com/erishen/tsm-hub/internal/store"
)

// Proxy 持有转发所需的全部依赖。
type Proxy struct {
	store  *store.Store
	router *router.Router
	health *router.Tracker
	rec    *quota.Recorder
	client *http.Client
	skills *skills.Library
	mcps   *mcpManager
	fastMgr fastPathMgr
	// mem 是 remember/recall 的 SQLite 持久化存储；打开失败时为 nil（降级：工具返回错误）。
	mem *memoryStore
}

// New 创建转发器。
func New(s *store.Store, rt *router.Router, h *router.Tracker, rec *quota.Recorder, sk *skills.Library) *Proxy {
	p := &Proxy{
		store:  s,
		router: rt,
		health: h,
		rec:    rec,
		skills: sk,
		mcps:   newMCPManager(),
		client: &http.Client{
			// 不用全局 Transport，避免被别的库改动；超时交给 context 控制。
			Transport: &http.Transport{
				MaxIdleConns:        256,
				MaxIdleConnsPerHost: 32,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
	if mem, err := openMemory(filepath.Join(s.DataDir(), "memory.db")); err != nil {
		// 记忆库打不开不拖垮网关：remember/recall 返回错误，其余能力不受影响。
		log.Printf("[memory] open sqlite failed, remember/recall degraded: %v", err)
	} else {
		p.mem = mem
		// provider 健康状态与记忆共用 memory.db 持久化（重启保留冷却/失败计数）。
		p.initHealthPersist(mem.db)
	}
	return p
}

// Result 描述一次转发的最终 outcome，用于记账。
type Result struct {
	ProviderID      string
	UpstreamModel   string
	PromptTokens    int
	CompletionToken int
	TotalTokens     int
	Status          int
	Stream          bool
	Latency         time.Duration
	Err             string
	// ProviderFault 表示失败源于上游服务端（网络错误或 5xx），计入健康熔断；
	// 客户端引发的 4xx 等错误不置位，避免坏请求把健康上游拖下架。
	ProviderFault bool
	// ModelFault 表示失败源于「该上游上模型不存在」（404 model not found）：
	// 计入模型级不可用（路由跳过该 provider×模型），但不计入 provider 健康熔断。
	ModelFault bool
	// Scene 是智能分流命中的场景（chat/reason/code/fast；非 auto 为空）。
	Scene string
	// FastPath 是确定性快路径命中标记（方法名/plugin:xx/codegen；未命中为空）。
	FastPath string
	// Attempt 是本请求实际尝试的第几个候选（1=首次命中；>1 表示发生过 failover）。
	Attempt int
	// Failover 是 failover 链：按顺序记录每个失败候选（不含最终命中的那个）。
	Failover []store.FailoverStep
	// ClientTools 请求里客户端声明的工具名（去重截断）。
	ClientTools []string
	// ExecTools agent 循环实际执行的工具名（含 skill-run 的 skill、mcp_*）。
	ExecTools []string
}

type usageObj struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// toolDecl 兼容 OpenAI 两种 tools 声明格式：
// {type:"function", function:{name}} 与 {type, name}。
type toolDecl struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

type chatRequest struct {
	Model  string     `json:"model"`
	Stream bool       `json:"stream"`
	Tools  []toolDecl `json:"tools"`
}

// clientTools 提取请求里声明的工具名（去重，最多 20 个）。
func clientTools(tools []toolDecl) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		n := t.Function.Name
		if n == "" {
			n = t.Name
		}
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
		if len(out) >= 20 {
			break
		}
	}
	return out
}

// Handle 处理一个代理请求。path 是 /v1/xxx 形式的 OpenAI 路径。
func (p *Proxy) Handle(w http.ResponseWriter, r *http.Request, key store.APIKey, path string, body []byte) Result {
	started := time.Now()

	var req chatRequest
	if len(body) > 0 {
		_ = json.Unmarshal(body, &req)
	}
	// auto：外部无脑调用——model 不传或传 "auto" 时，由网关按请求内容自动选择场景路由。
	// 响应头 X-Llm-Router-Scene 返回实际命中的场景与信号（如 "code:写个"），便于观测。
	scene := ""
	if req.Model == "" || req.Model == "auto" {
		var rule string
		scene, rule = router.ClassifyScene(body)
		req.Model = scene
		w.Header().Set("X-Llm-Router-Scene", rule)
	}
	if req.Model == "" {
		return p.fail(w, started, key, req.Model, http.StatusBadRequest, "model is required", scene)
	}
	clientTools := clientTools(req.Tools)
	// key 模型白名单只约束具体模型；场景路由（chat/fast/reason/code 等显式路由）对所有 key 放行。
	if len(key.Models) > 0 && !allowsModel(key.Models, req.Model) && !p.router.HasRoute(req.Model) {
		slog.Warn("request forbidden", "key", key.Prefix, "model", req.Model, "allowed", key.Models)
		return p.fail(w, started, key, req.Model, http.StatusForbidden,
			fmt.Sprintf("key is not allowed to use model %q", req.Model), scene)
	}
	// 网关 agent：客户端不传 tools 时，自动附加内置工具并在服务端执行循环。
	if p.agentChat(body, r) {
		if handled, res := p.agentRun(w, r, key, path, body, req, scene); handled {
			res.Scene = scene
			return res
		}
	}
	slog.Info("chat request", "model", req.Model, "key", key.ID)

	cands, err := p.router.Pick(req.Model)
	if err != nil {
		slog.Warn("no candidate for route", "model", req.Model, "err", err.Error())
		return p.fail(w, started, key, req.Model, http.StatusBadGateway, err.Error(), scene)
	}

	var lastErr string
	var failChain []store.FailoverStep
	for i, c := range cands {
		res, retryable := p.attempt(w, r, c, path, body, req)
		res.Scene = scene
		res.Attempt = i + 1
		res.ClientTools = clientTools
		if retryable {
			lastErr = res.Err
			failChain = append(failChain, store.FailoverStep{
				ProviderID: c.ProviderID,
				Model:      c.UpstreamModel,
				Error:      res.Err,
			})
			// 模型不存在是模型级问题（failover 换其他家即可），不计入 provider 健康熔断。
			if !res.ModelFault {
				p.health.ReportFailure(c.ProviderID, res.Err)
			}
			continue
		}
		switch {
		case res.ProviderFault:
			p.health.ReportFailure(c.ProviderID, res.Err)
		case res.Err == "":
			p.health.ReportSuccess(c.ProviderID, time.Since(started))
		}
		res.Failover = failChain
		p.account(key, req.Model, res)
		return res
	}
	// 所有候选都失败。
	res := Result{
		ProviderID: strings.Join(providerIDs(cands), ","),
		Status:     http.StatusBadGateway,
		Stream:     req.Stream,
		Latency:    time.Since(started),
		Err:        lastErr,
		Scene:      scene,
		Attempt:    len(cands),
		Failover:   failChain,
	}
	p.account(key, req.Model, res)
	writeError(w, res.Status, "upstream_unavailable", orDefault(lastErr, "all upstream providers failed"))
	return res
}

func providerIDs(cs []router.Candidate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ProviderID)
	}
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// isModelNotFound 判断上游 404 响应体是否属于「模型不存在」语义。
// 保守匹配：必须同时出现 model 与 not found/不存在 类关键词，避免误判
// 其他 404（如路径错误、key 权限不足等），那些仍按普通 4xx 直接透传。
func isModelNotFound(body string) bool {
	lower := strings.ToLower(body)
	hasModel := strings.Contains(lower, "model") || strings.Contains(lower, "型号") ||
		strings.Contains(lower, "route") || strings.Contains(lower, "模型")
	if !hasModel {
		return false
	}
	for _, k := range []string{"not found", "not_found", "does not exist", "not exist", "no such", "不存在", "未找到"} {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

// attempt 尝试把请求转发给某个候选。
// 返回值 retryable=true 表示本次没有向客户端写出任何字节，可以换下一个候选重试。
func (p *Proxy) attempt(w http.ResponseWriter, r *http.Request, c router.Candidate, path string, body []byte, req chatRequest) (Result, bool) {
	started := time.Now()
	adapter := GetAdapter(c.Provider)

	upBody, err := rewriteBody(body, c.UpstreamModel, req.Stream)
	if err != nil {
		// 客户端请求体不合法：直接回 400，且不换候选（换谁都一样）。
		res := Result{ProviderID: c.ProviderID, UpstreamModel: c.UpstreamModel,
			Status: http.StatusBadRequest, Stream: req.Stream, Latency: time.Since(started), Err: err.Error()}
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return res, false
	}

	// 协议适配：把 OpenAI 格式的请求体转换为目标协议格式（如 Anthropic）
	convertedBody, extraHeaders, err := adapter.ConvertRequest(upBody, c.UpstreamModel)
	if err != nil {
		res := Result{ProviderID: c.ProviderID, UpstreamModel: c.UpstreamModel,
			Status: http.StatusBadGateway, Stream: req.Stream, Latency: time.Since(started),
			Err: fmt.Sprintf("convert request: %v", err), ProviderFault: true}
		writeError(w, http.StatusBadGateway, "protocol_error", res.Err)
		return res, true
	}
	upBody = convertedBody

	timeout := time.Duration(c.Provider.TimeoutMS) * time.Millisecond
	if c.Provider.TimeoutMS <= 0 {
		timeout = time.Duration(p.store.Settings().DefaultTimeoutMS) * time.Millisecond
	}
	if req.Stream {
		timeout *= 5 // 流式是长连接，给更宽裕的整体超时
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	upReq, err := http.NewRequestWithContext(ctx, r.Method, adapter.UpstreamURL(c.Provider, path), bytes.NewReader(upBody))
	if err != nil {
		return Result{ProviderID: c.ProviderID, UpstreamModel: c.UpstreamModel, Status: http.StatusBadGateway,
			Stream: req.Stream, Latency: time.Since(started), Err: err.Error(), ProviderFault: true}, true
	}
	copyHeaders(upReq.Header, r.Header)
	// 协议适配器自定义认证头（如 Azure 用 api-key，Gemini 用 URL 参数不需要认证头）
	if authKey, authVal := adapter.AuthHeader(c.Provider); authKey != "" {
		upReq.Header.Set(authKey, authVal)
	}
	upReq.Header.Set("Content-Type", "application/json")
	for k, v := range extraHeaders {
		upReq.Header.Set(k, v)
	}
	for k, v := range c.Provider.Headers {
		upReq.Header.Set(k, v)
	}
	upReq.ContentLength = int64(len(upBody))

	resp, err := p.client.Do(upReq)
	if err != nil {
		slog.Warn("upstream request failed", "provider", c.ProviderID, "model", req.Model, "upstream_model", c.UpstreamModel, "err", err.Error())
		return Result{ProviderID: c.ProviderID, UpstreamModel: c.UpstreamModel, Status: http.StatusBadGateway,
			Stream: req.Stream, Latency: time.Since(started),
			Err: fmt.Sprintf("upstream request failed: %v", err), ProviderFault: true}, true
	}
	defer resp.Body.Close()

	// 5xx 视为可重试（尚未向客户端写任何字节），并计入上游故障。
	if resp.StatusCode >= 500 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		slog.Warn("upstream 5xx", "provider", c.ProviderID, "model", req.Model, "upstream_model", c.UpstreamModel,
			"status", resp.StatusCode, "body", decodeUpstreamBody(resp, b))
		return Result{ProviderID: c.ProviderID, UpstreamModel: c.UpstreamModel, Status: resp.StatusCode,
			Stream: req.Stream, Latency: time.Since(started),
			Err: fmt.Sprintf("upstream %d: %s", resp.StatusCode, decodeUpstreamBody(resp, b)), ProviderFault: true}, true
	}

	// 429（限流 / 免费额度耗尽）与 403（免费额度耗尽 / key 无权限，如阿里云百炼
	// 免费包到期）同样可重试：换到下一候选（failover 路由会因此自动降级）。
	// 并立即给该 provider 记冷却（默认 60s），期间不再被 smart/健康过滤选中，
	// 避免免费家限流后每次请求都先白吃一次 4xx。
	// 402（余额不足 / Payment Required）：付费模型额度耗尽，failover 到下一候选，
	// 并长冷却 1 小时（余额耗尽需用户充值才会恢复，避免每次请求都先白吃一次 402）。
	if resp.StatusCode == http.StatusPaymentRequired {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		slog.Warn("upstream payment required (balance exhausted)", "provider", c.ProviderID, "model", req.Model,
			"upstream_model", c.UpstreamModel, "status", resp.StatusCode, "body", decodeUpstreamBody(resp, b))
		p.health.ReportThrottle(c.ProviderID, "balance exhausted: "+decodeUpstreamBody(resp, b), time.Hour)
		return Result{ProviderID: c.ProviderID, UpstreamModel: c.UpstreamModel, Status: resp.StatusCode,
			Stream: req.Stream, Latency: time.Since(started),
			Err: fmt.Sprintf("upstream %d: %s", resp.StatusCode, decodeUpstreamBody(resp, b)), ProviderFault: true}, true
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		slog.Warn("upstream throttled", "provider", c.ProviderID, "model", req.Model, "upstream_model", c.UpstreamModel,
			"status", resp.StatusCode, "body", decodeUpstreamBody(resp, b))
		// 429（tpm/rpm 限流）通常秒级恢复，短冷却即可；403（免费额度耗尽 /
		// key 无权限）持续较久，用配置的长冷却（默认 120s）。避免限流家被
		// 长时间摘除后只剩同样不可用的候选。
		throttleSec := p.store.Settings().Smart.ThrottleSec
		if throttleSec <= 0 {
			throttleSec = 120
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			throttleSec = 15
		}
		p.health.ReportThrottle(c.ProviderID, decodeUpstreamBody(resp, b), time.Duration(throttleSec)*time.Second)
		return Result{ProviderID: c.ProviderID, UpstreamModel: c.UpstreamModel, Status: resp.StatusCode,
			Stream: req.Stream, Latency: time.Since(started),
			Err: fmt.Sprintf("upstream %d: %s", resp.StatusCode, decodeUpstreamBody(resp, b)), ProviderFault: true}, true
	}

	// 非流式且上游返回 2xx/3xx 但 body 带 OpenAI error（部分供应商过载时如此）：
	// 视为上游故障换下一候选，避免把坏响应当成功透传给客户端。
	// 注意：只 peek 前 8KB 判断结构，完整 body 仍要透传，不能把响应截断给客户端。
	if !req.Stream && resp.StatusCode < 400 {
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		var eb struct {
			Error map[string]any `json:"error"`
		}
		if json.Unmarshal(peek, &eb) == nil && eb.Error != nil {
			return Result{ProviderID: c.ProviderID, UpstreamModel: c.UpstreamModel, Status: http.StatusBadGateway,
				Stream: req.Stream, Latency: time.Since(started),
				Err: fmt.Sprintf("upstream error: %s", decodeUpstreamBody(resp, peek)), ProviderFault: true}, true
		}
		rest, _ := io.ReadAll(resp.Body)
		resp.Body = io.NopCloser(bytes.NewReader(append(peek, rest...)))
	}

	// 上游 4xx 直接透传；记日志便于定位（如上游 "model is not found" 是哪家、哪个模型）。
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		resp.Body = io.NopCloser(bytes.NewReader(b))
		slog.Info("upstream 4xx",
			"provider", c.ProviderID, "model", req.Model, "upstream_model", c.UpstreamModel,
			"status", resp.StatusCode, "body", decodeUpstreamBody(resp, b))
		// 404 模型不存在（model not found / model route not found 等）：标记该 provider×模型
		// 不可用（默认 30 分钟），并把本次请求交给下一个候选——同一模型其他家可能可用，
		// 不用等用户手动排查（如 sensenova 探测到但实际不存在的模型）。
		if resp.StatusCode == http.StatusNotFound && isModelNotFound(string(b)) {
			unavailSec := p.store.Settings().Smart.UnavailableSec
			if unavailSec <= 0 {
				unavailSec = 1800
			}
			p.store.MarkModelUnavailable(c.ProviderID, c.UpstreamModel, decodeUpstreamBody(resp, b), time.Duration(unavailSec)*time.Second)
			return Result{ProviderID: c.ProviderID, UpstreamModel: c.UpstreamModel, Status: resp.StatusCode,
				Stream: req.Stream, Latency: time.Since(started),
				Err: fmt.Sprintf("upstream %d (model unavailable): %s", resp.StatusCode, decodeUpstreamBody(resp, b)),
				ProviderFault: true, ModelFault: true}, true
		}
	}

	if req.Stream {
		res := p.streamResponse(w, resp, c, adapter)
		res.Latency = time.Since(started)
		// 首帧之前就失败才允许重试；一旦写过帧就只能认了。
		return res, false
	}

	res := p.bufferedResponse(w, resp, c, adapter)
	res.Latency = time.Since(started)
	return res, false
}

// bufferedResponse 处理非流式响应：整包读完再回传，便于解析 usage。
func (p *Proxy) bufferedResponse(w http.ResponseWriter, resp *http.Response, c router.Candidate, adapter ProtocolAdapter) Result {
	data, err := io.ReadAll(io.LimitReader(resp.Body, p.store.Settings().MaxBodyBytes))
	res := Result{
		ProviderID:    c.ProviderID,
		UpstreamModel: c.UpstreamModel,
		Status:        resp.StatusCode,
	}
	if err != nil {
		res.Err = fmt.Sprintf("read upstream body: %v", err)
		res.ProviderFault = true // 上游连接中途断开
		writeError(w, http.StatusBadGateway, "upstream_read_error", res.Err)
		return res
	}

	// 协议适配：把目标协议的响应转换为 OpenAI 格式
	if resp.StatusCode < 400 && len(data) > 0 {
		if converted, err := adapter.ConvertResponse(data); err == nil {
			data = converted
		}
	}

	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("X-LLM-Router-Provider", c.ProviderID)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(data)

	var payload struct {
		Usage usageObj `json:"usage"`
	}
	if err := json.Unmarshal(data, &payload); err == nil {
		res.PromptTokens = payload.Usage.PromptTokens
		res.CompletionToken = payload.Usage.CompletionTokens
		res.TotalTokens = payload.Usage.TotalTokens
	}
	if resp.StatusCode >= 400 {
		res.Err = fmt.Sprintf("upstream %d: %s", resp.StatusCode, decodeUpstreamBody(resp, data))
		// 5xx 在 attempt 里已按可重试处理，走到这里说明是 4xx 等客户端问题，
		// 不置 ProviderFault，避免坏请求把健康上游熔断摘除。
	}
	return res
}

// streamResponse 逐帧转发 SSE，并从最后一帧里取 usage。
func (p *Proxy) streamResponse(w http.ResponseWriter, resp *http.Response, c router.Candidate, adapter ProtocolAdapter) Result {
	res := Result{
		ProviderID:    c.ProviderID,
		UpstreamModel: c.UpstreamModel,
		Status:        resp.StatusCode,
		Stream:        true,
	}
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-LLM-Router-Provider", c.ProviderID)
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	var prompt, completion, total int
	br := bufio.NewReaderSize(resp.Body, 32*1024)
	wrote := false
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			// 协议适配：把目标协议的 SSE 事件转换为 OpenAI 格式
			if strings.HasPrefix(line, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if converted, skip, convErr := adapter.ConvertStreamEvent([]byte(payload)); convErr == nil && !skip {
					line = "data: " + string(converted) + "\n"
					// 从转换后的事件里解析 usage
					if pt, ct, tt, ok := parseSSEUsage(line); ok {
						prompt, completion, total = pt, ct, tt
					}
				} else if skip {
					// 跳过该事件（如 Anthropic 的 message_start/content_block_start 等）
					continue
				}
			}
			wrote = true
			if _, werr := io.WriteString(w, line); werr != nil {
				res.Err = fmt.Sprintf("write client: %v", werr)
				res.PromptTokens, res.CompletionToken, res.TotalTokens = prompt, completion, total
				return res
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			if !wrote {
				// 首帧之前断流：上游问题，标记为故障。
				res.Err = fmt.Sprintf("read upstream stream: %v", err)
				res.ProviderFault = true
			}
			res.PromptTokens, res.CompletionToken, res.TotalTokens = prompt, completion, total
			return res
		}
	}
	if resp.StatusCode >= 400 {
		res.Err = fmt.Sprintf("upstream %d in stream", resp.StatusCode)
	}
	res.PromptTokens, res.CompletionToken, res.TotalTokens = prompt, completion, total
	return res
}

// parseSSEUsage 从 "data: {...}" 帧里解析 usage。
func parseSSEUsage(line string) (int, int, int, bool) {
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "" || payload == "[DONE]" {
		return 0, 0, 0, false
	}
	var chunk struct {
		Usage *usageObj `json:"usage"`
	}
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil || chunk.Usage == nil {
		return 0, 0, 0, false
	}
	u := chunk.Usage
	total := u.TotalTokens
	if total == 0 {
		total = u.PromptTokens + u.CompletionTokens
	}
	return u.PromptTokens, u.CompletionTokens, total, true
}

// rewriteBody 改写 model 名，并在流式请求里注入 stream_options 以索取 usage。
func rewriteBody(body []byte, upstreamModel string, stream bool) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("invalid json body: %w", err)
	}
	m["model"] = json.RawMessage(strconv.Quote(upstreamModel))
	if stream {
		if _, has := m["stream_options"]; !has {
			m["stream_options"] = json.RawMessage(`{"include_usage":true}`)
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("re-marshal body: %w", err)
	}
	return out, nil
}

// upstreamURL 拼接上游地址。
//
// 上游 base_url 常见两种写法：https://api.openai.com/v1 或 https://api.openai.com，
func allowsModel(allowed []string, model string) bool {
	for _, m := range allowed {
		if m == model || m == "*" {
			return true
		}
	}
	return false
}

func copyResponseHeaders(dst, src http.Header) {
	for k, vs := range src {
		lk := strings.ToLower(k)
		if lk == "content-length" || lk == "transfer-encoding" || lk == "connection" {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func compact(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// decodeUpstreamBody 读取上游错误响应体并格式化为人类可读的错误信息：
//  1. 自动检测并解压 gzip 压缩响应体（上游返回 Content-Encoding: gzip 时
//     http.Client 未自动解压的情况，直接存二进制会导致前端乱码）；
//  2. 解析 JSON 错误响应，提取 error.message / message 字段，去掉冗长的
//     JSON 结构（如 {"error":{"message":"...","type":"...","code":"..."}}）；
//  3. 去掉换行，截断到 300 字符。
func decodeUpstreamBody(resp *http.Response, b []byte) string {
	body := b
	// 检测 gzip：Content-Encoding 头或魔数 0x1f 0x8b
	if resp != nil && strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") ||
		len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b {
		if gr, err := gzip.NewReader(bytes.NewReader(b)); err == nil {
			if decompressed, err := io.ReadAll(gr); err == nil {
				body = decompressed
			}
			gr.Close()
		}
	}
	s := string(body)
	// 尝试解析 JSON，提取 error.message 或 message
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err == nil {
		if errObj, ok := raw["error"].(map[string]any); ok {
			if msg, ok := errObj["message"].(string); ok && msg != "" {
				s = msg
			}
		} else if msg, ok := raw["message"].(string); ok && msg != "" {
			s = msg
		}
	}
	return compact(s)
}

// isFreeModel 判断一次上游调用是否免费：模型名带 :free/-free 后缀，或
// 该 provider 最近一次探测快照里标记为 Free（与路由 smartScore 的判据一致）。
func isFreeModel(st *store.Store, providerID, upstreamModel string) bool {
	if strings.Contains(upstreamModel, ":free") || strings.HasSuffix(upstreamModel, "-free") {
		return true
	}
	p, ok := st.GetProvider(providerID)
	if !ok {
		return false
	}
	for i := range p.ProbeModels {
		if p.ProbeModels[i].ID == upstreamModel && p.ProbeModels[i].Free {
			return true
		}
	}
	return false
}

// account 把一次请求写入用量流水。
func (p *Proxy) account(key store.APIKey, model string, res Result) {
	st := p.store.Settings()
	// 计价优先按真实上游模型查表（别名 smart/cheap 永远命中不了 gpt-4o 的单价），
	// 再回落到客户端模型名，最后用 default。
	price, ok := st.Pricing[res.UpstreamModel]
	if !ok {
		price, ok = st.Pricing[model]
	}
	if !ok {
		price = st.Pricing["default"]
	}
	cost := float64(res.PromptTokens)/1000*price.InputPer1K + float64(res.CompletionToken)/1000*price.OutputPer1K
	// 免费模型（上游探测 Free 或 :free/-free 后缀）不计成本。
	if isFreeModel(p.store, res.ProviderID, res.UpstreamModel) {
		cost = 0
	}
	total := res.TotalTokens
	if total == 0 {
		total = res.PromptTokens + res.CompletionToken
	}
	_ = p.rec.Record(store.UsageRecord{
		TS:              time.Now(),
		KeyID:           key.ID,
		Model:           model,
		ProviderID:      res.ProviderID,
		UpstreamModel:   res.UpstreamModel,
		ClientTools:     res.ClientTools,
		ExecTools:       res.ExecTools,
		PromptTokens:    res.PromptTokens,
		CompletionToken: res.CompletionToken,
		TotalTokens:     total,
		CostUSD:         cost,
		LatencyMS:       res.Latency.Milliseconds(),
		Stream:          res.Stream,
		Status:          res.Status,
		Error:           res.Err,
		Scene:           res.Scene,
		FastPath:        res.FastPath,
		Attempt:         res.Attempt,
		Failover:        res.Failover,
	})
}

// fail 写错误响应并记账。
func (p *Proxy) fail(w http.ResponseWriter, started time.Time, key store.APIKey, model string, status int, msg string, scene string) Result {
	res := Result{Status: status, Latency: time.Since(started), Err: msg, Scene: scene}
	p.account(key, model, res)
	writeError(w, status, httpStatusSlug(status), msg)
	return res
}

func httpStatusSlug(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusTooManyRequests:
		return "rate_limited"
	default:
		return "bad_gateway"
	}
}

// writeError 输出 OpenAI 风格的错误体。
func writeError(w http.ResponseWriter, status int, code, msg string) {	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    code,
			"code":    code,
		},
	})
}

// writeJSON 输出 JSON 响应（agent 回放用）。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
