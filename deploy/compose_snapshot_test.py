#!/usr/bin/env python3
"""Opt-in real Docker/age snapshot lifecycle; uses only uniquely owned fixtures."""
import hashlib
import atexit
import json
import os
from pathlib import Path
import secrets
import signal
import shutil
import subprocess
import sys
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parents[1]
IMAGE = "alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce"
ACTIVE_FIXTURES = []


@unittest.skipUnless(os.environ.get("LITE2API_TEST_DOCKER") == "1", "set LITE2API_TEST_DOCKER=1 for real isolated Docker snapshot acceptance")
class ComposeSnapshotLifecycle(unittest.TestCase):
    def run_command(self, command, *, env=None, success=True, timeout=60, input=None):
        result = subprocess.run(command, env=env or self.env, input=input, capture_output=True, text=True, timeout=timeout)
        if success:
            self.assertEqual(result.returncode, 0, "fixture command failed: " + result.stderr[-2000:])
        else:
            self.assertNotEqual(result.returncode, 0, "unsafe or incomplete snapshot unexpectedly succeeded")
        return result

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="lite2api-compose-lifecycle-")
        self.root = Path(self.temporary.name)
        self.prefix = "lite2api-lifecycle-" + secrets.token_hex(6)
        self.containers, self.volumes = [], []
        self.env = dict(os.environ)
        self.cleaned = False
        ACTIVE_FIXTURES.append(self)
        self.addCleanup(self.cleanup)
        self.assertTrue(shutil.which("age") and shutil.which("age-keygen"), "real age/age-keygen required")
        self.run_command(["docker", "info", "--format", "{{.ServerVersion}}"], timeout=15)
        self.run_command(["docker", "image", "inspect", IMAGE], timeout=15)
        self.run_command(["age-keygen", "-o", str(self.root / "identity")])
        recipient = self.run_command(["age-keygen", "-y", str(self.root / "identity")]).stdout.strip()
        self.recipient = recipient
        self.core = self.create_volume("core", "lite2api-data", self.prefix)
        self.decoy = self.create_volume("decoy", "lite2api-grok-data", self.prefix+"-other-project")
        self.run_command(["docker", "run", "--rm", "--network", "none", "-v", self.core+":/data", IMAGE, "sh", "-ec", 'printf \'{"server":{"client_keys_path":"client_keys.json"}}\\n\' >/data/config.json; printf \'{"version":1,"keys":[]}\\n\' >/data/client_keys.json'])
        runtime = self.root / "runtime"
        for directory, name in [("cliproxyapi", "config.yaml"), ("grok2api", "config.yaml"), ("gemini-web2api", "config.json")]:
            (runtime / directory).mkdir(parents=True)
            (runtime / directory / name).write_text("{}\n")
        (runtime / "cliproxyapi/auths").mkdir()
        (self.root / "environment").write_text("LITE2API_API_KEYS="+secrets.token_hex(32)+"\nLITE2API_ADMIN_TOKEN="+secrets.token_hex(32)+"\n")
        self.env.update(LITE2API_DATA_VOLUME=self.core, LITE2API_COMPOSE_ENV_FILE=str(self.root / "environment"), LITE2API_CHANNEL_RUNTIME_DIR=str(runtime), AGE_IDENTITY_FILE=str(self.root / "identity"))
        self.env.pop("GROK2API_DATA_VOLUME", None)
        self.env.pop("GROK2API_QUALITY_VOLUME", None)
        self.start_producer("core-writer", ["-v", self.core+":/data"])
        self.start_producer("auth-writer", ["-v", str(runtime / "cliproxyapi/auths")+":/data"])

    def create_volume(self, suffix, label, project):
        name = self.prefix+"-"+suffix
        self.volumes.append(name)
        self.run_command(["docker", "volume", "create", "--label", "com.docker.compose.volume="+label, "--label", "com.docker.compose.project="+project, name])
        return name

    def start_producer(self, suffix, mounts):
        name = self.prefix+"-"+suffix
        self.containers.append(name)
        self.run_command(["docker", "run", "-d", "--name", name, "--network", "none", "--read-only", "--pids-limit", "32", "--cap-drop", "ALL", *mounts, IMAGE, "sh", "-ec", 'n=0; while :; do n=$((n+1)); printf "%s\\n" "$n" >/data/counter.tmp; mv /data/counter.tmp /data/counter; sleep 0.02; done'])

    def cleanup(self):
        if self.cleaned:
            return
        self.cleaned = True
        for name in reversed(self.containers):
            self.assertTrue(name.startswith(self.prefix+"-"))
            subprocess.run(["docker", "rm", "-f", name], capture_output=True, timeout=15)
        for name in reversed(self.volumes):
            self.assertTrue(name.startswith(self.prefix+"-"))
            subprocess.run(["docker", "volume", "rm", name], capture_output=True, timeout=15)
        self.temporary.cleanup()

    def assert_resumed(self):
        for name in self.containers:
            paused = self.run_command(["docker", "inspect", "--format", "{{.State.Paused}}", name]).stdout.strip()
            self.assertEqual(paused, "false", "snapshot left its producer paused")

    def test_consistent_snapshot_roundtrip_and_failed_capture_resume(self):
        snapshot = self.root / "snapshot"
        self.run_command(["bash", str(ROOT / "deploy/snapshot-compose-state.sh"), str(snapshot), self.recipient], timeout=120)
        self.assert_resumed()
        metadata = (snapshot / "METADATA").read_text()
        self.assertIn("consistency=producers_paused_and_host_unchanged", metadata)
        self.assertNotIn("grok2api-data.tar.age", (snapshot / "SHA256SUMS").read_text(), "optional volume from another Compose project was captured")
        self.run_command(["bash", str(ROOT / "deploy/verify-compose-snapshot.sh"), str(snapshot)], timeout=60)
        restored = self.root / "restored"
        result = self.run_command([sys.executable, str(ROOT / "deploy/state_archive.py"), "restore", "compose", "--snapshot", str(snapshot), "--identity", str(self.root / "identity"), "--output", str(restored)])
        self.assertTrue(json.loads(result.stdout)["ok"])
        self.assertEqual((restored / "environment").read_bytes(), (self.root / "environment").read_bytes())
        self.assertEqual(json.loads((restored / "lite2api-data/client_keys.json").read_text()), {"version": 1, "keys": []})
        self.assertTrue((restored / "channel-runtime/cliproxyapi/auths/counter").is_file())
        captured = int((restored / "lite2api-data/counter").read_text())
        deadline = time.monotonic()+5
        while time.monotonic() < deadline:
            current = int(self.run_command(["docker", "exec", self.containers[0], "cat", "/data/counter"]).stdout)
            if current > captured:
                break
            time.sleep(0.05)
        self.assertGreater(current, captured, "producer did not resume writes")
        failed = self.root / "failed-snapshot"
        self.run_command(["bash", str(ROOT / "deploy/snapshot-compose-state.sh"), str(failed), "age1invalid-recipient"], success=False, timeout=60)
        self.assertFalse(failed.exists())
        self.assert_resumed()


if __name__ == "__main__":
    def cleanup_all():
        for fixture in ACTIVE_FIXTURES:
            fixture.cleanup()
    atexit.register(cleanup_all)
    def interrupted(signum, _frame):
        cleanup_all()
        raise SystemExit(128+signum)
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGHUP, interrupted)
    unittest.main()
