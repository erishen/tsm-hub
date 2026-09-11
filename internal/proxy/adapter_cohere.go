package proxy

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/erishen/tsm-hub/internal/store"
)

// CohereAdapter 是 Cohere API 协议的适配器。
//
// Cohere 与 OpenAI 的主要差异：
// - URL：https://api.cohere.ai/v1/chat
// - 请求体：message + chat_history（而非 messages 数组）
// - 响应体：text（而非 choices[].message.content）
// - 流式：SSE 事件格式不同（is_finished 标志）
type CohereAdapter struct{}

func (a *CohereAdapter) Name() string { return "cohere" }

func (a *CohereAdapter) UpstreamURL(provider store.Provider, clientPath string) string {
	base := strings.TrimRight(provider.BaseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat"
	}
	return base + "/v1/chat"
}

func (a *CohereAdapter) AuthHeader(provider store.Provider) (key, value string) {
	return "Authorization", "Bearer " + provider.ResolvedAPIKey()
}

// cohereMessage 是 Cohere 的 chat_history 消息格式。
type cohereMessage struct {
	Role    string `json:"role"`
	Message string `json:"message"`
}

// cohereRequest 是 Cohere 的请求体格式。
type cohereRequest struct {
	Message     string          `json:"message"`
	ChatHistory []cohereMessage `json:"chat_history,omitempty"`
	Model       string          `json:"model,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
}

func (a *CohereAdapter) ConvertRequest(body []byte, upstreamModel string) ([]byte, map[string]string, error) {
	var req openAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, fmt.Errorf("parse openai request: %w", err)
	}

	// 分离 system 消息和最后一条 user 消息
	var systemText strings.Builder
	chatHistory := make([]cohereMessage, 0, len(req.Messages))
	var lastUserMessage string

	for i, m := range req.Messages {
		if m.Role == "system" {
			if systemText.Len() > 0 {
				systemText.WriteString("\n\n")
			}
			systemText.WriteString(m.Content)
			continue
		}
		// Cohere 角色：USER / CHATBOT
		role := "USER"
		if m.Role == "assistant" {
			role = "CHATBOT"
		}
		// 最后一条消息作为 message，之前的作为 chat_history
		if i == len(req.Messages)-1 && m.Role == "user" {
			lastUserMessage = m.Content
		} else {
			chatHistory = append(chatHistory, cohereMessage{Role: role, Message: m.Content})
		}
	}

	// 如果最后一条不是 user 消息，把所有消息都放到 chat_history，message 留空
	if lastUserMessage == "" && len(req.Messages) > 0 {
		lastMsg := req.Messages[len(req.Messages)-1]
		lastUserMessage = lastMsg.Content
	}

	// system 消息拼接到 message 前面
	if systemText.Len() > 0 {
		lastUserMessage = systemText.String() + "\n\n" + lastUserMessage
	}

	cohereReq := cohereRequest{
		Message:     lastUserMessage,
		ChatHistory: chatHistory,
		Model:       upstreamModel,
		MaxTokens:   4096,
		Stream:      req.Stream,
	}
	if req.Temperature != nil {
		cohereReq.Temperature = *req.Temperature
	}
	if req.MaxTokens != nil {
		cohereReq.MaxTokens = *req.MaxTokens
	}

	out, err := json.Marshal(cohereReq)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal cohere request: %w", err)
	}
	return out, nil, nil
}

// cohereResponse 是 Cohere 的非流式响应格式。
type cohereResponse struct {
	Text          string `json:"text"`
	GenerationID  string `json:"generation_id"`
	FinishReason  string `json:"finish_reason"`
	Meta          struct {
		Tokens struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"tokens"`
	} `json:"meta"`
}

func (a *CohereAdapter) ConvertResponse(body []byte) ([]byte, error) {
	var resp cohereResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse cohere response: %w", err)
	}

	// 转换 finish_reason
	finishReason := "stop"
	switch resp.FinishReason {
	case "MAX_TOKENS":
		finishReason = "length"
	case "COMPLETE", "STOP":
		finishReason = "stop"
	}

	openAIResp := openAIResponse{
		ID:      resp.GenerationID,
		Object:  "chat.completion",
		Created: 0,
		Model:   "",
		Choices: []openAIResponseChoice{
			{
				Index: 0,
				Message: openAIMessage{
					Role:    "assistant",
					Content: resp.Text,
				},
				FinishReason: finishReason,
			},
		},
		Usage: openAIUsage{
			PromptTokens:     resp.Meta.Tokens.InputTokens,
			CompletionTokens: resp.Meta.Tokens.OutputTokens,
			TotalTokens:      resp.Meta.Tokens.InputTokens + resp.Meta.Tokens.OutputTokens,
		},
	}

	out, err := json.Marshal(openAIResp)
	if err != nil {
		return nil, fmt.Errorf("marshal openai response: %w", err)
	}
	return out, nil
}

// cohereStreamChunk 是 Cohere 流式响应的单个 chunk 格式。
type cohereStreamChunk struct {
	IsFinished bool   `json:"is_finished"`
	EventType  string `json:"event_type"`
	Text       string `json:"text,omitempty"`
	Response   struct {
		GenerationID string `json:"generation_id"`
		Meta         struct {
			Tokens struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"tokens"`
		} `json:"meta"`
	} `json:"response,omitempty"`
}

func (a *CohereAdapter) ConvertStreamEvent(data []byte) ([]byte, bool, error) {
	if string(data) == "[DONE]" {
		return data, false, nil
	}

	var chunk cohereStreamChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		// 无法解析的事件直接透传
		return data, false, nil
	}

	// 跳过非文本生成事件
	if chunk.EventType != "text-generation" && !chunk.IsFinished {
		return nil, true, nil
	}

	// 如果是结束事件，返回 [DONE]
	if chunk.IsFinished {
		return []byte("[DONE]"), false, nil
	}

	openAIChunk := openAIStreamChunk{
		ID:      chunk.Response.GenerationID,
		Object:  "chat.completion.chunk",
		Created: 0,
		Model:   "",
		Choices: []openAIStreamChoice{
			{
				Index: 0,
				Delta: openAIMessage{
					Role:    "assistant",
					Content: chunk.Text,
				},
				FinishReason: "",
			},
		},
	}

	out, err := json.Marshal(openAIChunk)
	if err != nil {
		return nil, false, err
	}
	return out, false, nil
}

func (a *CohereAdapter) StreamDoneEvent() string {
	return "[DONE]"
}
