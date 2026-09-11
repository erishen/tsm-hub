package adapter

import (
	"strings"

	"github.com/erishen/tsm-hub/internal/store"
)

// AzureAdapter 是 Azure OpenAI 协议的适配器。
//
// Azure OpenAI 与 OpenAI 的主要差异：
// - URL 格式：https://{resource}.openai.azure.com/openai/deployments/{deployment}/chat/completions?api-version=xxx
// - 认证头：api-key（而非 Authorization: Bearer）
// - 模型名通过 deployment 名指定（在 URL 路径中），请求体中的 model 字段被忽略
// - 请求体和响应体基本兼容 OpenAI 格式，无需转换
//
// 用户在 Base URL 中填：https://{resource}.openai.azure.com/openai/deployments/{deployment}
type AzureAdapter struct{}

func (a *AzureAdapter) Name() string { return "azure" }

func (a *AzureAdapter) UpstreamURL(provider store.Provider, clientPath string) string {
	base := strings.TrimRight(provider.BaseURL, "/")
	// 客户端路径 /v1/chat/completions → /chat/completions
	path := strings.TrimPrefix(clientPath, "/v1")
	if path == "" {
		path = "/chat/completions"
	}
	// 追加 api-version 查询参数
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + path + sep + "api-version=2024-02-15-preview"
}

func (a *AzureAdapter) AuthHeader(provider store.Provider) (key, value string) {
	return "api-key", provider.ResolvedAPIKey()
}

func (a *AzureAdapter) ConvertRequest(body []byte, upstreamModel string) ([]byte, map[string]string, error) {
	// Azure 请求体格式基本兼容 OpenAI，无需转换
	// 但 Azure 忽略请求体中的 model 字段（用 URL 中的 deployment 名），所以不需要替换模型名
	return body, nil, nil
}

func (a *AzureAdapter) ConvertResponse(body []byte) ([]byte, error) {
	// Azure 响应体格式基本兼容 OpenAI，无需转换
	return body, nil
}

func (a *AzureAdapter) ConvertStreamEvent(data []byte) ([]byte, bool, error) {
	// Azure 流式响应格式基本兼容 OpenAI，无需转换
	return data, false, nil
}

func (a *AzureAdapter) StreamDoneEvent() string {
	return "[DONE]"
}
