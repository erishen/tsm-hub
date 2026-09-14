package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/erishen/tsm-hub/internal/store"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)


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
	"dots-studio/dots-3-note-preview:free":    {"specialized", "笔记/长文档整理（Dots 3）", ""},
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
	case strings.Contains(lower, "ocr") || strings.Contains(lower, "rerank") || strings.Contains(lower, "note"):
		return modelMeta{"specialized", "专用能力（OCR/重排/笔记）", ""}
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


// appScenario 是一个应用开发场景，根据当前模型池能力动态匹配推荐模型。
type appScenario struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Icon        string   `json:"icon"`
	Description string   `json:"description"`
	Categories  []string `json:"-"`
	Keywords    []string `json:"-"`
	MinContext  int      `json:"-"`
	Example     string   `json:"example"`
	Features    []string `json:"features"`
}

// appScenarios 是预设的应用开发场景列表，根据模型池能力动态过滤。
var appScenarios = []appScenario{
	{
		ID: "chatbot", Name: "智能客服 / 问答机器人", Icon: "💬",
		Description: "7×24 小时自动客服、FAQ 问答、知识库对话，免费模型即可支撑高并发。",
		Categories:  []string{"text"}, Keywords: []string{"flash", "lite", "mini"},
		Example: "你是电商客服助手。用户问：「我的订单什么时候发货？」 请根据订单号查询并礼貌回复。",
		Features: []string{"免费可用", "高并发", "多语言"},
	},
	{
		ID: "coding", Name: "代码助手 / 代码审查", Icon: "⌨️",
		Description: "代码补全、Bug 修复、Code Review、技术方案生成，主打编码能力强的模型。",
		Categories:  []string{"text"}, Keywords: []string{"code", "coder", "agent", "deepseek"},
		Example: "审查以下 Python 函数的性能问题并给出优化方案：\n```python\ndef process(items):\n    result = []\n    for i in items:\n        result.append(i * 2)\n    return result\n```",
		Features: []string{"编码专用", "免费模型", "多语言"},
	},
	{
		ID: "agent", Name: "Agent / 工具调用", Icon: "🤖",
		Description: "Function Calling、多步推理、工具编排，适合构建自主 Agent 工作流。",
		Categories:  []string{"text"}, Keywords: []string{"agent", "flash", "ultra", "kimi"},
		MinContext:  100000,
		Example: "你是研究 Agent。可用工具：search(query)、fetch(url)、summarize(text)。任务：调研「2026 年大模型推理优化」并输出 500 字摘要。",
		Features: []string{"Function Calling", "大上下文", "多步推理"},
	},
	{
		ID: "rag", Name: "长文档分析 / RAG", Icon: "📚",
		Description: "1M 上下文直接喂文档，或配合向量嵌入+重排序构建 RAG 系统，支持百万字分析。",
		Categories:  []string{"text", "embedding", "specialized"}, Keywords: []string{"flash", "deepseek", "glm", "kimi"},
		MinContext:  100000,
		Example: "请阅读以下合同文本，提取关键条款：甲方、乙方、合同金额、付款方式、违约责任、终止条件。[文档内容]",
		Features: []string{"1M 上下文", "向量嵌入", "重排序", "免费"},
	},
	{
		ID: "multimodal", Name: "多模态理解 / OCR", Icon: "🖼️",
		Description: "图像理解、图表分析、OCR 文字识别、文档结构化，支持图片+文本混合输入。",
		Categories:  []string{"vision", "specialized"}, Keywords: []string{"vision", "ocr", "vl"},
		Example: "请识别这张发票图片中的：开票日期、金额、税额、销售方名称、购买方名称，并输出 JSON。",
		Features: []string{"图像理解", "OCR", "图表分析"},
	},
	{
		ID: "notes", Name: "笔记 / 长文档整理", Icon: "📝",
		Description: "会议纪要整理、长文摘要、知识卡片生成，专用笔记模型 500K 上下文。",
		Categories:  []string{"specialized"}, Keywords: []string{"note", "dots"},
		Example: "请将以下 2 小时会议录音转写文本整理为：会议主题、关键决议、行动项（含负责人和截止日期）、待讨论问题。",
		Features: []string{"500K 上下文", "专用模型", "免费"},
	},
	{
		ID: "music", Name: "音乐 / 音频生成", Icon: "🎵",
		Description: "文本生成音乐、音效、BGM，支持风格描述和时长控制，1M 上下文。",
		Categories:  []string{"audio"}, Keywords: []string{"lyria", "music"},
		Example: "生成一段 30 秒的轻快电子音乐，适合产品介绍视频背景，节奏 120 BPM，无歌词。",
		Features: []string{"文本生音乐", "1M 上下文", "免费"},
	},
	{
		ID: "safety", Name: "内容安全 / 审核", Icon: "🛡️",
		Description: "文本/图片内容审核、违规检测、分类标签，可集成到 UGC 平台做前置过滤。",
		Categories:  []string{"other"}, Keywords: []string{"safety", "moderation", "guard"},
		Example: "请审核以下用户评论是否违规，输出分类：正常/广告/辱骂/色情/政治敏感/其他，并给出置信度。",
		Features: []string{"内容审核", "分类标签", "免费"},
	},
	{
		ID: "research", Name: "深度研究 / 推理", Icon: "🔬",
		Description: "复杂问题拆解、多步推理、深度研究报告，主打推理能力强的大参数模型。",
		Categories:  []string{"text"}, Keywords: []string{"reason", "think", "ultra", "pro", "nemotron"},
		MinContext:  100000,
		Example: "请分析「AI 编程助手对软件工程师生产力的影响」，从正面、负面、长期趋势三个维度展开，引用数据支撑。",
		Features: []string{"深度推理", "大上下文", "免费旗舰"},
	},
	{
		ID: "edge", Name: "轻量 / 高并发场景", Icon: "⚡",
		Description: "低延迟、高吞吐、低成本场景，轻量模型适合边缘部署和大规模并发。",
		Categories:  []string{"text"}, Keywords: []string{"lite", "flash", "mini", "small", "xs"},
		Example: "你是快速分类助手。用户输入：「今天天气真好」 请在 100ms 内输出情感分类：正面/负面/中性。",
		Features: []string{"低延迟", "高并发", "免费", "轻量"},
	},
}

