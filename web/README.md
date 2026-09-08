# 管理台前端（Angular 19）

llm-router 的 Web 控制台。构建产物输出到 `../internal/web/dist/browser`，
由 Go 的 `//go:embed all:dist` 打进二进制，所以部署时只需要一个可执行文件。

## 开发

包管理器用 **pnpm**（仓库里 `.npmrc` 已放宽 peer 检查）。也可以在项目根用 `make web-install`，
它会自动探测 pnpm，没装则回退 npm。

```bash
pnpm install                # 装依赖（国内先 pnpm config set registry https://registry.npmmirror.com）
pnpm start                  # dev server :4200，/api 通过 proxy.conf.json 转发到 :9070
pnpm run build              # 产物 → ../internal/web/dist/browser
pnpm run check:syntax       # 不依赖 node_modules 的 TS 语法自检
```

> pnpm 10 起默认不执行依赖的 postinstall，`esbuild` 的二进制下不下来会导致 `ng build` 失败。
> `package.json` 里的 `pnpm.onlyBuiltDependencies` 已放行 esbuild；若仍报找不到 esbuild，
> 执行 `pnpm approve-builds` 或 `pnpm rebuild esbuild`。

更省事的是在项目根用 `make dev`：它会先杀掉 :9070/:4200 上的残留进程，再重新编译后端并
把前后台一起拉起来（日志在 `.dev/`），`make dev-logs` 跟日志、`make dev-stop` 停。

手动起的话，dev 模式下后端要另开一个终端跑：

```bash
cd .. && ./bin/llm-router -data ./data -addr :9070
```

打开 <http://localhost:4200>，用 `data/config.json` 里的 `settings.admin_token` 登录。

## 页面

| 路由 | 说明 |
|------|------|
| `/` | 概览：provider 健康、今日 tokens / 成本 / 错误率、近 7 日柱状图 |
| `/providers` | 上游增删改查；编辑时 API Key 留脱敏值不会覆盖原值 |
| `/routes` | 对外模型别名 → 上游候选，failover / weighted |
| `/keys` | 签发 Token Key（明文只显示一次）、启停、删除、各 Key 用量 |
| `/usage` | 按天 / 模型 / Key 聚合 + 最近 100 条流水 |

## 结构

```
src/
├── main.ts                   # bootstrapApplication + 路由表
├── styles.css                # 全局样式（浅色主题，无 UI 库）
└── app/
    ├── api.service.ts        # 管理 API 封装：自动带 X-Session-Token，401 自动登出
    ├── models.ts             # 与 Go 端 JSON 对齐的类型
    ├── app.component.ts      # 登录 + 侧边栏外壳
    ├── dashboard.component.ts
    ├── providers.component.ts
    ├── routes.component.ts
    ├── keys.component.ts
    └── usage.component.ts
```

约定：

- 全部 standalone 组件 + signals，不引入 NgModule；
- 不引入 UI 组件库，样式集中在 `styles.css`（CSS 变量改主题色）；
- 所有后端交互走 `ApiService`，错误统一转成 `Error.message` 抛给组件。

## 排障

| 现象 | 原因 |
|------|------|
| 访问 `/` 看到"管理台前端尚未构建" | 没跑 `pnpm run build`，Go embed 的是占位页；API 不受影响 |
| dev 模式接口 404 | 后端没起，或端口不是 9070（改 `proxy.conf.json`）|
| `ng build` 报 TS 类型错 | 先 `pnpm run check:syntax` 排除语法问题，再看具体类型（多与 `strictTemplates` 有关）|
| `ERR_PNPM_PEER_DEP_ISSUES` | `.npmrc` 已放宽；确认是在 `web/` 目录下执行的安装 |
