# 前端包管理器：优先 pnpm，没装则回退 npm（可用 make PM=pnpm ... 显式指定）
PM         ?= $(shell command -v pnpm >/dev/null 2>&1 && echo pnpm || echo npm)
# 覆盖率目标可显式指定 Go：make cover GO=$HOME/sdk/go1.25.0/bin/go
GO         ?= go

BINARY     := bin/tsm-hub
MOCK       := bin/mockupstream
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# macOS 15 上纯 Go 内部链接产物的 dyld 会报 "missing LC_UUID"，
# 统一用 external linkmode 规避（与 cicd-platform 的处理一致）。
export CGO_ENABLED := 1
LDFLAGS := -X main.version=$(VERSION) -linkmode=external

.PHONY: all build run test test-race check vet fmt fmt-check cover web-install web-build web-dev web-check dev dev-stop dev-logs dev-status mock mock-run mock-run-500 clean smoke help docker-build docker-up docker-down docker-stop docker-restart docker-logs docker-status privacy-check alert-check

all: build

# macOS 15 上 external linkmode 产物的签名会被判定无效，直接执行会被 SIGKILL(137)；
# 重新打一个 ad-hoc 签名即可运行。
ifeq ($(shell uname),Darwin)
define sign
	@codesign -s - -f $(1) 2>/dev/null || true
endef
else
define sign
endef
endif

## build: 编译服务端（含内嵌前端）
build:
	@mkdir -p bin
	go build -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/server
	$(call sign,$(BINARY))
	@echo "✓ built $(BINARY) ($(VERSION))"

## install-mcps: 预装 MCP server 依赖（npm 系 + serena，平台开箱即用）
install-mcps:
	./scripts/install-mcps.sh

## mock: 编译本地 mock 上游（联调用）
mock:
	@mkdir -p bin
	go build -ldflags '$(LDFLAGS)' -o $(MOCK) ./cmd/mockupstream
	$(call sign,$(MOCK))

## mock-run: 前台起 mock 上游（流式模式，:8799），配合 Playground「一键 Mock 联调」
mock-run: mock
	$(MOCK) -addr :8799 -mode stream

## mock-run-500: 前台起 mock 上游（固定 500，验证故障转移/错误路径）
mock-run-500: mock
	$(MOCK) -addr :8799 -mode 500

## run: 用 ./data 启动（默认 :9070）
run: build
	./$(BINARY) -data ./data

## check: 纯本地聚合门禁（gofmt + 单测 + vet + 隐私），不依赖运行中的容器
# alert-check 需 promtool/amtool 与 11 个容器在跑，故刻意不并入；cover 需完整 Go SDK
# 且耗时较长，同样保持独立。CI 或提交前跑这一条即可覆盖全部静态检查。
check: fmt-check test vet privacy-check

## test: 跑单测与端到端冒烟（先过 gofmt 门禁）
test: fmt-check
	go test -ldflags '-linkmode=external' ./...

## fmt-check: 校验 Go 源码格式，gofmt -l 非空即失败（防止历史漂移反复出现）
fmt-check:
	@out=`gofmt -l internal cmd`; \
	if [ -n "$$out" ]; then \
		echo "✗ gofmt drift detected, run 'make fmt' first:"; \
		echo "$$out"; \
		exit 1; \
	fi
	@echo "✓ gofmt clean"

## privacy-check: 校验 data/config.json* 权限(0600)且 provider api_key 全是 env: 引用
privacy-check:
	@python3 -S scripts/config-privacy-check.py --check

## alert-check: 钉钉桥单测 + MCP 桥 /metrics 冒烟 + dashboard 结构 + AM/Prometheus 配置（后两项需监控栈已 up）
alert-check:
	@python3 -S -m unittest discover -s deploy -p 'test_*.py'
	@sh deploy/test_mcp_gateway_metrics.sh
	@python3 -S -c "import json; d=json.load(open('deploy/grafana/dashboards/tsm-hub.json')); ids=[p['id'] for p in d['panels']]; assert len(ids)==len(set(ids)), 'duplicate panel ids'; assert d['version']>=9, 'dashboard version too old'; assert len(ids)>=39, 'missing v8 panels'; assert '重启归零' in next(p for p in d['panels'] if p['id']==36)['description'], 'panel 36 counter-scope note missing'; exprs=' '.join(t['expr'] for p in d['panels'] for t in p.get('targets',[])); assert all(m in exprs for m in ('tsm_hub_upstream_error_classes_total','tsm_hub_http_first_byte_seconds_bucket','flow=')), 'v8 metrics not wired into panels'"
	@docker compose exec -T alertmanager amtool check-config /etc/alertmanager/alertmanager.yml
	@docker compose exec -T prometheus promtool check config /etc/prometheus/prometheus.yml
	@docker compose exec -T prometheus promtool check rules /etc/prometheus/prometheus.rules.yml

