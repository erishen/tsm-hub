package proxy

// 网关 agent：客户端不传 tools 时，网关自动附加内置工具池并在服务端执行
// tool_calls 循环（最多 max_rounds 轮），最终把答案返回给客户端。
// - 非流式：直接返回最终完整响应（含工具执行过程不暴露给客户端）。
// - 流式：内部用非流式跑完循环，拿到最终答案后以标准 SSE chunk 回放。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/erishen/tsm-hub/internal/proxy/adapter"
	"github.com/erishen/tsm-hub/internal/router"
	"github.com/erishen/tsm-hub/internal/store"
)

// chatMessage 是 OpenAI messages 的通用结构。
type chatMessage map[string]any

// agentChat 判断是否走网关 agent：客户端未传 tools 且未显式关闭（全局/key 级/请求头）。
func (p *Proxy) agentChat(body []byte, r *http.Request, key store.APIKey) bool {
	headerOff := strings.EqualFold(r.Header.Get("X-Llm-Router-Agent"), "off")
	return agentGate(body, p.store.Settings().Agent.Disabled, key.AgentDisabled, headerOff)
}

// agentGate 是 agent 启用判断的纯函数（便于测试）。
// 启用条件：全局未禁用 + key 未禁用 + 请求头未关 + 无客户端 tools + 有 messages。
func agentGate(body []byte, disabled, keyDisabled, headerOff bool) bool {
	if disabled || keyDisabled || headerOff {
		return false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	raw, has := m["tools"]
	if has && string(raw) != "null" {
		return false // 客户端自带 tools：尊重客户端，纯透传
	}
	msgs, has := m["messages"]
	if !has {
		return false
	}
	var arr []json.RawMessage
	if json.Unmarshal(msgs, &arr) != nil || len(arr) == 0 {
		return false
	}
	return true
}

// agentResult 是 agent 循环的最终产物。
type agentResult struct {
	// answer 最终回答文本（内容纯文本，无 tool_calls）。
	answer string
	// rounds 实际执行轮数。
	rounds int
	// usage 汇总（最后成功响应）。
	usage struct{ prompt, completion, total int }
	// err 循环中无法恢复的错误。
	err string
	// provider 最终响应的 provider。
	provider string
	// execTools 本轮实际执行过的工具名（含 skill 名、mcp_*，按执行序）。
	execTools []string
	// stream 回放用的响应 id。
	id string
	// model 上游模型名。
	model string
}

// agentRun 执行工具循环。返回 true 表示已由 agent 处理（写了 w）。
func (p *Proxy) agentRun(w http.ResponseWriter, r *http.Request, key store.APIKey, path string, body []byte, req chatRequest, scene string) (bool, Result) {
	started := time.Now()
	res := agentResult{id: newID("chatcmpl")}

	// 注入技能（key 已配置 inject_skills 时）。
	// 注意：API 层（api.go）对 chat/completions 已统一注入过一次技能 system
	// 消息（agent 路径与透传路径共用该入口），这里不再重复注入，否则同一份
	// 技能清单会出现两遍，浪费上下文 token。
	_ = key.InjectSkills // inject handled at API layer; see api.go withRecovery

	maxRounds := p.store.Settings().Agent.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 4
	}
	if maxRounds > 12 {
		maxRounds = 12
	}

	msgs, err := parseMessages(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid messages: "+err.Error())
		return true, Result{Status: http.StatusBadRequest, Err: err.Error()}
	}
	// 加一条 system 说明工具池语义。
	msgs = append([]chatMessage{{
		"role":    "system",
		"content": "你是运行在 tsm-hub 网关上的 agent。你可以调用网关提供的工具来回答问题；工具由网关执行，你不需要向用户解释工具调用过程，直接给出基于工具结果的最终回答。",
	}}, msgs...)

	// 确定性快路径：纯代码能回答的问题（算术/时间/日期/换算/统计/进制/字数）
	// 直接返回，零模型调用、零上游消耗。内置匹配器未命中时尝试已生成插件
	// （plugin:xxx）与 codegen（LLM 生成检测器并持久化复用）。
	if answer, method := p.FastPathTry(r, key, path, lastUserText(msgs)); answer != "" {
		res.answer = answer
		res.model = "fastpath:" + method
		res.rounds = 1
		result := Result{
			ProviderID:    "fastpath",
			UpstreamModel: res.model,
			Status:        http.StatusOK,
			Stream:        req.Stream,
			Latency:       time.Since(started),
			FastPath:      method,
			Attempt:       1,
		}
		// fastpath 命中也记一笔流水（零 token 零成本），便于统计快路径命中率。
		result.Scene = scene
		p.account(key, req.Model, result)
		if req.Stream {
			p.writeSSEReplay(w, res, result)
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Llm-Router-Provider", "fastpath")
			w.Header().Set("X-Llm-Router-Fastpath", method)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      res.id,
				"object":  "chat.completion",
				"created": time.Now().Unix(),
				"model":   res.model,
				"choices": []map[string]any{{
					"index":         0,
					"message":       map[string]any{"role": "assistant", "content": res.answer},
					"finish_reason": "stop",
				}},
				"usage": map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
			})
		}
		return true, result
	}

	round := 0
	for {
		round++
		if round > maxRounds {
			res.err = fmt.Sprintf("tool loop exceeded %d rounds", maxRounds)
			break
		}
		upBody, upProvider, upErr, upStatus, ok := p.agentRound(r, key, path, req.Model, msgs)
		if !ok {
			// 上游 4xx：原样透传给客户端（不 502、不换候选）。
			if upStatus > 0 {
				result := Result{
					ProviderID: upProvider, Status: upStatus, Stream: req.Stream,
					Latency: time.Since(started), Err: "upstream " + upErr,
				}
				if !req.Stream {
					writeError(w, upStatus, "upstream_error", upErr)
				}
				result.Scene = scene
				p.account(key, req.Model, result)
				return true, result
			}
			res.err = upErr
			break
		}
		res.provider = upProvider
		res.rounds = round
		// 解析上游响应
		call, finalText, usage, parseErr := parseAgentResponse(upBody)
		if parseErr != "" {
			res.err = parseErr
			break
		}
		res.usage = usage
		if finalText != "" {
			res.answer = finalText
			res.model = upstreamModelOf(upBody)
			break
		}
		if call == nil {
			// 无 tool_calls 也无文本（异常）
			res.err = "upstream returned empty response without tool calls"
			break
		}
		// 执行工具并回填
		assistantMsg := chatMessage{
			"role":       "assistant",
			"content":    nil,
			"tool_calls": call,
		}
		msgs = append(msgs, assistantMsg)
		for _, tc := range call {
			fn := tc["function"].(map[string]any)
			name, _ := fn["name"].(string)
			argsRaw, _ := fn["arguments"].(string)
			var args toolArgs
			if argsRaw != "" {
				_ = json.Unmarshal([]byte(argsRaw), &args)
			}
			if args == nil {
				args = toolArgs{}
			}
			id, _ := tc["id"].(string)
			// 技能调用记录具体技能名（skill:<name>），便于按技能聚合归因。
			execName := name
			if name == "skill-run" {
				if sk := args.str("skill"); sk != "" {
					execName = "skill:" + sk
				}
			}
			res.execTools = append(res.execTools, execName)
			out := p.execTool(key.ID, name, args)
			msgs = append(msgs, chatMessage{
				"role":         "tool",
				"tool_call_id": id,
				"content":      out,
			})
		}
	}

	if res.answer == "" && res.err == "" {
		res.err = "agent produced no answer"
	}

	result := Result{
		ProviderID:     res.provider,
		UpstreamModel:  res.model,
		Status:         http.StatusOK,
		Stream:         req.Stream,
		Latency:        time.Since(started),
		PromptTokens:   res.usage.prompt,
		CompletionToken: res.usage.completion,
		TotalTokens:    res.usage.total,
		ClientTools:    clientTools(req.Tools),
		ExecTools:      res.execTools,
	}
	if res.err != "" {
		result.Status = http.StatusBadGateway
		result.Err = res.err
		result.ProviderFault = true
		p.health.ReportFailure(res.provider, res.err)
		result.Scene = scene
		p.account(key, req.Model, result)
		writeError(w, http.StatusBadGateway, "upstream_unavailable", res.err)
		return true, result
	}
	p.health.ReportSuccess(res.provider, time.Since(started))
	result.Scene = scene
	p.account(key, req.Model, result)

	if req.Stream {
		p.writeSSEReplay(w, res, result)
	} else {
		w.Header().Set("Content-Type", "application/json")
		if res.provider != "" {
			w.Header().Set("X-Llm-Router-Provider", res.provider)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      res.id,
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   orDefault(res.model, req.Model),
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": res.answer},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     res.usage.prompt,
				"completion_tokens": res.usage.completion,
				"total_tokens":      res.usage.total,
			},
		})
	}
	return true, result
}

