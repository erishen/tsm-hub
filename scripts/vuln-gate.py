#!/usr/bin/env python3
"""govulncheck 分级门禁：可达漏洞按「谁该负责修」分层判定。

    python3 -S scripts/vuln-gate.py --version v1.9.0 [options]
    python3 -S scripts/vuln-gate.py --report .data/govulncheck.json
    python3 -S scripts/vuln-gate.py --self-test

不直接拿 ``govulncheck ./...`` 的退出码当门禁，是为了区分责任方并保留可
执行的修复路径：

  * 第三方模块里被真实调用到的漏洞 → 升级依赖即可解决，**硬失败**；
  * 标准库里被真实调用到的漏洞 → 需升级 Go 工具链；本仓 ``go.mod`` 钉在
    Go 1.26.11（已修掉此前 12 条公告），Makefile 默认 ``VULN_STRICT=1`` 让它
    们同样**硬失败**；``VULN_STRICT=0``（不带 ``--strict``）才退回 WARN；
  * 仅在模块/包层面出现、调用链没到的漏洞 → 计数字、不拦截。

报告解析失败、扫描器报错、缺少 config/SBOM 对象一律判失败，不静默放行。

判据取自 govulncheck v1.9.0 的 JSON 流：带 ``function`` 帧的 finding 才是
「被真实调用」，只有 ``module``/``version`` 的 finding 是信息性条目；
**trace[0] 是脆弱符号本身**，用它所属的模块归属，而不是整条调用链里出现过的
所有模块（否则「标准库漏洞恰好经过第三方包调用」会被误判成第三方问题）。
SBOM 里没有 ``version`` 的模块就是主模块本身。
"""
from __future__ import annotations

import argparse
import json
import subprocess
import sys

MAX_LIST = 20


def parse_report(text: str) -> list[dict]:
    """govulncheck -json 输出是一串**缩进过的** JSON 对象，不是 JSONL。"""
    decoder = json.JSONDecoder()
    objs: list[dict] = []
    i = 0
    n = len(text)
    while i < n:
        while i < n and text[i].isspace():
            i += 1
        if i >= n:
            break
        obj, i = decoder.raw_decode(text, i)
        if not isinstance(obj, dict):
            raise ValueError("report contains a non-object JSON value")
        objs.append(obj)
    if not objs:
        raise ValueError("empty report")
    return objs


def collect(objs: list[dict]) -> dict:
    config = None
    sbom = None
    osvs: dict[str, dict] = {}
    findings: list[dict] = []
    for obj in objs:
        if "config" in obj:
            config = obj["config"]
        if "SBOM" in obj or "sbom" in obj:
            sbom = obj.get("SBOM") or obj.get("sbom")
        if "osv" in obj and isinstance(obj["osv"], dict) and obj["osv"].get("id"):
            osvs[obj["osv"]["id"]] = obj["osv"]
        if "finding" in obj and isinstance(obj["finding"], dict):
            findings.append(obj["finding"])
    if not isinstance(config, dict):
        raise ValueError("missing config object (scanner did not finish?)")

    main_modules: set[str] = set()
    if isinstance(sbom, dict):
        for mod in sbom.get("modules") or []:
            if isinstance(mod, dict) and mod.get("path") and not mod.get("version"):
                main_modules.add(mod["path"])

    called: dict[str, set[str]] = {}
    fixed: dict[str, set[str]] = {}
    info: set[str] = set()
    for finding in findings:
        oid = finding.get("osv", "?")
        trace = finding.get("trace") or []
        if finding.get("fixed_version"):
            fixed.setdefault(oid, set()).add(finding["fixed_version"])
        if any(isinstance(frame, dict) and frame.get("function") for frame in trace):
            # 首帧 = 脆弱符号；用它归属模块，而非整条调用链。
            first = next(
                (f for f in trace if isinstance(f, dict) and f.get("module")), None
            )
            called.setdefault(oid, set()).add(first["module"] if first else "?")
        else:
            info.add(oid)

    third: set[str] = set()
    stdlib: set[str] = set()
    for oid, modules in called.items():
        if any(m != "stdlib" and m not in main_modules for m in modules):
            third.add(oid)
        else:
            stdlib.add(oid)
    return {
        "config": config,
        "osvs": osvs,
        "third": third,
        "stdlib": stdlib,
        "info": info - set(called),
        "fixed": fixed,
        "main": main_modules,
    }


def describe(oid: str, data: dict) -> str:
    osv = data["osvs"].get(oid, {})
    summary = (osv.get("summary") or "").replace("\n", " ").strip()
    versions = ", ".join(sorted(data["fixed"].get(oid, ()))) or "no fix listed"
    line = "%s (fixed in %s)" % (oid, versions)
    if summary:
        line += " - %s" % summary[:90]
    return line


def verdict(data: dict, strict: bool) -> tuple[bool, list[str]]:
    cfg = data["config"]
    lines = [
        "scanner %s | go %s | db %s"
        % (
            cfg.get("scanner_version", "?"),
            cfg.get("go_version", "?"),
            cfg.get("db_last_modified", "?"),
        ),
        "reachable %d (third-party %d, stdlib %d); informational %d"
        % (
            len(data["third"]) + len(data["stdlib"]),
            len(data["third"]),
            len(data["stdlib"]),
            len(data["info"]),
        ),
    ]
    if data["stdlib"]:
        blocking = "BLOCKING (--strict)" if strict else "WARN (use --strict to block)"
        lines.append("%s: %d stdlib advisories:" % (blocking, len(data["stdlib"])))
        for oid in sorted(data["stdlib"])[:MAX_LIST]:
            lines.append("  " + describe(oid, data))
        if len(data["stdlib"]) > MAX_LIST:
            lines.append("  ... and %d more" % (len(data["stdlib"]) - MAX_LIST))
        lines.append(
            "  Upgrade the Go toolchain to the fixed version listed above;"
        )
        lines.append("  see docs/TODO.md and SECURITY.md for the supported-version policy.")
    if data["third"]:
        lines.append("FAIL: %d reachable third-party vulnerabilities:" % len(data["third"]))
        for oid in sorted(data["third"])[:MAX_LIST]:
            lines.append("  " + describe(oid, data))
        if len(data["third"]) > MAX_LIST:
            lines.append("  ... and %d more" % (len(data["third"]) - MAX_LIST))
    ok = not data["third"] and (not strict or not data["stdlib"])
    return ok, lines


