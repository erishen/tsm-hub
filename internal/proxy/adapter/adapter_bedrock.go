package adapter

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/erishen/tsm-hub/internal/store"
)

// BedrockAdapter 是 AWS Bedrock 协议的适配器。
//
// AWS Bedrock 是一个模型平台，支持多种底层模型（Anthropic Claude、Meta Llama、Amazon Titan 等）。
// 本适配器默认使用 Anthropic Claude 模型格式（Bedrock 上最常用的模型）。
//
// 主要差异：
// - 认证：AWS Signature V4（需要 access_key 和 secret_key）
// - URL：https://bedrock-runtime.{region}.amazonaws.com/model/{model-id}/invoke
// - 流式：/invoke-with-response-stream
// - 请求体：Anthropic 格式（与 AnthropicAdapter 相同）
// - 响应体：Anthropic 格式（与 AnthropicAdapter 相同）
//
// 用户配置：
// - Base URL：https://bedrock-runtime.us-east-1.amazonaws.com
// - API Key：access_key:secret_key（用冒号分隔）
// - 模型：anthropic.claude-3-sonnet-20240229-v1:0
type BedrockAdapter struct{}

func (a *BedrockAdapter) Name() string { return "bedrock" }

func (a *BedrockAdapter) UpstreamURL(provider store.Provider, clientPath string) string {
	base := strings.TrimRight(provider.BaseURL, "/")
	// 模型名在 SignRequest 中替换到 URL 中
	return base + "/model/PLACEHOLDER/invoke"
}

func (a *BedrockAdapter) AuthHeader(provider store.Provider) (key, value string) {
	// Bedrock 使用 AWS SigV4 签名，认证头在 SignRequest 中生成
	return "", ""
}

// SignRequest 实现 RequestSigner 接口，对请求进行 AWS SigV4 签名。
func (a *BedrockAdapter) SignRequest(req *http.Request, body []byte, provider store.Provider) error {
	accessKey, secretKey, err := awsCredentials(provider)
	if err != nil {
		return err
	}
	region := awsRegion(provider.BaseURL)

	// 从 extraHeaders 中获取模型名（在 ConvertRequest 中设置的 X-Bedrock-Model）
	model := req.Header.Get("X-Bedrock-Model")
	if model == "" {
		model = "anthropic.claude-3-sonnet-20240229-v1:0"
	}
	req.Header.Del("X-Bedrock-Model")

	// 替换 URL 中的模型名占位符
	req.URL.Path = strings.Replace(req.URL.Path, "PLACEHOLDER", model, 1)

	// AWS SigV4 签名
	return signAWSRequest(req, body, accessKey, secretKey, region, "bedrock")
}

func (a *BedrockAdapter) ConvertRequest(body []byte, upstreamModel string) ([]byte, map[string]string, error) {
	// 解析 OpenAI 请求，转换为 Anthropic 格式（Bedrock 上的 Claude 模型用 Anthropic 格式）
	anthropic := &AnthropicAdapter{}
	converted, _, err := anthropic.ConvertRequest(body, upstreamModel)
	if err != nil {
		return nil, nil, err
	}
	// Bedrock 的 invoke 端点需要在 URL 中包含模型名，这里通过 extraHeaders 传递模型名
	// 实际 URL 替换在 attempt 中处理（需要访问 provider）
	return converted, map[string]string{"X-Bedrock-Model": upstreamModel}, nil
}

func (a *BedrockAdapter) ConvertResponse(body []byte) ([]byte, error) {
	// Bedrock 的响应体是 Anthropic 格式，复用 AnthropicAdapter 的转换
	anthropic := &AnthropicAdapter{}
	return anthropic.ConvertResponse(body)
}

func (a *BedrockAdapter) ConvertStreamEvent(data []byte) ([]byte, bool, error) {
	// Bedrock 的流式响应是 Anthropic 格式，复用 AnthropicAdapter 的转换
	anthropic := &AnthropicAdapter{}
	return anthropic.ConvertStreamEvent(data)
}

func (a *BedrockAdapter) StreamDoneEvent() string {
	anthropic := &AnthropicAdapter{}
	return anthropic.StreamDoneEvent()
}

// === AWS Signature V4 实现 ===

// awsCredentials 解析 Provider 的 API Key 字段，提取 access_key 和 secret_key。
// 格式：access_key:secret_key
func awsCredentials(provider store.Provider) (accessKey, secretKey string, err error) {
	key := provider.ResolvedAPIKey()
	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid AWS credentials format, expected access_key:secret_key")
	}
	return parts[0], parts[1], nil
}

// awsRegion 从 Base URL 中提取 region。
// 格式：https://bedrock-runtime.us-east-1.amazonaws.com → us-east-1
func awsRegion(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "us-east-1"
	}
	host := u.Hostname()
	// bedrock-runtime.us-east-1.amazonaws.com → us-east-1
	parts := strings.Split(host, ".")
	if len(parts) >= 3 && parts[0] == "bedrock-runtime" {
		return parts[1]
	}
	return "us-east-1"
}

// signAWSRequest 对 HTTP 请求进行 AWS Signature V4 签名。
func signAWSRequest(req *http.Request, body []byte, accessKey, secretKey, region, service string) error {
	now := time.Now().UTC()
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	// 设置必要的头
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("Host", req.URL.Host)

	// 1. 创建规范请求
	payloadHash := sha256Hex(body)
	canonicalHeaders, signedHeaders := canonicalHeaders(req)
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		canonicalQuery(req.URL),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	// 2. 创建待签名字符串
	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", date, region, service)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	// 3. 计算签名密钥
	signingKey := awsSignatureKey(secretKey, date, region, service)

	// 4. 计算签名
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	// 5. 添加 Authorization 头
	authorization := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, credentialScope, signedHeaders, signature)
	req.Header.Set("Authorization", authorization)

	return nil
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func awsSignatureKey(secretKey, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretKey), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	return kSigning
}

func canonicalHeaders(req *http.Request) (canonical, signed string) {
	headers := make(map[string]string)
	for k, v := range req.Header {
		if len(v) > 0 {
			headers[strings.ToLower(k)] = strings.TrimSpace(v[0])
		}
	}
	// host 头需要单独添加
	headers["host"] = req.URL.Host

	var keys []string
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var canonicalParts, signedParts []string
	for _, k := range keys {
		canonicalParts = append(canonicalParts, k+":"+headers[k])
		signedParts = append(signedParts, k)
	}
	return strings.Join(canonicalParts, "\n") + "\n", strings.Join(signedParts, ";")
}

func canonicalURI(u *url.URL) string {
	if u.Path == "" {
		return "/"
	}
	return u.Path
}

func canonicalQuery(u *url.URL) string {
	if u.RawQuery == "" {
		return ""
	}
	values, _ := url.ParseQuery(u.RawQuery)
	var keys []string
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, v := range values[k] {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// readRequestBody 读取请求体（用于签名），同时保留 body 可重复读取。
func readRequestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return []byte{}, nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(strings.NewReader(string(body)))
	return body, nil
}
