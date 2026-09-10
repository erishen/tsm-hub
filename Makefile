# 前端包管理器：优先 pnpm，没装则回退 npm（可用 make PM=pnpm ... 显式指定）
PM         ?= $(shell command -v pnpm >/dev/null 2>&1 && echo pnpm || echo npm)

BINARY     := bin/llm-router
MOCK       := bin/mockupstream
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# macOS 15 上纯 Go 内部链接产物的 dyld 会报 "missing LC_UUID"，
# 统一用 external linkmode 规避（与 cicd-platform 的处理一致）。
export CGO_ENABLED := 1
LDFLAGS := -X main.version=$(VERSION) -linkmode=external

.PHONY: all build run test vet fmt cover web-install web-build web-dev web-check dev dev-stop dev-logs dev-status mock mock-run mock-run-500 clean smoke help

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

## test: 跑单测与端到端冒烟
test:
	go test -ldflags '-linkmode=external' ./...

cover:
	go test -ldflags '-linkmode=external' -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

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

## docker-build: 构建镜像（建议先 make web-build，否则管理台是占位页）
docker-build:
	docker build -t llm-router:latest .

## docker-up: docker compose 启动（挂载 ./data）
docker-up:
	docker compose up -d --build

clean: dev-stop
	rm -rf bin coverage.out $(DEV_DIR)

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //'
