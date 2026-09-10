package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/erishen/llm-router/internal/auth"
	"github.com/erishen/llm-router/internal/proxy"
	"github.com/erishen/llm-router/internal/quota"
	"github.com/erishen/llm-router/internal/router"
	"github.com/erishen/llm-router/internal/store"
)

// adminMux 注册管理 API（Go 1.22 起 net/http 支持方法与路径参数）。
func (s *Server) adminMux() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/admin/overview", s.admin(s.handleOverview))
	m.HandleFunc("GET /api/admin/providers", s.admin(s.handleListProviders))
	m.HandleFunc("GET /api/admin/providers/balances", s.admin(s.handleProviderBalances))
	m.HandleFunc("GET /api/admin/models/catalog", s.admin(s.handleModelsCatalog))
	m.HandleFunc("POST /api/admin/models/refresh", s.admin(s.handleRefreshModels))
	m.HandleFunc("POST /api/admin/providers", s.admin(s.handleUpsertProvider))
	m.HandleFunc("POST /api/admin/providers/probe", s.admin(s.handleProbeModels))
	m.HandleFunc("DELETE /api/admin/providers/{id}", s.admin(s.handleDeleteProvider))
	m.HandleFunc("GET /api/admin/routes", s.admin(s.handleListRoutes))
	m.HandleFunc("POST /api/admin/routes", s.admin(s.handleUpsertRoute))
	m.HandleFunc("DELETE /api/admin/routes/{model}", s.admin(s.handleDeleteRoute))
	m.HandleFunc("GET /api/admin/keys", s.admin(s.handleListKeys))
	m.HandleFunc("POST /api/admin/keys", s.admin(s.handleCreateKey))
	m.HandleFunc("POST /api/admin/keys/{id}/toggle", s.admin(s.handleToggleKey))
	m.HandleFunc("GET /api/admin/keys/{id}/plaintext", s.admin(s.handleRevealKey))
	m.HandleFunc("PATCH /api/admin/keys/{id}", s.admin(s.handleUpdateKey))
	m.HandleFunc("DELETE /api/admin/keys/{id}", s.admin(s.handleDeleteKey))
	m.HandleFunc("GET /api/admin/usage", s.admin(s.handleUsage))
	m.HandleFunc("POST /api/admin/usage/clear", s.admin(s.handleClearUsage))
	m.HandleFunc("GET /api/admin/observability/overview", s.admin(s.handleObservability))
	m.HandleFunc("GET /api/admin/health", s.admin(s.handleAdminHealth))
	m.HandleFunc("GET /api/admin/settings", s.admin(s.handleGetSettings))
	m.HandleFunc("POST /api/admin/settings", s.admin(s.handleUpdateSettings))
	m.HandleFunc("GET /api/admin/skills", s.admin(s.handleListSkills))
	m.HandleFunc("GET /api/admin/skills/{name}", s.admin(s.handleGetSkill))
	m.HandleFunc("GET /api/admin/mcps", s.admin(s.handleListMcps))
	m.HandleFunc("POST /api/admin/mcps/{name}", s.admin(s.handleUpsertMcp))
	m.HandleFunc("DELETE /api/admin/mcps/{name}", s.admin(s.handleDeleteMcp))
	m.HandleFunc("GET /api/admin/tools", s.admin(s.handleListTools))
	m.HandleFunc("POST /api/admin/tools/invoke", s.admin(s.handleInvokeTool))
	m.HandleFunc("GET /api/admin/fastpath", s.admin(s.handleListFastpath))
	m.HandleFunc("POST /api/admin/fastpath/{name}/promote", s.admin(s.handlePromoteFastpath))
	m.HandleFunc("DELETE /api/admin/fastpath/{name}", s.admin(s.handleDeleteFastpath))
	m.HandleFunc("POST /api/admin/fastpath/generate", s.admin(s.handleFastpathGenerate))
	m.HandleFunc("GET /api/admin/external-tools", s.admin(s.handleListExternalTools))
	m.HandleFunc("POST /api/admin/external-tools/{name}/adopt", s.admin(s.handleAdoptExternalTool))
	m.HandleFunc("DELETE /api/admin/external-tools/{name}", s.admin(s.handleDeleteExternalTool))
	m.HandleFunc("GET /api/admin/external-skills/candidates", s.admin(s.handleListExternalSkillCandidates))
	m.HandleFunc("GET /api/admin/external-mcps/candidates", s.admin(s.handleListExternalMcpCandidates))
	m.HandleFunc("POST /api/admin/external-mcps/suggest", s.admin(s.handleSuggestExternalMcp))
	m.HandleFunc("POST /api/admin/external-mcps/{server}/adopt", s.admin(s.handleAdoptExternalMcp))
	m.HandleFunc("GET /api/admin/sandbox/status", s.admin(s.handleSandboxStatus))
	m.HandleFunc("GET /api/admin/memory", s.admin(s.handleListMemory))
	m.HandleFunc("DELETE /api/admin/memory", s.admin(s.handleClearMemory))
	return m
}

// admin 校验管理口令（X-Admin-Token 或登录会话 X-Session-Token）。
func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		expected := s.store.Settings().AdminToken
		if tok := r.Header.Get("X-Admin-Token"); tok != "" && auth.ConstantTimeEqual(tok, expected) {
			next(w, r)
			return
		}
		if sess := r.Header.Get("X-Session-Token"); sess != "" && s.sess.Valid(sess) {
			next(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "admin token required")
	}
}

// loginFail 记录某个来源 IP 的失败登录状态，用于防暴力尝试。
type loginFail struct {
	count int
	until time.Time // 超过阈值后在此时间前拒绝该 IP
}

const (
	loginMaxFails = 5
	loginLockDur  = 10 * time.Minute
)

// keyRevealWindow 是新建 key 明文可重看的短窗口：创建后立即展示一次，
// 若页面被误关，窗口内可从管理台补看；超窗后明文彻底消失（服务端只存哈希）。
const keyRevealWindow = 2 * time.Minute

type keyReveal struct {
	plain string
	until time.Time
}

// handleAdminLogin 用管理口令换取会话 token。
func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	ip := clientIP(r)
	if !s.loginAllowed(ip) {
		writeError(w, http.StatusTooManyRequests, "too_many_attempts",
			"too many failed login attempts, try again later")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Token == "" {
		req.Token = r.Header.Get("X-Admin-Token")
	}
	if !auth.ConstantTimeEqual(req.Token, s.store.Settings().AdminToken) {
		s.loginFailed(ip)
		writeError(w, http.StatusUnauthorized, "unauthorized", "wrong admin token")
		return
	}
	s.loginOK(ip)
	writeJSON(w, http.StatusOK, map[string]any{
		"session_token": s.sess.Issue(req.Token),
		"expires_in":    int64((12 * time.Hour).Seconds()),
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) loginAllowed(ip string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	f, ok := s.loginFails[ip]
	if !ok {
		return true
	}
	if time.Now().Before(f.until) {
		return false
	}
	if f.count >= loginMaxFails {
		// 锁定期已过：复位，允许重试。
		delete(s.loginFails, ip)
	}
	return true
}

func (s *Server) loginFailed(ip string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	f := s.loginFails[ip]
	f.count++
	if f.count >= loginMaxFails {
		f.until = time.Now().Add(loginLockDur)
	}
	s.loginFails[ip] = f
}

func (s *Server) loginOK(ip string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	delete(s.loginFails, ip)
}

// newID 生成短随机 ID。
func newID() string {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// ---------- overview ----------

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	providers := s.store.ListProviders()
	healthy := 0
	for _, p := range providers {
		if p.Enabled && s.health.Available(p.ID) {
			healthy++
		}
	}
	var today quota.Agg
	for _, a := range s.rec.Daily(1) {
		today = a.Agg
	}
	var total quota.Agg
	for _, a := range s.rec.PerKey() {
		total = total.Add(a)
	}
	s.mu.RLock()
	reqs := s.requests
	s.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"version":        "1",
		"providers":      len(providers),
		"healthy":        healthy,
		"routes":         len(s.store.ListRoutes()),
		"keys":           len(s.store.ListKeys()),
		"requests_total": reqs,
		"today":          today,
		"total":          total,
	})
}