def run_scan(go: str, version: str) -> str:
    cmd = [go, "run", "golang.org/x/vuln/cmd/govulncheck@" + version, "-json", "./..."]
    proc = subprocess.run(cmd, capture_output=True, text=True, check=False)
    if proc.returncode != 0 and "exit status 3" not in (proc.stderr or ""):
        sys.stderr.write(
            "FAIL govulncheck failed (rc=%d):\n%s\n" % (proc.returncode, (proc.stderr or "")[-2000:])
        )
        raise SystemExit(2)
    return proc.stdout


def self_test() -> int:
    def obj(**kw: object) -> dict:
        return kw

    cfg = obj(
        config=obj(scanner_version="v1.9.0", go_version="go1.26.11", db_last_modified="test")
    )
    sbom = obj(
        SBOM=obj(
            modules=[
                obj(path="example.com/app"),
                obj(path="example.com/lib", version="v1.0.0"),
            ]
        )
    )
    osv_a = obj(osv=obj(id="GO-TEST-1", summary="third party issue"))
    osv_b = obj(osv=obj(id="GO-TEST-2", summary="stdlib issue"))
    called_third = obj(
        finding=obj(
            osv="GO-TEST-1",
            fixed_version="v1.0.1",
            trace=[
                obj(module="example.com/lib", version="v1.0.0", package="lib", function="Do"),
                obj(module="example.com/app", package="main", function="main"),
            ],
        )
    )
    called_stdlib = obj(
        finding=obj(
            osv="GO-TEST-2",
            fixed_version="v1.26.9",
            trace=[
                obj(module="stdlib", version="v1.26.11", package="net/http", function="Serve"),
                obj(module="example.com/app", package="main", function="main"),
            ],
        )
    )
    informational = obj(
        finding=obj(osv="GO-TEST-3", trace=[obj(module="example.com/lib", version="v1.0.0")])
    )
    base = [cfg, sbom, osv_a, osv_b]

    ok, _ = verdict(collect(base + [called_third]), strict=False)
    if ok:
        raise AssertionError("reachable third-party vuln must fail")
    ok, _ = verdict(collect(base + [called_stdlib, informational]), strict=False)
    if not ok:
        raise AssertionError("stdlib-only advisory must not fail without --strict")
    ok, _ = verdict(collect(base + [called_stdlib]), strict=True)
    if ok:
        raise AssertionError("--strict must fail on stdlib advisory")
    ok, _ = verdict(collect(base + [informational]), strict=False)
    if not ok:
        raise AssertionError("informational-only report must pass")
    for broken in ([], [sbom]):
        try:
            collect(broken)
        except ValueError:
            continue
        raise AssertionError("report without config must be rejected")
    for text in ("", "not json", "[]"):
        try:
            parse_report(text)
        except ValueError:
            continue
        raise AssertionError("malformed JSON stream must be rejected")
    # 结论行走 main() 的真实分支；覆盖 strict / 非 strict / 失败三种输出，
    # 防止只在「现场扫描」路径才触发的 NameError 再次漏过自测。
    if result_line(True, True) != "OK   no reachable vulnerabilities":
        raise AssertionError("strict success line changed")
    if result_line(True, False) != "OK   no reachable third-party vulnerabilities":
        raise AssertionError("non-strict success line changed")
    if result_line(False, True) != "FAIL vulnerability gate":
        raise AssertionError("failure line changed")
    print("OK   self-test passed (9 cases)")
    return 0


def result_line(ok: bool, strict: bool) -> str:
    """最终结论行。抽成函数以便 self-test 覆盖 strict / 非 strict 两条路径。"""
    if not ok:
        return "FAIL vulnerability gate"
    label = (
        "no reachable vulnerabilities"
        if strict
        else "no reachable third-party vulnerabilities"
    )
    return "OK   " + label


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--report", help="读取已有 -json 报告；不指定则现场扫描")
    ap.add_argument("--go", default="go", help="go 可执行文件")
    ap.add_argument("--version", default="v1.9.0", help="govulncheck 固定版本")
    ap.add_argument("--save", help="把原始 -json 报告另存到该路径")
    ap.add_argument("--strict", action="store_true", help="标准库告警也判失败")
    ap.add_argument("--self-test", action="store_true", help="只跑解析器自测")
    args = ap.parse_args()

    if args.self_test:
        return self_test()
    try:
        if args.report:
            with open(args.report, "r", encoding="utf-8") as fh:
                text = fh.read()
        else:
            text = run_scan(args.go, args.version)
        if args.save and text:
            with open(args.save, "w", encoding="utf-8") as fh:
                fh.write(text)
        data = collect(parse_report(text))
    except (OSError, ValueError, SystemExit) as exc:
        print("FAIL cannot evaluate vulnerability report: %s" % exc)
        return 2
    ok, lines = verdict(data, args.strict)
    for line in lines:
        print(line)
    print(result_line(ok, args.strict))
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
