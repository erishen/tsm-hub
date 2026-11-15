#!/usr/bin/env python3
"""前端依赖锁一致性门禁。

    python3 -S scripts/check-web-lock.py

``web/`` 用 pnpm，唯一依赖真相源是 ``web/pnpm-lock.yaml``。Makefile 里
``PM ?= pnpm || npm`` 在未装 pnpm 的机器上会回退到 npm，此时 npm 会生成
``web/package-lock.json``；那份文件 **不入库**（仓库 .gitignore 显式忽略）。
理由：同一个 package.json 挂两份锁文件，改依赖时极易只刷新其中一份，
后提交的 CI 与本地构建就会静默地用不同版本的依赖。

本门禁把「确定性」变成可校验的规则，而不是靠提交第二份锁文件：
  1. pnpm-lock.yaml 的 importer specifier 必须与 package.json 依赖逐项一致
     （改了 package.json 却忘了刷新锁会被挡下）；
  2. web/pnpm-lock.yaml 必须被 git 跟踪（锁文件本身不能被误删/漏提交）；
  3. web/package-lock.json 必须 **不** 被 git 跟踪（防止协作者绕过本机
     ~/.gitignore_global 规则，把 npm 回退产物带进公开仓）。

只依赖 Python 标准库，不需要 node_modules，也不联网。
"""
from __future__ import annotations

import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
WEB = ROOT / "web"
PNPM_LOCK = "web/pnpm-lock.yaml"
NPM_LOCK = "web/package-lock.json"


def tracked(path: str) -> bool:
    return (
        subprocess.run(
            ["git", "ls-files", "--error-unmatch", path],
            cwd=ROOT,
            capture_output=True,
            check=False,
        ).returncode
        == 0
    )


def check_specifiers(errors: list[str]) -> int:
    try:
        pkg = json.loads((WEB / "package.json").read_text(encoding="utf-8"))
        text = (WEB / "pnpm-lock.yaml").read_text(encoding="utf-8")
    except (OSError, ValueError) as e:
        errors.append("cannot read web/package.json or pnpm-lock.yaml: %s" % e)
        return 0

    want = {**pkg.get("dependencies", {}), **pkg.get("devDependencies", {})}
    head = re.search(r"^importers:\s*$", text, re.M)
    if not head:
        errors.append("pnpm-lock.yaml has no importers: section")
        return 0
    rest = text[head.end():]
    tail = re.search(r"^packages:\s*$", rest, re.M)
    section = rest[:tail.start()] if tail else rest
    # 单 importer 工程：键形如 "      '@angular/core':" 后跟 specifier/version
    got = dict(
        re.findall(
            r"^\s+'?([@\w./-]+)'?:\s*\n\s+specifier:\s*(\S+)",
            section,
            re.M,
        )
    )

    missing = sorted(set(want) - set(got))
    extra = sorted(set(got) - set(want))
    changed = [(k, want[k], got[k]) for k in want if k in got and got[k] != want[k]]
    if missing:
        errors.append("pnpm-lock.yaml missing specifiers: %s" % ", ".join(missing))
    if extra:
        errors.append("pnpm-lock.yaml has extra specifiers: %s" % ", ".join(extra))
    for name, in_pkg, in_lock in changed:
        errors.append(
            "specifier drift %s: package.json=%s pnpm-lock.yaml=%s"
            % (name, in_pkg, in_lock)
        )
    if not want:
        errors.append("package.json has no dependencies at all")
    return len(want)


def main() -> int:
    errors: list[str] = []
    count = check_specifiers(errors)
    if not tracked(PNPM_LOCK):
        errors.append("%s is not tracked -- the dependency lock must be committed" % PNPM_LOCK)
    if tracked(NPM_LOCK):
        errors.append(
            "%s is tracked -- web uses pnpm; two lockfiles will drift. "
            "Remove it from the index (git rm --cached) and keep .gitignore's rule" % NPM_LOCK
        )
    if errors:
        for e in errors:
            print("FAIL " + e)
        print("--- web dependency lock check FAILED")
        return 1
    print(
        "OK   %s matches web/package.json (%d deps); npm fallback lock is not tracked"
        % (PNPM_LOCK, count)
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
