package proxy

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/erishen/tsm-hub/internal/store"
)

// AnthropicAdapter 是 Anthropic Claude API 协议的适配器。
// 把 OpenAI 格式的请求/响应转换为 Anthropic 格式。
//
// Anthropic API 与 OpenAI 的主要差异：
// - 路径：/v1/messages（而非 /v1/chat/completions）
// - 请求：system 从 messages 中分离为顶层字段；必须传 max_tokens
// - 响应：content 是数组（[{type:"text", text:"..."}]），而非字符串
// - 流式：事件类型不同（message_start/content_block_delta/message_stop 等）
type AnthropicAdapter struct{}

func (a *AnthropicAdapter) Name() string { return "anthropic" }

func (a *AnthropicAdapter) UpstreamURL(provider store.Provider, clientPath string) string {
	// Anthropic 固定用 /v1/messages，忽略客户端路径
	base := strings.TrimRight(provider.BaseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}

func (a *AnthropicAdapter) AuthHeader(provider store.Provider) (key, value string) {
	return "x-api-key", provider.ResolvedAPIKey()
}

// anthropicRequest 是 Anthropic /v1/messages 的请求体格式。
type anthropicRequest struct {
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens"`
	Messages  []anthropicMessage  `json:"messages"`
	System    string              `json:"system,omitempty"`
	Stream    bool                `json:"stream,omitempty"`
	Tools     []anthropicTool     `json:"tools,omitempty"`
	Metadata  map[string]any      `json:"metadata,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // 字符串或内容块数组
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

// openAIMessage 是 OpenAI 格式的消息（用于解析请求体）。
type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openAIRequest 是 OpenAI 格式的请求体（用于解析需要转换的字段）。
type openAIRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Tools    []openAITool    `json:"tools,omitempty"`
}

type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

func (a *AnthropicAdapter) ConvertRequest(body []byte, upstreamModel string) ([]byte, map[string]string, error) {
	var req openAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, fmt.Errorf("parse openai request: %w", err)
	}

	// 分离 system 消息和普通消息
	var systemText strings.Builder
	messages := make([]anthropicMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == "system" {
			if systemText.Len() > 0 {
				systemText.WriteString("\n\n")
			}
			systemText.WriteString(m.Content)
			continue
		}
		// Anthropic 不支持 "system" 角色，其余角色（user/assistant/tool）直接映射
		role := m.Role
		if role == "tool" {
			role = "user" // tool 消息转为 user 角色（Anthropic 用 tool_result 内容块）
		}
		messages = append(messages, anthropicMessage{
			Role:    role,
			Content: m.Content,
		})
	}

	// 转换 tools 格式
	tools := make([]anthropicTool, 0, len(req.Tools))
	for _, t := range req.Tools {
		if t.Type != "function" {
			continue
		}
		tools = append(tools, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		})
	}

	anthropicReq := anthropicRequest{
		Model:     upstreamModel,
		MaxTokens: 4096, // OpenAI 不传 max_tokens，Anthropic 必须传，给一个合理默认值
		Messages:  messages,
		System:    systemText.String(),
		Stream:    req.Stream,
		Tools:     tools,
	}

	out, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	// Anthropic 需要 anthropic-version 头
	extraHeaders := map[string]string{
		"anthropic-version": "2023-06-01",
	}

	return out, extraHeaders, nil
}

// anthropicResponse 是 Anthropic 非流式响应格式。
type anthropicResponse struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Role       string             `json:"role"`
	Model      string             `json:"model"`
	Content    []anthropicContent `json:"content"`
	StopReason string             `json:"stop_reason"`
	Usage      anthropicUsage     `json:"usage"`
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// openAIResponse 是 OpenAI 格式的响应。
type openAIResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []openAIResponseChoice `json:"choices"`
	Usage   openAIUsage            `json:"usage"`
}

type openAIResponseChoice struct {
	Index        int            `json:"index"`
	Message      openAIMessage  `json:"message"`
	FinishReason string         `json:"finish_reason"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (a *AnthropicAdapter) ConvertResponse(body []byte) ([]byte, error) {
	var resp anthropicResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse anthropic response: %w", err)
	}

	// 提取文本内容
	var text strings.Builder
	for _, c := range resp.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}

	// 转换 finish_reason
	finishReason := "stop"
	switch resp.StopReason {
	case "end_turn", "stop_sequence":
		finishReason = "stop"
	case "max_tokens":
		finishReason = "length"
	case "tool_use":
		finishReason = "tool_calls"
	}

	openAIResp := openAIResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: 0, // 客户端通常不依赖这个字段
		Model:   resp.Model,
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
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}

	out, err := json.Marshal(openAIResp)
	if err != nil {
		return nil, fmt.Errorf("marshal openai response: %w", err)
	}
	return out, nil
}

