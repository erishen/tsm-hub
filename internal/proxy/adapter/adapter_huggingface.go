package adapter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/erishen/tsm-hub/internal/store"
)

// HuggingFaceAdapter 是 Hugging Face Inference API 协议的适配器。
//
// Hugging Face 与 OpenAI 的主要差异：
// - URL：https://api-inference.huggingface.co/models/{model-id}
// - 请求体：inputs + parameters（而非 messages）
// - 响应体：[{ generated_text }]（而非 choices[].message.content）
// - 不支持流式（原生 API），如需流式请用 OpenAI 兼容端点
//
// 用户配置：
// - Base URL：https://api-inference.huggingface.co
// - API Key：Hugging Face API Token（hf_...）
// - 模型：meta-llama/Meta-Llama-3-8B-Instruct 等
type HuggingFaceAdapter struct{}

func (a *HuggingFaceAdapter) Name() string { return "huggingface" }

func (a *HuggingFaceAdapter) UpstreamURL(provider store.Provider, clientPath string) string {
	base := strings.TrimRight(provider.BaseURL, "/")
	// 模型名在请求体转换时获取，这里先用占位符
	// 实际 URL 替换在 ConvertRequest 的 extraHeaders 中传递模型名
	return base + "/models/PLACEHOLDER"
}

func (a *HuggingFaceAdapter) AuthHeader(provider store.Provider) (key, value string) {
	return "Authorization", "Bearer " + provider.ResolvedAPIKey()
}

// SignRequest 实现 RequestSigner 接口，替换 URL 中的模型名占位符。
// Hugging Face 不需要签名，但需要把模型名放到 URL 路径中。
func (a *HuggingFaceAdapter) SignRequest(req *http.Request, body []byte, provider store.Provider) error {
	model := req.Header.Get("X-HF-Model")
	if model == "" {
		model = "meta-llama/Meta-Llama-3-8B-Instruct"
	}
	req.Header.Del("X-HF-Model")
	// 替换 URL 中的模型名占位符
	req.URL.Path = strings.Replace(req.URL.Path, "PLACEHOLDER", model, 1)
	return nil
}

// huggingFaceRequest 是 Hugging Face 的请求体格式。
type huggingFaceRequest struct {
	Inputs     string                 `json:"inputs"`
	Parameters map[string]interface{} `json:"parameters,omitempty"`
	Options    map[string]interface{} `json:"options,omitempty"`
}

// huggingFaceResponse 是 Hugging Face 的响应体格式。
type huggingFaceResponse []struct {
	GeneratedText string `json:"generated_text"`
}

func (a *HuggingFaceAdapter) ConvertRequest(body []byte, upstreamModel string) ([]byte, map[string]string, error) {
	var req openAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, fmt.Errorf("parse openai request: %w", err)
	}

	// 把 messages 拼接成单个 inputs 字符串
	var inputs strings.Builder
	for i, m := range req.Messages {
		if i > 0 {
			inputs.WriteString("\n\n")
		}
		switch m.Role {
		case "system":
			inputs.WriteString("System: " + m.Content)
		case "user":
			inputs.WriteString("User: " + m.Content)
		case "assistant":
			inputs.WriteString("Assistant: " + m.Content)
		default:
			inputs.WriteString(m.Role + ": " + m.Content)
		}
	}
	// 最后加上 Assistant: 前缀，引导模型生成回复
	if inputs.Len() > 0 {
		inputs.WriteString("\n\nAssistant: ")
	}

	hfReq := huggingFaceRequest{
		Inputs: inputs.String(),
		Parameters: map[string]interface{}{
			"max_new_tokens": 4096,
			"return_full_text": false,
		},
		Options: map[string]interface{}{
			"wait_for_model": true,
		},
	}
	if req.Temperature != nil {
		hfReq.Parameters["temperature"] = *req.Temperature
	}
	if req.MaxTokens != nil {
		hfReq.Parameters["max_new_tokens"] = *req.MaxTokens
	}

	out, err := json.Marshal(hfReq)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal huggingface request: %w", err)
	}
	// 通过 extraHeaders 传递模型名，在 SignRequest 或 attempt 中替换 URL
	return out, map[string]string{"X-HF-Model": upstreamModel}, nil
}

func (a *HuggingFaceAdapter) ConvertResponse(body []byte) ([]byte, error) {
	var resp huggingFaceResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		// 可能是错误响应，直接返回
		return body, nil
	}

	// 提取生成的文本
	var text string
	if len(resp) > 0 {
		text = resp[0].GeneratedText
	}

	openAIResp := openAIResponse{
		ID:      "hf-" + randomHex(8),
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
			PromptTokens:     0, // Hugging Face 原生 API 不返回 token 统计
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

func (a *HuggingFaceAdapter) ConvertStreamEvent(data []byte) ([]byte, bool, error) {
	// Hugging Face 原生 API 不支持流式，直接透传
	return data, false, nil
}

func (a *HuggingFaceAdapter) StreamDoneEvent() string {
	return "[DONE]"
}