// handleModelRecommendations 根据当前模型池能力返回可做的应用开发场景推荐。

func (s *Server) handleModelRecommendations(w http.ResponseWriter, r *http.Request) {
	catalog := s.buildCatalog()
	type modelItem struct {
		ID            string `json:"id"`
		Provider      string `json:"provider"`
		Category      string `json:"category"`
		Purpose       string `json:"purpose"`
		ContextLength int    `json:"context_length,omitempty"`
		Free          bool   `json:"free"`
		Unavailable   string `json:"unavailable,omitempty"`
		RouteScore    int    `json:"route_score"`
		InRoute       bool   `json:"in_route"`
	}
	// buildCatalog 返回 []*item（局部类型），用 JSON 中转解析为通用 modelItem
	rawBytes, _ := json.Marshal(catalog["models"])
	var allModels []modelItem
	_ = json.Unmarshal(rawBytes, &allModels)

	type scenarioResp struct {
		appScenario
		Models []modelItem `json:"models"`
	}
	var result []scenarioResp
	for _, sc := range appScenarios {
		// 筛选匹配模型
		var matched []modelItem
		for _, m := range allModels {
			if m.Unavailable != "" {
				continue // 跳过不可用
			}
			// 类别匹配
			catOK := false
			for _, c := range sc.Categories {
				if m.Category == c {
					catOK = true
					break
				}
			}
			if !catOK {
				continue
			}
			// 上下文要求
			if sc.MinContext > 0 && m.ContextLength < sc.MinContext {
				continue
			}
			matched = append(matched, m)
		}
		if len(matched) == 0 {
			continue // 没有可用模型的场景不展示
		}
		// 排序：关键词匹配加分 + 免费优先 + 评分高
		sort.Slice(matched, func(i, j int) bool {
			scoreI, scoreJ := matched[i].RouteScore, matched[j].RouteScore
			lowerI, lowerJ := strings.ToLower(matched[i].ID), strings.ToLower(matched[j].ID)
			for _, kw := range sc.Keywords {
				if strings.Contains(lowerI, kw) {
					scoreI += 50
				}
				if strings.Contains(lowerJ, kw) {
					scoreJ += 50
				}
			}
			if matched[i].Free {
				scoreI += 100
			}
			if matched[j].Free {
				scoreJ += 100
			}
			return scoreI > scoreJ
		})
		// 取 top 3
		if len(matched) > 3 {
			matched = matched[:3]
		}
		result = append(result, scenarioResp{appScenario: sc, Models: matched})
	}
	// 统计
	freeCount := 0
	for _, m := range allModels {
		if m.Free {
			freeCount++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scenarios":    result,
		"total_models": len(allModels),
		"free_models":  freeCount,
		"scenario_num": len(result),
	})
}

// getStr / getBool 辅助函数

