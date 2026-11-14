"""dingtalk_bridge.py 的门禁测试（仅标准库；make alert-check 会跑）。

重点覆盖三件事：加签算法与 daoye-engine 同款、Alertmanager payload 转钉钉消息
不丢关键字段、以及**任何路径都不得把 webhook URL / secret 写进日志或响应**。
"""
from __future__ import annotations

import base64
import hashlib
import hmac
import importlib.util
import json
import os
import unittest
import urllib.parse
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("dingtalk_bridge", os.path.join(HERE, "dingtalk_bridge.py"))
bridge = importlib.util.module_from_spec(_spec)
assert _spec.loader is not None
_spec.loader.exec_module(bridge)

FAKE_URL = "https://oapi.dingtalk.com/robot/send?access_token=fake-token-for-test"
FAKE_SECRET = "SECfake-secret-for-test"
OTHER_URL = "https://example.com/webhook?token=abc"


def am_payload(count=1, status="firing"):
    return {
        "version": "4",
        "status": status,
        "receiver": "dingtalk",
        "groupLabels": {"alertname": "TSMHubUpstreamErrorRateHigh"},
        "commonLabels": {"severity": "warning"},
        "externalURL": "http://alertmanager:9093",
        "alerts": [
            {
                "status": status,
                "labels": {
                    "alertname": "TSMHubUpstreamErrorRateHigh",
                    "severity": "warning",
                    "provider": "agnes",
                    # 高基数/敏感字段绝不能被带进消息
                    "api_key": "must-not-appear",
                    "session_id": "must-not-appear",
                },
                "annotations": {
                    "summary": "上游 agnes 近 5 分钟错误率超过 30%",
                    "description": "核对余额与额度配置",
                },
                "startsAt": "2026-11-14T12:00:00.123Z",
                "generatorURL": "http://prometheus:9090/graph?g0.expr=secret expr",
            }
            for _ in range(count)
        ],
    }


class FakeResponse:
    def __init__(self, body=b'{"errcode":0,"errmsg":"ok"}'):
        self._body = body

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False

    def read(self):
        return self._body


class SignedUrlTest(unittest.TestCase):
    def test_dingtalk_signature_matches_daoye_algorithm(self):
        ts = 1700000000000
        url = bridge.signed_url(FAKE_URL, FAKE_SECRET, now_ms=ts)
        parsed = urllib.parse.urlsplit(url)
        q = urllib.parse.parse_qs(parsed.query)
        self.assertEqual(q["access_token"], ["fake-token-for-test"])
        self.assertEqual(q["timestamp"], [str(ts)])
        digest = hmac.new(
            FAKE_SECRET.encode(), f"{ts}\n{FAKE_SECRET}".encode(), hashlib.sha256
        ).digest()
        # parse_qs 已把 %3D 解回 '='，故直接比 base64 原文。
        self.assertEqual(q["sign"], [base64.b64encode(digest).decode()])
        self.assertEqual(parsed.netloc, "oapi.dingtalk.com")

    def test_no_secret_means_no_signature(self):
        self.assertEqual(bridge.signed_url(FAKE_URL, ""), FAKE_URL)

    def test_non_dingtalk_host_untouched(self):
        self.assertEqual(bridge.signed_url(OTHER_URL, FAKE_SECRET), OTHER_URL)

    def test_display_url_strips_query(self):
        self.assertEqual(bridge.display_url(FAKE_URL),
                         "https://oapi.dingtalk.com/robot/send?...")


class BusinessOkTest(unittest.TestCase):
    def test_errcode_zero(self):
        self.assertEqual(bridge.business_ok('{"errcode":0}'), (True, ""))

    def test_keyword_or_sign_rejection(self):
        ok, why = bridge.business_ok('{"errcode":310000,"errmsg":"关键词不匹配"}')
        self.assertFalse(ok)
        self.assertIn("310000", why)

    def test_empty_or_unknown_body_not_blocking(self):
        self.assertEqual(bridge.business_ok(""), (True, ""))
        self.assertEqual(bridge.business_ok("not json"), (True, ""))


