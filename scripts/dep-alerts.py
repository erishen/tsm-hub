#!/usr/bin/env python3
"""依赖安全告警审计（报告型，不做门禁）。

    make dep-alerts                          # 在线：拉 GitHub Dependabot open 告警
    python3 -S scripts/dep-alerts.py --json  # 结构化输出，便于脚本消费
    python3 -S scripts/dep-alerts.py --report .data/alerts-all.json
    python3 -S scripts/dep-alerts.py --self-test

仓库 2026-11-16 开启 Dependabot vulnerability alerts / security updates 后，
前端锁文件暴露出大量传递依赖公告。这些告警**只能在本机修**（刷 pnpm/npm
锁文件需要在用户自己的 Terminal 里跑完整安装），且数量会随上游公告增减，
所以这里只做「可复跑的体检报告」，不挂进 ``make check``、不拦截提交。

数据来源是 GitHub REST：

    gh api --paginate 'repos/OWNER/REPO/dependabot/alerts?state=open&per_page=100'

⚠️ 该端点**不支持 ``page`` 参数**（返回 400 "Pagination using the `page`
parameter is not supported"），必须靠 ``--paginate`` 走 Link 游标；输出是
多个 JSON 数组直接拼接的流，不能 ``json.loads`` 整份文本。``--report``
接受同样格式的文件或 ``-``（stdin），便于离线复现与自测。

只依赖 Python 标准库；在线模式额外需要已登录且有仓库读权限的 ``gh`` CLI。
"""
from __future__ import annotations

import argparse
import collections
import json
import subprocess
import sys
from pathlib import Path

DEFAULT_REPO = "erishen/tsm-hub"
SEVERITY_ORDER = ["low", "medium", "high", "critical"]
API_PATH = "repos/{repo}/dependabot/alerts?state=open&per_page=100"


def parse_stream(text: str) -> list[dict]:
    """解析 ``gh api --paginate`` 产生的拼接 JSON 数组流。"""
    decoder = json.JSONDecoder()
    pos, alerts = 0, []
    while pos < len(text):
        while pos < len(text) and text[pos].isspace():
            pos += 1
        if pos >= len(text):
            break
        obj, pos = decoder.raw_decode(text, pos)
        if isinstance(obj, dict):
            raise ValueError("API returned an error object: %s" % obj.get("message", "?"))
        if not isinstance(obj, list):
            raise ValueError("unexpected JSON value in stream: %s" % type(obj).__name__)
        alerts.extend(o for o in obj if isinstance(o, dict))
    if not alerts:
        raise ValueError("no alerts found in report (empty stream)")
    return alerts


def normalize(alert: dict) -> dict:
    """把单条告警拍平成报告用的字段，容忍缺失/None。"""
    advisory = alert.get("security_advisory") or {}
    dependency = alert.get("dependency") or {}
    vulnerability = alert.get("security_vulnerability") or {}
    patched = vulnerability.get("first_patched_version") or {}
    package = dependency.get("package") or {}
    return {
        "number": alert.get("number"),
        "package": package.get("name") or "?",
        "severity": advisory.get("severity") or "unknown",
        "scope": dependency.get("scope") or "unknown",
        "manifest": dependency.get("manifest_path") or "unknown",
        "current": dependency.get("version_requirement") or "-",
        "fixed": patched.get("identifier") or "-",
        "cve": advisory.get("cve_id") or advisory.get("ghsa_id") or "-",
    }


