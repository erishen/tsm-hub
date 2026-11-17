# 依赖安全基线与修复路线

> 2026-11-16 快照。开启 Dependabot vulnerability alerts / security updates 后首次全量盘点。
> 数据源：`gh api --paginate 'repos/erishen/tsm-hub/dependabot/alerts?state=open&per_page=100'`。

## 结论先行

**89 条 open 告警 / 33 个包**，其中 3 critical、35 high。**20 条是 runtime**，其余 69 条
是构建期（development）传递依赖。修这些必须**在用户本机 Terminal 刷锁文件**，本沙箱
跑不动完整的 pnpm/npm 安装（见下文「本机操作」）。开启安全更新 ≠ 告警清零。

| 维度 | 分布 |
|------|------|
| 严重度 | low 11 / medium 40 / high 35 / critical 3 |
| scope | development 69 / runtime 20 |
| manifest | `web/pnpm-lock.yaml` 82 / `mcp/package-lock.json` 7 |

`make dep-alerts` 可随时复跑这份盘点（报告型，不拦截提交）。

## 重点：runtime 的 10 条 high

| 包 | 当前 | 最低修复 | 条数 | 说明 |
|----|------|----------|------|------|
| `@angular/common` / `router` / `core` / `compiler` | 19.2.25 | **无补丁版本** | 10 | Angular 19 分支没有可用修复版本，只能整体升 major |
| `zod` | 3.25.76 | 4.6.6 | 1 | MCP 桥运行时依赖，major 跃迁 |
| `@modelcontextprotocol/sdk` | 1.0.1 | 1.24.0 / 1.32.0 | 1 high + 1 medium | 同上，两条公告修复版本不同 |

另有 5 条 runtime medium：`@hono/node-server` 2.1.5→2.1.6（2 条）、`glob`
13.0.6→14.0.2、`minimatch` 10.2.6→11.0.1、`@modelcontextprotocol/sdk` 的低危项。

⚠️ **PR #15（`@angular/cli` 19→22）不解决运行时的 10 条 high**：它只升 CLI，
`@angular/core` / `common` / `router` / `compiler` 仍锁在 19.2.25。要清掉必须整包
升级到同一 major。

## development 高危（不进生产 bundle，但影响构建机）

3 条 critical 全在构建链：`piscina` 4.8.0、`shell-quote` 1.10.0、`proxy-addr` 2.0.7。
其余集中在 `tar` 6.2.1（8 条）、`node-forge` 1.4.0（16 条）、`follow-redirects`
1.16.0（3 条）、`vite` 6.4.2（7 条）、`webpack-dev-server` 5.2.2 等——全部来自
Angular 19 的构建工具链，随 Angular 整体升级会一并消掉大半。

## 为什么 Dependabot 自动修不完

开启安全更新后 GitHub 跑了一轮 `dynamic` job：

- ✅ `node-forge` 1.4.0 → 1.4.3：自动开 **PR #16**，只改 `web/pnpm-lock.yaml`，
  CI 四个 job 全绿、可合并（**合并需单独授权**）。
- ✅ `setup-go` / `sqlite` 等：自动 rebase，其中 sqlite PR #13 因 main 已升级被关闭。
- ❌ `minimatch`：run `43862960981` / `43880562902` 报 `security_update_not_possible`
  —— web 侧 `latest-resolvable-version: 9.0.9`，`lowest-non-vulnerable-version: 11.0.1`。
  隔了一个 major 且被上游构建链约束，自动更新直接放弃。

剩下这些告警需要人工决策版本跃迁，GitHub 不会替你做。

### minimatch 专项（2026-11-17 起，配置已改、lock 待本机刷新）

三条 open 告警同源于 `GHSA-g3hv-75wg-m249`（CVE-2026-118764，minimatch ReDoS，
medium，修复版 11.0.1）：

| alert | manifest | scope | 锁中版本 | 锁定链 |
|-------|----------|-------|----------|--------|
| #99 | `web/pnpm-lock.yaml` | development | 9.0.9 | `@tufjs/models` / `glob@10.4.5` / `ignore-walk@7.0.0`（均 `^9`） |
| #55 | `web/pnpm-lock.yaml` | development | 2.1.4 | `brace-expansion`（minimatch 9 的下游，修复版 2.1.7） |
| #5 | `mcp/package-lock.json` | runtime | 10.2.6 | MCP server 传递依赖 |

**已落地的配置改动**（本仓已提交，锁文件尚未刷新）：

- `web/package.json` → `pnpm.overrides.minimatch = 11.0.1`：强制覆盖三条 `^9` 链；
  minimatch 11 只依赖 `brace-expansion@^5.0.12` 与 `@isaacs/nonrecursive-glob-match`，
  升级后 #55 的 brace-expansion 2.1.4 随之消失。
- `mcp/package.json` → npm 顶层 `overrides.minimatch = 11.0.1`。

⚠️ **这不是刷锁**：`package.json` 声明了 override，但 `pnpm-lock.yaml` /
`package-lock.json` 仍停在旧版本——中间态下 `dep-alerts` 仍会报这 3 条，属预期。
必须按下方「本机操作」刷新锁后告警才会消除。**CI 不装前端/MCP 依赖**，故中间态
不会让 CI 变红，但也不能代替本机验证。

跨 major 注意：minimatch 11 的 `engines` 为 `18 || 20 || >=22`，其依赖
`brace-expansion@5` 要求 Node 20+（本机 Node 22 满足）。上游锁的是 API 兼容的
`^9`/`^10` 范围，强制到 11 后须以 `pnpm run build` 实测构建，不能只看告警数。

