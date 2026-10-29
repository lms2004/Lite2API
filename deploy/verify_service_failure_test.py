"""A 200 response must not survive protocol or source-verification exceptions."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("failure_acceptance", Path(__file__).with_name("verify_service.py"))
acceptance = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(acceptance)

COMPLETE = json.dumps({"choices": [{"index": 0, "message": {"content": "fixture"}, "finish_reason": "stop"}]}).encode()


class Response(io.BytesIO):
    def __init__(self, body, status=200):
        super().__init__(body)
        self.status = status
        self.headers = {"X-Lite2API-Request-Id": "fixture-request"}


class Opener:
    def __init__(self, body=COMPLETE, state=b"{}"):
        self.body, self.state = body, state

    def open(self, request, timeout):
        if request.full_url.endswith("/v1/models"):
            return Response(b"{}", 401)
        if request.full_url.endswith("/admin/api/state"):
            return Response(self.state)
        return Response(self.body)


class FailedAcceptance(unittest.TestCase):
    def test_http_200_with_malformed_protocol_cannot_pass(self):
        bodies = [b"not-json", b"[]", b"null", b'{"choices":[null]}',
                  b'{"choices":[{"message":{"content":"ok","tool_calls":false},"finish_reason":"stop"}]}']
        for body in bodies:
            with self.subTest(body=body), patch.object(acceptance.urllib.request, "build_opener", return_value=Opener(body)):
                result = acceptance.verify("http://127.0.0.1", [{"model": "fixture"}],
                                           environ={"LITE2API_API_KEYS": "fixture-client"})
                self.assertFalse(result["ok"])
                self.assertFalse(result["cases"][-1]["ok"])
                self.assertEqual(result["cases"][-1]["reason"], "transport_or_protocol_failure")

    def test_completed_call_with_invalid_source_evidence_cannot_pass(self):
        for state in (b"not-json", b"{}", b'{"stats":{"recent":null}}',
                      b'{"stats":{"recent":[null]}}'):
            with self.subTest(state=state), patch.object(acceptance.urllib.request, "build_opener", return_value=Opener(state=state)):
                result = acceptance.verify("http://127.0.0.1", [{"model": "fixture", "expect_account": "source"}],
                                           environ={"LITE2API_API_KEYS": "fixture-client", "LITE2API_ADMIN_TOKEN": "fixture-admin"})
                row = result["cases"][-1]
                self.assertTrue(row["complete"])
                self.assertFalse(row["ok"])
                self.assertFalse(result["ok"])
                self.assertEqual(row["reason"], "transport_or_protocol_failure")

    def test_cli_writes_failure_report_and_exits_nonzero(self):
        with tempfile.TemporaryDirectory(prefix="lite2api-gate-failure-") as temporary:
            root = Path(temporary)
            manifest, environment, report = root / "cases.json", root / "environment", root / "report.json"
            manifest.write_text(json.dumps({"version": 1, "cases": [{"model": "fixture"}]}))
            environment.write_text("LITE2API_API_KEYS=fixture-client\nLITE2API_ADMIN_TOKEN=fixture-admin\n")
            argv = ["verify_service.py", "--url", "http://127.0.0.1", "--cases", str(manifest),
                    "--env-file", str(environment), "--output", str(report)]
            with patch("sys.argv", argv), patch.object(acceptance.urllib.request, "build_opener", return_value=Opener(b"not-json")), contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaises(SystemExit) as exit_status:
                    acceptance.main()
            self.assertEqual(exit_status.exception.code, 1)
            self.assertFalse(json.loads(report.read_text())["ok"])


if __name__ == "__main__":
    unittest.main()