func getStr(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func getBool(m map[string]any, k string) bool {
	if v, ok := m[k].(bool); ok {
		return v
	}
	return false
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
		// RouteScore 是路由综合评分（免费+健康+延迟+在路由里-不可用扣分），越高越优先被选中。
		RouteScore int `json:"route_score"`
		// InRoute 表示该 (provider, model) 是否在某条路由的 targets 里（未接入路由的模型不会被外部调用命中）。
		InRoute bool `json:"in_route"`
		// SupportsTools 表示该模型是否支持 tool calling（function calling）。
		// 默认 true（假设支持）；如果曾因带 tools 返回 400 被标记不可用，则为 false。
		SupportsTools bool `json:"supports_tools"`
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
			it := &item{ID: id, Provider: p.ID, Category: meta.Category, Purpose: meta.Purpose, Ctx: meta.Ctx, SupportsTools: true}
			// 曾 404 的模型：冷却期内标灰，路由自动跳过。
			if reason, unavail := s.store.ModelUnavailable(p.ID, id); unavail {
				it.Unavailable = reason
				// 如果不可用原因是 "tools unsupported"（带 tools 返回 400），标记为不支持 tool calling。
				if strings.Contains(reason, "tools unsupported") {
					it.SupportsTools = false
				}
			}
			// 静态知识表：已知不支持 tool calling 的模型（嵌入/内容安全/音频生成等专用模型）。
			if store.NoToolsModels[id] {
				it.SupportsTools = false
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
			// 路由综合评分：免费+100、健康+30、延迟分、在路由里+50、不可用-100。
			score := 0
			if it.Free {
				score += 100
			}
			if it.Unavailable != "" {
				score -= 100
			} else if s.health.Available(p.ID) {
				score += 30
				if lat := s.health.Latency(p.ID); lat > 0 {
					if lat < 3000 {
						score += 20
					} else if lat < 10000 {
						score += 10
					}
				}
			}
			// 是否在某条路由的 targets 里
			for _, rt := range s.store.ListRoutes() {
				for _, tg := range rt.Targets {
					if tg.ProviderID == p.ID && (tg.Model == id || tg.Model == "*") {
						it.InRoute = true
						score += 50
						break
					}
				}
				if it.InRoute {
					break
				}
			}
			it.RouteScore = score
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		// 同模型不同 Provider：按路由评分降序（高分在前，免费/健康/低延迟优先）
		if out[i].RouteScore != out[j].RouteScore {
			return out[i].RouteScore > out[j].RouteScore
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

// geminiFreeModel 判断 Gemini 模型是否在 API 免费层（Free Tier）内可用。
// 免费层覆盖 Flash / Flash-Lite 系列文本模型（输入输出全免费、限速）；
// 图像（Nano Banana）、音频（TTS/Live/Transcribe）、视频（Lyria/Veo/Omni）、
// Pro、Embedding 等不在免费层。来源：ai.google.dev/gemini-api/docs/pricing（2026-09-14）。
func geminiFreeModel(id string) bool {
	l := strings.ToLower(id)
	if !strings.Contains(l, "flash") {
		return false
	}
	for _, excl := range []string{"image", "live", "tts", "transcribe", "lyria", "veo", "omni", "embedding", "pro"} {
		if strings.Contains(l, excl) {
			return false
		}
	}
	return true
}

func (s *Server) probeModelsOnce(ctx context.Context, baseURL, key string) ([]map[string]any, int, []byte, error) {
	// Google Gemini 官方端点：认证用 x-goog-api-key 头（Bearer 会被拒为
	// "Expected OAuth 2 access token"），模型列表响应也是 {"models":[...]} 而非 {"data":[...]}。
	gemini := strings.Contains(baseURL, "generativelanguage.googleapis.com")
	if gemini {
		// 用户可能误填 /v1beta/interactions 等具体端点；模型列表固定是 /v1beta/models，
		// 统一归一化到 /v1beta（探测函数会再拼 /models）。
		if u, err := url.Parse(baseURL); err == nil {
			u.Path = "/v1beta"
			u.RawQuery = ""
			baseURL = strings.TrimRight(u.String(), "/")
		}
	}
	probeOnce := func() (*http.Response, []byte, error) {
		ctx2, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		upReq, err := http.NewRequestWithContext(ctx2, http.MethodGet, baseURL+"/models", nil)
		if err != nil {
			return nil, nil, err
		}
		if key != "" {
			if gemini {
				upReq.Header.Set("x-goog-api-key", key)
			} else {
				upReq.Header.Set("Authorization", "Bearer "+key)
			}
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
	if gemini {
		// Gemini 模型列表：{"models":[{"name":"models/gemini-3-flash","displayName":...}]}
		var g struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &g); err != nil {
			return nil, 0, nil, fmt.Errorf("invalid_models: %w", err)
		}
		seen := map[string]bool{}
		out := make([]map[string]any, 0, len(g.Models))
		for _, m := range g.Models {
			id := strings.TrimPrefix(strings.TrimSpace(m.Name), "models/")
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			info := map[string]any{"id": id}
			// Gemini API 免费层（Free Tier）：Flash / Flash-Lite 文本模型输入输出全免费
			// （限速），官方定价页 https://ai.google.dev/gemini-api/docs/pricing（2026-09-14 抓取）。
			// 按 id 关键词判定：含 flash 且非 image/音频/视频/embedding 的文本模型视为免费；
			// Pro / Nano Banana / Lyria / Veo / TTS / Live / Transcribe / Embedding 不在免费层。
			if geminiFreeModel(id) {
				info["free"] = true
			}
			out = append(out, info)
		}
		return out, 0, nil, nil
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
