#!/bin/sh
# mcp_gateway.c 的 /metrics 冒烟门禁（由 make alert-check 调用）。
#
# 为什么需要：5 座 MCP 桥共用这一份 C 源码，但镜像是构建期编译的，
# Go 测试与 promtool 都碰不到它。这里在本机编译一份、起在高位随机端口，
# 用真实 HTTP 请求验证指标格式、计数与隐私边界，再清理进程。
# 不 POST /mcp，因此不会 spawn 子进程、不占会话槽位、不触网。
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
src="$root/deploy/mcp_gateway.c"
tmpdir=$(mktemp -d)
bin="$tmpdir/mcp_gateway"
body="$tmpdir/metrics.txt"
glog="$tmpdir/gateway.log"

cleanup() {
    if [ -n "${pid:-}" ]; then kill "$pid" 2>/dev/null || true; fi
    rm -rf "$tmpdir"
}
trap cleanup EXIT HUP INT TERM

# -Wall -Wextra 下任何告警都会让 cc 返回非 0，等同编译期门禁。
cc -O2 -std=c11 -Wall -Wextra -o "$bin" "$src"

port=$((21000 + $$ % 20000))
"$bin" "$port" "cat" > "$glog" 2>&1 &
pid=$!

i=0
until curl -s --noproxy '*' --max-time 1 -o /dev/null "http://127.0.0.1:$port/health"; do
    i=$((i + 1))
    if [ "$i" -ge 40 ]; then
        echo "FAIL: mcp_gateway did not become ready" >&2
        cat "$glog" >&2
        exit 1
    fi
    sleep 0.1
done

# 制造一次 404，顺带验证 route=other 与 status=4xx 两条计数器。
curl -s --noproxy '*' --max-time 2 -o /dev/null "http://127.0.0.1:$port/not-a-route" || true
curl -s --noproxy '*' --max-time 2 "http://127.0.0.1:$port/metrics" > "$body"

fail() { echo "FAIL: $1" >&2; exit 1; }
# 用 ERE：字面花括号要写 \{ ... \}，BRE 下它会被当成区间量词而匹配不到样本行。
count() { grep -E -c "$1" "$body" 2>/dev/null || true; }

[ "$(count '^# HELP mcp_gateway_')" = "10" ] || fail "expected 10 HELP lines, got $(count '^# HELP mcp_gateway_')"
[ "$(count '^# TYPE mcp_gateway_')" = "10" ] || fail "expected 10 TYPE lines, got $(count '^# TYPE mcp_gateway_')"
[ "$(count '^mcp_gateway_up 1$')" = "1" ] || fail "missing mcp_gateway_up 1"
[ "$(count '^mcp_gateway_http_requests_total\{route="metrics"\} 1$')" = "1" ] || fail "route=metrics counter not 1"
[ "$(count '^mcp_gateway_http_requests_total\{route="other"\} 1$')" = "1" ] || fail "route=other counter not 1"
[ "$(count '^mcp_gateway_http_responses_total\{status="4xx"\} 1$')" = "1" ] || fail "status=4xx counter not 1"
[ "$(count '^mcp_gateway_sessions_max 4$')" = "1" ] || fail "missing default max sessions"
[ "$(count '^mcp_gateway_http_requests_total\{route="health"\} 1$')" = "1" ] || fail "route=health counter not 1"

# /metrics 不鉴权：命令（含宿主绝对路径）、sid、URL、密钥样式片段一律不得出现。
if grep -Ein '/Users/|/home/|node_modules|session_id|sid=|sk-|Bearer|https?://|cmd=' "$body"; then
    fail "/metrics leaked a command, path, URL or secret-looking fragment"
fi

echo "mcp_gateway /metrics smoke: OK (10 families, counters correct, no sensitive strings)"
