package proxy

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/erishen/tsm-hub/internal/store"
)

// GeminiAdapter 是 Google Gemini API 协议的适配器。
//
// Gemini 与 OpenAI 的主要差异：
// - URL：https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent?key={api_key}
// - 认证：API key 作为 URL 参数（而非请求头）
// - 请求体：contents/parts 格式（而非 messages）
// - 响应体：candidates[].content.parts[].text（而非 choices[].message.content）
// - 流式：:streamGenerateContent，SSE 事件格式不同
type GeminiAdapter struct{}

func (a *GeminiAdapter) Name() string { return "gemini" }

func (a *GeminiAdapter) UpstreamURL(provider store.Provider, clientPath string) string {
	base := strings.TrimRight(provider.BaseURL, "/")
	// 如果用户已经填了完整的 URL（包含 /models/），直接使用
	if strings.Contains(base, "/models/") {
		sep := "?"
		if strings.Contains(base, "?") {
			sep = "&"
		}
		return base + sep + "key=" + provider.ResolvedAPIKey()
	}
	// 否则默认用 generateContent 端点（模型名在请求体转换时处理）
	return base + "/v1beta/models/gemini-pro:generateContent?key=" + provider.ResolvedAPIKey()
}

func (a *GeminiAdapter) AuthHeader(provider store.Provider) (key, value string) {
	// Gemini 用 URL 参数传递 API key，不需要认证头
	return "", ""
}

// geminiRequest 是 Gemini API 的请求体格式。
type geminiRequest struct {
	Contents          []geminiContent    `json:"contents"`
	SystemInstruction *geminiContent     `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenConfig   `json:"generationConfig,omitempty"`
	Tools             []geminiTool        `json:"tools,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text,omitempty"`
}

type geminiGenConfig struct {
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
	Temperature     float64 `json:"temperature,omitempty"`
	TopP            float64 `json:"topP,omitempty"`
	TopK            int     `json:"topK,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunction `json:"functionDeclarations,omitempty"`
}

type geminiFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

func (a *GeminiAdapter) ConvertRequest(body []byte, upstreamModel string) ([]byte, map[string]string, error) {
	var req openAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, fmt.Errorf("parse openai request: %w", err)
	}

	// 分离 system 消息和普通消息
	var systemText strings.Builder
	contents := make([]geminiContent, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == "system" {
			if systemText.Len() > 0 {
				systemText.WriteString("\n\n")
			}
			systemText.WriteString(m.Content)
			continue
		}
		// Gemini 角色：user / model（OpenAI 用 user / assistant）
		role := m.Role
		if role == "assistant" {
			role = "model"
		}
		if role == "tool" {
			role = "user" // tool 消息转为 user 角色
		}
		contents = append(contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: m.Content}},
		})
	}

	// 转换 tools 格式
	tools := make([]geminiTool, 0)
	if len(req.Tools) > 0 {
		funcs := make([]geminiFunction, 0, len(req.Tools))
		for _, t := range req.Tools {
			if t.Type != "function" {
				continue
			}
			funcs = append(funcs, geminiFunction{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			})
		}
		if len(funcs) > 0 {
			tools = append(tools, geminiTool{FunctionDeclarations: funcs})
		}
	}

	geminiReq := geminiRequest{
		Contents:         contents,
		GenerationConfig: &geminiGenConfig{MaxOutputTokens: 4096},
		Tools:            tools,
	}
	if systemText.Len() > 0 {
		geminiReq.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: systemText.String()}},
		}
	}

	out, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal gemini request: %w", err)
	}
	return out, nil, nil
}

// geminiResponse 是 Gemini 非流式响应格式。
type geminiResponse struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata geminiUsage       `json:"usageMetadata"`
}

type geminiCandidate struct {
	Content       geminiContent `json:"content"`
	FinishReason  string        `json:"finishReason"`
	Index         int           `json:"index"`
}

type geminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

func (a *GeminiAdapter) ConvertResponse(body []byte) ([]byte, error) {
	var resp geminiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse gemini response: %w", err)
	}

	// 提取文本内容
	var text strings.Builder
	if len(resp.Candidates) > 0 {
		for _, p := range resp.Candidates[0].Content.Parts {
			text.WriteString(p.Text)
		}
	}

	// 转换 finish_reason
	finishReason := "stop"
	if len(resp.Candidates) > 0 {
		switch resp.Candidates[0].FinishReason {
		case "MAX_TOKENS":
			finishReason = "length"
		case "STOP", "SAFETY", "RECITATION", "OTHER":
			finishReason = "stop"
		}
	}

	openAIResp := openAIResponse{
		ID:      "gemini-" + randomHex(8),
		Object:  "chat.completion",
		Created: 0,
		Model:   "",
		Choices: []openAIResponseChoice{
			{
				Index: 0,
				Message: openAIMessage{
					Role:    "assistant",
					Content: text.String(),
				},
				FinishReason: finishReason,
			},
		},
		Usage: openAIUsage{
			PromptTokens:     resp.UsageMetadata.PromptTokenCount,
			CompletionTokens: resp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      resp.UsageMetadata.TotalTokenCount,
		},
	}

	out, err := json.Marshal(openAIResp)
	if err != nil {
		return nil, fmt.Errorf("marshal openai response: %w", err)
	}
	return out, nil
}

// geminiStreamChunk 是 Gemini 流式响应的单个 chunk 格式。
type geminiStreamChunk struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata geminiUsage       `json:"usageMetadata,omitempty"`
}

func (a *GeminiAdapter) ConvertStreamEvent(data []byte) ([]byte, bool, error) {
	if string(data) == "[DONE]" {
		return data, false, nil
	}

	var chunk geminiStreamChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		// 无法解析的事件直接透传
		return data, false, nil
	}

	// 提取文本增量
	var text strings.Builder
	if len(chunk.Candidates) > 0 {
		for _, p := range chunk.Candidates[0].Content.Parts {
			text.WriteString(p.Text)
		}
	}

	// 如果没有文本内容且没有 usage，跳过
	if text.Len() == 0 && chunk.UsageMetadata.TotalTokenCount == 0 {
		return nil, true, nil
	}

	// 检查是否是最后一个 chunk（包含 finishReason）
	finishReason := ""
	if len(chunk.Candidates) > 0 && chunk.Candidates[0].FinishReason != "" {
		switch chunk.Candidates[0].FinishReason {
		case "MAX_TOKENS":
			finishReason = "length"
		default:
			finishReason = "stop"
		}
	}

	openAIChunk := openAIStreamChunk{
		ID:      "gemini-" + randomHex(8),
		Object:  "chat.completion.chunk",
		Created: 0,
		Model:   "",
		Choices: []openAIStreamChoice{
			{
				Index: 0,
				Delta: openAIMessage{
					Role:    "assistant",
					Content: text.String(),
				},
				FinishReason: finishReason,
			},
		},
	}

	out, err := json.Marshal(openAIChunk)
	if err != nil {
		return nil, false, err
	}
	return out, false, nil
}

func (a *GeminiAdapter) StreamDoneEvent() string {
	return "[DONE]"
}

// randomHex 生成指定长度的随机十六进制字符串。
func randomHex(n int) string {
	const hexChars = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = hexChars[i%len(hexChars)]
	}
	return string(b)
}
