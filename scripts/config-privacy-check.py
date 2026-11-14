#!/usr/bin/env python3
"""配置隐私门禁：检查 / 脱敏 data/config.json*。

只输出文件名、provider id 与计数，绝不打印任何密钥值。

    python3 -S scripts/config-privacy-check.py --check
    python3 -S scripts/config-privacy-check.py --redact
    python3 -S scripts/config-privacy-check.py --check --data-dir /tmp/somewhere

规则：
  1. 每个 config.json* 的权限必须是 0600（历史上出现过 0644 的迁移前备份）；
  2. 每个 providers[].api_key 必须形如 env:XXX —— 明文上游 key 一律判失败。

--redact 只处理 config.json.bak-* 历史备份，把明文 api_key 原地替换成
env:TSM_HUB_KEY_<PROVIDER_ID> 引用（保留完整配置结构，可继续当回滚点），
并统一 chmod 0600。现用 config.json 永远跳过，避免误伤运行中的服务。
"""

from __future__ import annotations

import argparse
import json
import os
import stat
import sys

REDACT_PREFIX = "env:TSM_HUB_KEY_"


def targets(data_dir: str) -> list[str]:
    if not os.path.isdir(data_dir):
        return []
    names = [
        n
        for n in os.listdir(data_dir)
        if n == "config.json" or n.startswith("config.json.bak-")
    ]
    return [os.path.join(data_dir, n) for n in sorted(names)]


def scan(path: str) -> tuple[list[str], list[str]]:
    """返回 (问题描述, 含明文 api_key 的 provider id 列表)。"""
    problems: list[str] = []
    mode = stat.S_IMODE(os.stat(path).st_mode)
    if mode != 0o600:
        problems.append(f"mode={oct(mode)} (want 0o600)")
    try:
        with open(path, encoding="utf-8") as f:
            doc = json.load(f)
    except (OSError, ValueError) as e:
        problems.append(f"unreadable JSON: {type(e).__name__}")
        return problems, []
    if not isinstance(doc, dict):
        problems.append("root is not a JSON object")
        return problems, []
    plaintext = [
        str(p.get("id", "?"))
        for p in doc.get("providers", [])
        if not str(p.get("api_key", "")).startswith("env:")
    ]
    return problems, plaintext


def redact(path: str) -> int:
    """把明文 api_key 精确替换成 env: 引用；返回替换的 provider 数。幂等。"""
    with open(path, encoding="utf-8") as f:
        raw = f.read()
    doc = json.loads(raw)
    changed = 0
    for p in doc.get("providers", []):
        key = str(p.get("api_key", ""))
        if not key or key.startswith("env:"):
            continue
        ident = str(p.get("id", "")).upper()
        old = json.dumps(key, ensure_ascii=False)
        new = json.dumps(REDACT_PREFIX + ident, ensure_ascii=False)
        if old not in raw:
            continue
        raw = raw.replace(old, new)
        changed += 1
    if changed:
        json.loads(raw)  # 写回前确认仍是合法 JSON
        tmp = path + ".tmp"
        with open(tmp, "w", encoding="utf-8") as f:
            f.write(raw)
        os.chmod(tmp, 0o600)
        os.replace(tmp, path)
    os.chmod(path, 0o600)
    return changed


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    g = ap.add_mutually_exclusive_group()
    g.add_argument("--check", action="store_true", help="只读检查（默认）")
    g.add_argument("--redact", action="store_true", help="脱敏历史备份")
    ap.add_argument("--data-dir", default="data", help="数据目录，默认 ./data")
    args = ap.parse_args()

    files = targets(args.data_dir)
    if not files:
        print(f"SKIP: {args.data_dir} 不存在或没有 config.json*（CI / 新克隆属正常）")
        return 0

    if args.redact:
        for path in files:
            if os.path.basename(path) == "config.json":
                print(f"SKIP {path} (live config: migrate manually via .env)")
                continue
            n = redact(path)
            print(f"REDACT {path}: {n} api_key(s) -> env ref, mode=0600")
        return 0

    bad = 0
    for path in files:
        problems, ids = scan(path)
        if ids:
            shown = ", ".join(ids[:5]) + (" ..." if len(ids) > 5 else "")
            problems.append(f"{len(ids)} plaintext api_key(s): {shown}")
        if problems:
            bad += 1
            print(f"FAIL {path}: " + "; ".join(problems))
        else:
            print(f"OK   {path}")
    print(f"--- {len(files) - bad}/{len(files)} files clean")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
