#!/usr/bin/env python3
"""Real age -> restore -> gateway acceptance, using only private synthetic state.

Run as root with age/age-keygen on PATH. A static test binary is built locally
unless LITE2API_TEST_BINARY names one. chroot preserves the restored config's
original absolute paths; it never reads or writes the host's service state.
"""
import atexit
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import io
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import socket
import subprocess
import tarfile
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request

REPO = Path(__file__).resolve().parents[1]
ARCHIVE = REPO / "deploy/state_archive.py"
BACKUP = REPO / "deploy/server-ops/backup-configs.sh"
VERIFY = REPO / "deploy/server-ops/verify-systemd-backup.sh"
GATE = REPO / "deploy/verify_service.py"
KEY_PATH = Path("srv/lite2api-private/custom-client-keys.json")
GRANT_PATH = Path("var/lib/cliproxyapi/auths/synthetic-grant.json")
CHILDREN = set()


def stop_process(process):
    if process.poll() is None:
        try:
            os.killpg(process.pid, signal.SIGTERM)
            process.wait(timeout=5)
        except (ProcessLookupError, subprocess.TimeoutExpired):
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait(timeout=5)
    CHILDREN.discard(process)


def cleanup_children():
    for process in tuple(CHILDREN):
        stop_process(process)


atexit.register(cleanup_children)


def command(args, *, env, data=None, timeout=30, success=True, cwd=REPO):
    process = subprocess.Popen([str(a) for a in args], stdin=subprocess.PIPE,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                               env=env, cwd=cwd, start_new_session=True)
    CHILDREN.add(process)
    try:
        stdout, stderr = process.communicate(data, timeout=timeout)
    except BaseException:
        stop_process(process)
        raise
    finally:
        if process.poll() is not None:
            CHILDREN.discard(process)
    if success is not None and (process.returncode == 0) != success:
        # No environment, decrypted material or response bodies enter failures.
        raise AssertionError(f"{Path(str(args[0])).name} returned unexpected status {process.returncode}; "
                             f"diagnostic={stderr.decode(errors='replace')[-300:]}")
    return stdout


def write_private(path, data):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.write_bytes(data if isinstance(data, bytes) else data.encode())
    path.chmod(0o600)


def encoded(value):
    return json.dumps(value, sort_keys=True).encode()


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


