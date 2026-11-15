#!/usr/bin/env python3
"""覆盖率门槛：直接解析 Go coverprofile，与下限比较（纯标准库）。

    python3 -S scripts/check-coverage.py --min 60.0
    python3 -S scripts/check-coverage.py --self-test

不调用 ``go tool cover``，也不要求 Go 工具链存在：coverprofile 每行形如

    <file>:<startLine>.<startCol>,<endLine>.<endCol> <numStmt> <count>

覆盖语句数 = ``count > 0`` 的块的 ``numStmt`` 之和，与 ``go tool cover -func``
输出的 total 行口径一致。缺失 / 空 / 格式错误 / 零语句一律判失败，
绝不静默放行（否则「覆盖率文件没生成」会被当成通过）。

``make cover-check`` 先跑 ``--self-test`` 再校验，保证解析器本身没坏。
"""
from __future__ import annotations

import argparse
import sys


def measure(text: str) -> tuple[int, int, float]:
    lines = text.splitlines()
    if not lines or not lines[0].startswith("mode:"):
        raise ValueError("missing 'mode:' header")
    total = 0
    covered = 0
    for lineno, line in enumerate(lines[1:], 2):
        if not line.strip():
            continue
        # 从右侧拆两次：路径里即使含空格也不会影响最后两个数字字段。
        parts = line.rsplit(" ", 2)
        if len(parts) != 3 or ":" not in parts[0]:
            raise ValueError("malformed coverprofile line %d" % lineno)
        try:
            nstmt = int(parts[1])
            count = int(parts[2])
        except ValueError:
            raise ValueError("malformed coverprofile line %d" % lineno) from None
        if nstmt < 0 or count < 0:
            raise ValueError("negative statement/count on line %d" % lineno)
        total += nstmt
        if count > 0:
            covered += nstmt
    if total == 0:
        raise ValueError("coverprofile has no statements")
    return covered, total, 100.0 * covered / total


def evaluate(text: str, min_pct: float) -> tuple[bool, str]:
    covered, total, pct = measure(text)
    ok = pct + 1e-9 >= min_pct
    msg = "coverage %.1f%% (%d/%d statements)" % (pct, covered, total)
    msg += " >= floor %.1f%%" % min_pct if ok else " < floor %.1f%%" % min_pct
    return ok, msg


def self_test() -> int:
    profile = "mode: set\na.go:1.1,2.2 3 1\nb.go:1.1,2.2 1 0\n"
    checks = 0

    ok, _ = evaluate(profile, 75.0)
    if not ok:
        raise AssertionError("exact boundary should pass")
    checks += 1

    ok, _ = evaluate(profile, 75.1)
    if ok:
        raise AssertionError("below floor should fail")
    checks += 1

    ok, _ = evaluate(profile, 0.0)
    if not ok:
        raise AssertionError("zero floor should pass")
    checks += 1

    bad = (
        "",
        "mode: set\n",
        "a.go:1.1,2.2 3 1\n",                 # 缺 mode 头
        "mode: set\na.go 3 1\n",               # 缺行列区间
        "mode: set\na.go:1.1,2.2 x 1\n",       # 非数字
        "mode: set\na.go:1.1,2.2 0 0\n",       # 零语句
    )
    for text in bad:
        try:
            evaluate(text, 60.0)
        except ValueError:
            checks += 1
        else:
            raise AssertionError("malformed profile should never pass")

    # path 中含空格仍可解析
    ok, _ = evaluate("mode: set\nmy dir/a.go:1.1,2.2 2 1\n", 100.0)
    if not ok:
        raise AssertionError("path with spaces should parse")
    checks += 1

    print("OK   self-test passed (%d cases)" % checks)
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--profile", default="coverage.out", help="Go coverprofile 路径")
    ap.add_argument("--min", type=float, default=60.0, help="覆盖率下限百分比")
    ap.add_argument("--self-test", action="store_true", help="只跑解析器自测")
    args = ap.parse_args()

    if args.self_test:
        return self_test()
    if not 0.0 <= args.min <= 100.0:
        print("FAIL --min must be within 0..100")
        return 2
    try:
        with open(args.profile, "r", encoding="utf-8") as fh:
            text = fh.read()
    except OSError as exc:
        print("FAIL cannot read %s: %s" % (args.profile, exc))
        return 2
    try:
        ok, msg = evaluate(text, args.min)
    except ValueError as exc:
        print("FAIL invalid coverprofile: %s" % exc)
        return 2
    print(("OK   " if ok else "FAIL ") + msg)
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
