#!/usr/bin/env python3
"""Alertmanager -> 钉钉群机器人桥接器（仅标准库，无第三方依赖）。

为什么需要它：Alertmanager 的 ``webhook_configs`` 发出的是自己的 JSON 协议，
钉钉机器人只认 ``{"msgtype": "markdown", ...}``；而且钉钉「加签」安全设置要求
每个请求带 ``timestamp`` + ``HMAC-SHA256`` 动态签名，Alertmanager 无法计算。
本服务做三件事：协议转换、中文消息排版、加签推送。

配置全部走环境变量（compose 从仓库根 .env 插值），**URL 与 secret 绝不写进
任何入库文件，也绝不出现在日志/HTTP 错误里**。加签算法与
``work/sale/daoye-engine/daoye_engine/notify.py`` 保持同款，复用同一个群机器人。

端点：
  GET  /healthz           健康探针（只回报 configured 布尔值，不回报 URL）
  POST / 或 /dingtalk     Alertmanager webhook；2xx 视为投递成功，其余触发重试

联调开关：
  DINGTALK_DRY_RUN=1      只记录摘要，不真发（不打扰群）
  X-Dingtalk-Dry-Run: 1   单次请求级 dry-run，用于验证 Alertmanager 真实 payload
"""
from __future__ import annotations

import base64
import hashlib
import hmac
import json
import logging
import os
import re
import time
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Dict, List, Tuple

URL_ENV = "DINGTALK_WEBHOOK_URL"
SECRET_ENV = "DINGTALK_WEBHOOK_SECRET"
KEYWORD_ENV = "DINGTALK_KEYWORD"
DRY_RUN_ENV = "DINGTALK_DRY_RUN"
TIMEOUT_ENV = "DINGTALK_HTTP_TIMEOUT"
PROM_ENV = "PROMETHEUS_EXTERNAL_URL"
GRAFANA_ENV = "GRAFANA_EXTERNAL_URL"
PORT_ENV = "DINGTALK_BRIDGE_PORT"

MAX_BODY = 1024 * 1024
MAX_ALERTS = 10
log = logging.getLogger("dingtalk-bridge")


def env(name: str, default: str = "") -> str:
    return os.environ.get(name, default).strip()


def is_dry_run() -> bool:
    return env(DRY_RUN_ENV) in ("1", "true", "True", "yes")


def signed_url(url: str, secret: str, now_ms: int | None = None) -> str:
    """钉钉「加签」：URL 追加 timestamp + sign，非钉钉 host 原样返回。

    sign = urlencode(base64(HMAC-SHA256(secret, f"{ts}\\n{secret}")))。
    """
    if not secret or "oapi.dingtalk.com" not in url:
        return url
    ts = str(now_ms if now_ms is not None else round(time.time() * 1000))
    digest = hmac.new(
        secret.encode("utf-8"), f"{ts}\n{secret}".encode("utf-8"), hashlib.sha256
    ).digest()
    sign = urllib.parse.quote_plus(base64.b64encode(digest))
    sep = "&" if "?" in url else "?"
    return f"{url}{sep}timestamp={ts}&sign={sign}"


def display_url(url: str) -> str:
    """日志里的 webhook 地址：剥掉 query（access_token / timestamp / sign 都在里面）。"""
    try:
        p = urllib.parse.urlsplit(url)
        base = f"{p.scheme}://{p.netloc}{p.path}"
    except ValueError:
        return "webhook"
    return f"{base}?..." if p.query else base


def redact(text: Any, *secrets: str) -> str:
    """兜底脱敏：异常文本里可能带完整带签 URL 或 secret 字面量。"""
    s = str(text or "")
    for secret in secrets:
        if secret:
            s = s.replace(secret, "...")
    s = re.sub(r"(access_token|sign|timestamp|secret)=[^&\s\"']+", r"\1=...", s)
    s = re.sub(r"(https?://[^\s\"')]+)\?[^\s\"')]+", r"\1?...", s)
    return s


def business_ok(body: str) -> Tuple[bool, str]:
    """HTTP 200 不等于推送成功：钉钉失败也回 200，只是 errcode != 0。"""
    raw = (body or "").strip()
    if not raw:
        return True, ""
    try:
        data = json.loads(raw)
    except (ValueError, TypeError):
        return True, ""  # 未知格式不阻塞
    if not isinstance(data, dict):
        return True, ""
    rc = data.get("errcode", data.get("code"))
    if rc not in (0, "0", None):
        return False, f"errcode={rc} {data.get('errmsg') or data.get('message') or ''}".strip()
    return True, ""


def clean(value: Any, limit: int = 300) -> str:
    """告警字段清洗：去控制字符、限长，避免把 markdown 结构或换行打乱。"""
    s = re.sub(r"[\x00-\x1f\x7f]", " ", str(value if value is not None else ""))
    s = s.replace("`", "'").replace("|", "\\|").strip()
    return s[:limit] + ("..." if len(s) > limit else "")


def fmt_time(value: Any) -> str:
    s = str(value or "")
    # Alertmanager 用 RFC3339；本地展示到秒即可，解析失败就原样截断。
    m = re.match(r"^(\d{4}-\d{2}-\d{2})[T ](\d{2}:\d{2}:\d{2})", s)
    return f"{m.group(1)} {m.group(2)}" if m else clean(s, 32)