class FormatMessageTest(unittest.TestCase):
    def test_firing_contains_key_fields_and_keyword(self):
        msg = bridge.format_message(am_payload(), "告警", "http://127.0.0.1:19092",
                                    "http://127.0.0.1:19091")
        self.assertEqual(msg["msgtype"], "markdown")
        title, text = msg["markdown"]["title"], msg["markdown"]["text"]
        self.assertIn("告警", title)
        self.assertIn("告警", text)
        self.assertIn("[触发] TSMHubUpstreamErrorRateHigh", text)
        self.assertIn("agnes", text)
        self.assertIn("warning", text)
        self.assertIn("2026-11-14 12:00:00", text)
        self.assertIn("Prometheus 告警", text)
        self.assertIn("Grafana 面板", text)

    def test_resolved_uses_recovery_wording(self):
        text = bridge.format_message(am_payload(status="resolved"))["markdown"]["text"]
        self.assertIn("[已恢复]", text)
        self.assertIn("已恢复", text)

    def test_truncates_long_batches(self):
        text = bridge.format_message(am_payload(count=12))["markdown"]["text"]
        self.assertIn("还有 2 条", text)
        self.assertEqual(text.count("**[触发]"), bridge.MAX_ALERTS)

    def test_no_high_cardinality_or_secret_fields_leak(self):
        text = bridge.format_message(am_payload())["markdown"]["text"]
        for forbidden in ("must-not-appear", "prometheus:9090", "generatorURL",
                          "fake-token", "access_token"):
            self.assertNotIn(forbidden, text)

    def test_custom_keyword_in_title_and_body(self):
        msg = bridge.format_message(am_payload(), "运维告警")
        self.assertIn("运维告警", msg["markdown"]["title"])
        self.assertIn("运维告警", msg["markdown"]["text"])


class SendTest(unittest.TestCase):
    def test_posts_signed_markdown_payload(self):
        captured = {}

        def fake_urlopen(req, timeout=None):
            captured["url"] = req.full_url
            captured["timeout"] = timeout
            captured["body"] = json.loads(req.data.decode("utf-8"))
            return FakeResponse()

        with mock.patch.object(bridge.urllib.request, "urlopen", fake_urlopen):
            ok, reason = bridge.send_to_dingtalk(
                bridge.format_message(am_payload()), FAKE_URL, FAKE_SECRET, 7.0)
        self.assertTrue(ok, reason)
        self.assertIn("timestamp=", captured["url"])
        self.assertIn("sign=", captured["url"])
        self.assertEqual(captured["body"]["msgtype"], "markdown")
        self.assertEqual(captured["timeout"], 7.0)
        self.assertNotIn(FAKE_SECRET, reason)

    def test_business_error_is_not_success(self):
        body = b'{"errcode":310000,"errmsg":"sign not match"}'
        with mock.patch.object(bridge.urllib.request, "urlopen",
                               lambda req, timeout=None: FakeResponse(body)):
            ok, reason = bridge.send_to_dingtalk(
                bridge.format_message(am_payload()), FAKE_URL, FAKE_SECRET)
        self.assertFalse(ok)
        self.assertIn("310000", reason)
        self.assertNotIn("fake-token-for-test", reason)

    def test_network_exception_is_redacted(self):
        leak = (f"HTTP Error 400 for {FAKE_URL}&timestamp=1700000000000"
                f"&sign=SIGNVALUE; secret={FAKE_SECRET}")

        def boom(req, timeout=None):
            raise RuntimeError(leak)

        with mock.patch.object(bridge.urllib.request, "urlopen", boom):
            ok, reason = bridge.send_to_dingtalk(
                bridge.format_message(am_payload()), FAKE_URL, FAKE_SECRET)
        self.assertFalse(ok)
        for forbidden in ("fake-token-for-test", "SIGNVALUE", FAKE_SECRET):
            self.assertNotIn(forbidden, reason)

    def test_redact_handles_url_query(self):
        s = bridge.redact(f"POST {FAKE_URL}&sign=abc", FAKE_SECRET)
        self.assertNotIn("fake-token-for-test", s)
        self.assertNotIn("sign=abc", s)


if __name__ == "__main__":
    unittest.main()
