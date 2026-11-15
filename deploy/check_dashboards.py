#!/usr/bin/env python3
"""Grafana dashboard 结构与 PromQL 形态门禁（仅标准库，不需要监控容器）。

    python3 -S deploy/check_dashboards.py
    python3 -S deploy/check_dashboards.py --dir /tmp/some-dashboards

背景：`make alert-check` 原先只用一段内联 python 校验 tsm-hub.json，
新增 cicdkit.json 时没有覆盖，导致 histogram_quantile 写成柯里化形式
（`f(q)(vector)`）与「直接对累计 bucket 裸值算分位」两类错误都绿着过。
这里把两个面板的断言收口到一处，并锁死直方图写法的语义。

tsm-hub.json（原有断言，逐条迁移，行为不变）：
  - panel id 唯一 / version >= 9 / 至少 39 个面板
  - panel 36 的「重启归零」口径说明
  - v8 三个指标确实接进面板表达式
cicdkit.json（新增）：
  - uid 与文件名一致 / panel id 唯一 / 至少 10 个面板
  - 每个 target 都引用 cicdkit 指标，不误接 tsm_hub_* 指标
  - histogram_quantile 必须是两参数逗号形式，不能写成 f(q)(vector)
  - 直方图分位必须同时含 sum by (le 与 rate(
  - 窗口级 gauge（runs_total / probe_total / deploys_total / active_runs）
    不得被 rate() 包裹：它们随 store trim 回落、重启归零，不是 counter
"""
from __future__ import annotations

import argparse
import json
import os
import re
import sys

WINDOW_GAUGES = ("runs_total", "probe_total", "deploys_total", "active_runs")


def expressions(doc: dict) -> list[tuple[int, str]]:
    return [
        (p.get("id", -1), t.get("expr", ""))
        for p in doc.get("panels", [])
        for t in p.get("targets", [])
    ]


def check_tsm_hub(doc: dict) -> None:
    panels = doc.get("panels", [])
    ids = [p["id"] for p in panels]
    assert len(ids) == len(set(ids)), "duplicate panel ids"
    assert doc.get("version", 0) >= 9, "dashboard version too old"
    assert len(ids) >= 39, "missing v8 panels"
    p36 = next(p for p in panels if p["id"] == 36)
    assert "重启归零" in p36["description"], "panel 36 counter-scope note missing"
    exprs = " ".join(e for _, e in expressions(doc))
    for metric in (
        "tsm_hub_upstream_error_classes_total",
        "tsm_hub_http_first_byte_seconds_bucket",
        "flow=",
    ):
        assert metric in exprs, "v8 metric not wired into panels: " + metric


def check_cicdkit(doc: dict) -> None:
    panels = doc.get("panels", [])
    ids = [p["id"] for p in panels]
    assert doc.get("uid") == "cicdkit", "uid must match the dashboard file name"
    assert len(ids) == len(set(ids)), "duplicate panel ids"
    assert len(ids) >= 10, "cicdkit panels missing"
    exprs = expressions(doc)
    assert exprs, "no targets defined"
    for pid, expr in exprs:
        assert "cicdkit" in expr, "panel %s does not query a cicdkit metric" % pid
        assert "tsm_hub_" not in expr, "panel %s accidentally queries tsm_hub_*" % pid
        # 窗口级 gauge 的 rate() 检查必须覆盖所有表达式，不能放进下面
        # histogram_quantile 的分支里（那些面板本身不含 rate()）。
        for gauge in WINDOW_GAUGES:
            assert not re.search(
                r"rate\(\s*cicdkit_" + gauge, expr
            ), "panel %s applies rate() to window-level gauge %s" % (pid, gauge)
        if "histogram_quantile" not in expr:
            continue
        # PromQL 的 histogram_quantile 是 (quantile, vector) 两参数函数，
        # 不是可柯里化调用；写成 f(q)(vector) 会被 Prometheus 直接判语法错。
        # 注意 sum by (le) 自身也带右括号，柯里化判据只认「第一个括号内
        # 没有逗号就闭合」这一种形态，避免误伤正确写法。
        assert re.search(
            r"histogram_quantile\(\s*[0-9.]+\s*,", expr
        ), "panel %s misses the two-argument quantile form" % pid
        assert not re.search(
            r"histogram_quantile\([^,)]*\)\s*\(", expr
        ), "panel %s uses curried histogram_quantile" % pid
        assert "sum by (le" in expr, "panel %s misses sum by (le)" % pid
        assert "rate(" in expr, "panel %s misses rate() over buckets" % pid


CHECKS = {
    "tsm-hub.json": check_tsm_hub,
    "cicdkit.json": check_cicdkit,
}


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument(
        "--dir",
        default=os.path.join("deploy", "grafana", "dashboards"),
        help="dashboard 目录（默认 deploy/grafana/dashboards）",
    )
    args = ap.parse_args()
    failed = 0
    for name, check in CHECKS.items():
        path = os.path.join(args.dir, name)
        try:
            with open(path, encoding="utf-8") as f:
                doc = json.load(f)
            check(doc)
            print("OK   %s (%d panels)" % (name, len(doc.get("panels", []))))
        except (OSError, ValueError, KeyError, StopIteration, AssertionError) as e:
            print("FAIL %s: %s" % (name, e))
            failed += 1
    print("--- %d/%d dashboards clean" % (len(CHECKS) - failed, len(CHECKS)))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