// anthropicStreamEvent 是 Anthropic 流式 SSE 事件的通用格式。
type anthropicStreamEvent struct {
	Type  string          `json:"type"`
	Index int             `json:"index,omitempty"`
	Delta json.RawMessage `json:"delta,omitempty"`
	Usage json.RawMessage `json:"usage,omitempty"`
}

// anthropicContentDelta 是 Anthropic content_block_delta 事件的 delta 字段。
type anthropicContentDelta struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// openAIStreamChunk 是 OpenAI 流式响应的 chunk 格式。
type openAIStreamChunk struct {
	ID      string                   `json:"id"`
	Object  string                   `json:"object"`
	Created int64                    `json:"created"`
	Model   string                   `json:"model"`
	Choices []openAIStreamChoice     `json:"choices"`
}

type openAIStreamChoice struct {
	Index        int            `json:"index"`
	Delta        openAIMessage  `json:"delta"`
	FinishReason string         `json:"finish_reason,omitempty"`
}

func (a *AnthropicAdapter) ConvertStreamEvent(data []byte) ([]byte, bool, error) {
	// [DONE] 直接透传
	if string(data) == "[DONE]" {
		return data, false, nil
	}

	var event anthropicStreamEvent
	if err := json.Unmarshal(data, &event); err != nil {
		// 无法解析的事件直接透传（避免丢失数据）
		return data, false, nil
	}

	switch event.Type {
	case "message_start":
		// OpenAI 不需要这个事件，跳过
		return nil, true, nil

	case "content_block_start":
		// 内容块开始，跳过（OpenAI 不需要）
		return nil, true, nil

	case "content_block_delta":
		// 文本增量，转换为 OpenAI 的 delta.content
		var delta anthropicContentDelta
		if err := json.Unmarshal(event.Delta, &delta); err != nil {
			return data, false, nil
		}
		if delta.Type != "text_delta" || delta.Text == "" {
			return nil, true, nil
		}
		chunk := openAIStreamChunk{
			ID:      "chatcmpl-anthropic",
			Object:  "chat.completion.chunk",
			Created: 0,
			Model:   "",
			Choices: []openAIStreamChoice{
				{
					Index: 0,
					Delta: openAIMessage{
						Role:    "assistant",
						Content: delta.Text,
					},
				},
			},
		}
		out, err := json.Marshal(chunk)
		if err != nil {
			return nil, false, err
		}
		return out, false, nil

	case "content_block_stop":
		// 内容块结束，跳过
		return nil, true, nil

	case "message_delta":
		// 消息增量（包含 stop_reason），转换为 OpenAI 的 finish_reason
		var delta struct {
			StopReason string `json:"stop_reason"`
		}
		if err := json.Unmarshal(event.Delta, &delta); err != nil {
			return nil, true, nil
		}
		finishReason := "stop"
		switch delta.StopReason {
		case "max_tokens":
			finishReason = "length"
		case "tool_use":
			finishReason = "tool_calls"
		}
		chunk := openAIStreamChunk{
			ID:      "chatcmpl-anthropic",
			Object:  "chat.completion.chunk",
			Created: 0,
			Model:   "",
			Choices: []openAIStreamChoice{
				{
					Index:        0,
					Delta:        openAIMessage{},
					FinishReason: finishReason,
				},
			},
		}
		out, err := json.Marshal(chunk)
		if err != nil {
			return nil, false, err
		}
		return out, false, nil

	case "message_stop":
		// 消息结束，转换为 OpenAI 的 [DONE]
		return []byte("[DONE]"), false, nil

	default:
		// 未知事件类型，跳过
		return nil, true, nil
	}
}

func (a *AnthropicAdapter) StreamDoneEvent() string {
	return "[DONE]"
}
