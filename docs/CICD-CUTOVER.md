# cicdkit → cicdkit-lume 切换手册

## 现状

`cicdkit-lume`（Lume 控制面 + 薄 runner）已功能对齐，两套同时在跑：

| 项 | 上游 cicdkit | cicdkit-lume |
| --- | --- | --- |
| 端口 | `127.0.0.1:10081` → 8080 | `127.0.0.1:18107` |
| 指标 | `/metrics` | `/metrics/cicdkit`（`/metrics` 被 agent-httpd 占用） |
| Prometheus job | `cicdkit` (`cicdkit:8080`) | `cicdkit-lume` (`cicdkit-lume:18107`) |
| 告警 | `CicdkitDown` critical | `CicdkitLumeDown` warning（并行期不提级） |
| 数据库 | `data-docker/store.json` | `.data/store.db`（已导入 6 项目 / 200 运行 / 169 部署） |

## 下线前的依赖检查（已完成）

- 上游 200 条运行全部 `trigger=manual`，webhook 0 次，`a2a-tasks/` 空，无 hook/cron。
- 除 `cicdkit` 自身外没有容器引用 `10081`。
- `data-docker/` **不能删**：5 个项目的 `ssh_key_path` 指向 `data-docker/ssh_aliyun_key`，
  还有 `kubeconfig-cicd`、`llm.json`、`peers-identity.json`；lume 已只读挂载。

## 正式切流（只改监控，不改代码）

1. `deploy/prometheus.yml`：把 job `cicdkit` 的 target 改为 `cicdkit-lume:18107`，
   `metrics_path` 改为 `/metrics/cicdkit`，删掉临时 job `cicdkit-lume`。
2. `deploy/prometheus.rules.yml`：删 `CicdkitLumeDown`，`CicdkitDown` 保持原样即自动盯上 lume。
3. Grafana：因为沿用 job 名 `cicdkit`，原 `cicdkit CI/CD 监控` 面板无需改动；
   删掉并行面板 `cicdkit-lume`（provisioning 目录下删 JSON，Grafana 会在 30s 内自动移除）。
4. `curl -X POST http://127.0.0.1:19092/-/reload` 并确认 `up{job="cicdkit"}` = 1。
5. `docker stop cicdkit`（不删容器、不删数据），观察 1–2 周。

## 回滚

`docker start cicdkit`，把第 1 步的 target 改回去再 reload。数据与挂载均未动。

## 待定

- 旧入口 `10081` 是否需要给 lume 做端口映射保住老书签。
- `CicdkitLumeDown` 何时从 warning 提级 critical（建议并行观察满两周后）。
