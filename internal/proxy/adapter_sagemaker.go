package proxy

import (
	"net/http"
	"strings"

	"github.com/erishen/tsm-hub/internal/store"
)

// SageMakerAdapter 是 AWS SageMaker 端点协议的适配器。
//
// SageMaker 是一个模型部署平台，用户可以部署各种模型（vLLM、TGI、Hugging Face 等）。
// 本适配器假设端点部署的是 OpenAI 兼容格式的模型（最常见的部署方式）。
//
// 主要差异：
// - 认证：AWS Signature V4（和 Bedrock 一样）
// - URL：https://runtime.sagemaker.{region}.amazonaws.com/endpoints/{endpoint}/invocations
// - 请求体/响应体：OpenAI 兼容格式（无需转换）
//
// 用户配置：
// - Base URL：https://runtime.sagemaker.us-east-1.amazonaws.com/endpoints/my-endpoint
// - API Key：access_key:secret_key（用冒号分隔）
// - 模型：填具体模型名（用于网关侧路由匹配，SageMaker 端点本身不区分模型）
type SageMakerAdapter struct {
	OpenAIAdapter
}

func (a *SageMakerAdapter) Name() string { return "sagemaker" }

func (a *SageMakerAdapter) UpstreamURL(provider store.Provider, clientPath string) string {
	base := strings.TrimRight(provider.BaseURL, "/")
	// 如果用户已经填了完整的 invocations URL，直接使用
	if strings.HasSuffix(base, "/invocations") {
		return base
	}
	// 否则追加 /invocations
	return base + "/invocations"
}

func (a *SageMakerAdapter) AuthHeader(provider store.Provider) (key, value string) {
	// SageMaker 使用 AWS SigV4 签名，认证头在 SignRequest 中生成
	return "", ""
}

// SignRequest 实现 RequestSigner 接口，对请求进行 AWS SigV4 签名。
func (a *SageMakerAdapter) SignRequest(req *http.Request, body []byte, provider store.Provider) error {
	accessKey, secretKey, err := awsCredentials(provider)
	if err != nil {
		return err
	}
	region := awsRegion(provider.BaseURL)
	// SageMaker 的 service 名是 sagemaker
	return signAWSRequest(req, body, accessKey, secretKey, region, "sagemaker")
}
