# 免费 token 源配置指南（2026-10-28 实测口径）

> 目标：给 tsm-hub 增补免费 LLM 上游，扩大 failover 池。
> 本次新增 **2 个已实测直连可达** 的免费源，均以**禁用态预埋**进 `data/config.json`
> （`enabled: false` + 占位 key），router 会跳过禁用 provider，不影响现网流量。

## 已预埋的两个源

### 1. ModelScope 魔搭（国内直连，性价比最高）

| 项 | 值 |
|---|---|
| 免费额度 | **2000 次/天**（全模型共享，单模型 ≤500 次/天，0 点重置，永久免费） |
| 端点 | `https://api-inference.modelscope.cn/v1`（OpenAI 兼容，已实测） |
| 前置 | 手机号注册 + **绑定阿里云账号**（必须，否则调用报错）+ 实名 |
| 预埋模型 | 14 个（2026-10-28 逐一实测可用）：DeepSeek-V4.1-Flash-1017（主力）、Qwen3.8-Flash-Next、Qwen3.8-27B、Qwen3.5-122B-A10B、DeepSeek-V4-Pro-0813、Qwen3.5-27B、Qwen3.5-397B-A17B、Qwen3.5-35B-A3B、Intern-S2-Preview、Nex-N2.5-Pro/mini、CompassJudger-1-32B、XiYanSQL-QwenCoder-32B ×2 |

**开通步骤：**
1. 注册/登录 https://modelscope.cn （手机号或阿里云账号）
2. 个人中心 → 账号设置 → **绑定阿里云账号**
3. 访问令牌页 https://modelscope.cn/my/myaccesstoken → 创建新令牌 → 勾选「大模型推理」→ 建议选**长期有效**
4. 拿到 Token（`ms-` 前缀）

**接入 tsm-hub（二选一）：**
- Admin 台：`http://localhost:19070` → Providers → 编辑 `modelscope` → 填入 Token → 启用
- 或改文件：`data/config.json` 里把 `REPLACE-ME-MODELSCOPE-TOKEN` 换成真实 Token、`enabled` 改 `true`，然后 `docker compose restart tsm-hub`

> 完整可调模型清单（37 个，含 MiniMax-M3、Step-5-Preview、ERNIE-4.5-300B 等）：
> `curl -s https://api-inference.modelscope.cn/v1/models`（无需鉴权）
> 想加别的模型，把 Model-Id 加进 provider 的 `models` 数组 + 路由 targets 即可。

### ~~2. OVHcloud AI Endpoints~~（已移除）

**大陆无法正常注册**：OVH 国际站/美国站明确不对中国（及土耳其）提供服务，注册页「税务居住国」无中国大陆可选；
变通注册需外区邮箱 + 选境外税务居住国 + 境外信用卡/PayPal，且订单有 24-48 工作小时人工审核，大陆用户极易被砍单；
即便账号注册成功，创建 AI Endpoints Token 还需 active Public Cloud 项目（要求绑支付方式）。已从 `data/config.json` 移除。

## 路由接线（已预埋）

- 主 failover 链（默认路由 / `agnes-2.0-flash` / `chat` / `fast`）：
  ModelScope 4 模型 priority=60-62（排在 nvidia=15、groq=30-34、siliconflow=55 之后，
  openrouter/sensenova=95+ 之前）。
- `free-chat` ← `Qwen/Qwen3.8-Flash-Next`（p=20）；`free-chat-zh` ← `ZhipuAI/GLM-5.2`（p=20）。

## 评估过但未采纳的源（避免再踩）

| 源 | 不采纳原因 |
|---|---|
| **OVHcloud AI Endpoints** | **大陆无法注册**：明确不对中国提供服务，税务居住国无大陆可选，绕道注册易被人工审核砍单，见上 |
| GitHub Models | **2026-07-30 已全量下线**，playground 和推理 API 都没了 |
| Cerebras | 免费层已改为「绑卡后 $5 试用额度、30 天过期」，不算真免费 |
| Hugging Face | 免费额度缩水到 $0.10/月，只够零星测试 |
| Mistral La Plateforme | 需手机验证，且本环境直连不通（HTTP 000） |
| SambaNova | 免费层每模型仅 20 次/天，太少 |
| Cloudflare Workers AI | 10k neurons/天可用，但端点要拼 account_id，且速率按 neurons 计费复杂，暂缓 |
| Together AI | 无免费层（$5 起充） |
| LLM7.io | 端点可达，但实测匿名免费层已取消（无 key 401），当前目录全为 pro 计费档 |
| glhf.chat / Chutes.ai | glhf 网关 526（SSL 坏）；Chutes `/v1/models` 404，均不可用 |

## 安全网

新 provider 的 key 填错/欠费时，命中 tsm-hub 的「从未成功且连续全败 → 指数退避（封顶 1h）」
熔断（`internal/router/health.go`，2026-10-28 加入），不会拖垮 failover 链；
修好 key 成功一次即复位。