## test-race: 在竞态检测器下跑单测（并发安全回归；比 test 慢，单独跑）
test-race:
	go test -race -ldflags '-linkmode=external' ./...

# go.mod / go.work 要求 go >= 1.25。若本机只装了 1.22，Go 会自动下载
# golang.org/toolchain 模块；该缓存被裁剪时 go test -cover 会报
# "go: no such tool covdata"。官方完整 SDK 的 pkg/tool 同样没有独立 covdata
# 二进制（Go 1.25 由 go 命令内置），所以不能用「文件是否存在」判断工具链，
# 改为按 GOROOT 路径识别残缺缓存并回退到 ~/sdk/go1.25.*；GO= 可显式指定。
# 这与仓库代码无关，make test / 镜像构建不受影响。
## cover: 生成覆盖率（自动绕开不完整的自动下载 toolchain）
cover:
	@go_bin="${GO}"; \
	if ! command -v "$${go_bin}" >/dev/null 2>&1; then \
		echo "✗ 找不到 Go 可执行文件： $${go_bin}"; \
		echo "  请把 Go 1.25 的 bin 加入 PATH，或用 make cover GO=/path/to/go 指定"; \
		exit 2; \
	fi; \
	go_path=$$(command -v "$${go_bin}"); \
	go_dir=$$(dirname "$${go_path}"); \
	export PATH="$${go_dir}:$${PATH}"; \
	goroot=$$($${go_bin} env GOROOT); \
	case "$${goroot}" in \
		*"/pkg/mod/golang.org/toolchain@"*) \
			sdk=""; sdkroot=""; \
			for d in "$$HOME"/sdk/go1.25*; do \
				if [ -x "$${d}/bin/go" ]; then sdk="$${d}/bin/go"; sdkroot="$${d}"; break; fi; \
			done; \
			if [ -n "$${sdk}" ]; then \
				echo "ℹ 当前 go 走的是不完整的自动下载 toolchain，自动改用官方 SDK： $${sdk}"; \
				go_bin="$${sdk}"; export GOROOT="$${sdkroot}"; export PATH="$${sdkroot}/bin:$${PATH}"; \
			else \
				echo "✗ 当前 go 走的是不完整的自动下载 toolchain（缺 covdata），且 ~/sdk 下没有官方 Go 1.25 SDK"; \
				echo "  GOROOT= $${goroot}"; \
				echo "  安装完整 SDK 后重跑："; \
				echo "    curl -fLO https://mirrors.aliyun.com/golang/go1.25.0.darwin-arm64.tar.gz"; \
				echo "    mkdir -p ~/sdk && tar -C ~/sdk -xzf go1.25.0.darwin-arm64.tar.gz && mv ~/sdk/go ~/sdk/go1.25.0"; \
				echo "  或用 make cover GO=/path/to/complete/go 指定"; \
				exit 2; \
			fi ;; \
	esac; \
	"$${go_bin}" test -ldflags '-linkmode=external' -coverprofile=coverage.out ./... || { \
		echo "✗ 覆盖率运行失败（原因见上方 go test 输出）；若报 no such tool covdata，可用 make cover GO=/path/to/complete/go 指定完整 SDK"; \
		exit 1; \
	}; \
	"$${go_bin}" tool cover -func=coverage.out | tail -1

vet:
	go vet ./...

fmt:
	gofmt -l -w .

## web-install: 安装前端依赖（pnpm 优先，未安装则用 npm）
web-install:
	cd web && $(PM) install