class RestoreLifecycle(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if os.geteuid() != 0 or not shutil.which("chroot"):
            raise unittest.SkipTest("restore runtime drills need root and chroot")
        if not shutil.which("age") or not shutil.which("age-keygen"):
            raise unittest.SkipTest("real age and age-keygen are required; no crypto stub is used")
        cls.workspace = tempfile.TemporaryDirectory(prefix="lite2api-restore-lifecycle-")
        cls.addClassCleanup(cls.workspace.cleanup)
        cls.directory = Path(cls.workspace.name)
        cls.env = {"PATH": os.environ["PATH"], "HOME": str(cls.directory),
                   "GOTOOLCHAIN": "local", "CGO_ENABLED": "0",
                   "NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost"}
        cls.identity = cls.directory / "identity.txt"
        command(["age-keygen", "-o", cls.identity], env=cls.env)
        cls.recipient = command(["age-keygen", "-y", cls.identity], env=cls.env).decode().strip()
        cls.wrong_identity = cls.directory / "wrong-identity.txt"
        command(["age-keygen", "-o", cls.wrong_identity], env=cls.env)
        binary = os.environ.get("LITE2API_TEST_BINARY")
        if binary:
            cls.binary = Path(binary).resolve()
        else:
            cls.binary = cls.directory / "lite2api"
            build_env = {name: os.environ[name] for name in
                         ("PATH", "HOME", "GOROOT", "GOPATH", "GOCACHE", "GOMODCACHE", "TMPDIR")
                         if name in os.environ}
            build_env.update(GOTOOLCHAIN="local", CGO_ENABLED="0")
            command(["go", "build", "-o", cls.binary, "./cmd/lite2api"],
                    env=build_env, timeout=120)
        command([cls.binary, "-version"], env=cls.env)

    def setUp(self):
        self.case_directory = tempfile.TemporaryDirectory(dir=self.directory, prefix="case-")
        self.addCleanup(self.case_directory.cleanup)
        self.case = Path(self.case_directory.name)
        self.source = self.case / "source"
        self.source.mkdir(mode=0o700)
        self.env = dict(type(self).env)
        self.env["LITE2API_BACKUP_ROOT"] = str(self.source)
        self.env["AGE_IDENTITY_FILE"] = str(self.identity)
        self.upstream_secret = secrets.token_hex(32)
        self.legacy_secret = secrets.token_hex(32)
        self.admin_secret = secrets.token_hex(32)
        self.keys, records = {}, []
        now = datetime.now(timezone.utc)
        for kind in ("active", "revoked", "expired", "disabled"):
            key_id = secrets.token_hex(8)
            secret = f"sk-l2a-{key_id}.{secrets.token_urlsafe(36)}"
            self.keys[kind] = secret
            if kind == "revoked":  # Delete persists revocation as an absent ID.
                self.revoked_id = key_id
                continue
            records.append({"id": key_id, "name": kind,
                            "prefix": f"sk-l2a-{key_id}...",
                            "secret_hash": hashlib.sha256(secret.encode()).hexdigest(),
                            "enabled": kind != "disabled", "models": ["allowed"],
                            "rpm": 1000, "concurrency": 2,
                            "created_at": (now-timedelta(days=2)).isoformat(),
                            "expires_at": ((now-timedelta(days=1)) if kind == "expired"
                                           else now+timedelta(days=1)).isoformat()})
        write_private(self.source / KEY_PATH, encoded({"version": 1, "keys": records}))
        # The archive must store independent regular files even for hardlinked
        # source files; restoring one name cannot depend on tar link semantics.
        (self.source / "etc/lite2api").mkdir(parents=True, mode=0o700)
        os.link(self.source / KEY_PATH, self.source / "etc/lite2api/keys-hardlink-copy.json")
        self.grant_root = self.source
        self.calls = 0
        self.calls_lock = threading.Lock()
        fixture = self

        class Adapter(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_POST(self):
                length = int(self.headers.get("Content-Length", "0"))
                if not 0 < length <= 1 << 20:
                    self.send_error(400)
                    return
                self.rfile.read(length)
                with fixture.calls_lock:
                    fixture.calls += 1
                grant = json.loads((fixture.grant_root / GRANT_PATH).read_text())
                accepted = (self.headers.get("Authorization") == "Bearer " + fixture.upstream_secret
                            and grant["state"] == "active"
                            and datetime.fromisoformat(grant["expires_at"]) > datetime.now(timezone.utc))
                result = ({"id": "restored-fixture", "object": "chat.completion",
                           "choices": [{"index": 0, "message": {"role": "assistant",
                                        "content": "restore-complete"}, "finish_reason": "stop"}]}
                          if accepted else {"error": {"type": "authentication_error",
                                                     "message": "synthetic grant requires reauthorization"}})
                body = encoded(result)
                self.send_response(200 if accepted else 401)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

        self.adapter = ThreadingHTTPServer(("127.0.0.1", 0), Adapter)
        self.adapter.daemon_threads = True
        self.adapter_thread = threading.Thread(target=self.adapter.serve_forever,
                                                kwargs={"poll_interval": 0.05}, daemon=True)
        self.adapter_thread.start()
        self.addCleanup(self.close_adapter)
        self.port = free_port()
        config = {"version": 1, "server": {
                    "listen": f"127.0.0.1:{self.port}", "client_keys_path": "/" + str(KEY_PATH),
                    "api_key_env": "LITE2API_API_KEYS", "admin_token_env": "LITE2API_ADMIN_TOKEN",
                    "allow_private_http_upstream": True, "queue_timeout": "25ms",
                    "response_header_timeout": "2s", "stream_idle_timeout": "2s",
                    "max_failover_attempts": 1, "failure_threshold": 1, "circuit_cooldown": "100ms"},
                  "accounts": [{"id": "restored-source", "type": "openai",
                    "base_url": f"http://127.0.0.1:{self.adapter.server_port}/v1",
                    "api_key_env": "RESTORE_UPSTREAM_KEY", "models": ["allowed", "blocked"],
                    "operations": ["openai.chat"], "enabled": True, "weight": 1,
                    "concurrency": 2}], "routes": {}}
        write_private(self.source / "etc/lite2api/config.json", encoded(config))
        write_private(self.source / "etc/lite2api/lite2api.env",
                      f"LITE2API_API_KEYS={self.legacy_secret}\nLITE2API_ADMIN_TOKEN={self.admin_secret}\n"
                      f"RESTORE_UPSTREAM_KEY={self.upstream_secret}\n")
        write_private(self.source / "etc/cliproxyapi/config.yaml", "host: 127.0.0.1\n")
        write_private(self.source / "etc/cliproxyapi/cliproxyapi.env",
                      "SYNTHETIC_ADAPTER_ENV=restore-fixture-only\n")
        self.write_grant("active")

    def close_adapter(self):
        self.adapter.shutdown()
        self.adapter.server_close()
        self.adapter_thread.join(timeout=2)
        self.assertFalse(self.adapter_thread.is_alive(), "fixture HTTP server survived cleanup")

    def write_grant(self, state):
        expiry = datetime.now(timezone.utc) + timedelta(hours=1)
        if state == "expired":
            expiry -= timedelta(days=2)
        write_private(self.source / GRANT_PATH, encoded({"state": state,
                      "access_token": "synthetic-adapter-material", "expires_at": expiry.isoformat()}))

    def backup(self, name="snapshot"):
        snapshot = self.case / name
        command(["bash", BACKUP, snapshot, self.recipient], env=self.env)
        command(["bash", VERIFY, snapshot], env=self.env)
        self.assertTrue((snapshot / "systemd-state.tar.age").read_bytes().startswith(b"age-encryption.org/"))
        return snapshot

    def restore(self, snapshot, name="restored", *, success=True, identity=None):
        output = self.case / name
        command(["python3", ARCHIVE, "restore", "systemd", "--snapshot", snapshot,
                 "--output", output, "--identity", identity or self.identity],
                env=self.env, success=success)
        if success:
            report = json.loads((output / "RESTORE.json").read_text())
            self.assertEqual(report["status"], "material_restored_business_acceptance_required")
        else:
            self.assertFalse(output.exists(), "failed restoration left partial destination state")
        return output

    @contextmanager
    def running_gateway(self, root):
        binary = root / "usr/local/bin/lite2api"
        binary.parent.mkdir(parents=True, mode=0o700)
        shutil.copyfile(self.binary, binary)
        binary.chmod(0o700)
        runtime_env = dict(self.env)
        for line in (root / "etc/lite2api/lite2api.env").read_text().splitlines():
            key, value = line.split("=", 1)
            runtime_env[key] = value
        self.grant_root = root
        log = self.case / (root.name + ".gateway.log")
        with log.open("wb") as sink:
            process = subprocess.Popen([shutil.which("chroot"), str(root),
                       "/usr/local/bin/lite2api", "-config", "/etc/lite2api/config.json"],
                       env=runtime_env, stdout=sink, stderr=sink, start_new_session=True)
            CHILDREN.add(process)
            try:
                deadline = time.monotonic() + 10
                while time.monotonic() < deadline:
                    if process.poll() is not None:
                        self.fail("restored gateway exited before liveness")
                    try:
                        status, _body = self.request("/livez")
                        if status == 200:
                            break
                    except OSError:
                        pass
                    time.sleep(0.02)
                else:
                    self.fail("restored gateway did not become live within its deadline")
                yield runtime_env
            finally:
                stop_process(process)

    def request(self, path, key="", model=None):
        headers = {"Content-Type": "application/json"}
        if key:
            headers["Authorization"] = "Bearer " + key
        body = encoded({"model": model, "messages": [{"role": "user", "content": "fixture"}]}) if model else None
        req = urllib.request.Request(f"http://127.0.0.1:{self.port}" + path, data=body, headers=headers)
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        try:
            response = opener.open(req, timeout=5)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            return response.status, response.read(1 << 20)

    def gate(self, root, runtime_env, *, success, env_file=None):
        cases = [{"name": "restored_managed_call", "model": "allowed", "key_env": "RESTORE_ALLOWED_KEY",
                  "operation": "openai.chat", "expect_account": "restored-source"},
                 {"name": "restored_model_allowlist", "model": "blocked", "key_env": "RESTORE_ALLOWED_KEY", "expect_status": 403}]
        for kind in ("revoked", "expired", "disabled"):
            cases.append({"name": "restored_" + kind + "_denied", "model": "allowed",
                          "key_env": "RESTORE_" + kind.upper() + "_KEY", "expect_status": 401})
        manifest = self.case / (root.name + ".acceptance.json")
        write_private(manifest, encoded({"version": 1, "cases": cases}))
        env = dict(runtime_env)
        for kind, key in self.keys.items():
            env["RESTORE_" + ("ALLOWED" if kind == "active" else kind.upper()) + "_KEY"] = key
        result = json.loads(command(["python3", GATE, "--url", f"http://127.0.0.1:{self.port}",
                           "--cases", manifest, "--env-file", env_file or root / "etc/lite2api/lite2api.env",
                           "--timeout", "15"], env=env, timeout=20, success=success))
        self.assertEqual(result["ok"], success)
        return result

    def encrypted_snapshot(self, plain, name):
        snapshot = self.case / name
        snapshot.mkdir(mode=0o700)
        command(["age", "-r", self.recipient, "-o", snapshot / "systemd-state.tar.age"],
                data=plain, env=self.env)
        write_private(snapshot / "METADATA", "format=lite2api-systemd-state-v2\n")
        self.update_checksum(snapshot)
        return snapshot

    def update_checksum(self, snapshot):
        artifact = snapshot / "systemd-state.tar.age"
        write_private(snapshot / "SHA256SUMS", hashlib.sha256(artifact.read_bytes()).hexdigest()
                      + "  systemd-state.tar.age\n")

    def mutate_archive(self, plain, *, extra=None, replace=None):
        output = io.BytesIO()
        with tarfile.open(fileobj=io.BytesIO(plain), mode="r:") as source:
            with tarfile.open(fileobj=output, mode="w:") as archive:
                for entry in source:
                    body = source.extractfile(entry).read() if entry.isfile() else None
                    if replace and entry.name in replace:
                        body = replace[entry.name]
                        entry.size = len(body)
                    archive.addfile(entry, io.BytesIO(body) if body is not None else None)
                if extra:
                    entry, body = extra
                    archive.addfile(entry, io.BytesIO(body) if body is not None else None)
        return output.getvalue()

    def test_real_crypto_restore_keeps_absolute_keys_and_enforces_business_access(self):
        snapshot = self.backup()
        restored = self.restore(snapshot)
        for path in (Path("etc/lite2api/config.json"), Path("etc/lite2api/lite2api.env"), KEY_PATH,
                     Path("etc/cliproxyapi/config.yaml"), Path("etc/cliproxyapi/cliproxyapi.env"), GRANT_PATH,
                     Path("etc/lite2api/keys-hardlink-copy.json")):
            self.assertEqual((restored / path).read_bytes(), (self.source / path).read_bytes())
            self.assertEqual((restored / path).stat().st_mode & 0o777, 0o600)
        state = json.loads((restored / "lite2api-state-manifest.json").read_text())
        self.assertTrue(state["client_keys_present"])
        self.assertEqual(state["client_keys_path"], str(KEY_PATH))
        records = json.loads((restored / KEY_PATH).read_text())["keys"]
        self.assertNotIn(self.revoked_id, {row["id"] for row in records})
        with self.running_gateway(restored) as runtime_env:
            before = self.calls
            for key, model, expected in ((self.keys["active"], "blocked", 403),
                         (self.keys["revoked"], "allowed", 401), (self.keys["expired"], "allowed", 401),
                         (self.keys["disabled"], "allowed", 401)):
                status, _ = self.request("/v1/chat/completions", key, model)
                self.assertEqual(status, expected)
                self.assertEqual(self.calls, before, "restored access denial reached upstream")
            status, catalog = self.request("/v1/models", self.keys["active"])
            self.assertEqual(status, 200)
            self.assertEqual([model["id"] for model in json.loads(catalog)["data"]], ["allowed"])
            status, body = self.request("/v1/chat/completions", self.keys["active"], "allowed")
            self.assertEqual(status, 200)
            result = json.loads(body)
            self.assertEqual(result["choices"][0]["message"]["content"], "restore-complete")
            self.assertEqual(result["choices"][0]["finish_reason"], "stop")
            self.assertEqual(self.calls, before + 1)
            report = self.gate(restored, runtime_env, success=True)
            self.assertEqual(self.calls, before + 2)
            self.assertTrue(all(case["ok"] for case in report["cases"]))
            allowed = next(row for row in report["cases"] if row["name"] == "restored_managed_call")
            self.assertTrue(allowed["complete"] and allowed["source_confirmed"])

    def test_restored_expired_or_revoked_grant_is_material_only_and_fails_gate(self):
        for state in ("revoked", "expired"):
            with self.subTest(state=state):
                self.write_grant(state)
                snapshot = self.backup("snapshot-" + state)
                restored = self.restore(snapshot, "restored-" + state)
                self.assertEqual((restored / GRANT_PATH).read_bytes(), (self.source / GRANT_PATH).read_bytes())
                with self.running_gateway(restored) as runtime_env:
                    before = self.calls
                    report = self.gate(restored, runtime_env, success=False)
                    self.assertGreater(self.calls, before, "failure gate never tested the restored source")
                    call = next(row for row in report["cases"] if row["name"] == "restored_managed_call")
                    self.assertFalse(call["ok"])
                    self.assertNotEqual(call["status"], 200)

    def test_wrong_identity_and_ciphertext_tamper_fail_without_partial_restore(self):
        snapshot = self.backup()
        wrong_env = dict(self.env, AGE_IDENTITY_FILE=str(self.wrong_identity))
        command(["bash", VERIFY, snapshot], env=wrong_env, success=False)
        self.restore(snapshot, "wrong-identity-output", identity=self.wrong_identity, success=False)
        damaged = self.case / "tampered"
        shutil.copytree(snapshot, damaged)
        path = damaged / "systemd-state.tar.age"
        data = bytearray(path.read_bytes()); data[-17] ^= 0x01; path.write_bytes(data)
        command(["bash", VERIFY, damaged], env=self.env, success=False)
        self.restore(damaged, "bad-checksum-output", success=False)
        self.update_checksum(damaged)  # The actual age authentication must also fail.
        command(["bash", VERIFY, damaged], env=self.env, success=False)
        self.restore(damaged, "bad-aead-output", success=False)

    def test_authenticated_unsafe_archive_members_and_empty_manifest_are_rejected(self):
        snapshot = self.backup()
        plain = command(["age", "--decrypt", "-i", self.identity, snapshot / "systemd-state.tar.age"], env=self.env)
        attacks = []
        for name in ("../escaped", "/absolute-escaped", "RESTORE.json", "etc/lite2api/config.json"):
            info = tarfile.TarInfo(name); info.size = 1; info.mode = 0o600
            attacks.append((name, self.mutate_archive(plain, extra=(info, b"x"))))
        for kind in (tarfile.SYMTYPE, tarfile.LNKTYPE):
            info = tarfile.TarInfo("etc/lite2api/unsafe-link"); info.type = kind; info.linkname = "../outside"
            attacks.append(("link-" + kind.decode(), self.mutate_archive(plain, extra=(info, None))))
        attacks.append(("empty-manifest", self.mutate_archive(plain, replace={"lite2api-state-manifest.json": b"{}"})))
        for index, (label, archive) in enumerate(attacks):
            with self.subTest(attack=label):
                malicious = self.encrypted_snapshot(archive, "attack-" + str(index))
                command(["bash", VERIFY, malicious], env=self.env, success=False)
                self.restore(malicious, "rejected-" + str(index), success=False)
                self.assertFalse((self.case / "escaped").exists())

    def test_existing_destination_and_symlink_ancestors_are_preserved_and_rejected(self):
        snapshot = self.backup()
        existing = self.case / "existing"
        write_private(existing / "sentinel", "untouched")
        command(["python3", ARCHIVE, "restore", "systemd", "--snapshot", snapshot,
                 "--output", existing, "--identity", self.identity], env=self.env, success=False)
        self.assertEqual((existing / "sentinel").read_text(), "untouched")
        destination_link = self.case / "destination-link"
        destination_link.symlink_to(existing, target_is_directory=True)
        command(["python3", ARCHIVE, "restore", "systemd", "--snapshot", snapshot,
                 "--output", destination_link, "--identity", self.identity], env=self.env, success=False)
        parent_link = self.case / "parent-link"
        parent_link.symlink_to(existing, target_is_directory=True)
        command(["python3", ARCHIVE, "restore", "systemd", "--snapshot", snapshot,
                 "--output", parent_link / "new-root", "--identity", self.identity], env=self.env, success=False)
        self.assertEqual(sorted(p.name for p in existing.iterdir()), ["sentinel"])

    def test_source_symlinks_and_checksum_artifact_symlinks_are_rejected(self):
        snapshot = self.backup()
        original = self.source / KEY_PATH
        original.unlink()
        original.symlink_to(self.source / "etc/lite2api/keys-hardlink-copy.json")
        command(["bash", BACKUP, self.case / "unsafe-source", self.recipient], env=self.env, success=False)
        self.assertFalse((self.case / "unsafe-source").exists())
        unsafe = self.case / "unsafe-snapshot"
        shutil.copytree(snapshot, unsafe)
        (unsafe / "systemd-state.tar.age").unlink()
        (unsafe / "systemd-state.tar.age").symlink_to(snapshot / "systemd-state.tar.age")
        command(["bash", VERIFY, unsafe], env=self.env, success=False)
        self.restore(unsafe, "unsafe-snapshot-output", success=False)

    def compose_material(self):
        # This covers actual Compose-format cryptography/material restoration,
        # without claiming a Docker volume/container recovery rehearsal.
        snapshot = self.case / "compose-snapshot"
        snapshot.mkdir(mode=0o700)
        output = io.BytesIO()
        config = json.loads((self.source / "etc/lite2api/config.json").read_text())
        config["server"]["client_keys_path"] = "client_keys.json"
        contents = {"config.json": encoded(config), "client_keys.json": (self.source / KEY_PATH).read_bytes()}
        with tarfile.open(fileobj=output, mode="w:") as archive:
            for name, data in contents.items():
                info = tarfile.TarInfo(name); info.mode = 0o600; info.size = len(data)
                archive.addfile(info, io.BytesIO(data))
        command(["age", "-r", self.recipient, "-o", snapshot / "lite2api-data.tar.age"], data=output.getvalue(), env=self.env)
        environment = (self.source / "etc/lite2api/lite2api.env").read_bytes()
        command(["age", "-r", self.recipient, "-o", snapshot / "environment.age"], data=environment, env=self.env)
        write_private(snapshot / "METADATA", "format=lite2api-compose-state-v2\n")
        hashes = "".join(hashlib.sha256((snapshot / name).read_bytes()).hexdigest() + "  " + name + "\n"
                         for name in ("lite2api-data.tar.age", "environment.age"))
        write_private(snapshot / "SHA256SUMS", hashes)
        command(["bash", REPO / "deploy/verify-compose-snapshot.sh", snapshot], env=self.env)
        restored = self.case / "compose-restored"
        report = json.loads(command(["python3", ARCHIVE, "restore", "compose", "--snapshot", snapshot,
                      "--output", restored, "--identity", self.identity], env=self.env))
        self.assertEqual(report["status"], "material_restored_business_acceptance_required")
        return restored, contents, environment

    def test_compose_real_crypto_material_restore(self):
        restored, contents, environment = self.compose_material()
        self.assertEqual((restored / "environment").read_bytes(), environment)
        for name, data in contents.items():
            self.assertEqual((restored / "lite2api-data" / name).read_bytes(), data)
            self.assertEqual((restored / "lite2api-data" / name).stat().st_mode & 0o777, 0o600)


    def test_compose_restored_material_runs_in_a_private_docker_volume(self):
        if os.environ.get("LITE2API_TEST_DOCKER") != "1":
            self.skipTest("set LITE2API_TEST_DOCKER=1 for the isolated Docker runtime drill")
        if not shutil.which("docker"):
            self.skipTest("Docker is unavailable")
        command(["docker", "info", "--format", "{{.ServerVersion}}"], env=self.env, timeout=10)
        restored, contents, _environment = self.compose_material()
        name = "lite2api-restore-test-" + secrets.token_hex(8)
        image, volume = name + ":fixture", name + "-data"
        image_tar = io.BytesIO()
        with tarfile.open(fileobj=image_tar, mode="w:") as archive:
            info = tarfile.TarInfo("lite2api")
            body = self.binary.read_bytes()
            info.mode, info.size = 0o555, len(body)
            archive.addfile(info, io.BytesIO(body))
        try:
            # The image contains only the locally built static fixture binary;
            # no image is pulled and no existing container/volume is selected.
            command(["docker", "import", "-", image], env=self.env, data=image_tar.getvalue())
            command(["docker", "volume", "create", "--label", "lite2api.restore_fixture=" + name,
                     volume], env=self.env)
            command(["docker", "create", "--name", name, "--network", "host", "--read-only",
                     "--memory", "128m", "--pids-limit", "64", "--cap-drop", "ALL",
                     "--security-opt", "no-new-privileges:true", "--env-file", restored / "environment",
                     "--mount", "type=volume,source=" + volume + ",target=/app/data", image,
                     "/lite2api", "-config", "/app/data/config.json"], env=self.env)
            command(["docker", "cp", str(restored / "lite2api-data") + "/.", name + ":/app/data"], env=self.env)
            command(["docker", "start", name], env=self.env)
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                try:
                    if self.request("/livez")[0] == 200:
                        break
                except OSError:
                    pass
                time.sleep(0.05)
            else:
                self.fail("restored Docker fixture did not become live")
            runtime_env = dict(self.env)
            for line in (restored / "environment").read_text().splitlines():
                key, value = line.split("=", 1)
                runtime_env[key] = value
            before = self.calls
            report = self.gate(restored, runtime_env, success=True, env_file=restored / "environment")
            self.assertTrue(all(case["ok"] for case in report["cases"]))
            self.assertEqual(self.calls, before + 1, "rejected Docker accesses reached the source")
            copied = self.case / "docker-restored-client-keys.json"
            command(["docker", "cp", name + ":/app/data/client_keys.json", copied], env=self.env)
            self.assertEqual(copied.read_bytes(), contents["client_keys.json"])
        finally:
            # Names are generated in this test; cleanup never enumerates or
            # removes host containers, images or volumes belonging to others.
            command(["docker", "rm", "--force", name], env=self.env, success=None, timeout=10)
            command(["docker", "volume", "rm", "--force", volume], env=self.env, success=None, timeout=10)
            command(["docker", "image", "rm", "--force", image], env=self.env, success=None, timeout=10)


if __name__ == "__main__":
    def interrupted(_signal, _frame):
        cleanup_children()
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    unittest.main(verbosity=2)
