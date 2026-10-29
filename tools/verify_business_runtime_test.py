"""Acceptance-tool regressions; HTTP fixtures are not real-provider evidence."""
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("business_runtime", ROOT / "tools/verify-business-runtime.py")
runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runtime)


class SourceSelection(unittest.TestCase):
    def select(self, route, accounts, operation="openai.chat"):
        return runtime.source_candidates({"routes": {"alias": route}, "accounts": accounts}, "alias", operation)

    def test_wildcard_respects_enabled_operation_and_requested_model(self):
        accounts = [
            {"id": "omitted", "models": ["alias"]},
            {"id": "disabled", "enabled": False, "models": ["alias"]},
            {"id": "wrong-operation", "enabled": True, "type": "anthropic", "models": ["alias"]},
            {"id": "wrong-model", "enabled": True, "models": ["unrelated"]},
            {"id": "ready", "enabled": True, "models": ["actual"], "model_map": {"alias": "actual"}},
        ]
        selected = self.select({"all_accounts": True}, accounts)
        self.assertEqual([(a["id"], model) for a, model, _, _ in selected], [("ready", "actual")])

    def test_empty_membership_does_not_implicitly_expand(self):
        account = {"id": "ready", "enabled": True, "models": ["alias"]}
        for route in ({}, {"targets": []}, {"accounts": []}, {"all_accounts": False}):
            self.assertEqual(self.select(route, [account]), [])
        self.assertEqual(self.select({"all_accounts": True}, []), [])

    def test_explicit_membership_and_credential_are_preserved(self):
        account = {"id": "ready", "enabled": True, "models": ["actual"]}
        route = {"targets": [{"account": "ready", "model": "actual", "credential": "public-index", "reasoning_effort": "high"}]}
        self.assertEqual(self.select(route, [account]), [(account, "actual", "high", "public-index")])
        self.assertEqual(self.select(route, [{**account, "enabled": False}]), [])
        self.assertEqual(self.select({"accounts": ["ready"], "upstream_model": "actual"}, [account])[0][1], "actual")

    def test_logical_capability_and_negative_evidence(self):
        cap = {"model": "logical", "upstream_model": "actual", "reasoning_efforts": ["auto"]}
        account = {"id": "ready", "enabled": True, "models": ["actual"], "capabilities": [cap]}
        route = {"model": "logical", "targets": [{"account": "ready"}]}
        self.assertEqual(self.select(route, [account])[0][1], "actual")
        cap["evidence"] = {"status": "unsupported"}
        self.assertEqual(self.select(route, [account]), [])
        self.assertEqual(self.select({"all_accounts": True, "upstream_model": "actual"}, [account]), [])


class CompletionChecks(unittest.TestCase):
    def test_client_timeout_kills_tool_process_group(self):
        with tempfile.TemporaryDirectory(prefix="lite2api-client-cleanup-") as directory:
            child_pid = Path(directory) / "child.pid"
            command = ["python3", "-c", "import pathlib,subprocess,time; p=subprocess.Popen(['sleep','30']); pathlib.Path(" + repr(str(child_pid)) + ").write_text(str(p.pid)); time.sleep(30)"]
            with self.assertRaises(subprocess.TimeoutExpired):
                runtime.run_bounded(command, timeout=0.5, text=True)
            pid = int(child_pid.read_text())
            for _ in range(50):
                stat = Path(f"/proc/{pid}/stat")
                if not stat.exists() or stat.read_text().split()[2] == "Z":
                    break
                time.sleep(0.02)
            else:
                self.fail("timed-out client's child is still running")

    def test_incomplete_json_and_stream_are_not_success(self):
        cases = [
            ("chat", {"choices": [{"message": {"content": "OK"}, "finish_reason": "stop"}]}),
            ("responses", {"status": "completed", "output": [{"type": "message", "content": [{"type": "output_text", "text": "OK"}]}]}),
            ("messages", {"stop_reason": "end_turn", "content": [{"type": "text", "text": "OK"}]}),
        ]
        for operation, body in cases:
            with self.subTest(operation=operation):
                self.assertTrue(runtime.complete(operation, json.dumps(body), {}))
                self.assertFalse(runtime.complete(operation, "{}", {}))
                self.assertFalse(runtime.complete(operation, "data: {}\n\n", {"stream": True}))
        chat = 'data: {"choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n'
        self.assertTrue(runtime.complete("chat", chat, {"stream": True}))
        self.assertFalse(runtime.complete("chat", chat + 'data: {}\n\n', {"stream": True}))

    def test_unknown_only_is_rejected_before_startup(self):
        result = subprocess.run(["python3", str(ROOT / "tools/verify-business-runtime.py"), "--binary", "/missing", "--source-config", "/missing", "--source-env", "/missing", "--route", "alias", "--output", "/missing", "--only", "typo"], capture_output=True, text=True, timeout=5)
        self.assertEqual(result.returncode, 2)
        self.assertIn("unknown --only", result.stderr)