def summarize(rows: list[dict]) -> dict:
    """按包聚合，并挑出 runtime 的高危项作为修复重点。"""
    by_severity = collections.Counter(r["severity"] for r in rows)
    by_scope = collections.Counter(r["scope"] for r in rows)
    by_manifest = collections.Counter(r["manifest"] for r in rows)
    groups: dict[str, dict] = {}
    for r in rows:
        g = groups.setdefault(
            r["package"],
            {"package": r["package"], "count": 0, "severity": collections.Counter(),
             "scopes": set(), "current": set(), "fixed": set(), "manifests": set()},
        )
        g["count"] += 1
        g["severity"][r["severity"]] += 1
        g["scopes"].add(r["scope"])
        g["current"].add(r["current"])
        if r["fixed"] != "-":
            g["fixed"].add(r["fixed"])
        g["manifests"].add(r["manifest"])
    rank = {s: i for i, s in enumerate(SEVERITY_ORDER)}
    packages = []
    for g in groups.values():
        worst = max(g["severity"], key=lambda s: rank.get(s, -1))
        packages.append({
            "package": g["package"],
            "count": g["count"],
            "worst": worst,
            "severity": dict(g["severity"]),
            "scopes": sorted(g["scopes"]),
            "current": sorted(g["current"]),
            "fixed": sorted(g["fixed"]),
            "manifests": sorted(g["manifests"]),
        })
    packages.sort(key=lambda p: (-rank.get(p["worst"], -1), -p["count"], p["package"]))
    focus = [
        r for r in rows
        if r["scope"] == "runtime" and r["severity"] in ("high", "critical")
    ]
    focus.sort(key=lambda r: (-rank.get(r["severity"], -1), r["package"], r["number"] or 0))
    return {
        "total": len(rows),
        "by_severity": dict(by_severity),
        "by_scope": dict(by_scope),
        "by_manifest": dict(by_manifest),
        "packages": packages,
        "runtime_focus": focus,
    }


def render(result: dict, repo: str, offline: bool) -> None:
    src = "offline report" if offline else "GitHub API"
    print("Dependabot open alerts | repo=%s | source=%s | total=%d"
          % (repo, src, result["total"]))
    sev = result["by_severity"]
    print("  severity : %s" % ", ".join("%s=%d" % (s, sev.get(s, 0)) for s in SEVERITY_ORDER)
          + ("  (+%d unknown)" % sum(v for k, v in sev.items() if k not in SEVERITY_ORDER)
             if any(k not in SEVERITY_ORDER for k in sev) else ""))
    print("  scope    : %s" % ", ".join("%s=%d" % kv for kv in sorted(result["by_scope"].items())))
    print("  manifest : %s" % ", ".join("%s=%d" % kv for kv in sorted(result["by_manifest"].items())))
    print("  packages : %d" % len(result["packages"]))
    print()
    print("%-28s %4s  %-9s %-26s %-11s %-9s %s"
          % ("package", "n", "worst", "severity", "current", "fixed", "scope"))
    for p in result["packages"]:
        print("%-28s %4d  %-9s %-26s %-11s %-9s %s"
              % (p["package"][:28], p["count"], p["worst"],
                 ",".join("%s:%d" % (k, v) for k, v in sorted(p["severity"].items())),
                 ",".join(p["current"])[:11], ",".join(p["fixed"])[:9] or "-",
                 ",".join(p["scopes"])))
    focus = result["runtime_focus"]
    print()
    print("Runtime high/critical (fix first): %d" % len(focus))
    for r in focus:
        print("  #%-4s %-9s %-28s %-10s -> %-10s %s"
              % (r["number"], r["severity"], r["package"], r["current"],
                 r["fixed"], r["cve"]))
    print()
    print("Report only — this command never fails the build.")
    print("See docs/DEPENDENCY-SECURITY.md for the fix plan and why lockfile")
    print("regeneration must run on your own machine, not in this sandbox.")


