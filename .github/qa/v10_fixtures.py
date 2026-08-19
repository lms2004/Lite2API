#!/usr/bin/env python3
import json
import threading
import time
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PROFILES = {
    45678: {"name": "Codex Fast Lane", "models": ["gpt-5.6-codex-fast"], "delay": 0.055, "flaky": False},
    45680: {"name": "Claude Steady", "models": ["claude-sonnet-4-6"], "delay": 0.150, "flaky": False},
    45681: {"name": "Gemini Vision", "models": ["gemini-3.6-pro-vision"], "delay": 0.360, "flaky": False},
    45683: {"name": "Grok Canary", "models": ["grok-4.5"], "delay": 0.095, "flaky": True},
}
COUNTERS = {port: 0 for port in PROFILES}


def iso(hours=0):
    return (datetime.now(timezone.utc) + timedelta(hours=hours)).isoformat().replace("+00:00", "Z")


class ModelHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        print(f"model:{self.server.server_port}: " + (fmt % args), flush=True)

    def send_json(self, status, payload):
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        profile = PROFILES[self.server.server_port]
        if self.path.rstrip("/") == "/v1/models":
            self.send_json(200, {
                "object": "list",
                "data": [
                    {"id": model, "object": "model", "owned_by": profile["name"]}
                    for model in profile["models"]
                ],
            })
            return
        self.send_json(404, {"error": {"message": "not found"}})

    def do_POST(self):
        profile = PROFILES[self.server.server_port]
        length = int(self.headers.get("Content-Length", "0") or "0")
        raw = self.rfile.read(length) if length else b"{}"
        try:
            request = json.loads(raw.decode("utf-8"))
        except Exception:
            request = {}
        if self.path.rstrip("/") != "/v1/chat/completions":
            self.send_json(404, {"error": {"message": "not found"}})
            return
        COUNTERS[self.server.server_port] += 1
        time.sleep(profile["delay"])
        if profile["flaky"] and COUNTERS[self.server.server_port] % 3 == 0:
            self.send_json(503, {"error": {"message": "Canary quality probe failed", "type": "upstream_error"}})
            return
        model = request.get("model") or profile["models"][0]
        prompt_tokens = 24 + len(json.dumps(request.get("messages") or [], ensure_ascii=False)) // 12
        is_quality = "只回复 OK" in raw.decode("utf-8", errors="ignore")
        output_tokens = 3 if is_quality else 18
        content = "OK" if is_quality else f"{profile['name']} response"
        self.send_json(200, {
            "id": f"chatcmpl-v10-{self.server.server_port}-{COUNTERS[self.server.server_port]}",
            "object": "chat.completion",
            "created": int(time.time()),
            "model": model,
            "choices": [{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": "stop"}],
            "usage": {
                "prompt_tokens": prompt_tokens,
                "completion_tokens": output_tokens,
                "total_tokens": prompt_tokens + output_tokens,
                "prompt_tokens_details": {"cached_tokens": prompt_tokens // 4},
            },
        })


class OAuthHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        print("oauth: " + (fmt % args), flush=True)

    def send_json(self, status, payload):
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.split("?", 1)[0] != "/v0/management/auth-files":
            self.send_json(404, {"error": "not found"})
            return
        self.send_json(200, {"files": [
            {
                "id": "codex-a.json", "auth_index": "codex-a", "provider": "openai",
                "label": "owner@studio.example", "account_type": "ChatGPT Team", "status": "active",
                "success": 1842, "failed": 9, "updated_at": iso(-1),
                "quota_windows": [
                    {"kind": "five_hour", "label": "5 小时", "used_percentage": 38.0, "reset_at": iso(2), "observed_at": iso(0), "source": "provider"},
                    {"kind": "seven_day", "label": "7 天", "used_percentage": 61.0, "reset_at": iso(96), "observed_at": iso(0), "source": "provider"},
                ],
            },
            {
                "id": "codex-b.json", "auth_index": "codex-b", "provider": "openai",
                "label": "backup@studio.example", "account_type": "ChatGPT Pro", "status": "active",
                "success": 936, "failed": 4, "updated_at": iso(-1),
                "quota_windows": [
                    {"kind": "five_hour", "label": "5 小时", "used_percentage": 72.0, "reset_at": iso(1), "observed_at": iso(0), "source": "provider"},
                    {"kind": "seven_day", "label": "7 天", "used_percentage": 44.0, "reset_at": iso(72), "observed_at": iso(0), "source": "provider"},
                ],
            },
            {
                "id": "claude.json", "auth_index": "claude-a", "provider": "claude",
                "label": "research@studio.example", "account_type": "Claude Max", "status": "active",
                "success": 1210, "failed": 11, "updated_at": iso(-1),
                "quota_windows": [
                    {"kind": "five_hour", "label": "5 小时", "used_percentage": 54.0, "reset_at": iso(3), "observed_at": iso(0), "source": "provider"},
                    {"kind": "seven_day_sonnet", "label": "Sonnet 周", "used_percentage": 84.0, "reset_at": iso(58), "observed_at": iso(0), "source": "provider"},
                ],
            },
            {
                "id": "gemini.json", "auth_index": "gemini-a", "provider": "gemini-cli",
                "label": "vision@studio.example", "account_type": "Workspace", "status": "active",
                "success": 702, "failed": 6, "updated_at": iso(-1),
                "quota_windows": [
                    {"kind": "daily", "label": "每日", "used_percentage": 46.0, "reset_at": iso(9), "observed_at": iso(0), "source": "provider"},
                ],
            },
            {
                "id": "antigravity.json", "auth_index": "anti-a", "provider": "antigravity",
                "label": "preview@studio.example", "account_type": "Preview", "status": "unavailable",
                "unavailable": True, "success": 88, "failed": 17, "updated_at": iso(-1),
                "next_retry_after": iso(2), "quota": {"exceeded": True},
                "quota_windows": [
                    {"kind": "model_cooldown", "label": "模型冷却", "status": "cooldown", "reset_at": iso(2), "observed_at": iso(0), "source": "provider"},
                ],
            },
        ]})


def main():
    servers = []
    for port in PROFILES:
        server = ThreadingHTTPServer(("127.0.0.1", port), ModelHandler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        servers.append(server)
    oauth = ThreadingHTTPServer(("127.0.0.1", 45682), OAuthHandler)
    threading.Thread(target=oauth.serve_forever, daemon=True).start()
    servers.append(oauth)
    print("fixtures ready", flush=True)
    while True:
        time.sleep(60)


if __name__ == "__main__":
    main()
