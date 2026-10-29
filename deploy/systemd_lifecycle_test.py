#!/usr/bin/env python3
"""Opt-in actual systemd lifecycle drill with private state and a fixture upstream.

Uses the checked-in service hardening properties, a unique transient unit and
only /var/lib/lite2api-lifecycle-* state. Never controls the production unit. This
proves local service/process behavior, not an external provider's availability.
"""
import argparse
from contextlib import ExitStack
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import pwd
import secrets
import shutil
import signal
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request

REPO = Path(__file__).resolve().parents[1]


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def command(args, timeout=30, check=True):
    result = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
    if check:
        # Commands here contain paths and service properties, never credential
        # values. Retain bounded systemd diagnostics when setup is rejected.
        require(result.returncode == 0, f"{Path(args[0]).name} failed ({result.returncode}): {result.stderr[-800:].strip()}")
    return result


def wait_for(predicate, timeout=10):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            if predicate():
                return
        except (OSError, ValueError):
            pass
        time.sleep(0.05)
    raise RuntimeError("bounded lifecycle condition did not complete")


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def run(binary):
    with ExitStack() as cleanup:
        return run_owned(binary, cleanup)


def run_owned(binary, cleanup):
    require(os.geteuid() == 0, "requires root for an isolated transient systemd unit")
    require(binary.is_file(), "provide the built binary with --binary")
    identity = pwd.getpwnam("lite2api")
    client_key, admin_key, upstream_key = (secrets.token_hex(32) for _ in range(3))
    holding, release = threading.Event(), threading.Event()

    class Upstream(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_GET(self):
            body = b'{"data":[{"id":"lifecycle-fixture"}]}'
            self.respond(body)

        def do_POST(self):
            require(self.headers.get("Authorization") == "Bearer " + upstream_key, "private fixture received wrong authentication")
            request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            if request["messages"][0]["content"] == "hold":
                holding.set()
                release.wait(20)
            self.respond(b'{"choices":[{"index":0,"message":{"role":"assistant","content":"complete"},"finish_reason":"stop"}]}')

        def respond(self, body):
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

    upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    cleanup.callback(upstream.server_close)
    upstream.daemon_threads = True
    worker = threading.Thread(target=upstream.serve_forever, daemon=True)
    worker.start()
    cleanup.callback(worker.join, timeout=3)
    cleanup.callback(upstream.shutdown)
    cases, pids = [], []
    port = free_port()
    unit = "lite2api-lifecycle-" + secrets.token_hex(8) + ".service"
    url = f"http://127.0.0.1:{port}"

    def ctl(action, *args, check=True):
        return command(["systemctl", action, *args, unit], check=check)

    def state():
        output = ctl("show", "--property=ActiveState,SubState,MainPID,NRestarts,LoadState", check=False).stdout
        return dict(line.split("=", 1) for line in output.splitlines() if "=" in line)

    def http(path, payload=None, key=None):
        headers = {"Content-Type": "application/json"}
        if key:
            headers["Authorization"] = "Bearer " + key
        request = urllib.request.Request(url + path, data=json.dumps(payload).encode() if payload is not None else None, headers=headers)
        try:
            response = urllib.request.urlopen(request, timeout=20)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            return response.status, json.loads(response.read(1 << 20))

    def call(content="ping"):
        status, response = http("/v1/chat/completions", {"model": "lifecycle-fixture", "messages": [{"role": "user", "content": content}]}, client_key)
        require(status == 200 and response["choices"][0]["finish_reason"] == "stop", "fixture business call did not complete")

    def ready():
        wait_for(lambda: http("/livez")[0] == 200)
        pid = int(state()["MainPID"])
        require(pid > 0, "transient service has no main PID")
        pids.append(pid)
        call()
        return pid

    # /run is commonly noexec and PrivateTmp hides host /tmp from the unit.
    # A unique /var/lib directory works with the production hardening policy.
    with tempfile.TemporaryDirectory(prefix="lite2api-lifecycle-", dir="/var/lib") as temporary:
        root = Path(temporary)
        root.chmod(0o755)
        executable = root / "lite2api"
        shutil.copyfile(binary, executable)
        executable.chmod(0o755)
        state_dir = root / "state"
        state_dir.mkdir(mode=0o1770)
        state_dir.chmod(0o1770)
        os.chown(state_dir, 0, identity.pw_gid)
        configuration = state_dir / "config.json"
        config = {"version": 1, "server": {
            "listen": f"127.0.0.1:{port}", "api_key_env": "LIFECYCLE_CLIENT_KEY",
            "admin_token_env": "LIFECYCLE_ADMIN_KEY", "allow_private_http_upstream": True,
            "client_keys_path": "client_keys.json", "request_log_path": "request.log",
            "admin_allowed_cidrs": ["127.0.0.0/8"], "response_header_timeout": "30s",
            "stream_idle_timeout": "30s"}, "accounts": [{
            "id": "lifecycle-fixture", "type": "openai", "enabled": True, "weight": 1,
            "api_key_env": "LIFECYCLE_UPSTREAM_KEY",
            "base_url": f"http://127.0.0.1:{upstream.server_port}/v1", "models": ["lifecycle-fixture"],
            "operations": ["openai.chat"], "concurrency": 1}], "routes": {}}

        def save_config(data):
            temporary = configuration.with_suffix(".next")
            temporary.write_text(data)
            temporary.chmod(0o600)
            os.chown(temporary, identity.pw_uid, identity.pw_gid)
            temporary.replace(configuration)

        save_config(json.dumps(config))
        environment = root / "environment"
        environment.write_text(f"LIFECYCLE_CLIENT_KEY={client_key}\nLIFECYCLE_ADMIN_KEY={admin_key}\nLIFECYCLE_UPSTREAM_KEY={upstream_key}\n")
        environment.chmod(0o600)
        properties = []
        service_section = False
        for line in (REPO / "deploy/lite2api.service").read_text().splitlines():
            if line.startswith("["):
                service_section = line == "[Service]"
                continue
            if not service_section or not line.strip() or line.startswith("#"):
                continue
            key, value = line.split("=", 1)
            if key == "ExecStart":
                continue
            if key == "EnvironmentFile":
                value = str(environment)
            if key == "ReadWritePaths":
                value = str(state_dir)
            properties.append("--property=" + key + "=" + value)
        launched = False
        try:
            launched = True
            command(["systemd-run", "--quiet", "--collect", "--unit=" + unit,
                     *properties, str(executable), "-config", str(configuration)])
            first = ready()
            require(http("/v1/models")[0] == 401 and http("/v1/models", key=admin_key)[0] == 401, "access separation failed")
            cases.append({"name": "start_and_completed_local_call", "ok": True})

            # Failed reload must preserve the currently serving configuration.
            save_config("{")
            ctl("kill", "--kill-whom=main", "--signal=HUP")
            time.sleep(0.2)
            require(int(state()["MainPID"]) == first, "invalid reload replaced the process")
            call()
            config["routes"]["lifecycle-reloaded-alias"] = {
                "upstream_model": "lifecycle-fixture", "accounts": ["lifecycle-fixture"]}
            save_config(json.dumps(config))
            ctl("kill", "--kill-whom=main", "--signal=HUP")
            wait_for(lambda: any(row["id"] == "lifecycle-reloaded-alias" for row in http("/v1/models", key=client_key)[1]["data"]))
            require(int(state()["MainPID"]) == first, "successful reload replaced the process")
            call()
            cases.append({"name": "invalid_and_valid_hup_preserve_service", "ok": True})

            ctl("restart")
            second = ready()
            require(second != first and not Path(f"/proc/{first}").exists(), "restart did not release the previous process")
            cases.append({"name": "restart_and_completed_local_call", "ok": True})

            ctl("kill", "--kill-whom=main", "--signal=KILL")
            wait_for(lambda: state().get("ActiveState") == "active" and int(state().get("MainPID", "0")) not in (0, second), 15)
            third = ready()
            require(int(state()["NRestarts"]) >= 1 and not Path(f"/proc/{second}").exists(), "failure was not automatically recovered")
            cases.append({"name": "sigkill_restart_on_failure_and_completed_local_call", "ok": True})

            pending_errors = []
            def pending_call():
                try:
                    call("hold")
                except BaseException as error:
                    pending_errors.append(type(error).__name__)
            pending = threading.Thread(target=pending_call)
            pending.start()
            require(holding.wait(3), "held request did not reach the private fixture")
            stopper = subprocess.Popen(["systemctl", "stop", unit], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            try:
                wait_for(lambda: state().get("ActiveState") == "deactivating", 5)
                release.set()
                stopper.communicate(timeout=20)
            finally:
                release.set()
                if stopper.poll() is None:
                    stopper.kill()
                    stopper.communicate(timeout=3)
            pending.join(timeout=5)
            require(stopper.returncode == 0 and not pending.is_alive() and not pending_errors, "graceful stop failed to deliver its active request")
            require(not Path(f"/proc/{third}").exists(), "stop retained its main process")
            with socket.socket() as listener:
                # Match Go's listener behavior; accepted connections may still
                # be in TIME_WAIT after the listening socket is gone.
                listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                listener.bind(("127.0.0.1", port))
                listener.listen(1)
            rows = [json.loads(line) for line in (state_dir / "request.log").read_text().splitlines()]
            require(rows and rows[-1].get("outcome") == "success", "stop did not flush the completed observation")
            cases.append({"name": "graceful_stop_delivers_active_call_flushes_log_releases_pid_and_port", "ok": True})
        except Exception as error:
            journal = command(["journalctl", "--no-pager", "--output=cat", "-n", "8", "--unit=" + unit], check=False).stdout[-3000:]
            for secret in (client_key, admin_key, upstream_key):
                journal = journal.replace(secret, "<redacted>")
            raise RuntimeError(str(error) + "; private_unit_diagnostic=" + journal) from error
        finally:
            release.set()
            if launched:
                ctl("stop", check=False)
                ctl("reset-failed", check=False)
            require(all(not Path(f"/proc/{pid}").exists() for pid in pids), "fixture process cleanup was incomplete")
    return {"ok": True, "scope": "real_systemd_private_state_fixture_upstream",
            "production_service_changed": False, "external_provider_calls": 0,
            "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
            "unit_template_sha256": hashlib.sha256((REPO / "deploy/lite2api.service").read_bytes()).hexdigest(),
            "private_unit_removed": state().get("LoadState") == "not-found", "cases": cases}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    def interrupted(*_args):
        raise InterruptedError("lifecycle drill interrupted")
    for signum in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        signal.signal(signum, interrupted)
    try:
        result = run(args.binary.resolve())
    except Exception as error:
        result = {"ok": False, "scope": "real_systemd_private_state_fixture_upstream", "error": str(error)}
    args.output.write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result, indent=2))
    return 0 if result["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