def format_message(
    payload: Dict[str, Any],
    keyword: str = "告警",
    prom_url: str = "",
    grafana_url: str = "",
) -> Dict[str, Any]:
    """把 Alertmanager v4 webhook 转成钉钉 markdown 消息。"""
    alerts: List[Dict[str, Any]] = list(payload.get("alerts") or [])
    status = str(payload.get("status") or "firing")
    firing = sum(1 for a in alerts if a.get("status", "firing") == "firing")
    resolved = len(alerts) - firing
    verb = "已恢复" if status == "resolved" else "触发"
    keyword = clean(keyword, 32)
    prefix = f"{keyword} | " if keyword else ""
    title = f"[{prefix}tsm-hub] {verb} {firing} 条 / 恢复 {resolved} 条"

    lines: List[str] = [f"### {title}", ""]
    for a in alerts[:MAX_ALERTS]:
        labels = a.get("labels") or {}
        ann = a.get("annotations") or {}
        a_status = "已恢复" if a.get("status") == "resolved" else "触发"
        name = clean(labels.get("alertname") or "unknown", 80)
        sev = clean(labels.get("severity") or "none", 24)
        provider = clean(labels.get("provider") or "-", 48)
        summary = clean(ann.get("summary") or name, 200)
        desc = clean(ann.get("description") or "", 300)
        lines.append(f"**[{a_status}] {name}** · 级别 {sev}")
        lines.append(f"> {summary}")
        if desc:
            lines.append(f"> {desc}")
        lines.append(f"> provider: {provider} · 开始: {fmt_time(a.get('startsAt'))}")
        lines.append("")
    if len(alerts) > MAX_ALERTS:
        lines.append(f"> 还有 {len(alerts) - MAX_ALERTS} 条，详见 Alertmanager。")
        lines.append("")

    links = []
    if prom_url:
        links.append(f"[Prometheus 告警]({prom_url.rstrip('/')}/alerts)")
    if grafana_url:
        links.append(f"[Grafana 面板]({grafana_url.rstrip('/')}/d/tsm-hub-overview)")
    if links:
        lines.append(" · ".join(links))

    # 自定义关键词安全设置下，标题与正文都要命中关键词；正文首行复用标题即可。
    return {"msgtype": "markdown", "markdown": {"title": title, "text": "\n".join(lines)}}


def send_to_dingtalk(
    message: Dict[str, Any], url: str, secret: str, timeout: float = 10.0
) -> Tuple[bool, str]:
    """加签并推送。返回值里的 URL 一律脱敏，异常文本经 redact 处理。"""
    data = json.dumps(message, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(
        signed_url(url, secret),
        data=data,
        headers={"Content-Type": "application/json; charset=utf-8"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read().decode("utf-8", errors="replace")
    except Exception as exc:  # noqa: BLE001 - 网络错误必须转成告警投递失败，不能让服务挂掉
        return False, redact(f"推送失败 {type(exc).__name__}: {exc}", secret, url)
    ok, why = business_ok(body)
    if not ok:
        return False, f"钉钉拒绝：{redact(why, secret)}（{display_url(url)}）"
    return True, f"已推送（{display_url(url)}）"


class Handler(BaseHTTPRequestHandler):
    server_version = "tsm-hub-dingtalk-bridge/1.0"

    def _json(self, code: int, obj: Dict[str, Any]) -> None:
        raw = json.dumps(obj, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, fmt: str, *args: Any) -> None:
        # BaseHTTPRequestHandler 默认把完整 path 打到 stderr；这里统一走 logging。
        log.debug("http %s - %s", self.address_string(), fmt % args)

    def do_GET(self) -> None:  # noqa: N802
        if self.path.split("?")[0] == "/healthz":
            self._json(200, {"ok": True, "configured": bool(env(URL_ENV)), "dry_run": is_dry_run()})
            return
        self._json(404, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802
        path = self.path.split("?")[0]
        if path not in ("/", "/dingtalk"):
            self._json(404, {"error": "not found"})
            return
        try:
            length = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            length = 0
        if length <= 0 or length > MAX_BODY:
            self._json(400, {"error": "invalid Content-Length"})
            return
        try:
            payload = json.loads(self.rfile.read(length).decode("utf-8"))
        except (ValueError, UnicodeDecodeError):
            self._json(400, {"error": "invalid JSON"})
            return
        if not isinstance(payload, dict):
            self._json(400, {"error": "payload must be an object"})
            return

        keyword = env(KEYWORD_ENV, "告警")
        message = format_message(payload, keyword, env(PROM_ENV), env(GRAFANA_ENV))
        url, secret = env(URL_ENV), env(SECRET_ENV)
        single_dry = self.headers.get("X-Dingtalk-Dry-Run") == "1"
        if is_dry_run() or single_dry or not url:
            log.info("[dry-run] %s | alerts=%d", message["markdown"]["title"],
                     len(payload.get("alerts") or []))
            self._json(200, {"ok": True, "dry_run": True, "title": message["markdown"]["title"]})
            return
        try:
            timeout = float(env(TIMEOUT_ENV, "10"))
        except ValueError:
            timeout = 10.0
        ok, reason = send_to_dingtalk(message, url, secret, timeout)
        if not ok:
            log.warning("%s", redact(reason, secret, url))
            self._json(502, {"ok": False, "error": redact(reason, secret, url)})
            return
        log.info("%s | %s", message["markdown"]["title"], reason)
        self._json(200, {"ok": True, "title": message["markdown"]["title"]})


def main() -> None:
    logging.basicConfig(
        level=os.environ.get("LOG_LEVEL", "INFO").upper(),
        format="%(asctime)s %(levelname)s %(message)s",
    )
    port = int(env(PORT_ENV, "8060") or "8060")
    # 只打印「是否配置」，绝不打印 URL / secret 本身。
    log.info("listening on :%d configured=%s dry_run=%s", port, bool(env(URL_ENV)), is_dry_run())
    ThreadingHTTPServer(("0.0.0.0", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