def self_test() -> int:
    a = {
        "number": 1,
        "security_advisory": {"severity": "high", "cve_id": "CVE-TEST-1"},
        "dependency": {"package": {"name": "lib-a"}, "scope": "runtime",
                       "manifest_path": "web/pnpm-lock.yaml", "version_requirement": "1.0.0"},
        "security_vulnerability": {"first_patched_version": {"identifier": "1.2.0"}},
    }
    b = {
        "number": 2,
        "security_advisory": {"severity": "critical", "ghsa_id": "GHSA-TEST-2"},
        "dependency": {"package": {"name": "lib-b"}, "scope": "development",
                       "manifest_path": "mcp/package-lock.json", "version_requirement": "2.0.0"},
        "security_vulnerability": {"first_patched_version": None},
    }
    c = dict(a, number=3, security_advisory={"severity": "low", "cve_id": "CVE-TEST-3"})
    stream = json.dumps([a, b]) + "\n" + json.dumps([c])
    rows = [normalize(x) for x in parse_stream(stream)]
    assert len(rows) == 3, "concatenated JSON stream"
    assert rows[1]["fixed"] == "-", "None first_patched_version"
    assert rows[1]["cve"] == "GHSA-TEST-2", "GHSA fallback"
    result = summarize(rows)
    assert result["total"] == 3 and len(result["packages"]) == 2
    assert result["by_severity"] == {"high": 1, "critical": 1, "low": 1}
    assert result["by_manifest"]["web/pnpm-lock.yaml"] == 2
    assert [r["number"] for r in result["runtime_focus"]] == [1], "runtime high only"
    assert [p["package"] for p in result["packages"]] == ["lib-b", "lib-a"], \
        "worst-severity ordering"
    filtered = summarize([r for r in rows if SEVERITY_ORDER.index(r["severity"]) >= 2])
    assert filtered["total"] == 2 and len(filtered["runtime_focus"]) == 1
    for bad in ("", "[]", '{"message":"nope"}'):
        try:
            parse_stream(bad)
        except ValueError:
            continue
        raise AssertionError("malformed stream must be rejected: %r" % bad)
    print("OK   self-test passed (8 cases)")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description="Summarize open Dependabot alerts (report only)")
    ap.add_argument("--repo", default=DEFAULT_REPO, help="OWNER/REPO (default: %s)" % DEFAULT_REPO)
    ap.add_argument("--report", help="read a saved --paginate JSON stream instead of calling gh ('-' = stdin)")
    ap.add_argument("--min-severity", choices=SEVERITY_ORDER, default="low",
                    help="only include alerts at or above this severity")
    ap.add_argument("--json", action="store_true", help="emit structured JSON instead of a table")
    ap.add_argument("--self-test", action="store_true", help="run offline assertions and exit")
    args = ap.parse_args()
    if args.self_test:
        return self_test()

    offline = bool(args.report)
    if offline:
        text = sys.stdin.read() if args.report == "-" else Path(args.report).read_text(encoding="utf-8")
    else:
        proc = subprocess.run(
            ["gh", "api", "--paginate", API_PATH.format(repo=args.repo)],
            capture_output=True, text=True,
        )
        if proc.returncode != 0:
            print("ERROR: gh api failed (rc=%d): %s" % (proc.returncode, proc.stderr.strip()[:400]),
                  file=sys.stderr)
            print("Hint: needs `gh auth login` and access to the repo's Dependabot alerts.",
                  file=sys.stderr)
            return 2
        text = proc.stdout
    try:
        alerts = parse_stream(text)
    except (ValueError, OSError) as exc:
        print("ERROR: %s" % exc, file=sys.stderr)
        return 2

    floor = SEVERITY_ORDER.index(args.min_severity)
    rows = [
        r for r in (normalize(a) for a in alerts)
        if r["severity"] not in SEVERITY_ORDER
        or SEVERITY_ORDER.index(r["severity"]) >= floor
    ]
    if not rows:
        print("No open alerts at or above '%s'." % args.min_severity)
        return 0
    result = summarize(rows)
    if args.json:
        print(json.dumps({"repo": args.repo, "source": "report" if offline else "api", **result},
                         ensure_ascii=False, indent=2))
    else:
        render(result, args.repo, offline)
    return 0


if __name__ == "__main__":
    sys.exit(main())
