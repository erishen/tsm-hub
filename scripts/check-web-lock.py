#!/usr/bin/env python3
"""依赖锁一致性门禁（web / mcp）。

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
     ~/.gitignore_global 规则，把 npm 回退产物带进公开仓）；
  4. 所有被跟踪锁文件里的 tarball 下载地址必须指向官方
     ``registry.npmjs.org``。本机为绕过网络问题用 ``--registry`` 走镜像后，
     npm 会把实际下载地址写进锁文件；提交前必须换回，公开仓与海外克隆者
     不应被写死到某个镜像站。

门禁名沿用历史命名 ``web-lock-check``，但规则 4 覆盖 web 与 mcp 下所有
被跟踪的锁文件。只依赖 Python 标准库，不需要 node_modules，也不联网。
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
PNPM_WS = "web/pnpm-workspace.yaml"

LOCK_NAMES = {
    "package-lock.json",
    "npm-shrinkwrap.json",
    "pnpm-lock.yaml",
    "yarn.lock",
}
ALLOWED_HOSTS = {"registry.npmjs.org"}
MIRROR_URL_RE = re.compile(r"https?://([A-Za-z0-9.-]*npmmirror[A-Za-z0-9.-]*)", re.I)
RESOLVED_PATTERNS = (
    r'"resolved"\s*:\s*"https?://([^/"\s]+)',  # npm: "resolved": "https://..."
    r'\bresolved\s+"https?://([^/"\s]+)',      # yarn: resolved "https://..."
)


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


def tracked_locks() -> list[str]:
    out = subprocess.run(
        ["git", "ls-files", "-z"], cwd=ROOT, capture_output=True, check=False
    )
    paths = [
        p.decode("utf-8", "surrogateescape") for p in out.stdout.split(b"\0") if p
    ]
    return [p for p in paths if Path(p).name in LOCK_NAMES]


def check_pnpm_settings(errors: list[str]) -> None:
    """pnpm 10+/11 只读 pnpm-workspace.yaml，package.json 的 pnpm.* 会被静默忽略。

    2026-11-17 实证：把 overrides.onlyBuiltDependencies 写在 package.json 后，
    pnpm 11 打印 WARN 并全部忽略，锁仍停在 minimatch 9.0.9；同时 approve-builds
    自动生成的占位值 "set this to true or false" 会让 install 以
    ERR_PNPM_IGNORED_BUILDS 失败。这里把「声明生效」变成可校验规则：
      1. web/pnpm-workspace.yaml 必须被跟踪（override 的真实落点）；
      2. 其中必须声明 minimatch=11.0.1 的 override；
      3. 不得残留 approve-builds 占位值；
      4. package.json 不得再有 pnpm.* 字段（写了也是误导）。
    """
    if not tracked(PNPM_WS):
        errors.append(
            "%s is not tracked -- pnpm 10+ ignores pnpm.* in package.json; "
            "overrides and allowBuilds must live in pnpm-workspace.yaml" % PNPM_WS
        )
    else:
        ws_text = (ROOT / PNPM_WS).read_text(encoding="utf-8")
        if not re.search(r"^\s*overrides:\s*$", ws_text, re.M) or not re.search(
            r"^\s+minimatch:\s*['\"]?11\.", ws_text, re.M
        ):
            errors.append(
                "%s missing overrides.minimatch=11.x -- pnpm 11 ignores "
                "package.json pnpm.overrides; lock will keep minimatch 9/10" % PNPM_WS
            )
        if "set this to true or false" in ws_text:
            errors.append(
                "%s has placeholder allowBuilds values -- replace each with "
                "true/false or pnpm install fails with ERR_PNPM_IGNORED_BUILDS" % PNPM_WS
            )

    try:
        pkg = json.loads((WEB / "package.json").read_text(encoding="utf-8"))
    except (OSError, ValueError) as e:
        errors.append("cannot read web/package.json: %s" % e)
    else:
        if "pnpm" in pkg:
            errors.append(
                "web/package.json still has a 'pnpm' field -- pnpm 10+ ignores "
                "it silently; move overrides/allowBuilds to %s" % PNPM_WS
            )


def check_registry_hosts(errors: list[str]) -> tuple[int, int]:
    """被跟踪锁文件的 tarball 地址必须指向官方源，不能写死本机镜像。"""
    locks = tracked_locks()
    n_urls = 0
    for rel in locks:
        try:
            text = (ROOT / rel).read_text(encoding="utf-8")
        except (OSError, UnicodeError) as e:
            errors.append("cannot read %s: %s" % (rel, e))
            continue
        bad = set()
        for pattern in RESOLVED_PATTERNS:
            for m in re.finditer(pattern, text):
                n_urls += 1
                host = m.group(1).lower()
                if host not in ALLOWED_HOSTS:
                    bad.add(host)
        # 兜底：镜像 host 出现在非 resolved 字段（注释 / 元数据）同样算污染
        bad.update(m.group(1).lower() for m in MIRROR_URL_RE.finditer(text))
        for host in sorted(bad):
            errors.append(
                "%s pins tarballs to non-official host %s -- restore "
                "registry.npmjs.org before committing (use --registry for local "
                "installs only)" % (rel, host)
            )
    return len(locks), n_urls


def main() -> int:
    errors: list[str] = []
    count = check_specifiers(errors)
    check_pnpm_settings(errors)
    if not tracked(PNPM_LOCK):
        errors.append("%s is not tracked -- the dependency lock must be committed" % PNPM_LOCK)
    if tracked(NPM_LOCK):
        errors.append(
            "%s is tracked -- web uses pnpm; two lockfiles will drift. "
            "Remove it from the index (git rm --cached) and keep .gitignore's rule" % NPM_LOCK
        )
    n_locks, n_urls = check_registry_hosts(errors)
    if errors:
        for e in errors:
            print("FAIL " + e)
        print("--- dependency lock check FAILED")
        return 1
    print(
        "OK   %s matches web/package.json (%d deps); npm fallback lock is not tracked"
        % (PNPM_LOCK, count)
    )
    print(
        "OK   %d tracked lockfile(s): %d tarball URLs all on registry.npmjs.org"
        % (n_locks, n_urls)
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
