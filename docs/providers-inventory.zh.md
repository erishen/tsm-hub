# tsm-hub Provider / 模型池梳理

> 快照时间：2026-10-28 11:07（healthz 实测 + data/config.json 磁盘配置交叉核对）；11:14 更新 alibailian 优先级
> 数据来源：`http://localhost:19070/healthz`（运行时）+ `data/config.json`（磁盘配置）

## 0. 一句话结论

**池子很大（14 provider / 104 模型），但现网实例还在跑旧配置**——10-28 新配的 ModelScope（14 模型）和复活的百炼（6 模型）都未加载，需要 `docker compose restart tsm-hub`；百炼免费额度有时限，已提为主链最优先（p10-13），重启后额度内请求直打百炼旗舰，不再先撞慢的 nvidia。

## 1. Provider 池总表（按层分组）

### 免费 · 稳态（healthz 实测健康）

| provider | 模型数 | 实测延迟(EWMA) | 请求/错误 | 说明 |
|---|---|---|---|---|
| deepseek | 2 | 9.5s | 30 / 0 | 全池唯一 0 错误，最稳（注：deepseek 官方为付费，此池当前 key 稳定可用） |
| agnes | 2 | 1.3s | 5 / 1 | apihub.agnes-ai.com，**免费**，全池最快 |
| kimi | 2 | 127s | 21 / 14 | 超时率高（14/21），位置 p99 兜底合理 |

### 生效中 · 免费主力（在用、带病）

| provider | 模型数 | 实测延迟(EWMA) | 请求/错误 | 问题 |
|---|---|---|---|---|
| nvidia | 3 | **97.7s** | 181 / 57 | 延迟被上游超时拉爆；却占主链 p=15 前排 |
| groq | 4 | 2.5s | 74 / 50 | 429 限流为主（qwen3.8-27b org 限流） |
| siliconflow | 21 | **101.5s** | 477 / 43 | 延迟同样被超时拉爆；embed/OCR 主力 |
| openrouter | 21 | 21.2s | 17 / 6 | 429 free-models-per-day 日配额尽（提示充 $10 解锁 1000/天） |
| sensenova | 8 | 23.0s | 181 / 1 | 健康，中速 |
| gemini | 14 | 无记录 | 0 / 0 | 完全闲置：14 个模型只有 2.5-flash 挂在主链 p=98 |
| glm | 2 | 无记录 | 0 / 0 | 仅主链 p=97 兜底位 |

### 已配置 · 待生效（healthz 无记录 = 旧实例没加载）

| provider | 模型数 | 说明 |
|---|---|---|
| modelscope | 14 | 10-28 新增；2000 次/天；主链已挂 11 个 target（p=56-68） |
| alibailian | 6 | 10-28 复活；**免费额度有时限，已提为主链最优先（p10-13）**，额度耗尽 403 → throttle 1h 自动跳过 |

### 已禁用 / 已死

| provider | 模型数 | 死因 |
|---|---|---|
| bai | 4 | 余额 0（balance=0 required=3674），29/29 全错，enabled=false |
| tokenrouter | 1 | 31/31 全超时，enabled=false |
| ovh | - | 大陆不可注册，已从配置删除 |

## 2. 模型明细与路由归属

标注含义：**主链** = '' / chat / fast / agnes-2.0-flash 四条共享链；**专链** = 专用路由；**闲置** = 已配置但未进任何路由。

### deepseek（当前 key 稳定可用）
| 模型 | 路由 |
|---|---|
| deepseek-v4-flash | 专链 `deepseek-v4-flash` 首选(p10) + `tr-zh` p60 |
| deepseek-v4-flash-vision-exp | 闲置 |

### agnes（免费）
| 模型 | 路由 |
|---|---|
| agnes-2.0-flash | 专链 `agnes-2.0-flash` 首选 |
| agnes-2.5-flash | 闲置 |

### kimi（超时率高）
| 模型 | 路由 |
|---|---|
| kimi-k2.6 | 主链 p99 兜底 + tr-zh p70 + deepseek-v4-flash 链 p20 |
| kimi-k2.7-code | 闲置（想用需直连模型名路由） |

### nvidia（免费，integrate.api.nvidia.com）
| 模型 | 路由 |
|---|---|
| z-ai/glm-5.3-flash | 主链 p15 |
| nvidia/nemotron-3-super-120b-a12b | 主链 p15 |
| openai/gpt-oss-20b | 主链 p15 |