## web-build: 构建 Angular 管理台，再同步到 internal/web/dist/browser（供 Go embed）
# 构建产物先落在 web/dist/browser（Angular 可正常清理），再拷进 embed 目录，
# 避免 outputPath 指向 workspace 外时 Angular 不做清理导致 mkdir 报 EEXIST。
web-build:
	cd web && mkdir -p dist && rm -rf dist/browser && $(PM) run build
	@mkdir -p internal/web/dist/browser
	@rm -rf internal/web/dist/browser/*
	@cp -R web/dist/browser/. internal/web/dist/browser/
	@echo "✓ frontend synced to internal/web/dist/browser"

## web-dev: Angular dev server（:4200，通过 proxy.conf.json 转发 /api 到 :9070）
web-dev:
	cd web && CI=true NG_FORCE_AUTOCOMPLETE=false $(PM) start

## web-check: 不依赖 node_modules 的 TS 语法自检
web-check:
	cd web && $(PM) run check:syntax

# ---- 本地联调（前后端一起起）----
DEV_DIR     := .dev
ROUTER_PORT ?= 9070
WEB_PORT    ?= 4200

# 按端口杀掉残留进程：macOS 用 lsof，Linux 退到 fuser
define kill_port
	@pids=`lsof -nP -ti tcp:$(1) 2>/dev/null`; \
	if [ -n "$$pids" ]; then echo "  stop :$(1) -> $$pids"; kill -9 $$pids 2>/dev/null || true; \
	elif command -v fuser >/dev/null 2>&1; then fuser -k -n tcp $(1) >/dev/null 2>&1 || true; fi
endef

## dev: 前台常驻，前后端日志同屏实时输出，Ctrl-C 一起停（先清掉旧进程）
dev: dev-stop build
	@mkdir -p $(DEV_DIR)
	@test -d web/node_modules || (echo "  installing web deps with $(PM) ..." && cd web && $(PM) install)
	@echo "  dev (foreground) — router :$(ROUTER_PORT) + web :$(WEB_PORT)，Ctrl-C 停止"
	@sh -c 'trap "kill -9 0" INT TERM EXIT; ./$(BINARY) -data ./data -addr :$(ROUTER_PORT) 2>&1 & (cd web && CI=true NG_FORCE_AUTOCOMPLETE=false $(PM) start) 2>&1 & echo "  both starting... (logs below, Ctrl-C to stop)"; wait'

## dev-stop: 停掉本地联调的后端与前端
dev-stop:
	@echo "stopping dev services"
	$(call kill_port,$(ROUTER_PORT))
	$(call kill_port,$(WEB_PORT))
	@for f in $(DEV_DIR)/*.pid; do \
		if [ -f "$$f" ]; then kill -9 `cat "$$f"` 2>/dev/null || true; fi; \
	done; rm -f $(DEV_DIR)/*.pid

## dev-logs: 跟踪本地联调日志（Ctrl-C 退出）
dev-logs:
	@tail -f $(DEV_DIR)/*.log

## dev-status: 看本地联调的进程与端口
dev-status:
	@lsof -nP -i tcp:$(ROUTER_PORT) -i tcp:$(WEB_PORT) 2>/dev/null || echo "no dev service listening"

## smoke: 启动 mock 上游 + 网关，跑一轮端到端验证
smoke: build mock
	./scripts/smoke.sh

## docker-build: 构建镜像（自动先构建前端，确保管理台是最新版本）
docker-build: web-build
	docker build -t tsm-hub:latest .

## docker-up: docker compose 启动（自动先构建前端，挂载 ./data，后台运行）
# compose 把容器内 9070 映射到宿主 19070；9070 是 make run 的本地直连端口。
docker-up: web-build
	docker compose up -d --build
	@echo "✓ tsm-hub started, visit http://127.0.0.1:19070"

## docker-down: 停止并删除 docker 容器（保留 ./data 数据）
docker-down:
	docker compose down
	@echo "✓ tsm-hub stopped and removed"

## docker-stop: 停止 docker 容器（不删除，可 docker-start 恢复）
docker-stop:
	docker compose stop
	@echo "✓ tsm-hub stopped"

## docker-start: 启动已停止的 docker 容器
docker-start:
	docker compose start
	@echo "✓ tsm-hub started"

## docker-restart: 重启 docker 容器
docker-restart:
	docker compose restart
	@echo "✓ tsm-hub restarted"

## docker-logs: 跟踪 docker 日志（Ctrl-C 退出）
docker-logs:
	docker compose logs -f --tail=100

## docker-status: 查看 docker 容器状态
# 宿主端口是 19070；本机 http_proxy 会接走 localhost 请求并返回 502，必须 --noproxy 绕过。
docker-status:
	@docker compose ps
	@echo ""
	@echo "健康检查："
	@curl -s --noproxy '*' http://127.0.0.1:19070/healthz 2>/dev/null | python3 -S -c "import sys,json; d=json.load(sys.stdin); print(f'  状态: {d[\"status\"]}, 运行: {d[\"uptime_s\"]}s, Provider: {len(d[\"providers\"])}个')" 2>/dev/null || echo "  服务未响应"

clean: dev-stop
	rm -rf bin coverage.out $(DEV_DIR)

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //'
