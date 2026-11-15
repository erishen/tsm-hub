# Security Policy / 安全政策

> 本文件是**漏洞报告与披露流程**；部署加固（TLS、密钥、沙箱、审计等）见
> [docs/SECURITY.md](./docs/SECURITY.md)。GitHub 会自动识别仓库根目录的
> `SECURITY.md`，并在 Security 页与「Report a vulnerability」入口展示。

## Supported versions / 支持版本

| 版本 | 是否接受安全修复 |
|------|------------------|
| `main` 分支 / 最新发布 | ✅ 是 |
| 更早的提交或自行 fork 的旧副本 | ❌ 否，请先在 `main` 上复现 |

本项目是**自托管**软件：更新二进制、升级构建用的 Go 工具链、以及加固运行环境
都由部署方负责。当前 `go.mod` 固定 Go 1.25 最低版本，而 Go 1.25 分支已有部分
标准库公告不再获得补丁；CI 的 `make vulncheck-gate` 会把这类告警列为 WARN，
工具链升级作为独立事项记在 [docs/TODO.md](./docs/TODO.md)。

## Reporting a vulnerability / 报告漏洞

**请不要用公开 Issue、Discussion 或 Pull Request 披露漏洞细节。**

优先使用 GitHub 的私密漏洞报告：仓库 → **Security** → **Report a vulnerability**
（即 `https://github.com/erishen/tsm-hub/security/advisories/new`）。
若该入口不可用，请通过 GitHub 个人资料页列出的联系方式私下联系维护者，
主题注明 `tsm-hub security`，待确认私密通道后再发送细节。

报告请尽量包含：

- 受影响的版本 / commit，以及发现日期；
- 影响面与可造成的后果（越权、密钥泄露、沙箱逃逸、拒绝服务等）；
- 复现步骤或最小 PoC、相关请求与响应片段；
- 运行环境（操作系统、部署方式、Go 版本、是否走 Docker / 反向代理）；
- 如已有修复建议，一并提供。

⚠️ **不要附带真实凭据**：上游 API Key、签发的 Token Key、`data/config.json`
原文、含 Authorization 头的日志，请先脱敏或替换为占位值。报告本身也应按敏感
信息处理。

## Response process / 处理流程

| 阶段 | 目标 |
|------|------|
| 确认收到 | 3 个工作日内回复，并确认私密沟通渠道 |
| 分级与复现 | 7 个工作日内给出初步判定（严重性、影响版本、是否为预期设计） |
| 修复 | 在 `main` 上修复并补回归测试 / 门禁，避免同类问题复发 |
| 披露 | 与报告者协商披露时间；修复可用后通过 GitHub Security Advisory 发布，并按意愿署名 |

若判定为「不在范围内」（见下节），也会说明原因并给出可行的加固建议。

## Scope / 范围

**在范围内**

- 网关请求处理、鉴权与自制 Token Key 的签发 / 校验 / 吊销；
- 管理台 API、会话与登录保护、配置文件与密钥处理（`env:` 引用、`enc:v1:` 加密）；
- Provider 协议适配、SSE 流式转发、错误与超时路径；
- Agent 工具、MCP / Skills 接入与 Docker 沙箱隔离；
- A2A peer 端点、内嵌管理台静态资源。

**不在范围内**

- 自托管部署自身的配置问题（弱口令、未启用 TLS、端口直接暴露公网、文件权限）；
- 由部署方自行接入的第三方上游平台、MCP server、技能文件或模型供应商；
- 需要「宿主机已失陷 / 已持有管理员凭据」才能利用的问题；
- 针对某个自托管实例的纯流量型拒绝服务；
- 依赖中**调用链不可达**的公告（CI 仍会记录，升级依赖时一并处理）。

## Safe harbor / 安全港

对出于善意、遵守本报告流程、不访问他人数据、不破坏服务可用性、不公开未修复
细节的安全研究，本项目不会追究法律责任，也不会向相关平台投诉。研究范围请
限于你自己部署的实例或明确获得授权的环境。

## Verification / 本地自检

```bash
make check            # 七道纯本地门禁（含隐私 / 面板 / 锁一致性）
make cover-check      # 覆盖率下限
make vulncheck        # 打印 govulncheck 原始报告
make vulncheck-gate   # CI 分级门禁（VULN_STRICT=1 连标准库告警也拦截）
```

相关文档：[docs/SECURITY.md](./docs/SECURITY.md) ·
[docs/PRIVACY.md](./docs/PRIVACY.md) ·
[docs/PRIVACY_AUDIT.md](./docs/PRIVACY_AUDIT.md) ·
[.github/workflows/ci.yml](./.github/workflows/ci.yml)
