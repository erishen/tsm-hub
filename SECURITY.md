# Security Policy / 安全政策

> 本文件是**漏洞报告与披露流程**；部署加固（TLS、密钥、沙箱、审计等）见
> [docs/SECURITY.md](./docs/SECURITY.md)。GitHub 会自动识别仓库根目录的
> `SECURITY.md` 并在 Security 页展示；仓库已开启 *Private vulnerability reporting*，
> 「Report a vulnerability」按钮可用。若入口因设置变更或 fork 而不可用，请用下方备用联系方式。

## Supported versions / 支持版本

| 版本 | 是否接受安全修复 |
|------|------------------|
| `main` 分支 / 最新发布 | ✅ 是 |
| 更早的提交或自行 fork 的旧副本 | ❌ 否，请先在 `main` 上复现 |

本项目是**自托管**软件：更新二进制、升级构建用的 Go 工具链、以及加固运行环境
都由部署方负责。当前 `go.mod` 钉在 **Go 1.26.11**（该补丁版修掉了 Go 1.25 分支
遗留的 12 条标准库公告）；CI 的 `make vulncheck-gate` 默认 `VULN_STRICT=1`，
第三方与标准库的可达漏洞**一律硬失败**，不再有 WARN 豁免。后续工具链升级见
[docs/TODO.md](./docs/TODO.md)。

## Reporting a vulnerability / 报告漏洞

**请不要用公开 Issue、Discussion 或 Pull Request 披露漏洞细节。**

优先使用 GitHub 的私密漏洞报告：仓库 → **Security** → **Report a vulnerability**
（即 `https://github.com/erishen/tsm-hub/security/advisories/new`；仓库已开启
*Private vulnerability reporting*，该按钮当前可见）。
若入口不可用（例如设置被关闭或你在 fork 中报告），请先回到**上游公开仓**使用
上述 PVR 入口提交；若 PVR 整体不可用，请通过维护者站点
<https://erishen.cn/about/> 上公布的联系渠道发起
**不含任何漏洞细节**的私下联络，主题注明 `tsm-hub security`，待确认私密通道后
再发送细节——**切勿改用公开 Issue / Discussion / PR**。

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
make vulncheck-gate   # CI 门禁（默认 VULN_STRICT=1，所有可达漏洞都拦截）
```

相关文档：[docs/SECURITY.md](./docs/SECURITY.md) ·
[docs/PRIVACY.md](./docs/PRIVACY.md) ·
[docs/PRIVACY_AUDIT.md](./docs/PRIVACY_AUDIT.md) ·
[.github/workflows/ci.yml](./.github/workflows/ci.yml)
