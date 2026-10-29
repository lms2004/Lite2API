#!/usr/bin/env python3
"""Exercise the real installer/build/gateway in a private chroot, never host systemd.

The systemctl shim only adapts process management. Real Go binaries, HTTP calls,
managed keys and the committed release acceptance program decide success.
Requires root and permission for local listeners/chroot. Prints summaries only.
"""

import argparse
import http.server
import json
import os
from pathlib import Path
import secrets
import shlex
import shutil
import signal
import socket
import stat
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def write(path, value, mode=0o600):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(value.encode() if isinstance(value, str) else value)
    path.chmod(mode)


def encoded(value):
    return json.dumps(value, sort_keys=True, indent=2) + "\n"


def checked_root():
    root = Path(os.environ["LITE2API_TEST_ROOT"])
    require(root == root.resolve() and root.name == "root" and root.parent.parent == Path("/tmp")
            and root.parent.name.startswith("lite2api-upgrade-lifecycle-"), "unsafe fixture root")
    require((root / ".fixture-owner").read_text() == os.environ["LITE2API_TEST_OWNER"], "fixture ownership mismatch")
    return root


def process_identity(pid):
    try:
        tail = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()
        if tail[0] == "Z":
            return None
        return {"start": tail[19], "root": str(Path(f"/proc/{pid}/root").resolve())}
    except (OSError, IndexError):
        return None


def owns_process(root, state):
    pid = state.get("pid")
    identity = process_identity(pid) if pid else None
    return bool(identity and identity["root"] == str(root) and identity["start"] == state.get("start"))


def stop_owned(root, state):
    if owns_process(root, state):
        pid = state["pid"]
        require(os.getpgid(pid) == pid, "fixture process lost its private process group")
        os.killpg(pid, signal.SIGTERM)
        deadline = time.monotonic() + 6
        while owns_process(root, state) and time.monotonic() < deadline:
            time.sleep(0.05)
        if owns_process(root, state):
            os.killpg(pid, signal.SIGKILL)
            deadline = time.monotonic() + 2
            while owns_process(root, state) and time.monotonic() < deadline:
                time.sleep(0.05)
        require(not owns_process(root, state), "fixture service did not stop")
    state.pop("pid", None)
    state.pop("start", None)


def parse_environment(path):
    result = {}
    for line in path.read_text().splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        parts = shlex.split(line, comments=True)
        require(len(parts) == 1 and "=" in parts[0], "invalid fixture environment")
        name, value = parts[0].split("=", 1)
        result[name] = value
    return result


def clean_environment():
    return {key: value for key, value in os.environ.items()
            if not key.startswith(("LITE2API_", "CLIPROXYAPI_", "ATOMCODE2API_", "DEEPSEEK_", "GROK2API_", "GEMINI_WEB2API_"))}


def fake_systemctl(arguments):
    """No business responses are generated here, only owned chroot processes."""
    root = checked_root()
    state_path = root / ".fixture/service.json"
    state = json.loads(state_path.read_text()) if state_path.exists() else {"enabled": False, "events": []}
    args = [value for value in arguments if value != "--quiet"]
    require(args and (len(args) == 1 or args[1] == "lite2api.service"), "unexpected fixture service")
    action = args[0]
    if action == "is-active":
        return 0 if owns_process(root, state) else 3
    if action == "is-enabled":
        return 0 if state["enabled"] else 1
    if action in ("enable", "disable"):
        state["enabled"] = action == "enable"
    elif action == "daemon-reload":
        pass
    elif action == "stop":
        stop_owned(root, state)
    elif action in ("start", "restart"):
        if action == "restart":
            stop_owned(root, state)
        if not owns_process(root, state):
            unit = (root / "etc/systemd/system/lite2api.service").read_text().splitlines()
            command = shlex.split(next(line.split("=", 1)[1] for line in unit if line.startswith("ExecStart=")))
            for index in (0, 2):
                require(command[index].startswith(str(root) + "/"), "unit command escapes fixture root")
                command[index] = command[index][len(str(root)):]
            require(command[0] == "/usr/local/bin/lite2api" and command[1] == "-config", "unexpected fixture executable")
            env = clean_environment()
            env.update(parse_environment(root / "etc/lite2api/lite2api.env"))
            env["GOMAXPROCS"], env["GOMEMLIMIT"] = "2", "128MiB"
            log_path = root / ".fixture/service.log"
            log_path.parent.mkdir(parents=True, exist_ok=True)
            with log_path.open("ab") as log:
                process = subprocess.Popen([shutil.which("chroot"), str(root), *command], env=env,
                                           stdin=subprocess.DEVNULL, stdout=log, stderr=log, start_new_session=True)
            deadline = time.monotonic() + 5
            identity = None
            while time.monotonic() < deadline:
                identity = process_identity(process.pid)
                if identity and identity["root"] == str(root):
                    break
                if process.poll() is not None:
                    break
                time.sleep(0.02)
            if not identity or identity["root"] != str(root):
                if process.poll() is None:
                    process.kill()
                    process.wait(timeout=2)
                raise RuntimeError("fixture executable did not enter its private root")
            state.update(pid=process.pid, start=identity["start"])
    else:
        raise RuntimeError("unsupported fixture systemctl action")
    state["events"].append(action)
    write(state_path, encoded(state))
    return 0


