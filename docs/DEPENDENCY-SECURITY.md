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
- ❌ `minimatch` in `/web`：run `43862960981` 报 `security_update_not_possible`
  —— `latest-resolvable-version: 9.0.9`，`lowest-non-vulnerable-version: 11.0.1`。
  隔了一个 major 且被 Angular 19 构建链约束，自动更新直接放弃。

也就是说，剩下这些告警需要人工决策版本跃迁，GitHub 不会替你做。

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
pnpm install --registry https://registry.npmmirror.com
pnpm audit                 # 看剩余告警
# 按第 2 档改完 package.json 后，再 pnpm install 刷新锁

# mcp/：npm 锁已跟踪（mcp/package-lock.json）
cd ../mcp
npm install --registry=https://registry.npmmirror.com
npm audit fix

# 回仓根复核
make check                 # 含 web-lock-check
make dep-alerts            # 告警数应下降
```

约束：

- `web/pnpm-lock.yaml` 是前端**唯一**真相源；`web/package-lock.json` 是
  `PM ?= pnpm || npm` 回退产物，被 `.gitignore` 忽略、绝不入库，`make web-lock-check`
  会把误提交挡下。
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
python3 -S scripts/dep-alerts.py --self-test           # 8 例离线断言
```

⚠️ 该 REST 端点**不支持 `page` 参数**（返回 400 `Pagination using the page
parameter is not supported`），分页必须靠 `gh api --paginate` 走 Link 游标；输出是
多个 JSON 数组拼接的流，脚本用 `JSONDecoder.raw_decode` 逐段解析。
