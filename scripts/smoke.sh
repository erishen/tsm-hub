#!/usr/bin/env bash
# 端到端冒烟：mock 上游(500) + mock 上游(ok) + 网关，验证路由降级、流式、鉴权与用量统计。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d)"
GW="127.0.0.1:19070"
BAD="127.0.0.1:18799"
OK="127.0.0.1:18798"
PIDS=()

# 端口预检：上次残留进程会让 mock 绑定失败，导致冒烟"假失败"（mock 失败不被 set -e 捕获）。
# 注意：探测命令必须放在 if 条件里（set -e 不作用于 if 条件），否则端口空闲时的非零返回会直接退出脚本。
check_port() {
  local port=$1
  if command -v lsof >/dev/null 2>&1; then
    if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
      echo "✗ 端口 $port 已被占用，请先释放（make dev-stop 或 kill 残留进程）" >&2
      exit 1
    fi
    return 0
  fi
  if command -v ss >/dev/null 2>&1; then
    if ss -ltn "sport = :$port" 2>/dev/null | grep -q .; then
      echo "✗ 端口 $port 已被占用，请先释放（make dev-stop 或 kill 残留进程）" >&2
      exit 1
    fi
  fi
}
for _p in "${GW##*:}" "${BAD##*:}" "${OK##*:}"; do check_port "$_p"; done

cleanup() {
  for pid in "${PIDS[@]:-}"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  done
  rm -rf "$TMP"
}
trap cleanup EXIT

pass() { printf "  \033[32m✓\033[0m %s\n" "$1"; }
fail() { printf "  \033[31m✗\033[0m %s\n" "$1"; exit 1; }
json() { node -e "let s='';process.stdin.on('data',d=>s+=d).on('end',()=>{try{console.log(eval('('+s+')').$1)}catch(e){console.log('')}})"; }

echo "→ 启动 mock 上游 (bad=$BAD, ok=$OK)"
"$ROOT/bin/mockupstream" -addr "$BAD" -mode 500 & PIDS+=($!)
"$ROOT/bin/mockupstream" -addr "$OK" -mode ok   & PIDS+=($!)

echo "→ 启动网关 $GW"
"$ROOT/bin/llm-router" -data "$TMP/data" -addr "$GW" -admin-token smoke-admin -log-level warn & PIDS+=($!)

for i in $(seq 1 50); do
  curl -sf "http://$GW/healthz" >/dev/null && break
  sleep 0.2
done
curl -sf "http://$GW/healthz" >/dev/null || fail "网关未就绪"
pass "healthz"

H=(-H "X-Admin-Token: smoke-admin" -H "Content-Type: application/json")

echo "→ 配置 provider / route"
curl -sf -X POST "http://$GW/api/admin/providers" "${H[@]}" -d "{\"id\":\"primary\",\"name\":\"会 500 的上游\",\"base_url\":\"http://$BAD/v1\",\"api_key\":\"sk-x\",\"models\":[\"mock-model\"],\"enabled\":true,\"weight\":100,\"priority\":1}" >/dev/null
curl -sf -X POST "http://$GW/api/admin/providers" "${H[@]}" -d "{\"id\":\"backup\",\"name\":\"正常上游\",\"base_url\":\"http://$OK/v1\",\"api_key\":\"sk-y\",\"models\":[\"mock-model\"],\"enabled\":true,\"weight\":100,\"priority\":2}" >/dev/null
curl -sf -X POST "http://$GW/api/admin/routes" "${H[@]}" -d '{"model":"smart","strategy":"failover","targets":[{"provider_id":"primary","model":"mock-model","weight":100,"priority":1},{"provider_id":"backup","model":"mock-model","weight":100,"priority":2}]}' >/dev/null
pass "provider + route 已写入"

echo "→ 签发 Token Key"
KEY="$(curl -sf -X POST "http://$GW/api/admin/keys" "${H[@]}" -d '{"name":"smoke","quota":{"rpm":50}}' | json key)"
[[ "$KEY" == sk-tr-* ]] || fail "签发的 Key 格式异常: $KEY"
pass "Key = ${KEY:0:12}…"

echo "→ 非流式请求（应自动从 500 上游降级到 backup）"
OUT="$(curl -sf -X POST "http://$GW/v1/chat/completions" -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"smart","messages":[{"role":"user","content":"hi"}]}')"
echo "$OUT" | grep -q "chatcmpl-mock" || fail "未拿到上游响应: $OUT"
echo "$OUT" | grep -q '"model":"mock-model"' || fail "模型名未按路由表改写: $OUT"
pass "failover 生效，model 已改写为 mock-model"

echo "→ 流式请求"
SSE="$(curl -s -N -X POST "http://$GW/v1/chat/completions" -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"smart","stream":true,"messages":[{"role":"user","content":"hi"}]}')"
echo "$SSE" | grep -q "data: " || fail "没有 SSE data 帧"
echo "$SSE" | grep -q "\[DONE\]" || fail "没有 [DONE] 结束帧"
pass "SSE 流式透传正常"

echo "→ 鉴权"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://$GW/v1/chat/completions" -H "Content-Type: application/json" -d '{"model":"smart"}')"
[ "$CODE" = "401" ] || fail "无 Key 应返回 401，实际 $CODE"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://$GW/v1/chat/completions" -H "Authorization: Bearer sk-openai-notours" -H "Content-Type: application/json" -d '{"model":"smart"}')"
[ "$CODE" = "401" ] || fail "非自制 Key 应返回 401，实际 $CODE"
pass "缺少/伪造 Key 均返回 401"

echo "→ 模型列表"
curl -sf "http://$GW/v1/models" -H "Authorization: Bearer $KEY" | grep -q '"smart"' || fail "/v1/models 未包含 smart"
pass "/v1/models 正常"

echo "→ 用量统计"
sleep 0.4
TOTAL="$(curl -sf "http://$GW/api/admin/usage?days=1" "${H[@]}" | node -e "let s='';process.stdin.on('data',d=>s+=d).on('end',()=>{const u=JSON.parse(s);console.log(u.days.reduce((a,b)=>a+b.total_tokens,0))})")"
[ "${TOTAL:-0}" -ge 28 ] || fail "用量未记账（期望 >=28，实际 ${TOTAL:-0}）"
pass "两次请求共 ${TOTAL} tokens 已记账"

echo
echo "全部冒烟通过 ✓"
