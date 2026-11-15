#!/usr/bin/env python3
"""隐私门禁：补足 config-privacy-check 看不到的「仓库面」。

    python3 -S scripts/tree-privacy-check.py               # 即将提交的内容（默认）
    python3 -S scripts/tree-privacy-check.py --cached-only  # 只扫暂存区（适合 pre-commit）

`make privacy-check` 只校验 ``data/config.json*`` —— 那是被 .gitignore 忽略的
**运行时配置**，干净克隆里根本不存在，脚本会直接 SKIP 返回 0。也就是说它挡不住
「把明文 API Key、构建机绝对路径或私有仓名写进待提交源码」。本守卫补的是
**发布面**：按字节检查下一次提交真正会带出去的内容。

扫描口径刻意取「即将发布什么」而不是「上一次提交里有什么」：否则清掉 HEAD 里
历史敏感串的那一次修复提交，会被 HEAD 旧内容误判为红。具体：
  - 路径基线取 ``git ls-tree -r HEAD``；文件在工作区存在时读**工作区版本**
    （含尚未暂存的修复），已删除且未暂存的路径不进入下一次提交，跳过；
  - ``git ls-files --others --exclude-standard`` 的未跟踪新文件（未被 ignore）纳入；
  - ``git diff --cached`` 的新增行再扫一遍，覆盖「暂存内容与工作区不一致」；
  - 干净克隆 / CI 里工作区 == HEAD，等价于整棵树扫描。

判据取高置信度形态，避免把占位符 / 测试假值 / 浏览器 UA 版本号刷成告警：
  - 只打印文件名、行号与规则名，**绝不打印命中内容本身**（可能就是真密钥）；
  - 形如 ``/Users/<user>/...`` 的占位路径不会被匹配（尖括号不在字符集内）；
  - 短占位（``sk-xxx`` / ``ghp_xxx`` / ``change-me-*``）够不到长度阈值；
  - ``Authorization: Bearer ...`` 只认「长且字母数字混合、不含 example/your/<token>
    等占位词」的串；文档里的 ``Bearer <token>`` / ``Bearer YOUR_TOKEN_HERE`` 放行；
  - JWT / 各类机器人 webhook 都要求固定前缀 + 真实长度密钥，不做宽松的
    ``token=...`` 通配（那会把所有配置示例刷成红）；
  - RFC1918 私有地址才算「真实内网 IP」，文档示例请用 192.0.2.0/24 等保留网段。
"""
from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys

# 私有标识词表刻意拆写：守卫源码自身不能含完整词，否则会命中自己
# （这类「守卫把自己判成泄漏」的问题在别的仓里踩过）。
_PRIVATE_MARKERS = [
    "individ" + "ular-invest",
    "Workspace" + "/CNB",
    "daoye" + "-engine",
    "cicd" + "-platform",
    "work" + "/sale",
]

_PLACEHOLDER_WORDS = (
    "example", "your", "placeholder", "dummy", "fake", "sample", "change",
    "redacted", "xxxx", "token", "secret", "insert", "here", "changeme",
)


def _is_real_bearer(m) -> bool:
    """Bearer 后跟的必须像真 token：长、字母数字混合、不含占位词。"""
    s = m.group(1)
    low = s.lower()
    return (
        len(s) >= 24
        and not any(w in low for w in _PLACEHOLDER_WORDS)
        and re.search(r"\d", s) is not None
        and re.search(r"[A-Za-z]", s) is not None
    )