class ExitStatusIntegration(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory(prefix="lite2api-business-tool-test-")
        cls.addClassCleanup(cls.temp.cleanup)
        cls.root = Path(cls.temp.name)
        cls.binary = cls.root / "lite2api"
        subprocess.run(["go", "build", "-o", str(cls.binary), "./cmd/lite2api"], cwd=ROOT, env={**os.environ, "GOTOOLCHAIN": "local"}, check=True, capture_output=True, timeout=90)

    @classmethod
    def tearDownClass(cls):
        cls.temp.cleanup()

    def test_termination_writes_failure_and_reaps_gateway_and_client(self):
        entered, release = threading.Event(), threading.Event()

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                self.rfile.read(int(self.headers.get("Content-Length", 0)))
                entered.set()
                release.wait(10)

            def log_message(self, *args):
                pass

        with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as server:
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            config_path, env_path, output = [self.root / name for name in ("cancel-config.json", "cancel-env", "cancel-report.json")]
            config_path.write_text(json.dumps({"version": 1, "server": {}, "accounts": [{"id": "fixture", "enabled": True, "api_key": "fixture-key", "base_url": f"http://127.0.0.1:{server.server_port}", "models": ["alias"]}], "routes": {"alias": {"accounts": ["fixture"]}}}))
            env_path.write_text("")
            process = subprocess.Popen(["python3", str(ROOT / "tools/verify-business-runtime.py"), "--binary", str(self.binary), "--source-config", str(config_path), "--source-env", str(env_path), "--route", "alias", "--output", str(output), "--only", "chat_json"], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                self.assertTrue(entered.wait(5), "fixture call did not begin")
                children = Path(f"/proc/{process.pid}/task/{process.pid}/children").read_text().split()
                self.assertGreaterEqual(len(children), 2)
                process.send_signal(signal.SIGTERM)
                stdout, stderr = process.communicate(timeout=12)
                self.assertEqual(process.returncode, 1, stdout + stderr)
                report = json.loads(output.read_text())
                self.assertFalse(report["ok"])
                self.assertEqual(report["runtime_error"], "AcceptanceInterrupted")
                for child in children:
                    self.assertFalse(Path(f"/proc/{child}").exists(), "acceptance left a child process behind")
            finally:
                release.set()
                if process.poll() is None:
                    process.kill()
                    process.wait(timeout=5)
                process.stdout.close()
                process.stderr.close()
                server.shutdown()
                thread.join(timeout=3)

    def test_cli_writes_report_and_returns_failure_for_incomplete_provider(self):
        for complete in (True, False):
            class Handler(BaseHTTPRequestHandler):
                def do_POST(self):
                    self.rfile.read(int(self.headers.get("Content-Length", 0)))
                    body = json.dumps({"choices": [{"message": {"content": "OK"}, "finish_reason": "stop"}]} if complete else {}).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)

                def log_message(self, *args):
                    pass

            with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as server:
                thread = threading.Thread(target=server.serve_forever, daemon=True)
                thread.start()
                try:
                    config = {"version": 1, "server": {"listen": "127.0.0.1:1"}, "accounts": [{"id": "fixture", "enabled": True, "type": "openai", "base_url": "http://127.0.0.1:" + str(server.server_port), "api_key": "fixture-upstream-key", "models": ["alias"]}], "routes": {"alias": {"accounts": ["fixture"]}}}
                    config_path, env_path, output = [self.root / name for name in ("config.json", "env", "report.json")]
                    config_path.write_text(json.dumps(config))
                    env_path.write_text("")
                    result = subprocess.run(["python3", str(ROOT / "tools/verify-business-runtime.py"), "--binary", str(self.binary), "--source-config", str(config_path), "--source-env", str(env_path), "--route", "alias", "--output", str(output), "--only", "chat_json"], capture_output=True, text=True, timeout=20)
                    report = json.loads(output.read_text())
                    self.assertEqual(result.returncode, 0 if complete else 1, result.stderr + json.dumps(report))
                    self.assertIs(report["ok"], complete)
                    self.assertIs(report["cases"][0]["complete"], complete)
                    self.assertEqual(len(report["logical_calls"]), 1)
                finally:
                    server.shutdown()
                    thread.join(timeout=3)


if __name__ == "__main__":
    unittest.main()
