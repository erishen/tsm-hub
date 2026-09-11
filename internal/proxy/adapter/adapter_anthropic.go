package adapter

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/erishen/tsm-hub/internal/store"
)

// AnthropicAdapter 是 Anthropic Claude API 协议的适配器。
type AnthropicAdapter struct{}

func (a *AnthropicAdapter) Name() string { return "anthropic" }

func (a *AnthropicAdapter) UpstreamURL(provider store.Provider, clientPath string) string {
	base := strings.TrimRight(provider.BaseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}

func (a *AnthropicAdapter) AuthHeader(provider store.Provider) (key, value string) {
	return "x-api-key", provider.ResolvedAPIKey()
}

// === Anthropic 特定类型 ===

type anthropicRequest struct {
	Model     string             `json:"model"`
	Messages  []anthropicMessage `json:"messages"`
	System    string             `json:"system,omitempty"`
	MaxTokens int                `json:"max_tokens"`
	Stream    bool               `json:"stream,omitempty"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

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

type anthropicStreamEvent struct {
	Type  string          `json:"type"`
	Index int             `json:"index,omitempty"`
	Delta json.RawMessage `json:"delta,omitempty"`
	Usage json.RawMessage `json:"usage,omitempty"`
}

type anthropicContentDelta struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// === 适配器方法实现 ===

func (a *AnthropicAdapter) ConvertRequest(body []byte, upstreamModel string) ([]byte, map[string]string, error) {
	var req openAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, fmt.Errorf("parse openai request: %w", err)
	}

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
		role := m.Role
		if role == "tool" {
			role = "user"
		}
		messages = append(messages, anthropicMessage{
			Role:    role,
			Content: m.Content,
		})
	}

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
		MaxTokens: 4096,
		Messages:  messages,
		System:    systemText.String(),
		Stream:    req.Stream,
		Tools:     tools,
	}

	out, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	extraHeaders := map[string]string{
		"anthropic-version": "2023-06-01",
	}
	return out, extraHeaders, nil
}

func (a *AnthropicAdapter) ConvertResponse(body []byte) ([]byte, error) {
	var resp anthropicResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse anthropic response: %w", err)
	}

	var text strings.Builder
	for _, c := range resp.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}

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
		Created: 0,
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

func (a *AnthropicAdapter) ConvertStreamEvent(data []byte) ([]byte, bool, error) {
	if string(data) == "[DONE]" {
		return data, false, nil
	}

	var event anthropicStreamEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return data, false, nil
	}

	switch event.Type {
	case "message_start", "content_block_start", "content_block_stop":
		return nil, true, nil

	case "content_block_delta":
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

	case "message_delta":
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
		return []byte("[DONE]"), false, nil
	}

	return data, false, nil
}

func (a *AnthropicAdapter) StreamDoneEvent() string {
	return "[DONE]"
}