# (规则名, 正则, 可选二次判定)：二次判定只用于压误报，绝不放宽长度/形态要求。
RULES = [
    ("private_key", re.compile(r"-----BEGIN (?:[A-Z ]+ )?PRIVATE KEY-----"), None),
    ("github_pat", re.compile(r"\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{50,})\b"), None),
    ("aws_access_key", re.compile(r"\bAKIA[0-9A-Z]{16}\b"), None),
    ("api_key_literal", re.compile(r"\bsk-[A-Za-z0-9_-]{32,}\b"), None),
    ("jwt_literal", re.compile(r"\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b"), None),
    ("slack_token", re.compile(r"\bxox[baprs]-[A-Za-z0-9-]{10,}\b"), None),
    (
        "bearer_token",
        re.compile(r"(?i)\bbearer\s+([A-Za-z0-9._~+/=-]{20,})"),
        _is_real_bearer,
    ),
    (
        "dingtalk_webhook",
        re.compile(r"oapi\.dingtalk\.com/robot/send\?access_token=[A-Za-z0-9]{16,}"),
        None,
    ),
    (
        "wecom_webhook",
        re.compile(r"qyapi\.weixin\.qq\.com/cgi-bin/webhook/send\?key=[0-9a-fA-F-]{20,}"),
        None,
    ),
    (
        "feishu_webhook",
        re.compile(r"open\.feishu\.cn/open-apis/bot/v2/hook/[0-9a-fA-F-]{20,}"),
        None,
    ),
    (
        "slack_webhook",
        re.compile(r"hooks\.slack\.com/services/T[A-Za-z0-9]{8,}/B[A-Za-z0-9]{8,}/[A-Za-z0-9]{16,}"),
        None,
    ),
    ("build_machine_path", re.compile(r"/(?:Users|home|root)/[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]*"), None),
    ("private_identity", re.compile("|".join(re.escape(m) for m in _PRIVATE_MARKERS)), None),
    (
        "private_ip",
        re.compile(
            r"\b(?:10\.\d{1,3}\.\d{1,3}\.\d{1,3}"
            r"|172\.(?:1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}"
            r"|192\.168\.\d{1,3}\.\d{1,3})\b"
        ),
        None,
    ),
]

MAX_HITS = 20
MAX_BLOB = 3_000_000


def git(*args: str) -> bytes:
    return subprocess.run(["git", *args], capture_output=True, check=False).stdout


def read_bytes(path: str) -> bytes | None:
    try:
        with open(path, "rb") as f:
            return f.read()
    except OSError:
        return None


def scan_lines(source: str, path: str, text: str, hits: list[str], lineno: int = 1) -> None:
    for i, line in enumerate(text.splitlines(), lineno):
        for rule, pattern, validator in RULES:
            m = pattern.search(line)
            if m and (validator is None or validator(m)):
                hits.append("%s:%s:%d [%s]" % (source, path, i, rule))
                break


def scan_blob(source: str, path: str, data: bytes, hits: list[str]) -> bool:
    if not data or len(data) > MAX_BLOB or b"\x00" in data[:1024]:
        return False  # 二进制产物 / 超大文件不逐行扫
    scan_lines(source, path, data.decode("utf-8", "replace"), hits)
    return True


def scan_prospective(hits: list[str]) -> tuple[int, int]:
    tracked = 0
    untracked = 0
    seen: set[str] = set()
    for line in git("ls-tree", "-r", "HEAD").decode("utf-8", "replace").splitlines():
        if "\t" not in line:
            continue
        meta, path = line.split("\t", 1)
        parts = meta.split()
        if len(parts) != 3 or parts[1] != "blob":
            continue
        seen.add(path)
        data = read_bytes(path)
        if data is None:  # 工作区已删除且未暂存
            continue
        if scan_blob("TREE", path, data, hits):
            tracked += 1
    for raw in git("ls-files", "--others", "--exclude-standard", "-z").split(b"\0"):
        if not raw:
            continue
        path = os.fsdecode(raw)
        if path in seen:
            continue
        data = read_bytes(path)
        if data is None:
            continue
        if scan_blob("NEW", path, data, hits):
            untracked += 1
    return tracked, untracked


def scan_cached(hits: list[str]) -> int:
    diff = git("diff", "--cached", "--unified=0", "--no-color").decode("utf-8", "replace")
    path = "?"
    lineno = 0
    lines = 0
    for line in diff.splitlines():
        if line.startswith("+++ b/"):
            path = line[6:]
        elif line.startswith("@@"):
            m = re.search(r"\+(\d+)", line)
            lineno = int(m.group(1)) if m else 0
        elif line.startswith("+") and not line.startswith("+++"):
            lines += 1
            scan_lines("CACHED", path, line[1:], hits, lineno)
            lineno += 1
    return lines


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--cached-only", action="store_true", help="只扫暂存区新增行")
    args = ap.parse_args()
    hits: list[str] = []
    if args.cached_only:
        n = scan_cached(hits)
        print("scanned %d staged added lines" % n)
    else:
        tracked, untracked = scan_prospective(hits)
        staged = scan_cached(hits)
        print("scanned %d tracked + %d new files + %d staged added lines" % (tracked, untracked, staged))
    if hits:
        for h in hits[:MAX_HITS]:
            print("FAIL " + h)
        if len(hits) > MAX_HITS:
            print("... and %d more" % (len(hits) - MAX_HITS))
        print("--- %d hit(s); values are never printed" % len(hits))
        return 1
    print("OK   no credentials, build paths or private identifiers in scope")
    return 0


if __name__ == "__main__":
    sys.exit(main())