// ---------- providers ----------

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]any, 0)
	for _, p := range s.store.ListProviders() {
		out = append(out, map[string]any{
			"id": p.ID, "name": p.Name, "base_url": p.BaseURL,
			"api_key": displaySecret(p.APIKey), "models": p.Models, "headers": p.Headers,
			"enabled": p.Enabled, "weight": p.Weight, "priority": p.Priority,
			"timeout_ms": p.TimeoutMS,
			"healthy":    s.health.Available(p.ID),
			"latency_ms": s.health.Latency(p.ID),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// displaySecret 决定管理台回显 api_key 的方式：
// env: 引用本身不含密钥，原样回显方便查看配置；其余一律脱敏。
func displaySecret(k string) string {
	if strings.HasPrefix(k, store.EnvKeyPrefix) {
		return k
	}
	return maskSecret(k)
}

// compact 压缩一段文本用于错误消息：去换行、截断。
func compact(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// handleProviderBalances 并行查询所有 Provider 已存 Key 的账户余额/额度，
// 供独立额度页面使用；无 Key 或查询失败不阻塞其余项。
// mock-local（一键 Mock 联调）无真实额度概念，直接排除。
func (s *Server) handleProviderBalances(w http.ResponseWriter, r *http.Request) {
	provs := s.store.ListProviders()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	type item struct {
		ID      string         `json:"id"`
		Name    string         `json:"name"`
		ProbeAt string         `json:"probe_at,omitempty"`
		Balance map[string]any `json:"balance,omitempty"`
		Error   string         `json:"error,omitempty"`
	}
	out := make([]item, 0, len(provs))
	for _, p := range provs {
		if p.ID == "mock-local" {
			continue
		}
		it := item{ID: p.ID, Name: p.Name}
		if !p.ProbeAt.IsZero() {
			it.ProbeAt = p.ProbeAt.Format(time.RFC3339)
		}
		out = append(out, it)
	}
	var wg sync.WaitGroup
	for i := range out {
		p, ok := s.store.GetProvider(out[i].ID)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(i int, p store.Provider) {
			defer wg.Done()
			key := p.ResolvedAPIKey()
			if key == "" {
				out[i].Error = "no_key"
				return
			}
			bal := s.probeBalance(ctx, strings.TrimRight(p.BaseURL, "/"), key)
			if bal == nil {
				out[i].Error = "unavailable"
				return
			}
			out[i].Balance = bal
		}(i, p)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"balances": out})
}

// parseOpenRouterKey: {"data":{"label":"…","limit":null,"limit_remaining":null,"usage":0,
// "is_free_tier":true,"expires_at":"…","rate_limit":{…}}}
// OpenRouter 无充值余额概念，额度信息来自 auth/key：已用 usage、上限 limit（null=无上限）、免费层标记。
func parseOpenRouterKey(body []byte) map[string]any {
	var raw struct {
		Data struct {
			Usage         float64  `json:"usage"`
			Limit         *float64 `json:"limit"`
			LimitRemaining *float64 `json:"limit_remaining"`
			IsFreeTier    bool     `json:"is_free_tier"`
			ExpiresAt     string   `json:"expires_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	d := raw.Data
	if d.Usage == 0 && d.Limit == nil && d.LimitRemaining == nil && !d.IsFreeTier && d.ExpiresAt == "" {
		return nil
	}
	m := map[string]any{
		"kind":         "openrouter",
		"usage":        d.Usage,
		"is_free_tier": d.IsFreeTier,
	}
	if d.Limit != nil {
		m["limit"] = *d.Limit
	}
	if d.LimitRemaining != nil {
		m["limit_remaining"] = *d.LimitRemaining
	}
	if d.ExpiresAt != "" {
		m["expires_at"] = d.ExpiresAt
	}
	return m
}

// parseOpenRouterCredits: {"data":{"total_credits":0,"total_usage":0}}
// OpenRouter 新接口兜底（与 /auth/key 二选一，命中即返回）。
func parseOpenRouterCredits(body []byte) map[string]any {
	var raw struct {
		Data struct {
			TotalCredits float64 `json:"total_credits"`
			TotalUsage   float64 `json:"total_usage"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.Data.TotalCredits == 0 && raw.Data.TotalUsage == 0 {
		return nil
	}
	return map[string]any{
		"kind":          "openrouter",
		"total_credits": raw.Data.TotalCredits,
		"total_usage":   raw.Data.TotalUsage,
	}
}

// probeAliBailianLimits 查询阿里云百炼限流配额（GET /api/v1/models/limits），
// 返回前 3 个模型配额摘要；免费额度剩余量无公开 API，由 note 指引控制台。
func (s *Server) probeAliBailianLimits(ctx context.Context, baseURL, key string) map[string]any {
	host := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/compatible-mode/v1")
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx2, http.MethodGet, host+"/api/v1/models/limits?page_size=100", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var raw struct {
		Output struct {
			Quotas []struct {
				Model      string `json:"model"`
				ModelLimit struct {
					UsageLimit       *int64 `json:"usage_limit"`
					UsageLimitPeriod *int64 `json:"usage_limit_period"`
				} `json:"model_limit"`
			} `json:"quotas"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	parts := make([]string, 0, 3)
	for _, q := range raw.Output.Quotas {
		if q.ModelLimit.UsageLimit != nil && q.ModelLimit.UsageLimitPeriod != nil {
			parts = append(parts, fmt.Sprintf("%s %d tokens/%d", q.Model, *q.ModelLimit.UsageLimit, *q.ModelLimit.UsageLimitPeriod))
		}
		if len(parts) >= 3 {
			break
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return map[string]any{
		"kind":  "platform_note",
		"plan":  "阿里云百炼",
		"quota": "限流配额：" + strings.Join(parts, " · "),
		"note":  "免费额度剩余量无公开 API，请在百炼控制台「免费额度」页查看；此为限流配额（每周期可调用 tokens 上限）",
		"url":   "https://bailian.console.aliyun.com",
	}
}

// isProbeRetryable 判断探测失败是否属于可重试的瞬时连接错误
//（unexpected EOF、连接重置、超时等），避免对 4xx/业务错误做无意义重试。
func isProbeRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, kw := range []string{"eof", "connection reset", "broken pipe", "connection refused"} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// agnesPricing 是 agnes 官方文本模型单价（$/1M tokens，输入/输出）。
// 来源：https://wiki.agnes-ai.com/en/docs/pricing（官方定价页，2026-09-09 抓取）。
// agnes 的 /v1/models 不返回价格字段，探测时按模型 id 补齐；价格变动需随官方页更新。
// 图像/视频模型按张/秒计费，非 token 计费，不在此表。
var agnesPricing = map[string][2]string{
	"agnes-2.0-flash":     {"0", "0"},
	"agnes-2.5-flash":     {"0", "0"},
	"agnes-2.5-pro":       {"0.45", "0.90"},
	"agnes-2.5-pro-alpha": {"0.45", "0.90"},
	"agnes-2.5-pro-beta":  {"0.10", "0.30"},
}

// modelMeta 描述一个模型的类别与用途（模型目录页展示）。
// Category: text（文本对话/编码）| vision（图像理解）| image（图像生成）| video | audio | embedding | other。
// 用途说明来自各模型官方/OpenRouter 目录（2026-09-09 整理）；新模型缺条目时按 id 关键词推断。
type modelMeta struct {
	Category string
	Purpose  string
	Ctx      string // 上下文窗口，未披露为空
}

// modelCatalog 静态模型知识表：id → 类别/用途/上下文。
// 来源：各平台官方文档 + OpenRouter 模型目录（2026-09-09 核验）。
var modelCatalog = map[string]modelMeta{
	// agnes（官方定价页 + FAQ）
	"agnes-2.0-flash":     {"text", "通用问答/客服/知识库/轻量编码，官方无限期免费", "256K"},
	"agnes-2.5-flash":     {"text", "免费通用增强版，编码/Agent 能力强（SWE 出色）", "512K"},
	"agnes-2.5-pro":       {"text", "复杂推理/深度编码/长任务，付费旗舰（$0.45/$0.90）", ""},
	"agnes-2.5-pro-alpha": {"text", "复杂推理（预览线，同 pro 定价）", ""},
	"agnes-2.5-pro-beta":  {"text", "复杂推理（beta 低价线 $0.10/$0.30）", ""},
	"agnes-3.0-flash":     {"text", "新一代 flash 模型（官方定价页暂未收录，用途待确认）", ""},
	"agnes-image-2.0-flash":  {"image", "图像生成（官方免费）", ""},
	"agnes-image-2.1-flash":  {"image", "图像生成增强版（官方免费）", ""},
	"agnes-image-2.5-flash":  {"image", "图像生成（新版本）", ""},
	"agnes-video-v2.0":       {"video", "视频生成（官方免费）", ""},
	"agnes-video-2.5":        {"video", "高清视频生成（按秒计费 $0.025/s 起）", ""},
	"agnes-video-2.5-flash":  {"video", "视频生成（同 2.5 公式，限时免费）", ""},
	// kimi / Moonshot
	"kimi-k2.7-code":  {"text", "编程/代码库理解/Agent 编程，支持图文视频输入（官方主打 Coding）", "256K"},
	"kimi-k2.6":       {"text", "通用对话/编码/推理（K2 系列）", "256K"},
	// DeepSeek
	"deepseek-v4-flash":            {"text", "通用文本/编码（V4 快速高性价比线）", "1M"},
	"deepseek-v4-flash-vision-exp": {"vision", "图像理解/OCR/图表分析，多模态 Agent（实验版，按文本价计费）", "1M"},
	// OpenRouter 免费层（用户配置）
	"inclusionai/ling-3.0-flash-fin:free":    {"text", "日常对话/起草（Ling 3.0 flash 免费线）", ""},
	"inclusionai/ling-3.0-flash-sante:free":  {"text", "日常对话/起草（Ling 3.0 flash 免费线）", ""},
	"liquid/lfm-2.5-2.6b:free":               {"text", "小型推理：Agent 工作流/数据抽取/RAG/长文本（官方不建议用于编码）", ""},
	"nex-agi/nex-n2.5-mini:free":             {"text", "通用对话/推理（免费 mini 线）", ""},
	"nex-agi/nex-n2.5-pro:free":              {"text", "通用对话/推理（免费 pro 线）", ""},
	"nvidia/nemotron-3-nano-omni-30b-a3b-reasoning:free": {"text", "轻量多模态推理（omni 系列 nano）", ""},
	"nvidia/nemotron-3-super-120b-a12b:free": {"text", "综合最强的免费模型之一：数学/推理强、速度快、长上下文", "1M"},
	"nvidia/nemotron-3-ultra-550b-a55b:free": {"text", "编码 Agent/深度研究/复杂推理/规划（免费旗舰）", "1M"},
	"nvidia/nemotron-3.5-content-safety:free": {"other", "内容安全分类/审核专用", ""},
	"nvidia/nemotron-3.5-lightning:free":      {"text", "快速响应/轻量任务（3.5 lightning）", ""},
	"openrouter/free":                         {"text", "OpenRouter 自动路由：把请求分发到可用免费模型", ""},
	"poolside/laguna-xs-2.1:free":             {"text", "软件工程 Agent 编码（小号）", ""},
	"poolside/laguna-s-2.1:free":              {"text", "软件工程 Agent 编码（标准）", ""},
	"cohere/north-mini-code:free":             {"text", "轻量编码/代码补全（North Mini Code）", ""},
	"dots-studio/dots-3-note-preview:free":    {"text", "笔记/长文档整理（Dots 3）", ""},
	"google/gemma-4-26b-a4b-it:free":          {"text", "通用对话/指令（Google Gemma 4）", ""},
	"google/gemma-4-31b-it:free":              {"text", "通用对话/指令（Google Gemma 4）", ""},
	"google/lyria-3-clip-preview":             {"audio", "音频/音乐生成（Lyria 3）", ""},
	"google/lyria-3-pro-preview":              {"audio", "音频/音乐生成高级版（Lyria 3）", ""},
	"thinkingmachines/inkling-small:free":     {"text", "通用推理/编码/Agent（975B MoE 小号）", "1M"},
	"thinkingmachines/inkling:free":           {"text", "通用推理/编码/Agent/多模态（975B MoE，41B 激活）", "1M"},
}

// inferModelMeta 对知识表未收录的模型按 id 关键词推断类别与用途。
func inferModelMeta(id string) modelMeta {
	lower := strings.ToLower(id)
	switch {
	case strings.Contains(lower, "image") || strings.Contains(lower, "dall-e") || strings.Contains(lower, "flux"):
		return modelMeta{"image", "图像生成", ""}
	case strings.Contains(lower, "video"):
		return modelMeta{"video", "视频生成", ""}
	case strings.Contains(lower, "lyria") || strings.Contains(lower, "audio") || strings.Contains(lower, "music") || strings.Contains(lower, "tts"):
		return modelMeta{"audio", "音频/语音生成", ""}
	case strings.Contains(lower, "embed"):
		return modelMeta{"embedding", "向量嵌入/检索", ""}
	case strings.Contains(lower, "vision") || strings.Contains(lower, "omni") || strings.Contains(lower, "vl"):
		return modelMeta{"vision", "图像/多模态理解", ""}
	case strings.Contains(lower, "safety") || strings.Contains(lower, "moder") || strings.Contains(lower, "guard"):
		return modelMeta{"other", "内容审核/安全分类", ""}
	case strings.Contains(lower, "reason") || strings.Contains(lower, "think"):
		return modelMeta{"text", "推理增强模型", ""}
	case strings.Contains(lower, "code") || strings.Contains(lower, "coder") || strings.Contains(lower, "agent"):
		return modelMeta{"text", "编码/Agent 方向", ""}
	default:
		return modelMeta{"text", "通用对话/生成", ""}
	}
}

// handleModelsCatalog 汇总所有 Provider 已配置的模型：合并去重、归类（文本/视觉/图像/视频/音频/嵌入/其他）、
// 附用途/上下文/免费/定价信息，供「模型目录」页展示。
func (s *Server) handleModelsCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.buildCatalog())
}

// baiFreeModels 是 B.AI 免费阵容（官方 2026-08-29 公布：GLM-5.3-Flash、DeepSeek-V4-Flash、
// DeepSeek-V4-Flash-Vision-Exp、Hy3、MiMo-V2.5、Qwen3.8-Flash 六大旗舰免费开放）。
// B.AI 的 /v1/models 不带 is_free/pricing 字段，探测无法自动识别；免费名单有时效，以官方公告为准。
var baiFreeModels = map[string]bool{
	"glm-5.3-flash": true, "deepseek-v4-flash": true, "deepseek-v4-flash-vision-exp": true,
	"hy3": true, "mimo-v2.5": true, "qwen3.8-flash": true,
}

// markFreeByProvider 按 Provider 免费名单修正探测结果：
//   - bai：官方免费阵容（上游 /v1/models 不带 is_free 字段，探测无法自动识别）；
//   - alibailian：用户确认当前已配置模型均有免费额度（上游同样不带免费字段），
//     把「已配置且探测返回」的模型标记免费，计费与目录两端自动一致。
func markFreeByProvider(p store.Provider, models []map[string]any) []map[string]any {
	configured := map[string]bool{}
	for _, id := range p.Models {
		configured[id] = true
	}
	for _, m := range models {
		id, _ := m["id"].(string)
		if id == "" {
			continue
		}
		switch {
		case p.ID == "bai" && baiFreeModels[id]:
			m["free"] = true
		case p.ID == "alibailian" && configured[id]:
			m["free"] = true
		}
	}
	return models
}

// handleRefreshModels 并行探测所有已配置 Key 的 Provider（非 mock-local），刷新模型快照后返回目录。
// 单个 Provider 失败不阻塞；providers 字段返回每个 Provider 的探测结果（ok / 错误摘要）。
func (s *Server) handleRefreshModels(w http.ResponseWriter, r *http.Request) {
	providers := s.store.ListProviders()
	statuses := make(map[string]string, len(providers))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range providers {
		if p.ID == "mock-local" || p.APIKey == "" {
			continue
		}
		wg.Add(1)
		go func(p store.Provider) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			out, status, body, err := s.probeModelsOnce(ctx, p.BaseURL, p.ResolvedAPIKey())
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				statuses[p.ID] = "探测失败: " + err.Error()
				return
			}
			if status != 0 {
				statuses[p.ID] = fmt.Sprintf("上游返回 %d: %s", status, compact(string(body)))
				return
			}
			out = markFreeByProvider(p, out)
			s.saveProbeSnapshot(p.BaseURL, p.APIKey, out)
			statuses[p.ID] = "ok"
		}(p)
	}
	wg.Wait()
	resp := s.buildCatalog()
	resp["providers"] = statuses
	writeJSON(w, http.StatusOK, resp)
}

// buildCatalog 聚合目录（快照优先于静态知识表），返回响应体（含 models 与最近探测时间）。
// 同一模型在不同 Provider 的免费/价格可能不同（如 deepseek-v4-flash 在官方付费、商汤免费），
// 因此按 Provider×模型展开为独立行，不合并去重。
func (s *Server) buildCatalog() map[string]any {
	providers := s.store.ListProviders()
	type item struct {
		ID            string   `json:"id"`
		Provider      string   `json:"provider"`
		Category      string   `json:"category"`
		Purpose       string   `json:"purpose"`
		Ctx           string   `json:"context,omitempty"`
		ContextLength int      `json:"context_length,omitempty"`
		Free          bool     `json:"free"`
		Pricing       *struct {
			Prompt     string `json:"prompt"`
			Completion string `json:"completion"`
		} `json:"pricing,omitempty"`
		// Unavailable 非空表示该模型曾在上游 404（model not found），冷却期内标灰、路由跳过。
		Unavailable string `json:"unavailable,omitempty"`
	}
	out := make([]*item, 0)
	var latestProbe time.Time
	for _, p := range providers {
		if p.ID == "mock-local" {
			continue // 本地联调 mock 不进目录
		}
		// 该 Provider 最近一次探测快照（免费/价格/上下文以上游实时返回为准，会随时间变）。
		probeByID := map[string]store.ProbeModel{}
		for _, pm := range p.ProbeModels {
			probeByID[pm.ID] = pm
		}
		if p.ProbeAt.After(latestProbe) {
			latestProbe = p.ProbeAt
		}
		for _, id := range p.Models {
			if id == "" {
				continue
			}
			meta, has := modelCatalog[id]
			if !has {
				meta = inferModelMeta(id)
			}
			it := &item{ID: id, Provider: p.ID, Category: meta.Category, Purpose: meta.Purpose, Ctx: meta.Ctx}
			// 曾 404 的模型：冷却期内标灰，路由自动跳过。
			if reason, unavail := s.store.ModelUnavailable(p.ID, id); unavail {
				it.Unavailable = reason
			}
			// 快照覆盖：该 Provider 最近一次探测的 context_length/free/pricing 优先于静态表。
			if pm, ok2 := probeByID[id]; ok2 {
				it.ContextLength = pm.ContextLength
				it.Free = pm.Free
				if pm.Pricing != nil {
					it.Pricing = &struct {
						Prompt     string `json:"prompt"`
						Completion string `json:"completion"`
					}{pm.Pricing.Prompt, pm.Pricing.Completion}
				}
			} else if p.ID == "agnes" {
				// agnes 官方定价静态表（仅 agnes Provider 适用，防止定价串到同 id 的其他 Provider）。
				if pr, ok2 := agnesPricing[id]; ok2 {
					it.Pricing = &struct {
						Prompt     string `json:"prompt"`
						Completion string `json:"completion"`
					}{pr[0], pr[1]}
					it.Free = pr[0] == "0" && pr[1] == "0"
				}
			} else if p.ID == "sensenova" {
				// SenseNova Token Plan 免费公测：其全部模型免费（自研 1500 次/5h、DeepSeek V4 Flash 500 次/5h）。
				it.Free = true
			} else if p.ID == "bai" && baiFreeModels[id] {
				// B.AI 免费阵容（探测无 is_free 字段时的静态兜底）。
				it.Free = true
			} else if p.ID == "alibailian" {
				// 阿里云百炼：上游 /v1/models 不带免费字段，当前已配置模型均确认有免费额度。
				it.Free = true
			} else if strings.HasSuffix(id, ":free") {
				// OpenRouter :free 后缀约定（id 层面即表示免费）。
				it.Free = true
			}
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Provider < out[j].Provider
	})
	resp := map[string]any{"models": out}
	if !latestProbe.IsZero() {
		resp["probe_at"] = latestProbe.Format(time.RFC3339)
	}
	return resp
}

// handleProbeModels 用给定的 base_url + API Key 探测上游 /v1/models，返回模型 id 列表（去重排序）。
// 用于管理台「按 Key 查询模型」：Key 支持 env: 引用，脱敏回显值（含省略号）不参与探测。
func (s *Server) handleProbeModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	req.BaseURL = strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if req.BaseURL == "" || (!strings.HasPrefix(req.BaseURL, "http://") && !strings.HasPrefix(req.BaseURL, "https://")) {
		writeError(w, http.StatusBadRequest, "bad_request", "base_url 必须是合法的 http(s) 地址")
		return
	}
	if strings.Contains(req.APIKey, store.MaskedSecretMarker) {
		writeError(w, http.StatusBadRequest, "bad_request", "API Key 是脱敏回显值，无法用于探测；请重新输入真实 Key（或填 env: 引用）")
		return
	}
	key := store.Provider{APIKey: req.APIKey}.ResolvedAPIKey()

	out, status, body, err := s.probeModelsOnce(r.Context(), req.BaseURL, key)
	if err != nil {
		note := ""
		if strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "reset") {
			note = "（上游连接被中断，已自动重试 1 次仍失败；请检查网络或本地代理 127.0.0.1:7897 是否稳定）"
		}
		writeError(w, http.StatusBadGateway, "upstream_unavailable", "无法连接上游: "+err.Error()+note)
		return
	}
	if status != 0 {
		if status == http.StatusUnauthorized {
			if key == "" {
				writeError(w, http.StatusBadRequest, "probe_failed",
					"上游要求鉴权但本次探测未携带 API Key（编辑已有 Provider 时 Key 为脱敏值，需重新输入完整 Key 或 env: 引用）；上游返回: "+compact(string(body)))
			} else {
				writeError(w, http.StatusBadRequest, "probe_failed",
					"上游拒绝了该 API Key（401），请检查 Key 是否完整有效；上游返回: "+compact(string(body)))
			}
			return
		}
		writeError(w, http.StatusBadRequest, "probe_failed",
			fmt.Sprintf("上游返回 %d: %s", status, compact(string(body))))
		return
	}
	result := map[string]any{"models": out}
	if bal := s.probeBalance(r.Context(), req.BaseURL, key); bal != nil {
		result["balance"] = bal
	}
	// 探测成功后把模型快照写回匹配的 Provider（目录页用最新免费/价格/上下文，会随时间变）。
	s.saveProbeSnapshot(req.BaseURL, req.APIKey, out)
	writeJSON(w, http.StatusOK, result)
}

// probeModelsOnce 探测上游 /v1/models（12s 单次超时，连接类瞬断/响应体截断自动重试 1 次），
// 返回去重排序的模型信息列表（含定价补齐与免费判定）。
// 返回约定：网络/解析错误 → err；上游非 200 → (nil, statusCode, body, nil)；成功 → (out, 0, nil, nil)。
func (s *Server) probeModelsOnce(ctx context.Context, baseURL, key string) ([]map[string]any, int, []byte, error) {
	probeOnce := func() (*http.Response, []byte, error) {
		ctx2, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		upReq, err := http.NewRequestWithContext(ctx2, http.MethodGet, baseURL+"/models", nil)
		if err != nil {
			return nil, nil, err
		}
		if key != "" {
			upReq.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(upReq)
		if err != nil {
			return nil, nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, nil, readErr
		}
		return resp, body, nil
	}
	resp, body, err := probeOnce()
	if err != nil && isProbeRetryable(err) {
		resp, body, err = probeOnce()
	}
	if err != nil {
		return nil, 0, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, body, nil
	}
	var payload struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int64  `json:"context_length"`
			IsFree        *bool  `json:"is_free"`
			Free          *bool  `json:"free"`
			Pricing       *struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, 0, nil, fmt.Errorf("invalid_models: %w", err)
	}
	seen := map[string]bool{}
	out := make([]map[string]any, 0, len(payload.Data))
	for _, m := range payload.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		info := map[string]any{"id": id}
		if m.ContextLength > 0 {
			info["context_length"] = m.ContextLength
		}
		// 单价（$/1M tokens）：OpenRouter 类平台在 models 响应带 pricing 字段；
		// agnes 等平台不返回，则用官方定价表按模型 id 补齐（免费模型同时标 free）。
		if m.Pricing != nil {
			info["pricing"] = map[string]string{
				"prompt":     m.Pricing.Prompt,
				"completion": m.Pricing.Completion,
			}
		} else if pr, ok := agnesPricing[id]; ok {
			info["pricing"] = map[string]string{"prompt": pr[0], "completion": pr[1]}
			if pr[0] == "0" && pr[1] == "0" {
				info["free"] = true
			}
		}
		// 免费判定：显式 is_free/free 字段，或 pricing 全 0。
		free := false
		switch {
		case m.IsFree != nil:
			free = *m.IsFree
		case m.Free != nil:
			free = *m.Free
		case m.Pricing != nil:
			free = m.Pricing.Prompt == "0" && m.Pricing.Completion == "0"
		}
		if free {
			info["free"] = true
		}
		out = append(out, info)
	}
	// 排序：FREE 模型在前，其余按 id 字典序（免费模型更常用，置顶便于选择）。
	sort.Slice(out, func(i, j int) bool {
		fi, _ := out[i]["free"].(bool)
		fj, _ := out[j]["free"].(bool)
		if fi != fj {
			return fi
		}
		return out[i]["id"].(string) < out[j]["id"].(string)
	})
	return out, 0, nil, nil
}

// saveProbeSnapshot 将一次成功探测的模型快照写回 base_url 与 Key 都匹配的 Provider（含 ProbeAt），
// 供模型目录页展示上游最新免费/价格/上下文；无匹配 Provider 时静默跳过。
// 同时匹配 Key：同一 base_url 可能挂多个 Provider（不同 Key），快照必须归属正确那个。
func (s *Server) saveProbeSnapshot(baseURL, apiKey string, models []map[string]any) {
	norm := strings.TrimRight(baseURL, "/")
	probeKey := store.Provider{APIKey: apiKey}.ResolvedAPIKey()
	for _, p := range s.store.ListProviders() {
		if strings.TrimRight(p.BaseURL, "/") != norm {
			continue
		}
		if p.ResolvedAPIKey() != probeKey {
			continue
		}
		pm := make([]store.ProbeModel, 0, len(models))
		for _, m := range models {
			item := store.ProbeModel{ID: m["id"].(string)}
			if v, ok := m["context_length"].(int64); ok {
				item.ContextLength = int(v)
			}
			item.Free, _ = m["free"].(bool)
			if pr, ok := m["pricing"].(map[string]string); ok {
				item.Pricing = &store.Pricing{Prompt: pr["prompt"], Completion: pr["completion"]}
			}
			pm = append(pm, item)
		}
		p.ProbeAt = time.Now()
		p.ProbeModels = pm
		_ = s.store.UpsertProvider(p) // 快照失败不阻塞探测响应
		return
	}
}

// probeBalance 尝试用同一 Key 查询上游账户余额/额度（token 可使用总量），
// 依次探测 Moonshot / DeepSeek / OpenAI 三种格式，命中即返回；失败静默。
func (s *Server) probeBalance(ctx context.Context, baseURL, key string) map[string]any {
	if key == "" {
		return nil
	}
	// 无公开余额接口的平台（如 SenseNova Token Plan、TokenRouter）：
	// 实测常见余额端点全 404，额度只在各自控制台/Dashboard 查看。
	// 这里返回官方公开的配额/平台信息（非实时余额），让额度页不至于空白。
	type platformNote struct{ host, plan, quota, reset, note, url string }
	platforms := []platformNote{
		{"token.sensenova.cn", "免费公测",
			"每5小时 1500 次调用（自研）/ 500 次（DeepSeek V4 Flash）",
			"每5小时独立刷新，无一次性总量限制",
			"无公开余额接口，剩余次数请在商汤控制台查看",
			"https://platform.sensenova.cn"},
		{"tokenrouter.com", "免费/低价模型聚合", "", "",
			"无公开余额接口，余额请在 TokenRouter Dashboard 查看",
			"https://www.tokenrouter.com"},
	}
	for _, pn := range platforms {
		if strings.Contains(baseURL, pn.host) {
			return map[string]any{
				"kind":   "platform_note",
				"plan":   pn.plan,
				"quota":  pn.quota,
				"reset":  pn.reset,
				"note":   pn.note,
				"url":    pn.url,
			}
		}
	}
	// 阿里云百炼（专属 endpoint …maas.aliyuncs.com）：免费额度剩余量无公开 API（仅控制台「免费额度」页可见），
	// 唯一可自动获取的是 /api/v1/models/limits 限流配额（每周期可调用 tokens 上限）。
	if strings.Contains(baseURL, "maas.aliyuncs.com") {
		if m := s.probeAliBailianLimits(ctx, baseURL, key); m != nil {
			return m
		}
	}
	type probe struct {
		path  string
		parse func([]byte) map[string]any
	}
	probes := []probe{
		{"/users/me/balance", parseMoonshotBalance},
		{"/user/balance", parseDeepSeekBalance},
		{"/dashboard/billing/subscription", parseOpenAISubscription},
		{"/dashboard/billing/usage", parseOpenAIUsage},
		{"/auth/key", parseOpenRouterKey},
		{"/credits", parseOpenRouterCredits},
	}
	for _, p := range probes {
		ctx2, cancel := context.WithTimeout(ctx, 3*time.Second)
		upReq, err := http.NewRequestWithContext(ctx2, http.MethodGet, baseURL+p.path, nil)
		if err != nil {
			cancel()
			continue
		}
		upReq.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(upReq)
		cancel()
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				resp.Body.Close()
			}
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if m := p.parse(body); m != nil {
			return m
		}
	}
	return nil
}

// parseMoonshotBalance: {"data":{"available_balance":14.99,"voucher_balance":14.99,"cash_balance":0}}
func parseMoonshotBalance(body []byte) map[string]any {
	var raw struct {
		Data struct {
			Available float64 `json:"available_balance"`
			Voucher   float64 `json:"voucher_balance"`
			Cash      float64 `json:"cash_balance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.Data.Available == 0 && raw.Data.Voucher == 0 && raw.Data.Cash == 0 {
		return nil
	}
	return map[string]any{
		"kind":     "moonshot",
		"available": raw.Data.Available,
		"voucher":   raw.Data.Voucher,
		"cash":      raw.Data.Cash,
	}
}

// flexFloat 兼容 JSON 字符串与数字两种表示（DeepSeek 余额字段是 "51.75" 字符串）。
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return err
		}
		*f = flexFloat(v)
		return nil
	}
	var n float64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexFloat(n)
	return nil
}

// parseDeepSeekBalance: {"balance_infos":[{"currency":"CNY","total_balance":"51.75","granted_balance":"0","topped_up_balance":"51.75"}]}
func parseDeepSeekBalance(body []byte) map[string]any {
	var raw struct {
		BalanceInfos []struct {
			Currency       string    `json:"currency"`
			TotalBalance   flexFloat `json:"total_balance"`
			GrantedBalance flexFloat `json:"granted_balance"`
			ToppedUp       flexFloat `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || len(raw.BalanceInfos) == 0 {
		return nil
	}
	b := raw.BalanceInfos[0]
	if b.TotalBalance == 0 && b.GrantedBalance == 0 && b.ToppedUp == 0 {
		return nil
	}
	return map[string]any{
		"kind":      "deepseek",
		"currency":  b.Currency,
		"total":     float64(b.TotalBalance),
		"granted":   float64(b.GrantedBalance),
		"topped_up": float64(b.ToppedUp),
	}
}

// parseOpenAISubscription: {"hard_limit_usd":120,"system_hard_limit_usd":120}
// 部分聚合平台（如 agnes）返回 1e8 量级占位值，视为未提供真实额度。
func parseOpenAISubscription(body []byte) map[string]any {
	var raw struct {
		HardLimitUSD float64 `json:"hard_limit_usd"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.HardLimitUSD <= 0 || raw.HardLimitUSD >= 1e7 {
		return nil
	}
	return map[string]any{"kind": "openai", "hard_limit_usd": raw.HardLimitUSD}
}

// parseOpenAIUsage: {"total_usage":123.45}（单位 0.01 USD）
func parseOpenAIUsage(body []byte) map[string]any {
	var raw struct {
		TotalUsage float64 `json:"total_usage"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.TotalUsage == 0 {
		return nil
	}
	return map[string]any{"kind": "openai", "total_usage_usd": raw.TotalUsage / 100}
}

func (s *Server) handleUpsertProvider(w http.ResponseWriter, r *http.Request) {
	var p store.Provider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if p.ID == "" {
		p.ID = slugify(p.Name)
	}
	if p.ID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "id or name is required")
		return
	}
	if p.BaseURL == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "base_url is required")
		return
	}
	if err := s.store.UpsertProvider(p); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": p.ID})
}

func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteProvider(id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- routes ----------

func (s *Server) handleListRoutes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"routes": s.store.ListRoutes()})
}

func (s *Server) handleUpsertRoute(w http.ResponseWriter, r *http.Request) {
	var rt store.Route
	if err := json.NewDecoder(r.Body).Decode(&rt); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if err := s.store.UpsertRoute(rt); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": rt.Model})
}

func (s *Server) handleDeleteRoute(w http.ResponseWriter, r *http.Request) {
	model := r.PathValue("model")
	if model == "_default" {
		model = "" // 通配兜底路由（空 model）用 _default 哨兵标识
	}
	if err := s.store.DeleteRoute(model); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- keys ----------

// topTools 把工具计数 map 转成按次数降序的名字列表（前 n 个）。
func topTools(m map[string]int, n int) []string {
	type kv struct{ k string; v int }
	all := make([]kv, 0, len(m))
	for k, v := range m {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].v > all[j].v })
	out := make([]string, 0, min(n, len(all)))
	for i, it := range all {
		if i >= n {
			break
		}
		out = append(out, it.k)
	}
	return out
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	rpm := s.limiter.Snapshot()
	per := s.rec.PerKey()
	out := make([]map[string]any, 0)
	for _, k := range s.store.ListKeys() {
		agg := per[k.ID]
		out = append(out, map[string]any{
			"id": k.ID, "name": k.Name, "prefix": k.Prefix, "enabled": k.Enabled,
			"models": k.Models, "quota": k.Quota,
			"created_at":    k.CreatedAt,
			"expires_at":    k.ExpiresAt,
			"inject_skills": k.InjectSkills,
			"usage":         agg,
			"rpm_current":   rpm[k.ID],
			// 新建 key 的 2 分钟窗口内为 true，前端展示「补看」入口。
			"revealable":    s.revealable(k.ID),
			// 工具归因：该调用方实际执行过 + 声明过的工具（按次数降序）。
			"tools_used":     topTools(s.rec.KeyTools(k.ID), 5),
			"tools_declared": topTools(s.rec.KeyClientTools(k.ID), 5),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string      `json:"name"`
		Models       []string    `json:"models"`
		Quota        store.Quota `json:"quota"`
		ExpiresIn    int64       `json:"expires_in_seconds"`
		InjectSkills string      `json:"inject_skills"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	plaintext, hash, display, err := auth.Generate()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// 默认注入技能清单：新签发的 key 天然带技能库上下文（可后续通过重新签发调整）。
	if req.InjectSkills == "" {
		req.InjectSkills = "list"
	}
	k := store.APIKey{
		ID:           newID(),
		Name:         req.Name,
		Prefix:       display,
		Hash:         hash,
		Enabled:      true,
		Models:       req.Models,
		Quota:        req.Quota,
		CreatedAt:    time.Now(),
		InjectSkills: req.InjectSkills,
	}
	if req.ExpiresIn > 0 {
		k.ExpiresAt = time.Now().Add(time.Duration(req.ExpiresIn) * time.Second)
	}
	if k.Name == "" {
		k.Name = "key-" + k.ID
	}
	if err := s.store.AddKey(k); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	// ⚠️ 明文只在创建响应与短窗口内可重看，之后只能靠哈希校验。
	s.revealMu.Lock()
	s.revealMap[k.ID] = keyReveal{plain: plaintext, until: time.Now().Add(keyRevealWindow)}
	s.revealMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "id": k.ID, "key": plaintext, "prefix": display,
		"warning": "请立即保存该 key；创建后 2 分钟内可从管理台补看，超窗后服务端只保存哈希",
	})
}

// revealable 报告某个 key 是否仍在新签发的明文可重看窗口内。
func (s *Server) revealable(id string) bool {
	s.revealMu.Lock()
	defer s.revealMu.Unlock()
	rv, ok := s.revealMap[id]
	if !ok {
		return false
	}
	if time.Now().After(rv.until) {
		delete(s.revealMap, id)
		return false
	}
	return true
}

// handleRevealKey 在短窗口内重看新建 key 的明文（误关页面兜底）。
// 只对创建后未过窗口的 key 生效；已查看/已过期/老 key 一律 404。
func (s *Server) handleRevealKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.revealMu.Lock()
	rv, ok := s.revealMap[id]
	if ok && time.Now().After(rv.until) {
		delete(s.revealMap, id)
		ok = false
	}
	s.revealMu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "not_found",
			"key 明文仅创建后 2 分钟内可重看，且只显示一次；服务端只保存哈希")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "key": rv.plain})
}