### groq（免费，限流中）
| 模型 | 路由 |
|---|---|
| qwen/qwen3.8-27b | 主链 p30 + 直通路由 `qwen/qwen3.8-27b` |
| openai/gpt-oss-20b | 主链 p32 + 直通路由 |
| openai/gpt-oss-120b | 主链 p34 + 直通路由 |
| openai/gpt-oss-safeguard-20b | 仅直通路由 |
| allam-2-7b | 闲置 |

### siliconflow（免费，embed/多模态主力）
| 模型 | 路由 |
|---|---|
| Qwen/Qwen3-8B | 主链 p55 + tr-zh p50 + free-chat p10 |
| THUDM/GLM-4-9B-0414 | free-chat-zh p10 |
| deepseek-ai/DeepSeek-R1-0528-Qwen3-8B | free-reason p10 |
| BAAI/bge-m3 | free-embed p10 |
| BAAI/bge-large-en-v1.5 | free-embed-en p10 |
| Qwen/Qwen2.5-7B-Instruct / THUDM/GLM-Z1-9B / tencent/Hunyuan-MT-7B / deepseek-ai/DeepSeek-OCR / PaddlePaddle/PaddleOCR-VL-1.5 / fnlp/MOSS-TTSD-v0.5 / FunAudioLLM×2 / Qwen3-Embedding-0.6B/4B/8B / Qwen3-Reranker-0.6B/4B/8B / BAAI/bge-large-zh-v1.5 / BAAI/bge-reranker-v2-m3 | 闲置（embed/rerank/TTS/OCR 类，供直连按名调用） |

### modelscope（免费 2000/天，**待重启生效**）
| 模型 | 路由 |
|---|---|
| Qwen/Qwen3.8-Flash-Next | 主链 p58 + free-chat p20 |
| deepseek-ai/DeepSeek-V4.1-Flash-1017 | 主链 p56 |
| Qwen/Qwen3.8-27B | 主链 p60 |
| Qwen/Qwen3.5-122B-A10B | 主链 p61 |
| deepseek-ai/DeepSeek-V4-Pro-0813 | 主链 p62 |
| Qwen/Qwen3.5-27B | 主链 p63 + free-chat-zh p20 |
| Qwen/Qwen3.5-397B-A17B | 主链 p64 |
| Qwen/Qwen3.5-35B-A3B | 主链 p65 |
| Shanghai_AI_Laboratory/Intern-S2-Preview | 主链 p66 |
| nex-agi/Nex-N2.5-Pro | 主链 p67 |
| nex-agi/Nex-N2.5-mini | 主链 p68 |
| opencompass/CompassJudger-1-32B-Instruct | 闲置 |
| XGenerationLab/XiYanSQL-QwenCoder-32B-2504 / -2412 | 闲置（SQL 专用） |

### alibailian（百炼免费额度，**待重启生效**）
| 模型 | 路由 |
|---|---|
| qwen3.8-max | **主链 p10（最优先）** |
| qwen3.8-2.4t-a95b | **主链 p11** |
| qwen3.8-max-0902 | **主链 p12** |
| kimi-k3 | **主链 p13** |
| qwen3.7-text-embedding-flash | free-embed p20 备胎 |
| deepseek-v4-pro-0813 | 闲置 |

> 10-28 11:14 更新：免费额度有时限，4 个 chat 模型已从 p70-73 提到 p10-13（默认/chat/fast 三条链）；agnes-2.0-flash 专链内为 p11-14（保持 agnes 自家 p10 第一）。额度耗尽报 403 → 走 ReportThrottle 1h 熔断，自动 failover 到 nvidia(15)，安全。

### openrouter（免费，日配额已限流）
| 模型 | 路由 |
|---|---|
| cohere/north-mini-code:free | 主链 p95 + free-coder p10 |
| openrouter/free | 主链 p95（weight=1） |
| nvidia/nemotron-3-nano-omni-30b-a3b-reasoning:free | 主链 p96 |
| poolside/laguna-s-2.1:free | 主链 p96 |
| gemma-4×2 / ling-3.0×2 / lfm-2.5 / nex×2 / nemotron-3-super:free / nemotron-3.5-content-safety / laguna-xs / inkling×2 / lyria×2 | 闲置 |

