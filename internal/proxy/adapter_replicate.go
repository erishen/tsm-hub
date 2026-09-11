package proxy

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/erishen/tsm-hub/internal/store"
)

// ReplicateAdapter 是 Replicate API 协议的适配器。
//
// Replicate 是一个模型运行平台，支持各种开源模型。
// 本适配器使用 Replicate 的同步模式（wait=true），创建预测后等待结果返回。
//
// 主要差异：
// - URL：https://api.replicate.com/v1/predictions?wait=true
// - 认证：Authorization: Bearer（Replicate API Token）
// - 请求体：{ model: "...", input: { prompt: "...", ... } }
// - 响应体：{ id, status, output: "..." 或 [...] }
// - 不支持流式
//
// 用户配置：
// - Base URL：https://api.replicate.com
// - API Key：Replicate API Token（r8_...）
// - 模型：meta/llama-2-70b-chat（模型名，自动使用最新版本）
type ReplicateAdapter struct{}

func (a *ReplicateAdapter) Name() string { return "replicate" }

func (a *ReplicateAdapter) UpstreamURL(provider store.Provider, clientPath string) string {
	base := strings.TrimRight(provider.BaseURL, "/")
	// 同步模式：wait=true，API 会等待预测完成后返回结果
	return base + "/v1/predictions?wait=true"
}

func (a *ReplicateAdapter) AuthHeader(provider store.Provider) (key, value string) {
	return "Authorization", "Bearer " + provider.ResolvedAPIKey()
}

// replicateRequest 是 Replicate 的请求体格式。
type replicateRequest struct {
	Model string                 `json:"model,omitempty"`
	Input map[string]interface{} `json:"input"`
}

// replicateResponse 是 Replicate 的响应体格式。
type replicateResponse struct {
	ID     string      `json:"id"`
	Status string      `json:"status"`
	Output interface{} `json:"output"`
	Error  string      `json:"error,omitempty"`
}

func (a *ReplicateAdapter) ConvertRequest(body []byte, upstreamModel string) ([]byte, map[string]string, error) {
	var req openAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, fmt.Errorf("parse openai request: %w", err)
	}

	// 把 messages 拼接成单个 prompt 字符串
	var prompt strings.Builder
	for i, m := range req.Messages {
		if i > 0 {
			prompt.WriteString("\n\n")
		}
		switch m.Role {
		case "system":
			prompt.WriteString("System: " + m.Content)
		case "user":
			prompt.WriteString("User: " + m.Content)
		case "assistant":
			prompt.WriteString("Assistant: " + m.Content)
		default:
			prompt.WriteString(m.Role + ": " + m.Content)
		}
	}
	// 最后加上 Assistant: 前缀，引导模型生成回复
	if prompt.Len() > 0 {
		prompt.WriteString("\n\nAssistant: ")
	}

	maxTokens := 4096
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	repReq := replicateRequest{
		Model: upstreamModel,
		Input: map[string]interface{}{
			"prompt":      prompt.String(),
			"max_tokens":  maxTokens,
			"temperature": 0.7,
		},
	}
	if req.Temperature != nil {
		repReq.Input["temperature"] = *req.Temperature
	}

	out, err := json.Marshal(repReq)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal replicate request: %w", err)
	}
	return out, nil, nil
}

func (a *ReplicateAdapter) ConvertResponse(body []byte) ([]byte, error) {
	var resp replicateResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		// 可能是错误响应，直接返回
		return body, nil
	}

	// 检查是否有错误
	if resp.Error != "" {
		return nil, fmt.Errorf("replicate error: %s", resp.Error)
	}

	// 提取输出文本（output 可能是字符串或字符串数组）
	var text string
	switch output := resp.Output.(type) {
	case string:
		text = output
	case []interface{}:
		var parts []string
		for _, p := range output {
			if s, ok := p.(string); ok {
				parts = append(parts, s)
			}
		}
		text = strings.Join(parts, "")
	default:
		text = fmt.Sprintf("%v", resp.Output)
	}

	openAIResp := openAIResponse{
		ID:      "rep-" + resp.ID,
		Object:  "chat.completion",
		Created: 0,
		Model:   "",
		Choices: []openAIResponseChoice{
			{
				Index: 0,
				Message: openAIMessage{
					Role:    "assistant",
					Content: text,
				},
				FinishReason: "stop",
			},
		},
		Usage: openAIUsage{
			PromptTokens:     0, // Replicate API 不返回 token 统计
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}

	out, err := json.Marshal(openAIResp)
	if err != nil {
		return nil, fmt.Errorf("marshal openai response: %w", err)
	}
	return out, nil
}

func (a *ReplicateAdapter) ConvertStreamEvent(data []byte) ([]byte, bool, error) {
	// Replicate 同步模式不支持流式，直接透传
	return data, false, nil
}

func (a *ReplicateAdapter) StreamDoneEvent() string {
	return "[DONE]"
}