// lastUserText 取消息列表最后一条 role=user 的文本（fastpath 分类对象）。
func lastUserText(msgs []chatMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i]["role"] == "user" {
			if s, ok := msgs[i]["content"].(string); ok {
				return s
			}
		}
	}
	return ""
}

// agentRound 对上游发起一轮非流式请求（带工具 schema），返回上游响应体。
// ok=false 时 err 非空；status=0 表示可重试（网络/5xx/429），status>0 表示
// 客户端级错误（4xx），直接透传不换候选。
// agentRound 走候选链做一轮上游补全。
// 返回 (body, providerID, errText, status, ok)：ok=true 表示 body 有效；
// status>0 表示上游 4xx（应原样透传），providerID 为该候选的真实 id；
// 其他失败时 errText 描述原因。
func (p *Proxy) agentRound(r *http.Request, key store.APIKey, path, model string, msgs []chatMessage) ([]byte, string, string, int, bool) {
	// 网关 agent 始终带工具 schema 向上游发起请求，所以 hasTools=true。
	cands, err := p.router.Pick(model, true)
	if err != nil {
		return nil, "", err.Error(), 0, false
	}
	var lastErr string
	for _, c := range cands {
		body, errMsg, status := p.agentUpstream(r, c, path, msgs)
		if status > 0 {
			return nil, c.ProviderID, errMsg, status, false
		}
		if errMsg != "" {
			lastErr = errMsg
			continue
		}
		return body, c.ProviderID, "", 0, true
	}
	return nil, "", orDefault(lastErr, "all upstream providers failed"), 0, false
}

