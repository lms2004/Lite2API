"""Optional-null compatibility without weakening completed-response checks."""
import importlib.util
import json
from pathlib import Path
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


SPEC = importlib.util.spec_from_file_location("null_acceptance", Path(__file__).with_name("verify_service.py"))
acceptance = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(acceptance)


def response_body(stream, tool_calls=None, finish_reason="stop", content="fixture", done=True):
    message = {"content": content, "tool_calls": tool_calls, "function_call": None}
    result = {"choices": [{"index": 0, "delta" if stream else "message": message, "finish_reason": finish_reason}]}
    encoded = json.dumps(result).encode()
    if stream:
        return b"data: " + encoded + b"\n\n" + (b"data: [DONE]\n\n" if done else b"")
    return encoded


class OptionalNullAcceptance(unittest.TestCase):
    def test_null_optional_tools_complete_in_json_and_stream(self):
        for stream in (False, True):
            for tools in (None, []):
                with self.subTest(stream=stream, tools=tools):
                    self.assertTrue(acceptance.protocol_complete("openai.chat", {"stream": stream, "tools": None, "functions": None}, response_body(stream, tools)))

    def test_null_tools_do_not_satisfy_required_tool_exchange(self):
        for stream in (False, True):
            with self.subTest(stream=stream):
                self.assertFalse(acceptance.protocol_complete("openai.chat", {"stream": stream, "tool_choice": "required"}, response_body(stream)))

    def test_malformed_optional_collections_are_not_empty(self):
        for stream in (False, True):
            for invalid in (False, {}, "", 0):
                with self.subTest(stream=stream, invalid=invalid):
                    with self.assertRaises(ValueError):
                        acceptance.protocol_complete("openai.chat", {"stream": stream}, response_body(stream, invalid))

    def test_null_tools_preserve_completion_requirements(self):
        for stream in (False, True):
            with self.subTest(stream=stream):
                self.assertFalse(acceptance.protocol_complete("openai.chat", {"stream": stream}, response_body(stream, finish_reason=None)))
                self.assertFalse(acceptance.protocol_complete("openai.chat", {"stream": stream}, response_body(stream, content=None)))
        self.assertFalse(acceptance.protocol_complete("openai.chat", {"stream": True}, response_body(True, done=False)))
        self.assertFalse(acceptance.protocol_complete("openai.chat", {"stream": True}, response_body(True) + b'data: {"choices":[]}\n\n'))

    def test_release_checker_accepts_http_fixture_with_null_optional_tools(self):
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_GET(self):
                self.send_response(401)
                self.end_headers()

            def do_POST(self):
                request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream" if request["stream"] else "application/json")
                self.end_headers()
                self.wfile.write(response_body(request["stream"]))

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            cases = [{"operation": "openai.chat", "model": "fixture", "stream": stream} for stream in (False, True)]
            result = acceptance.verify(f"http://127.0.0.1:{server.server_port}", cases, environ={"LITE2API_API_KEYS": "fixture-client", "LITE2API_ADMIN_TOKEN": "fixture-admin"}, timeout=5)
            self.assertTrue(result["ok"], result)
            self.assertEqual(sum(bool(row.get("complete")) for row in result["cases"]), 2)
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)


if __name__ == "__main__":
    unittest.main()