class FixtureUpstream(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    calls = 0
    calls_lock = threading.Lock()

    def log_message(self, *_args):
        pass

    def respond(self, body, content_type="application/json"):
        body = body.encode()
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        self.respond('{"data":[{"id":"fixture-upstream"}]}')

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
        require(body["model"] == "fixture-upstream", "gateway failed the fixture model mapping")
        with self.calls_lock:
            type(self).calls += 1
        if body.get("stream"):
            self.respond('data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n', "text/event-stream")
        else:
            self.respond('{"id":"fixture-result","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}')


BAD_MAIN = r'''package main
import("encoding/json";"flag";"net/http";"os";"path/filepath")
var version="fixture-bad"
func main(){
 configPath:=flag.String("config","/etc/lite2api/config.json","");check:=flag.Bool("check-config",false,"");show:=flag.Bool("version",false,"");flag.Parse()
 if *check{return};if *show{println(version);return}
 var cfg struct{Server struct{Listen string `json:"listen"`; ClientKeysPath string `json:"client_keys_path"`} `json:"server"`}
 data,_:=os.ReadFile(*configPath);_ = json.Unmarshal(data,&cfg)
 mutations:=map[string]bool{}
 corrupt:=func(name,path,value string){_ = os.MkdirAll(filepath.Dir(path),0700);err:=os.WriteFile(path,[]byte(value),0600);got,_:=os.ReadFile(path);mutations[name]=err==nil&&string(got)==value}
 corrupt("config",*configPath,"candidate-corrupt-config")
 corrupt("environment","/etc/lite2api/lite2api.env","LITE2API_API_KEYS=candidate-key\nLITE2API_ADMIN_TOKEN=candidate-admin\nFIXTURE_CLIENT_KEY=candidate-managed\n")
 corrupt("absolute_client_keys",cfg.Server.ClientKeysPath,"candidate-corrupt-keys")
 corrupt("state","/var/lib/lite2api/restore-marker.bin","candidate-corrupt-state")
 corrupt("new_state","/var/lib/lite2api/candidate-only.bin","candidate-added-state")
 audit,_:=json.Marshal(mutations);_ = os.WriteFile("/.fixture/candidate-mutations.json",audit,0600)
 handler:=http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  w.Header().Set("Content-Type","application/json")
  if r.URL.Path=="/v1/models"&&(r.Header.Get("Authorization")==""||r.Header.Get("Authorization")=="Bearer candidate-admin"){w.WriteHeader(401);_,_=w.Write([]byte(`{"error":{"code":"invalid_key"}}`));return}
  w.Header().Set("X-Lite2API-Request-Id","candidate-incomplete")
  _,_=w.Write([]byte(`{"ok":true}`))
 });_ = http.ListenAndServe(cfg.Server.Listen,handler)
}
'''


class Lifecycle:
    def __init__(self, source, directory):
        self.source, self.directory = source, directory
        self.repo, self.root = directory / "repo", directory / "root"
        self.root.mkdir(mode=0o700)
        self.owner = secrets.token_hex(24)
        write(self.root / ".fixture-owner", self.owner)
        self.env = clean_environment()
        self.env.update(LITE2API_TEST_ROOT=str(self.root), LITE2API_TEST_OWNER=self.owner,
                        LITE2API_INSTALL_ROOT=str(self.root), GOTOOLCHAIN="local", CGO_ENABLED="0")
        self.results = []
        self.go = os.environ.get("GO_BIN") or shutil.which("go")
        require(self.go, "Go compiler is required")
        self.env["GO_BIN"] = self.go
        self.port = self.free_port()
        self.url = f"http://127.0.0.1:{self.port}"
        self.env["LITE2API_ACCEPTANCE_URL"] = self.url
        self.upstream = http.server.ThreadingHTTPServer(("127.0.0.1", 0), FixtureUpstream)
        self.upstream.daemon_threads = True
        self.upstream_thread = threading.Thread(target=self.upstream.serve_forever, daemon=True)
        self.upstream_thread.start()
        self.config = self.root / "etc/lite2api/config.json"
        self.environment = self.root / "etc/lite2api/lite2api.env"
        self.binary = self.root / "usr/local/bin/lite2api"
        self.unit = self.root / "etc/systemd/system/lite2api.service"
        self.keys = self.root / "custom-state/client_keys.json"
        self.marker = self.root / "var/lib/lite2api/restore-marker.bin"
        self.profile = self.root / "etc/lite2api/acceptance.json"

    @staticmethod
    def free_port():
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            return listener.getsockname()[1]

    def run(self, args, *, timeout=180, expect=None, cwd=None):
        process = subprocess.Popen([str(value) for value in args], env=self.env, cwd=cwd,
                                   stdout=subprocess.PIPE, stderr=subprocess.STDOUT, start_new_session=True)
        try:
            output = process.communicate(timeout=timeout)[0].decode(errors="replace")
        except BaseException:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.communicate(timeout=5)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.communicate(timeout=5)
            raise
        if expect is not None:
            require((process.returncode == 0) == expect,
                    f"{Path(str(args[0])).name} returned unexpected status {process.returncode}: {output[-1500:]}")
        return process.returncode, output

    def ctl(self, action, expect=True):
        return self.run(["systemctl", action, "lite2api.service"], timeout=15, expect=expect)

    def state(self):
        path = self.root / ".fixture/service.json"
        return json.loads(path.read_text()) if path.exists() else {"enabled": False}

    def http(self, path, payload=None, key=None):
        headers = {"Content-Type": "application/json"}
        if key:
            headers["Authorization"] = "Bearer " + key
        data = json.dumps(payload).encode() if payload is not None else None
        request = urllib.request.Request(self.url + path, data=data, headers=headers)
        try:
            response = urllib.request.urlopen(request, timeout=5)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            body = response.read((4 << 20) + 1)
            require(len(body) <= 4 << 20, "fixture response exceeded its bound")
            return response.status, response.headers, json.loads(body)

    def wait_live(self):
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            try:
                if self.http("/livez")[0] == 200:
                    return
            except (OSError, ValueError):
                pass
            time.sleep(0.05)
        raise RuntimeError("fixture gateway did not become live")

    def seed(self):
        self.repo.mkdir()
        for name in ("cmd", "internal"):
            shutil.copytree(self.source / name, self.repo / name)
        for name in ("go.mod", "config.example.json"):
            shutil.copy2(self.source / name, self.repo / name)
        (self.repo / "deploy").mkdir()
        for name in ("install-lite2api-systemd.sh", "lite2api.service", "verify_service.py"):
            shutil.copy2(self.source / "deploy" / name, self.repo / "deploy" / name)
        self.run(["git", "init", "-q", self.repo], expect=True)
        self.run(["git", "config", "user.email", "fixture@example.invalid"], cwd=self.repo, expect=True)
        self.run(["git", "config", "user.name", "Isolated Lifecycle Fixture"], cwd=self.repo, expect=True)
        self.commit("good candidate")
        fakebin = self.directory / "bin"
        fakebin.mkdir()
        wrapper = "#!/bin/sh\nexec " + shlex.quote(sys.executable) + " " + shlex.quote(str(Path(__file__).resolve())) + " --fake-systemctl \"$@\"\n"
        write(fakebin / "systemctl", wrapper, 0o700)
        self.env["PATH"] = str(fakebin) + os.pathsep + self.env["PATH"]
        self.binary.parent.mkdir(parents=True)
        self.run([self.go, "build", "-buildvcs=false", "-trimpath", "-ldflags=-X main.version=fixture-original", "-o", self.binary, "./cmd/lite2api"], cwd=self.repo, expect=True)
        upstream_port = self.upstream.server_address[1]
        config = {
            "version": 1,
            "server": {"listen": f"127.0.0.1:{self.port}", "client_keys_path": "/custom-state/client_keys.json",
                       "request_log_path": "/var/lib/lite2api/request.log", "queue_timeout": "1s",
                       "response_header_timeout": "3s", "stream_idle_timeout": "3s", "max_failover_attempts": 1},
            "accounts": [{"id": "fixture-source", "type": "openai", "adapter_id": "generic-openai",
                          "base_url": f"http://127.0.0.1:{upstream_port}/v1", "auth_header": "none", "enabled": True,
                          "concurrency": 2, "models": ["fixture-upstream"], "operations": ["openai.chat"],
                          "capabilities": [{"model": "fixture-upstream", "upstream_model": "fixture-upstream", "reasoning_efforts": ["auto"],
                                            "evidence": {"status": "declared", "source": "model_catalog", "observed_at": "2026-10-28T00:00:00Z"}}]}],
            "routes": {name: {"targets": [{"account": "fixture-source", "model": "fixture-upstream"}]} for name in ("allowed", "blocked")},
        }
        write(self.config, encoded(config))
        write(self.environment, "LITE2API_API_KEYS=fixture-legacy-key\nLITE2API_ADMIN_TOKEN=fixture-admin-key\nLITE2API_ADMIN_AUTO_LOGIN=false\n", 0o640)
        write(self.marker, b"original persistent state\n")
        self.keys.parent.mkdir(parents=True)
        unit = (self.source / "deploy/lite2api.service").read_text()
        unit = unit.replace("/etc/lite2api", str(self.root / "etc/lite2api"))
        unit = unit.replace("/usr/local/bin/lite2api", str(self.binary))
        unit = unit.replace("User=lite2api", "User=root").replace("Group=lite2api", "Group=root")
        write(self.unit, "# original fixture unit\n" + unit, 0o644)
        self.ctl("enable")
        self.ctl("start")
        self.wait_live()
        status, _, response = self.http("/admin/api/client-keys", {"name": "existing-tool", "models": ["allowed"], "rpm": 200,
                                                                "concurrency": 2, "expires_at": "2099-01-01T00:00:00Z"}, "fixture-admin-key")
        require(status == 201 and response.get("secret", "").startswith("sk-l2a-"), "real managed key was not issued")
        self.managed_key = response["secret"]
        with self.environment.open("a") as output:
            output.write("FIXTURE_CLIENT_KEY=" + self.managed_key + "\n")
        self.assert_client_boundary()

    def commit(self, message):
        self.run(["git", "add", "."], cwd=self.repo, expect=True)
        self.run(["git", "commit", "-qm", message], cwd=self.repo, expect=True)

    def assert_client_boundary(self):
        status, headers, response = self.http("/v1/chat/completions", {"model": "allowed", "messages": [{"role": "user", "content": "fixture call"}]}, self.managed_key)
        require(status == 200 and response.get("choices", [{}])[0].get("finish_reason") == "stop", "existing managed key did not complete a real call")
        logical_id = headers.get("X-Lite2API-Request-Id")
        require(logical_id, "real gateway did not expose its logical request ID")
        _, _, state = self.http("/admin/api/state", key="fixture-admin-key")
        observed = next((row for row in state["stats"]["recent"] if row["request_id"] == logical_id), None)
        require(observed and observed["account_id"] == "fixture-source" and observed["outcome"] == "success", "completed call lost its source association")
        before = FixtureUpstream.calls
        status, _, _ = self.http("/v1/chat/completions", {"model": "blocked", "messages": [{"role": "user", "content": "forbidden"}]}, self.managed_key)
        require(status == 403 and FixtureUpstream.calls == before, "managed key whitelist did not reject before upstream submission")
        require(self.http("/v1/models")[0] == 401, "anonymous boundary failed")
        require(self.http("/v1/models", key="fixture-admin-key")[0] == 401, "admin credential became a model credential")

    def snapshot(self):
        result = {}
        for name, path in {"config": self.config, "environment": self.environment, "absolute_client_keys": self.keys,
                           "binary": self.binary, "unit": self.unit, "state_marker": self.marker}.items():
            info = path.stat()
            result[name] = {"body": path.read_bytes(), "mode": stat.S_IMODE(info.st_mode), "uid": info.st_uid, "gid": info.st_gid}
        return result

    def assert_snapshot(self, before):
        for name, path in {"config": self.config, "environment": self.environment, "absolute_client_keys": self.keys,
                           "binary": self.binary, "unit": self.unit, "state_marker": self.marker}.items():
            info = path.stat()
            require(path.read_bytes() == before[name]["body"], f"rollback did not restore {name}")
            require((stat.S_IMODE(info.st_mode), info.st_uid, info.st_gid) == (before[name]["mode"], before[name]["uid"], before[name]["gid"]),
                    f"rollback did not restore {name} access metadata")
        require(not (self.root / "var/lib/lite2api/candidate-only.bin").exists(), "rollback retained candidate-only state")

    def installer(self, expect):
        return self.run(["bash", self.repo / "deploy/install-lite2api-systemd.sh"], timeout=180, expect=expect)

    def missing_profile(self):
        before, state = self.snapshot(), self.state()
        _, output = self.installer(False)
        require("require LITE2API_ACCEPTANCE_CASES" in output, "missing acceptance profile was not the rejection reason")
        self.assert_snapshot(before)
        require(self.state().get("pid") == state.get("pid") and self.state()["enabled"], "missing profile changed the running service")
        self.assert_client_boundary()
        self.results.append({"name": "missing_profile_rejected_before_mutation", "ok": True})

    def normal_upgrade(self):
        write(self.profile, encoded({"version": 1, "cases": [
            {"name": "managed_call", "model": "allowed", "operation": "openai.chat", "key_env": "FIXTURE_CLIENT_KEY", "expect_account": "fixture-source"},
            {"name": "managed_stream", "model": "allowed", "operation": "openai.chat", "stream": True, "key_env": "FIXTURE_CLIENT_KEY", "expect_account": "fixture-source"},
            {"name": "model_whitelist", "model": "blocked", "operation": "openai.chat", "key_env": "FIXTURE_CLIENT_KEY", "expect_status": 403},
        ]}))
        before = self.snapshot()
        self.installer(True)
        require(self.binary.read_bytes() != before["binary"]["body"], "normal upgrade did not install the committed release binary")
        for name, path in {"config": self.config, "environment": self.environment, "absolute_client_keys": self.keys, "state_marker": self.marker}.items():
            require(path.read_bytes() == before[name]["body"], f"normal upgrade changed existing {name}")
        acceptance = sorted((self.root / "var/lib/lite2api-rollbacks").glob("*/release-acceptance.json"))
        result = json.loads(acceptance[-1].read_text())
        require(result["ok"] and all(row.get("source_confirmed") for row in result["cases"] if row["name"] in ("managed_call", "managed_stream")), "normal upgrade did not prove complete business calls on its declared source")
        self.assert_client_boundary()
        self.results.append({"name": "committed_upgrade_passed_real_business_and_access_acceptance", "ok": True})

    def bad_candidate(self):
        write(self.repo / "cmd/lite2api/main.go", BAD_MAIN)
        with (self.repo / "deploy/lite2api.service").open("a") as output:
            output.write("\n# candidate unit must be rolled back\n")
        self.commit("live but incomplete destructive candidate")

    def rollback(self, active):
        if not active:
            self.ctl("stop")
            self.ctl("disable")
        before = self.snapshot()
        old_logs = (self.root / "var/lib/lite2api/request.log").read_bytes()
        previous_snapshots = set((self.root / "var/lib/lite2api-rollbacks").iterdir())
        self.installer(False)
        mutations = json.loads((self.root / ".fixture/candidate-mutations.json").read_text())
        require(len(mutations) == 5 and all(mutations.values()), "bad candidate did not exercise all intended state mutations")
        self.assert_snapshot(before)
        state = self.state()
        require(state["enabled"] == active and owns_process(self.root, state) == active, "rollback did not restore the original active/enabled state")
        snapshots = set((self.root / "var/lib/lite2api-rollbacks").iterdir()) - previous_snapshots
        require(len(snapshots) == 1, "rollback snapshot was not retained")
        snapshot = snapshots.pop()
        release = json.loads((snapshot / "release-acceptance.json").read_text())
        require(not release["ok"] and any(row.get("status") == 200 and row.get("complete") is False for row in release["cases"]), "candidate was not rejected for its incomplete HTTP 200 business result")
        if active:
            require(json.loads((snapshot / "rollback-acceptance.json").read_text())["ok"], "restored release did not pass real rollback acceptance")
            require((self.root / "var/lib/lite2api/request.log").read_bytes().startswith(old_logs), "restoration lost the pre-upgrade request history")
        else:
            require(not (snapshot / "rollback-acceptance.json").exists(), "inactive rollback unexpectedly started the service")
            self.ctl("start")
            self.wait_live()
        self.assert_client_boundary()
        if not active:
            self.ctl("stop")
            require(not self.state()["enabled"] and not owns_process(self.root, self.state()), "post-check changed original inactive state")
        self.results.append({"name": "rollback_restored_" + ("active_enabled" if active else "inactive_disabled"), "ok": True,
                             "restored": sorted(before), "managed_key_call_and_whitelist": True,
                             "incomplete_200_rejected": True, "candidate_mutations_confirmed": True})

    def close(self):
        try:
            if "PATH" in self.env and str(self.directory / "bin") in self.env["PATH"]:
                self.ctl("stop")
        finally:
            # Also recover a child started immediately before its systemctl
            # adapter was interrupted while writing the PID record. Only the
            # unique private root and that PID's start time authorize a signal.
            try:
                for entry in Path("/proc").iterdir():
                    if not entry.name.isdigit():
                        continue
                    identity = process_identity(int(entry.name))
                    if identity and identity["root"] == str(self.root):
                        stop_owned(self.root, {"pid": int(entry.name), "start": identity["start"]})
            finally:
                self.upstream.shutdown()
                self.upstream.server_close()
                self.upstream_thread.join(timeout=3)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    require(os.geteuid() == 0, "the isolated chroot lifecycle test requires root")

    def cancelled(_signum, _frame):
        raise InterruptedError("isolated lifecycle test was cancelled")

    for signum in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(signum, cancelled)
    report = {"ok": False, "scope": "private_git_commit_private_chroot_real_go_real_http", "host_systemd_used": False, "cases": []}
    started = time.monotonic()
    try:
        with tempfile.TemporaryDirectory(prefix="lite2api-upgrade-lifecycle-", dir="/tmp") as path:
            fixture = Lifecycle(args.source.resolve(), Path(path))
            try:
                fixture.seed()
                fixture.missing_profile()
                fixture.normal_upgrade()
                fixture.bad_candidate()
                fixture.rollback(active=True)
                fixture.rollback(active=False)
                report["cases"] = fixture.results
                report["ok"] = True
            finally:
                report["cases"] = fixture.results
                fixture.close()
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        report["error"] = str(error)
    report["elapsed_seconds"] = round(time.monotonic() - started, 2)
    result = encoded(report)
    if args.output:
        write(args.output, result)
    print(result, end="")
    return 0 if report["ok"] else 1


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--fake-systemctl":
        raise SystemExit(fake_systemctl(sys.argv[2:]))
    raise SystemExit(main())
