#!/usr/bin/env python3
"""tsm-hub glm 免费额度守护 + 自动收编。

glm 平台有两档：
  - glm-4-flash  永久免费模型（实测可用，本脚本主盯这个）
  - glm-5.3-flash 付费档，此前长期 HTTP 429 限频（仅在恢复后才作深兜底）

本脚本做两件事：
  1. 探测免费的 glm-4-flash 是否可用；可用则确保它已收编进主兜底链
     （prio 97，排在 sensenova 96 之后、gemini 98 之前）；若因配置漂移被移除则自动补回。
  2. 探测付费 glm-5.3-flash 是否脱离限频；恢复后自动以 prio 100 收编为最后兜底
     （排在 kimi 99 之后，纯冗余，非免费）。

用法：
    python3 monitor_glm.py            # 立即探一次
    # 挂 crontab（每 6 小时自动探）：
    #   0 */6 * * * cd <repo-root> && /usr/bin/python3 monitor_glm.py >> monitor_glm.log 2>&1

依赖：仅标准库（urllib/json），无第三方包。
"""
import json
import os
import sys
import urllib.error
import urllib.request

CFG = os.path.join(os.path.dirname(os.path.abspath(__file__)), "data", "config.json")
GW = "http://127.0.0.1:19070"
ROUTE_KEYS = ["", "agnes-2.0-flash", "chat", "fast", "deepseek-v4-flash"]


def load_cfg():
    with open(CFG) as f:
        return json.load(f)


def probe(glm, model):
    base = glm["base_url"].rstrip("/") + "/chat/completions"
    body = json.dumps({
        "model": model,
        "messages": [{"role": "user", "content": "reply: ok"}],
        "max_tokens": 8,
    }).encode()
    req = urllib.request.Request(
        base, data=body,
        headers={"Authorization": f"Bearer {glm.get('api_key', '')}",
                 "Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            json.load(r)
            return True, None
    except urllib.error.HTTPError as e:
        return False, f"HTTP {e.code}"
    except Exception as e:  # noqa: BLE001
        return False, str(e)[:80]


def admin_get(token, path):
    req = urllib.request.Request(GW + path, headers={"X-Admin-Token": token})
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.load(r)


def admin_upsert_route(token, route):
    req = urllib.request.Request(
        GW + "/api/admin/routes", data=json.dumps(route).encode(),
        headers={"X-Admin-Token": token, "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=10) as r:
        return r.status


def ensure_target(routes, token, key, provider_id, model, priority):
    rt = routes.get(key)
    if not rt:
        print(f"  路由 {key!r} 不存在，跳过")
        return
    targets = rt.get("targets", [])
    if any(t.get("provider_id") == provider_id and t.get("model") == model for t in targets):
        return  # 已存在
    targets.append({"provider_id": provider_id, "model": model,
                    "priority": priority, "weight": 1})
    rt["targets"] = targets
    st = admin_upsert_route(token, rt)
    print(f"  路由 {key!r} 补回 {provider_id}/{model} @prio{priority} -> HTTP {st}")


def main():
    cfg = load_cfg()
    token = cfg.get("settings", {}).get("admin_token") or cfg.get("admin_token")
    glm = next((p for p in cfg["providers"] if p["name"] == "glm"), None)
    if not glm:
        print("glm provider not found in config")
        sys.exit(1)

    routes = admin_get(token, "/api/admin/routes")
    if isinstance(routes, list):  # 兼容 list / dict 两种返回
        routes = {r.get("model") or r.get("key"): r for r in routes}

    # 1) 免费 glm-4-flash：可用则守护其位于 prio 97
    ok4, err4 = probe(glm, "glm-4-flash")
    if ok4:
        print("glm-4-flash（免费）可用，守护主兜底链 prio97…")
        for key in ROUTE_KEYS:
            ensure_target(routes, token, key, "glm", "glm-4-flash", 97)
        print("完成：免费 glm-4-flash 已在兜底链（sensenova 96 之后、gemini 98 之前）。")
    else:
        print(f"glm-4-flash（免费）暂不可用（{err4}），跳过。")

    # 2) 付费 glm-5.3-flash：脱离限频才收编为 prio100 深兜底（非免费）
    ok5, err5 = probe(glm, "glm-5.3-flash")
    if ok5:
        print("glm-5.3-flash（付费）已脱离限频，收编为 prio100 深兜底…")
        for key in ROUTE_KEYS:
            ensure_target(routes, token, key, "glm", "glm-5.3-flash", 100)
        print("完成：付费 glm-5.3-flash 已作为最后兜底进入兜底链。")
    else:
        print(f"glm-5.3-flash（付费）仍限频（{err5}），暂不收编。")


if __name__ == "__main__":
    main()