### sensenova（免费）
| 模型 | 路由 |
|---|---|
| sensenova-6.8-flash-lite | 主链 p96 |
| deepseek-v4-flash | deepseek-v4-flash 链 p11 备胎 |
| deepseek-v4-pro / kimi-k3 / glm-5.2 / 6.7-flash-lite / u1-fast / u1.5-lite | 闲置 |

### gemini（免费层）—— 13/14 闲置
仅 `gemini-2.5-flash` 挂主链 p98。2.5-flash-lite / 3.x 全系（3-flash-preview、3.1/3.5/3.6/3.7/3.8 系列）、native-audio×2 全部未进路由。

### glm（免费）
| 模型 | 路由 |
|---|---|
| glm-4-flash | 主链 p97 兜底 |
| glm-5.3-flash | 闲置 |

## 3. 路由表总览（12 条）

| 路由名 | 链路（p 升序） |
|---|---|
| ''（默认）/ chat / fast | **alibailian×4(10-13)** → nvidia×3(15) → groq×3(30/32/34) → siliconflow Qwen3-8B(55) → modelscope×11(56-68) → openrouter×4(95-96) → sensenova(96) → glm(97) → gemini(98) → kimi(99) |
| agnes-2.0-flash | agnes 自家 p10 打头 → alibailian×4(11-14) → 同上主链 |
| tr-zh | siliconflow Qwen3-8B(50) → deepseek-v4-flash(60) → kimi-k2.6(70) |
| free-chat | siliconflow Qwen3-8B(10) → modelscope Qwen3.8-Flash-Next(20) |
| free-chat-zh | siliconflow GLM-4-9B(10) → glm-4-flash(20) → modelscope Qwen3.5-27B(20) |
| free-coder | openrouter north-mini-code:free(10) 单点 |
| free-reason | siliconflow R1-0528-Qwen3-8B(10) 单点 |
| free-embed | siliconflow bge-m3(10) → alibailian qwen3.7-text-embedding-flash(20) |
| free-embed-en | siliconflow bge-large-en-v1.5(10) 单点 |
| deepseek-v4-flash | deepseek(10) → sensenova(11) → nvidia×3(15) → kimi-k2.6(20) |
| openai/gpt-oss-120b / 20b / qwen3.8-27b / gpt-oss-safeguard-20b | groq 直通路由 |

## 4. 问题清单与建议

| # | 级别 | 问题 | 建议 |
|---|---|---|---|
| 1 | 🔴 | **新配置未生效**：modelscope/alibailian 不在 healthz，现网仍跑旧配置 | 用户在 Mac 执行 `cd work/golang/tsm-hub && docker compose restart tsm-hub` |
| 2 | 🔴 | **主链中段慢节点**：nvidia p15 EWMA 97s（超时堆积）。alibailian 已提至 p10-13 最前（免费额度有时限），额度内不再撞 nvidia；额度耗尽熔断后仍会先撞 nvidia | 重启生效后观察一轮；若仍慢，把 3 个 nvidia 主链 target 的 p 降到 45-50 或移除，让 groq(30)→siliconflow(55)→modelscope(56) 顶上 |
| 3 | 🟡 | groq 429 比例 50/74；openrouter 免费日配额尽 | groq 保 p30 依赖退避熔断自愈即可；openrouter 充 $10 解锁 1000/天可选 |
| 4 | 🟡 | 主链 31 个 target 过长，4 条链 90% 重复 | 属设计冗余不算 bug；真正修正优先级后再精简 |
| 5 | 🟢 | bai/tokenrouter 死条目污染 healthz 展示 | 可从 config 删除（或保留 enabled=false 不碍事） |
| 6 | 🟢 | gemini 13 个模型、siliconflow 大半模型闲置 | 它们是"按名直调"备用池，不算问题；要用就加路由 |

## 5. 恒死退避提示

10-28 已在 `internal/router/health.go` 落地恒死指数退避（从未成功过的 provider 连续失败 ≥9 次后冷却 120s→480s→1h 封顶），但该改动在 **Go 二进制里，需 rebuild 镜像**才生效（`docker compose build && up -d`），restart 不够。当前 healthz 里 bai "balance=0" 仍在反复探测（29 次全错）就是旧二进制没有退避的证据。