// agentUpstream 构造并发送一轮上游请求（非流式）。
// 返回 status>0 表示该状态应原样透传给客户端（4xx）。
func (p *Proxy) agentUpstream(r *http.Request, c router.Candidate, path string, msgs []chatMessage) ([]byte, string, int) {
	reqBody := map[string]any{
		"model":       c.UpstreamModel,
		"messages":    msgs,
		"stream":      false,
		"tools":       p.toolSchemas(),
		"tool_choice": "auto",
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err.Error(), 0
	}
	timeout := time.Duration(c.Provider.TimeoutMS) * time.Millisecond
	if c.Provider.TimeoutMS <= 0 {
		timeout = time.Duration(p.store.Settings().DefaultTimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, adapter.UpstreamURL(c.Provider.BaseURL, path), bytes.NewReader(raw))
	if err != nil {
		return nil, err.Error(), 0
	}
	upReq.Header.Set("Authorization", "Bearer "+c.Provider.ResolvedAPIKey())
	upReq.Header.Set("Content-Type", "application/json")
	for k, v := range c.Provider.Headers {
		upReq.Header.Set(k, v)
	}
	upReq.ContentLength = int64(len(raw))

	resp, err := p.client.Do(upReq)
	if err != nil {
		p.health.ReportFailure(c.ProviderID, err.Error())
		return nil, fmt.Sprintf("upstream request failed: %v", err), 0
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode >= 500 {
		p.health.ReportFailure(c.ProviderID, compact(string(b)))
		return nil, fmt.Sprintf("upstream %d: %s", resp.StatusCode, compact(string(b))), 0
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		throttleSec := p.store.Settings().Smart.ThrottleSec
		if throttleSec <= 0 {
			throttleSec = 60
		}
		p.health.ReportThrottle(c.ProviderID, compact(string(b)), time.Duration(throttleSec)*time.Second)
		return nil, fmt.Sprintf("upstream 429: %s", compact(string(b))), 0
	}
	if resp.StatusCode < 400 {
		// 部分上游（如 openrouter 免费模型过载）返回 200 + {"error":{...}} 或空 body：
		// 视为上游故障，触发 failover 换下一候选，而不是把坏响应当成功。
		var eb struct {
			Error map[string]any `json:"error"`
		}
		if json.Unmarshal(b, &eb) == nil && eb.Error != nil {
			p.health.ReportFailure(c.ProviderID, compact(string(b)))
			return nil, fmt.Sprintf("upstream error: %s", compact(string(b))), 0
		}
		if len(bytes.TrimSpace(b)) == 0 {
			p.health.ReportFailure(c.ProviderID, "empty upstream response")
			return nil, "upstream returned empty response", 0
		}
	}
	if resp.StatusCode >= 400 {
		// 4xx 是客户端/上游固定错误：原样透传，不换候选、不驱逐 provider。
		return b, compact(string(b)), resp.StatusCode
	}
	p.health.ReportSuccess(c.ProviderID, 0)
	return b, "", 0
}

// parseAgentResponse 解析上游响应：tool_calls / 最终文本 / usage。
func parseAgentResponse(body []byte) (toolCalls []map[string]any, finalText string, usage struct{ prompt, completion, total int }, parseErr string) {
	var d struct {
		Error   map[string]any `json:"error"`
		Choices []struct {
			Message struct {
				Content   any               `json:"content"`
				ToolCalls []map[string]any `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, "", usage, "invalid upstream json: " + err.Error()
	}
	if d.Error != nil {
		return nil, "", usage, fmt.Sprintf("upstream error: %v", d.Error)
	}
	usage.prompt, usage.completion, usage.total = d.Usage.PromptTokens, d.Usage.CompletionTokens, d.Usage.TotalTokens
	if len(d.Choices) == 0 {
		return nil, "", usage, "upstream returned no choices"
	}
	msg := d.Choices[0].Message
	if len(msg.ToolCalls) > 0 {
		return msg.ToolCalls, "", usage, ""
	}
	if s, ok := msg.Content.(string); ok {
		return nil, s, usage, ""
	}
	if arr, ok := msg.Content.([]any); ok {
		var b strings.Builder
		for _, part := range arr {
			if m, ok := part.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return nil, b.String(), usage, ""
	}
	return nil, "", usage, ""
}

// writeSSEReplay 把最终答案按标准 SSE chunk 回放给客户端。
func (p *Proxy) writeSSEReplay(w http.ResponseWriter, res agentResult, result Result) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if result.ProviderID != "" {
		w.Header().Set("X-Llm-Router-Provider", result.ProviderID)
	}
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	model := orDefault(res.model, "agent")
	created := time.Now().Unix()

	chunk := func(delta string, finish string) {
		obj := map[string]any{
			"id":      res.id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         map[string]any{"content": delta},
				"finish_reason": finish,
			}},
		}
		if delta == "" {
			obj["choices"] = []map[string]any{{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": finish,
			}}
		}
		raw, _ := json.Marshal(obj)
		fmt.Fprintf(w, "data: %s\n\n", raw)
		if flusher != nil {
			flusher.Flush()
		}
	}

	// 按小块回放文本，客户端逐 token 渲染。按 rune 切，避免切碎 UTF-8 多字节字符。
	const step = 24
	runes := []rune(res.answer)
	for i := 0; i < len(runes); i += step {
		end := i + step
		if end > len(runes) {
			end = len(runes)
		}
		chunk(string(runes[i:end]), "")
	}
	chunk("", "stop")
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// parseMessages 从请求体提取 messages。
func parseMessages(body []byte) ([]chatMessage, error) {
	var m struct {
		Messages []chatMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	if len(m.Messages) == 0 {
		return nil, fmt.Errorf("messages required")
	}
	return m.Messages, nil
}

// injectSkillBody 在请求体 messages 头部插入技能 system 消息。
func injectSkillBody(body []byte, content string) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	raw, has := m["messages"]
	if !has {
		return body
	}
	var msgs []json.RawMessage
	if json.Unmarshal(raw, &msgs) != nil {
		return body
	}
	sys, err := json.Marshal(map[string]string{"role": "system", "content": content})
	if err != nil {
		return body
	}
	msgs = append([]json.RawMessage{sys}, msgs...)
	enc, _ := json.Marshal(msgs)
	m["messages"] = enc
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

func upstreamModelOf(body []byte) string {
	var d struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &d)
	return d.Model
}

func newID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