func (s *Server) handleToggleKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := s.store.SetKeyEnabled(r.PathValue("id"), req.Enabled); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": req.Enabled})
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteKey(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleUpdateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string      `json:"name"`
		Models       []string    `json:"models"`
		Quota        store.Quota `json:"quota"`
		InjectSkills string      `json:"inject_skills"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if err := s.store.UpdateKey(r.PathValue("id"), req.Name, req.Models, req.Quota, req.InjectSkills); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- usage ----------

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &days); err != nil || days <= 0 {
			days = 7
		}
		if days > 90 {
			days = 90
		}
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &limit); err != nil || limit <= 0 {
			limit = 50
		}
		if limit > 500 {
			limit = 500
		}
	}
	byModel := s.rec.ByModel()
	models := make([]map[string]any, 0, len(byModel))
	for m, a := range byModel {
		models = append(models, map[string]any{"model": m, "usage": a})
	}
	perKey := s.rec.PerKey()
	keys := make([]map[string]any, 0, len(perKey))
	for k, a := range perKey {
		keys = append(keys, map[string]any{"key_id": k, "usage": a})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"days":   s.rec.Daily(days),
		"models": models,
		"keys":   keys,
		"recent": s.rec.Recent(limit),
	})
}

// handleClearUsage 清空全部用量流水（DELETE 语义用 POST 便于前端一键触发）。
func (s *Server) handleClearUsage(w http.ResponseWriter, r *http.Request) {
	if err := s.rec.Clear(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cleared": true})
}

// ---------- observability ----------

// aggView 是带派生指标的用量聚合视图（平均延迟、错误率）。
type aggView struct {
	quota.Agg
	AvgLatencyMS int64   `json:"avg_latency_ms"`
	ErrorRate    float64 `json:"error_rate"`
}

// validProviderID 判定 provider id 是否为合法 id（历史 bug 曾把上游错误体写进
// provider_id，此类记录无法归因，聚合展示时跳过）。
func validProviderID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func toAggView(a quota.Agg) aggView {
	return aggView{Agg: a, AvgLatencyMS: a.AvgLatency(), ErrorRate: a.ErrorRate()}
}

// knownToolName 判断调用方声明的工具名是否命中网关现有能力。
// 除精确匹配外，兼容 MCP 客户端命名风格 server__tool ↔ 网关 mcp_server_tool
// （如 fs__read_file ↔ mcp_fs_read_file），避免把自家能力误判为外部自创。
func knownToolName(name string, known map[string]bool) bool {
	if known[name] {
		return true
	}
	if i := strings.Index(name, "__"); i > 0 {
		alt := "mcp_" + name[:i] + "_" + name[i+2:]
		if known[alt] {
			return true
		}
	}
	return false
}

// handleObservability 返回可观测性总览：总览卡片、provider 维度、场景维度、趋势。
// 数据全部来自内存聚合（启动时回放，运行中增量），不触发任何上游查询。
func (s *Server) handleObservability(w http.ResponseWriter, r *http.Request) {
	days := 14
	if v := r.URL.Query().Get("days"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &days); err != nil || days <= 0 {
			days = 14
		}
		if days > 90 {
			days = 90
		}
	}
	nameByID := map[string]string{}
	for _, p := range s.store.ListProviders() {
		nameByID[p.ID] = p.Name
	}

	var today, week, month quota.Agg
	for _, dp := range s.rec.Daily(days) {
		month = month.Add(dp.Agg)
	}
	for _, dp := range s.rec.Daily(7) {
		week = week.Add(dp.Agg)
	}
	for _, dp := range s.rec.Daily(1) {
		today = today.Add(dp.Agg)
	}

	failoverBy := s.rec.FailoverBy()
	// 工具执行归因：TOP 20 个被实际调用的工具（含 mcp_* 与 skill 名）。
	allTools := s.rec.ToolStats()
	if len(allTools) > 20 {
		allTools = allTools[:20]
	}
	toolStats := make([]map[string]any, 0, len(allTools))
	for _, t := range allTools {
		toolStats = append(toolStats, map[string]any{
			"name": t.Name, "calls": t.Calls, "key_count": t.KeyCount,
		})
	}
	// 技能维度：skill:<name> 前缀的执行统计。
	skillStats := make([]map[string]any, 0)
	for _, t := range s.rec.ToolStats() {
		if !strings.HasPrefix(t.Name, "skill:") {
			continue
		}
		skillStats = append(skillStats, map[string]any{
			"skill": strings.TrimPrefix(t.Name, "skill:"), "calls": t.Calls, "key_count": t.KeyCount,
		})
	}
	// MCP 维度：mcp_<server>_<tool> 按 server 聚合。
	mcpCalls := map[string]int{}
	mcpKeys := map[string]int{}
	for _, t := range s.rec.ToolStats() {
		if !strings.HasPrefix(t.Name, "mcp_") {
			continue
		}
		server := strings.TrimPrefix(t.Name, "mcp_")
		if i := strings.Index(server, "_"); i > 0 {
			server = server[:i]
		}
		if server == "" {
			server = "unknown"
		}
		mcpCalls[server] += t.Calls
		if mcpKeys[server] < t.KeyCount {
			mcpKeys[server] = t.KeyCount
		}
	}
	mcpStats := make([]map[string]any, 0, len(mcpCalls))
	for sv, calls := range mcpCalls {
		mcpStats = append(mcpStats, map[string]any{"server": sv, "calls": calls, "key_count": mcpKeys[sv]})
	}
	sort.Slice(mcpStats, func(i, j int) bool { return mcpStats[i]["calls"].(int) > mcpStats[j]["calls"].(int) })
	// 外部自创工具发现：客户端声明但不在网关目录里的工具（含录用状态）。
	known := map[string]bool{}
	for _, t := range s.proxy.ToolCatalog() {
		known[t.Name] = true
	}
	// 条件性内置工具（ReadRoot/Sandbox 未启用时不在目录，但仍是网关能力，不算外部自创）。
	for _, n := range []string{"read_file", "csv_analyze", "execute_code"} {
		known[n] = true
	}
	adopted := map[string]bool{}
	for _, t := range s.store.ListExternalTools() {
		adopted[t.Name] = true
	}
	extStats := make([]map[string]any, 0)
	for _, t := range s.rec.ClientToolStats() {
		if knownToolName(t.Name, known) {
			continue // 系统内已有能力（含命名归一化），不算外部自创
		}
		extStats = append(extStats, map[string]any{
			"name": t.Name, "calls": t.Calls, "key_count": t.KeyCount,
			"adopted": adopted[t.Name],
		})
	}
	if len(extStats) > 20 {
		extStats = extStats[:20]
	}
	provs := make([]map[string]any, 0)
	for id, a := range s.rec.Providers() {
		if !validProviderID(id) {
			continue // 历史坏数据（错误体被写入 provider_id），无归因价值
		}
		name := nameByID[id]
		if name == "" {
			name = id
		}
		provs = append(provs, map[string]any{
			"id": id, "name": name, "usage": toAggView(a),
			// 作为 failover 失败候选被跳过的次数（稳定性反向指标）。
			"skipped": failoverBy[id],
		})
	}
	sort.Slice(provs, func(i, j int) bool {
		return provs[i]["usage"].(aggView).Requests > provs[j]["usage"].(aggView).Requests
	})

	scenes := make([]map[string]any, 0)
	for sc, a := range s.rec.Scenes() {
		scenes = append(scenes, map[string]any{"scene": sc, "usage": toAggView(a)})
	}
	sort.Slice(scenes, func(i, j int) bool {
		return scenes[i]["usage"].(aggView).Requests > scenes[j]["usage"].(aggView).Requests
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"today":     toAggView(today),
		"week":      toAggView(week),
		"month":     toAggView(month),
		"providers": provs,
		"scenes":    scenes,
		"trend":     s.rec.Daily(days),
		"tools":        toolStats,
		"skills":       skillStats,
		"mcps":         mcpStats,
		"external_tools": extStats,
	})
}

// ---------- health ----------

func (s *Server) handleAdminHealth(w http.ResponseWriter, r *http.Request) {
	// 遍历全部 provider：无请求记录的也展示（默认健康），不依赖 Tracker 快照。
	providers := s.store.ListProviders()
	out := make([]router.ProviderHealth, 0, len(providers))
	for _, p := range providers {
		out = append(out, s.health.Health(p.ID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// ---------- settings ----------

// ---------- skills ----------

func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	if s.skills == nil || s.skills.Dir() == "" {
		writeJSON(w, http.StatusOK, map[string]any{"dir": "", "skills": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dir":    s.skills.Dir(),
		"skills": s.skills.List(),
	})
}

func (s *Server) handleGetSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.skills == nil {
		writeError(w, http.StatusNotFound, "not_found", "skills library not configured")
		return
	}
	d, ok := s.skills.Get(name)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "skill not found")
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// ---------- mcps / tools ----------

// handleListMcps 返回 MCP server 配置 + 连接状态 + 工具数。
func (s *Server) handleListMcps(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Settings().Mcps
	names := make([]string, 0, len(cfg))
	for n := range cfg {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		c := cfg[name]
		st := s.proxy.MCPStatuses()[name]
		out = append(out, map[string]any{
			"name":      name,
			"command":   c.Command,
			"args":      c.Args,
			"env":       c.Env,
			"transport": c.Transport,
			"url":       c.URL,
			"connected":     st.Connected,
			"tools":         st.Tools,
			"tool_details":  st.ToolDetails,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"mcps": out})
}

// handleUpsertMcp 新增/更新一个 MCP server 配置，并重建连接。
func (s *Server) handleUpsertMcp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid mcp name")
		return
	}
	var c struct {
		Command    string            `json:"command"`
		Args       []string          `json:"args"`
		Env        map[string]string `json:"env"`
		Transport  string            `json:"transport"`
		URL        string            `json:"url"`
		TimeoutSec int               `json:"timeout_sec"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if c.Transport == "" {
		c.Transport = "stdio"
	}
	if c.Transport == "http" {
		if c.URL == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "url required for http transport")
			return
		}
		if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
			writeError(w, http.StatusBadRequest, "bad_request", "url must be http(s)")
			return
		}
	} else if c.Command == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "command required for stdio transport")
		return
	}
	if err := s.store.Update(func(cfg *store.Config) error {
		if cfg.Settings.Mcps == nil {
			cfg.Settings.Mcps = map[string]store.MCPServer{}
		}
		cfg.Settings.Mcps[name] = store.MCPServer{Command: c.Command, Args: c.Args, Env: c.Env, Transport: c.Transport, URL: c.URL, TimeoutSec: c.TimeoutSec}
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.proxy.ResetMCP()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteMcp 删除一个 MCP server 配置并断开连接。
func (s *Server) handleDeleteMcp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.Update(func(cfg *store.Config) error {
		if cfg.Settings.Mcps != nil {
			delete(cfg.Settings.Mcps, name)
		}
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.proxy.ResetMCP()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleListFastpath 返回快路径状态：内置匹配器 + 晋升/运行时插件。
func (s *Server) handleListFastpath(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"builtin": s.proxy.FastMatchers(),
		"plugins": s.proxy.FastPluginList(),
	})
}

// handlePromoteFastpath 把插件晋升为正式检测器（移入 promoted 目录）。
// mode: fastpath（拦截，默认）/ tool（注册为工具，可被 LLM 与外部调用）/ both（两者）。
func (s *Server) handlePromoteFastpath(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var c struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&c); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if err := s.proxy.PromoteFastPlugin(name, c.Mode); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteFastpath 删除插件（promoted 也可删）。
func (s *Server) handleDeleteFastpath(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.proxy.DeleteFastPlugin(name); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleFastpathGenerate 手动触发 codegen：对 query 生成检测器并立即验证。
func (s *Server) handleFastpathGenerate(w http.ResponseWriter, r *http.Request) {
	var c struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if strings.TrimSpace(c.Query) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "query required")
		return
	}
	answer, method, chain := s.proxy.FastPathProbe(r, store.APIKey{}, "/v1/chat/completions", c.Query)
	writeJSON(w, http.StatusOK, map[string]any{"answer": answer, "method": method, "chain": chain})
}

// handleListTools 返回网关工具池目录（内置 + 条件 + MCP）。
func (s *Server) handleListTools(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tools": s.proxy.ToolCatalog()})
}

// handleListExternalTools 列出外部自创工具：声明统计 + 录用状态 + 已录用实现。
func (s *Server) handleListExternalTools(w http.ResponseWriter, r *http.Request) {
	known := map[string]bool{}
	for _, t := range s.proxy.ToolCatalog() {
		known[t.Name] = true
	}
	// 条件性内置工具（ReadRoot/Sandbox 未启用时不在目录，但仍是网关能力，不算外部自创）。
	for _, n := range []string{"read_file", "csv_analyze", "execute_code"} {
		known[n] = true
	}
	adopted := map[string]bool{}
	impls := map[string]store.ExternalTool{}
	for _, t := range s.store.ListExternalTools() {
		adopted[t.Name] = true
		impls[t.Name] = t
	}
	out := make([]map[string]any, 0)
	for _, t := range s.rec.ClientToolStats() {
		if knownToolName(t.Name, known) {
			continue
		}
		item := map[string]any{
			"name": t.Name, "calls": t.Calls, "key_count": t.KeyCount,
			"adopted": adopted[t.Name],
		}
		if t2, ok := impls[t.Name]; ok {
			item["impl_type"] = t2.ImplType
			item["description"] = t2.Description
			item["kind"] = t2.Kind
			if item["kind"] == "" {
				item["kind"] = "tool"
			}
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"external_tools": out})
}

// handleListExternalSkillCandidates 返回外部技能候选（技能库页"外部技能"区）：
//   - 已录用 kind=skill 的能力（管理员在监控页录用为技能的），合并调用统计；
//   - 未录用但调用方声明带技能特征（skill: / skill_ 前缀）的候选，
//     供管理员在技能库页择优录用（adopt kind=skill）。
func (s *Server) handleListExternalSkillCandidates(w http.ResponseWriter, r *http.Request) {
	known := map[string]bool{}
	for _, t := range s.proxy.ToolCatalog() {
		known[t.Name] = true
	}
	for _, n := range []string{"read_file", "csv_analyze", "execute_code"} {
		known[n] = true
	}
	stats := map[string]struct{ Calls, Keys int }{}
	for _, t := range s.rec.ClientToolStats() {
		stats[t.Name] = struct{ Calls, Keys int }{t.Calls, t.KeyCount}
	}
	adopted := map[string]store.ExternalTool{}
	for _, t := range s.store.ListExternalTools() {
		if t.Kind == "skill" {
			adopted[t.Name] = t
		}
	}
	out := make([]map[string]any, 0, len(adopted)+4)
	seen := map[string]bool{}
	for _, t := range s.store.ListExternalTools() {
		if t.Kind != "skill" {
			continue
		}
		item := map[string]any{
			"name": t.Name, "description": t.Description, "adopted": true,
			"adopted_at": t.AdoptedAt, "kind": "skill",
		}
		if st, ok := stats[t.Name]; ok {
			item["calls"] = st.Calls
			item["key_count"] = st.Keys
		}
		out = append(out, item)
		seen[t.Name] = true
	}
	for _, t := range s.rec.ClientToolStats() {
		if seen[t.Name] || known[t.Name] {
			continue
		}
		if !strings.HasPrefix(t.Name, "skill:") && !strings.HasPrefix(t.Name, "skill_") {
			continue
		}
		out = append(out, map[string]any{
			"name": t.Name, "calls": t.Calls, "key_count": t.KeyCount,
			"adopted": false, "kind": "skill",
		})
	}
	sort.Slice(out, func(i, j int) bool {
		ai, _ := out[i]["adopted"].(bool)
		aj, _ := out[j]["adopted"].(bool)
		if ai != aj {
			return ai
		}
		ci, _ := out[i]["calls"].(int)
		cj, _ := out[j]["calls"].(int)
		if ci != cj {
			return ci > cj
		}
		return out[i]["name"].(string) < out[j]["name"].(string)
	})
	writeJSON(w, http.StatusOK, map[string]any{"candidates": out})
}

// handleAdoptExternalTool 录用（或更新）一个外部自创能力（工具或技能）。
// body: {description, kind(tool|skill), impl_type(none/js/alias), impl_source}
//   kind=tool  —— 与 fastpath 晋升一致，js 检测器可被 gen_ 调用；
//   kind=skill —— 技能说明模式：impl_type 仅 none，impl_source 为该技能的指令说明，
//                 skill-run 调用时作为技能说明注入（与目录技能行为一致）。
func (s *Server) handleAdoptExternalTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, "/\\ ") {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid tool name")
		return
	}
	var c struct {
		Description string `json:"description"`
		Kind        string `json:"kind"`
		ImplType    string `json:"impl_type"`
		ImplSource  string `json:"impl_source"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&c); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if c.Kind == "" {
		c.Kind = "tool"
	}
	if c.Kind != "tool" && c.Kind != "skill" {
		writeError(w, http.StatusBadRequest, "bad_request", "kind must be tool/skill")
		return
	}
	if c.ImplType == "" {
		c.ImplType = "none"
	}
	if c.Kind == "skill" {
		// 技能录用只登记说明（skill-run 注入指令文本），不支持 js/alias 执行。
		c.ImplType = "none"
	} else if c.ImplType != "none" && c.ImplType != "js" && c.ImplType != "alias" {
		writeError(w, http.StatusBadRequest, "bad_request", "impl_type must be none/js/alias")
		return
	}
	if (c.ImplType == "js" || c.ImplType == "alias") && strings.TrimSpace(c.ImplSource) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "impl_source required for js/alias")
		return
	}
	if c.ImplType == "js" {
		if !s.proxy.ValidateJSDetector(c.ImplSource) {
			writeError(w, http.StatusBadRequest, "bad_request", "js implementation must define detect(text)")
			return
		}
	}
	if err := s.store.AdoptExternalTool(store.ExternalTool{
		Name:        name,
		Description: c.Description,
		Kind:        c.Kind,
		ImplType:    c.ImplType,
		ImplSource:  c.ImplSource,
		AdoptedAt:   time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteExternalTool 取消录用一个外部工具。
func (s *Server) handleDeleteExternalTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.DeleteExternalTool(name); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// 常见 MCP server 的 npx 启动命令（与前端常用模板一致）：外部候选接入时自动预填。
// 调用方声明只包含工具名（server__tool），不含连接配置，命中此表即可一键预填。
var knownMcpCommands = map[string]string{
	"fs":         "npx -y @modelcontextprotocol/server-filesystem",
	"memory":     "npx -y @modelcontextprotocol/server-memory",
	"serena":     "npx -y serena-mcp",
	"think":      "npx -y @modelcontextprotocol/server-think",
	"fetch":      "npx -y mcp-server-fetch",
	"github":     "npx -y @modelcontextprotocol/server-github",
	"git":        "npx -y @modelcontextprotocol/server-git",
	"sqlite":     "npx -y @modelcontextprotocol/server-sqlite",
	"time":       "npx -y @modelcontextprotocol/server-time",
	"context7":   "npx -y @upstash/context7-mcp",
	"puppeteer":  "npx -y @modelcontextprotocol/server-puppeteer",
	"playwright": "npx -y @executeautomation/playwright-mcp-server",
	"google-maps": "npx -y @modelcontextprotocol/server-google-maps",
	"brave-search": "npx -y @modelcontextprotocol/server-brave-search",
	"firecrawl":  "npx -y firecrawl-mcp",
}

// handleSuggestExternalMcp 用网关自身 LLM 为外部 MCP 候选推断接入方式：
// 调用方声明只含工具名，不含连接配置；让模型根据 server/工具名给出
// transport/command/args/url 建议，命中常见包或自建服务均可解释。
func (s *Server) handleSuggestExternalMcp(w http.ResponseWriter, r *http.Request) {
	var c struct {
		Server string `json:"server"`
		Tools  []struct {
			Name  string `json:"name"`
			Calls int    `json:"calls"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&c); err != nil || strings.TrimSpace(c.Server) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "server required")
		return
	}
	if len(c.Tools) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "tools required")
		return
	}
	var sb strings.Builder
	for _, t := range c.Tools {
		sb.WriteString(fmt.Sprintf("- %s（调用 %d 次）\n", t.Name, t.Calls))
	}
	prompt := fmt.Sprintf(`你是 MCP（Model Context Protocol）服务器接入助手。外部调用方在 LLM 网关里声明了以下外部 MCP 工具，但没有提供连接配置。请根据 server 名与工具名给出可直接落地的接入建议。

server 名: %s
工具声明:
%s
只输出 JSON（不要 markdown 围栏、不要任何解释文字）：
{"transport":"stdio 或 http","command":"启动命令，如 npx -y @xxx/yyy","args":["参数数组，可空"],"url":"http 模式的端点 URL；stdio 模式空串","env_hint":"可能需要配置的环境变量或密钥名；没有则空串","notes":"一句中文说明：这是公开知名包 / 自建服务 / 不确定，以及为什么"}

规则（按优先级）：
1. server 名对得上公开知名的 npx 包 → 给出确切命令（如 @modelcontextprotocol/*、serena-mcp 等），notes 说明是公开包。
2. 疑似自建/私有服务（内部项目缩写、私有工具名）→ 也要给出可执行的模板命令：优先 "npx -y <server>"，若名字明显是脚本/进程名则用 "node ./<server>.js"；args 按需；notes 写"按命名惯例推断的模板命令，实际启动方式与所需密钥请向服务提供方核实"。
3. 绝不允许 command 为空，绝不允许只输出"不确定"；拿不准就按规则 2 给模板并在 notes 里说明这是推断。`, c.Server, sb.String())
	msgs := []proxy.ChatMessage{{"role": "user", "content": prompt}}
	body, why := s.proxy.Complete(r, store.APIKey{}, "/v1/chat/completions", msgs)
	if len(body) == 0 {
		if why == "" {
			why = "无可用 chat 路由"
		}
		writeError(w, http.StatusBadGateway, "suggest_failed", "LLM 建议失败："+why)
		return
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Choices) == 0 {
		writeError(w, http.StatusBadGateway, "suggest_failed", "LLM 响应无 choices")
		return
	}
	sug, err := parseSuggestJSON(resp.Choices[0].Message.Content)
	if err != nil {
		writeError(w, http.StatusBadGateway, "suggest_failed", "LLM 未按 JSON 格式返回："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestion": sug})
}

// parseSuggestJSON 从模型输出中提取建议 JSON（兼容 ```json 围栏与前后杂文）。
func parseSuggestJSON(content string) (map[string]any, error) {
	s := strings.TrimSpace(content)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
		if i := strings.Index(s, "\n"); i >= 0 && strings.Contains(s[:i], "json") {
			s = strings.TrimSpace(s[i+1:])
		}
	}
	if i := strings.Index(s, "{"); i >= 0 {
		s = s[i:]
		if j := strings.LastIndex(s, "}"); j >= 0 {
			s = s[:j+1]
		}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	// 归一化字段
	if v, ok := out["command"].(string); ok {
		out["command"] = v
	} else {
		out["command"] = ""
	}
	if v, ok := out["transport"].(string); ok && (v == "stdio" || v == "http") {
		out["transport"] = v
	} else {
		out["transport"] = "stdio"
	}
	for _, k := range []string{"url", "env_hint", "notes"} {
		if _, ok := out[k].(string); !ok {
			out[k] = ""
		}
	}
	if _, ok := out["args"].([]any); !ok {
		out["args"] = []any{}
	}
	return out, nil
}

// handleListExternalMcpCandidates 返回外部 MCP server 候选：
// 调用方声明的 server__tool 风格工具（未命中网关能力、未在网关 MCP 配置）按 server 聚合，
// 供管理员择优接入网关（一键写入 Settings.Mcps 并常驻连接）。
func (s *Server) handleListExternalMcpCandidates(w http.ResponseWriter, r *http.Request) {
	known := map[string]bool{}
	for _, t := range s.proxy.ToolCatalog() {
		known[t.Name] = true
	}
	configured := s.store.Settings().Mcps
	byServer := map[string]map[string]int{} // server -> tool -> calls
	serverCalls := map[string]int{}
	serverKeys := map[string]int{}
	for _, t := range s.rec.ClientToolStats() {
		if known[t.Name] {
			continue
		}
		i := strings.Index(t.Name, "__")
		if i <= 0 {
			continue // 非 server__tool 风格，不是 MCP 候选
		}
		server := t.Name[:i]
		if !validProviderID(server) {
			continue
		}
		if _, ok := configured[server]; ok {
			continue // 已接入网关的 server，不算外部候选
		}
		if byServer[server] == nil {
			byServer[server] = map[string]int{}
		}
		byServer[server][t.Name] += t.Calls
		serverCalls[server] += t.Calls
		if serverKeys[server] < t.KeyCount {
			serverKeys[server] = t.KeyCount
		}
	}
	out := make([]map[string]any, 0, len(byServer))
	for sv, tools := range byServer {
		toolList := make([]map[string]any, 0, len(tools))
		for name, calls := range tools {
			toolList = append(toolList, map[string]any{"name": name, "calls": calls})
		}
		sort.Slice(toolList, func(i, j int) bool {
			return toolList[i]["calls"].(int) > toolList[j]["calls"].(int)
		})
		out = append(out, map[string]any{
			"server": sv, "calls": serverCalls[sv], "key_count": serverKeys[sv],
			"tools":             toolList,
			"suggested_command": knownMcpCommands[sv],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["calls"].(int) > out[j]["calls"].(int)
	})
	writeJSON(w, http.StatusOK, map[string]any{"candidates": out})
}

// handleAdoptExternalMcp 把外部 MCP 候选接入网关：写入 Settings.Mcps 并重连。
// body: {transport(stdio|http), url, command, args, env}
func (s *Server) handleAdoptExternalMcp(w http.ResponseWriter, r *http.Request) {
	server := r.PathValue("server")
	if server == "" || !validProviderID(server) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid mcp server name")
		return
	}
	var c struct {
		Transport  string            `json:"transport"`
		URL        string            `json:"url"`
		Command    string            `json:"command"`
		Args       []string          `json:"args"`
		Env        map[string]string `json:"env"`
		TimeoutSec int               `json:"timeout_sec"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&c); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if c.Transport == "" {
		c.Transport = "stdio"
	}
	if c.Transport == "http" {
		if c.URL == "" || (!strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://")) {
			writeError(w, http.StatusBadRequest, "bad_request", "valid http(s) url required for http transport")
			return
		}
	} else if c.Transport == "stdio" {
		if c.Command == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "command required for stdio transport")
			return
		}
	} else {
		writeError(w, http.StatusBadRequest, "bad_request", "transport must be stdio/http")
		return
	}
	if err := s.store.Update(func(cfg *store.Config) error {
		if cfg.Settings.Mcps == nil {
			cfg.Settings.Mcps = map[string]store.MCPServer{}
		}
		cfg.Settings.Mcps[server] = store.MCPServer{Transport: c.Transport, URL: c.URL, Command: c.Command, Args: c.Args, Env: c.Env, TimeoutSec: c.TimeoutSec}
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.proxy.ResetMCP()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSandboxStatus 返回 Docker 沙箱（execute_code）状态：配置 + docker 可用性 + 支持语言。
func (s *Server) handleSandboxStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.proxy.SandboxStatus())
}

// handleListMemory 返回会话记忆（remember/recall 内容，ns 形如 mem:<keyID>:<key>）。
func (s *Server) handleListMemory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"entries": s.proxy.ListMemory()})
}

// handleClearMemory 清空全部会话记忆。
func (s *Server) handleClearMemory(w http.ResponseWriter, r *http.Request) {
	s.proxy.ClearMemory()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleInvokeTool 一键测试工具：执行内置或 MCP 工具并返回结果文本。
func (s *Server) handleInvokeTool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "tool name required")
		return
	}
	if req.Args == nil {
		req.Args = map[string]any{}
	}
	result := s.proxy.InvokeTool(req.Name, req.Args)
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st := s.store.Settings()
	writeJSON(w, http.StatusOK, map[string]any{		"listen":             st.Listen,
		"default_timeout_ms": st.DefaultTimeoutMS,
		"max_body_bytes":     st.MaxBodyBytes,
		"fail_threshold":     st.FailThreshold,
		"cooldown_sec":       st.CooldownSec,
		"pricing":            st.Pricing,
		"smart":              st.Smart,
		"data_file":          s.store.Path(),
		"admin_token_set":    st.AdminToken != "",
	})
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var patch struct {
		DefaultTimeoutMS int                    `json:"default_timeout_ms"`
		MaxBodyBytes     int64                  `json:"max_body_bytes"`
		FailThreshold    int                    `json:"fail_threshold"`
		CooldownSec      int                    `json:"cooldown_sec"`
		Pricing          map[string]store.Price `json:"pricing"`
		Smart            *store.SmartScoreCfg   `json:"smart"`
	}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	err := s.store.Update(func(c *store.Config) error {
		if patch.DefaultTimeoutMS > 0 {
			c.Settings.DefaultTimeoutMS = patch.DefaultTimeoutMS
		}
		if patch.MaxBodyBytes > 0 {
			c.Settings.MaxBodyBytes = patch.MaxBodyBytes
		}
		if patch.FailThreshold > 0 {
			c.Settings.FailThreshold = patch.FailThreshold
		}
		if patch.CooldownSec > 0 {
			c.Settings.CooldownSec = patch.CooldownSec
		}
		if len(patch.Pricing) > 0 {
			if c.Settings.Pricing == nil {
				c.Settings.Pricing = map[string]store.Price{}
			}
			for k, v := range patch.Pricing {
				c.Settings.Pricing[k] = v
			}
		}
		if patch.Smart != nil {
			c.Settings.Smart = *patch.Smart
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- 小工具 ----------

func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + store.MaskedSecretMarker + s[len(s)-4:]
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return out
}