## 修复路线（按风险递增）

1. **低风险**：合并 #16（node-forge 安全补丁）；`mcp/` 里的 patch/minor 项用
   `npm audit fix`（`@hono/node-server`、`glob` 等）。Actions 升 v7 的
   #9/#10/#11/#14 属于 CI 维护，与依赖告警无关，可顺带消除 Node 20 弃用提示。
2. **中**：Angular 19 → 22 **全套**（`core` / `common` / `compiler` / `router` /
   `forms` / `platform-browser` + `cli` / `build-angular` / `compiler-cli` +
   `typescript` / `zone.js` / `rxjs`）。这一步同时清掉 runtime 的 10 条 high
   和大部分 development 高危，但要重跑前端构建与管理台回归。
3. **高**：MCP SDK major 升级（`@modelcontextprotocol/sdk` 1.32 + `zod` 4）。
   `npm audit fix --force` 有破坏性，必须改 `mcp/package.json` 并回归 MCP 桥
   （`/metrics` 冒烟 + `make alert-check`）。

## 本机操作（必须在本机 Terminal 跑）

**为什么不能在沙箱里做**：pnpm/npm 全量依赖解析吃内存会被 OOM kill（exit 137），
出口到 `registry.npmjs.org` 常年 ECONNRESET；实测刷 lockfile 只能在用户自己的 Mac
上跑。国内环境用镜像源：

```bash
# web/：pnpm 是唯一依赖真相源
cd web
pnpm install --registry https://registry.npmmirror.com   # 会应用 pnpm.overrides.minimatch
pnpm run build                                            # 跨 major 必做：验证前端仍能构建
pnpm audit                                                # 看剩余告警

# mcp/：npm 锁已跟踪（mcp/package-lock.json）
cd ../mcp
npm install --registry=https://registry.npmmirror.com    # 会应用顶层 overrides.minimatch
npm audit fix

# 回仓根复核
make check                 # 含 web-lock-check（会校验 tarball host）
make dep-alerts            # 89 → 应至少少 3 条（#99/#55/#5）
```

> **minimatch 专项提示**：`pnpm install` / `npm install` 会把 override 解析出的
> 11.0.1 写进锁文件。若走镜像下载，`pnpm` 9 的 lockfile v9 不内嵌 tarball URL（无 host
> 问题），但 npm 会把实际下载 URL 写进 `mcp/package-lock.json`——提交前必须把
> `registry.npmmirror.com` 换回 `registry.npmjs.org`（只换 host，不动版本/integrity），
> 否则 `web-lock-check` 会拦下。若 `pnpm run build` 因跨 major 失败，撤销
> `web/package.json` 的 `pnpm.overrides` 后重新 `pnpm install` 即可回到稳定态。

约束：

- `web/pnpm-lock.yaml` 是前端**唯一**真相源；`web/package-lock.json` 是
  `PM ?= pnpm || npm` 回退产物，被 `.gitignore` 忽略、绝不入库，`make web-lock-check`
  会把误提交挡下。
- **`mcp/package-lock.json` 的 `resolved` host 必须统一为官方
  `registry.npmjs.org`**：`--registry` 只影响本机当次下载，但 npm 会把实际 tarball
  URL 写进锁文件，直接提交会把海外克隆者也指向国内镜像。2026-11-16 已把历史遗留的
  27 条 `registry.npmmirror.com` 换回（路径结构与官方一致，版本/integrity 未变）；
  `make web-lock-check` 现在会拦截任何非官方 host，防止再次带入。
- pnpm 9 的 lockfile v9 不内嵌 tarball URL，不需要旧版那种把
  `registry.npmjs.org` 全量 `sed` 成镜像 host 的临时招数。
- 改了 `web/package.json` 就必须同步刷新 `pnpm-lock.yaml`（importer specifier
  不一致会被 `web-lock-check` 拦）。
- CI 不跑 `pnpm install`：Dockerfile 只编译 Go，前端产物由宿主预先构建并 embed。

## 为什么不进 `make check`

`dep-alerts` 需要 gh CLI + 仓库权限 + 网络，且告警数量随上游公告波动；把它做成
硬门禁只会得到一个永久红的 CI。`make check` 仍是七道纯本地、可离线复现的静态门禁，
`make dep-alerts` 是手动体检命令，永远不改变构建结论（API 调用失败除外）。

```bash
make dep-alerts                                        # 全量表格
python3 -S scripts/dep-alerts.py --min-severity high   # 只看 high/critical
python3 -S scripts/dep-alerts.py --json                # 结构化输出
python3 -S scripts/dep-alerts.py --report .data/alerts-all.json   # 离线复现快照
python3 -S scripts/dep-alerts.py --self-test           # 离线断言（无需 gh / 网络）
```

「告警清零」是这份体检最理想的结果，脚本按成功处理：API 返回 `[]`（或多个空页
拼接）时 `--json` 输出 `total: 0` 的合法 JSON、普通模式打印 `all clear`，退出码 0；
只有连一个 JSON 数组都没解析到（空响应）才判为上游异常（rc=2）。这样消费方不会
把「全清」误读成审计失败。

⚠️ 该 REST 端点**不支持 `page` 参数**（返回 400 `Pagination using the page
parameter is not supported`），分页必须靠 `gh api --paginate` 走 Link 游标；输出是
多个 JSON 数组拼接的流，脚本用 `JSONDecoder.raw_decode` 逐段解析。
