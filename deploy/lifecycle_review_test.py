"""Independent lifecycle regressions. Cancellation uses controlled age output; real crypto is tested separately."""
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parents[1]

def load(name, relative):
    spec = importlib.util.spec_from_file_location(name, ROOT / relative)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

archive = load('review_state_archive', 'deploy/state_archive.py')
acceptance = load('review_acceptance', 'deploy/verify_service.py')

def tar_bytes(files):
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode='w') as target:
        for name, value in files.items():
            item = tarfile.TarInfo(name)
            item.size, item.mode = len(value), 0o600
            target.addfile(item, io.BytesIO(value))
    return output.getvalue()

class LifecycleReview(unittest.TestCase):
    def test_snapshot_does_not_report_success_when_producer_resume_fails(self):
        source = (ROOT / 'deploy/snapshot-compose-state.sh').read_text()
        cleanup = source[source.index('cleanup() {'):source.index('\ntrap cleanup EXIT')]
        script = 'set -euo pipefail\nstage_dir=/tmp/lite2api-review-stage-does-not-exist\nartifacts=()\npaused_producers=(fixture-only)\ndocker() { return 1; }\n' + cleanup + '\ntrue\ncleanup\n'
        result = subprocess.run(['bash'], input=script, capture_output=True, text=True, timeout=5)
        self.assertNotEqual(result.returncode, 0, 'backup exited successfully while its producer remained paused')

    def test_restorer_can_write_material_files(self):
        with tempfile.TemporaryDirectory(prefix='lite2api-write-review-') as temporary:
            archive.read_archive(io.BytesIO(tar_bytes({'etc/lite2api/config.json': b'{}'})), output=Path(temporary))

    def test_business_acceptance_checks_requested_tools_and_indexes(self):
        cases = [
            ({'tools': [{'type': 'function', 'function': {'name': 'lookup'}}], 'tool_choice': 'required'},
             {'choices': [{'index': 0, 'message': {'content': 'plain text'}, 'finish_reason': 'stop'}]}),
            ({'n': 2}, {'choices': [{'index': 0, 'message': {'content': 'a'}, 'finish_reason': 'stop'}, {'index': 0, 'message': {'content': 'b'}, 'finish_reason': 'stop'}]}),
        ]
        for request, response in cases:
            with self.subTest(request=request):
                self.assertFalse(acceptance.protocol_complete('openai.chat', request, json.dumps(response).encode()))

    def test_business_acceptance_rejects_frames_after_stream_terminal(self):
        request = {'stream': True}
        complete = {'choices': [{'index': 0, 'delta': {'content': 'ok'}, 'finish_reason': 'stop'}]}
        trailing = b'data: ' + json.dumps(complete).encode() + b'\n\n' + b'data: [DONE]\n\n' + b'data: {"choices":[]}'
        self.assertFalse(acceptance.protocol_complete('openai.chat', request, trailing))

        responses = (
            b'data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}\n\n'
            b'data: {"type":"response.output_text.delta","delta":"late"}\n\n'
        )
        self.assertFalse(acceptance.protocol_complete('openai.responses', request, responses))

    def test_oauth_channel_release_metadata_and_patch_manifest_match(self):
        compose = (ROOT / 'compose.channels.yml').read_text()
        dockerfile = (ROOT / 'channels/cliproxyapi/Dockerfile').read_text()
        installer = (ROOT / 'deploy/install-cliproxyapi-systemd.sh').read_text()
        for source in (compose, dockerfile, installer):
            self.assertIn('v6.10.9-lite2api.9', source)
            self.assertIn('785b00c3127eea6aa207f1207ead8a2aa93690a3', source)
        self.assertIn('2026-10-28', compose)
        self.assertIn('2026-10-28', dockerfile)
        patches = [
            'quota-snapshot', 'routing-reliability', 'auth-refresh',
            'credential-routing', 'oauth-recovery',
        ]
        for patch in patches:
            name = f'cliproxyapi-{patch}.patch'
            self.assertIn(name, dockerfile)
            self.assertIn(name, installer)

    def test_messages_acceptance_checks_delta_type_and_block(self):
        for kind, field, block in (("text_delta", "text", "text"),
                                   ("thinking_delta", "thinking", "thinking"),
                                   ("signature_delta", "signature", "thinking")):
            for value in ("ok", 42, None, {}, [], True):
                for actual_block in (block, "tool_use"):
                    with self.subTest(kind=kind, value=value, block=actual_block):
                        frames = [
                            {"type": "message_start", "message": {"type": "message", "content": []}},
                            {"type": "content_block_start", "index": 0,
                             "content_block": {"type": actual_block, "text": "ok", "thinking": "ok"}},
                            {"type": "content_block_delta", "index": 0,
                             "delta": {"type": kind, field: value}},
                            {"type": "content_block_stop", "index": 0},
                            {"type": "message_delta", "delta": {"stop_reason": "end_turn"}},
                            {"type": "message_stop"},
                        ]
                        body = b"".join(b"data: " + json.dumps(frame).encode() + b"\n\n" for frame in frames)
                        self.assertEqual(acceptance.protocol_complete("anthropic.messages", {"stream": True}, body),
                                         isinstance(value, str) and actual_block == block)

    def test_stream_acceptance_requires_dispatched_protocol_terminal(self):
        streams = {
            "openai.chat": (
                b'data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}\n\n'
                b'data: [DONE]\n\n'),
            "openai.responses": (
                b'data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}\n\n'),
            "anthropic.messages": b"".join(b"data: " + json.dumps(frame).encode() + b"\n\n" for frame in [
                {"type": "message_start", "message": {"type": "message", "content": []}},
                {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": "ok"}},
                {"type": "content_block_stop", "index": 0},
                {"type": "message_delta", "delta": {"stop_reason": "end_turn"}},
                {"type": "message_stop"},
            ]),
        }
        for operation, stream in streams.items():
            with self.subTest(operation=operation):
                self.assertTrue(acceptance.protocol_complete(operation, {"stream": True}, stream))
                self.assertTrue(acceptance.protocol_complete(operation, {"stream": True}, stream + b": keepalive\n"))
                for body in (stream.rstrip(b"\n"), stream[:-1], stream + b'event: error\n', stream + b'data: {"error":'):
                    self.assertFalse(acceptance.protocol_complete(operation, {"stream": True}, body))
                if operation != "openai.chat":
                    self.assertFalse(acceptance.protocol_complete(operation, {"stream": True}, stream + b'data: [DONE]\n\n'))

    def test_manifest_must_be_a_complete_object(self):
        for manifest in (b'{}', b'[]', b'false'):
            with self.subTest(manifest=manifest):
                payload = tar_bytes({'etc/lite2api/config.json': b'{}', 'etc/lite2api/lite2api.env': b'FIXTURE=1\n', archive.MANIFEST: manifest})
                with self.assertRaises((ValueError, TypeError)):
                    archive.read_archive(io.BytesIO(payload), require_manifest=True)

    def test_created_hardlinked_state_is_readable_by_own_restorer(self):
        with tempfile.TemporaryDirectory(prefix='lite2api-archive-review-') as temporary:
            root = Path(temporary)
            config = root / 'etc/lite2api'
            config.mkdir(parents=True)
            (config / 'config.json').write_text('{"server":{"client_keys_path":"client_keys.json"},"accounts":[]}')
            (config / 'lite2api.env').write_text('FIXTURE=1\n')
            (config / 'client_keys.json').write_text('{"version":1,"keys":[]}')
            os.link(config / 'client_keys.json', config / 'client_keys.backup')
            created = subprocess.run([sys.executable, str(ROOT / 'deploy/state_archive.py'), 'create', '--root', str(root), 'etc/lite2api'], capture_output=True, timeout=10)
            self.assertEqual(created.returncode, 0, created.stderr.decode())
            archive.read_archive(io.BytesIO(created.stdout), require_manifest=True)

    def test_sigterm_cleans_plaintext_restore_and_child(self):
        with tempfile.TemporaryDirectory(prefix='lite2api-cancel-review-') as temporary:
            root = Path(temporary)
            snapshot = root / 'snapshot'
            snapshot.mkdir()
            encrypted = snapshot / 'systemd-state.tar.age'
            encrypted.write_bytes(b'controlled decryption fixture')
            (snapshot / 'METADATA').write_text('format=lite2api-systemd-state-v1\n')
            (snapshot / 'SHA256SUMS').write_text(hashlib.sha256(encrypted.read_bytes()).hexdigest() + '  systemd-state.tar.age\n')
            identity = root / 'identity'
            identity.write_text('fixture only; not a real decryption test')
            payload = root / 'clear.tar'
            payload.write_bytes(tar_bytes({'etc/lite2api/config.json': b'{}', 'etc/lite2api/lite2api.env': b'FIXTURE_SECRET=private-fixture\n'}))
            binary = root / 'bin'
            binary.mkdir()
            fake = binary / 'age'
            fake.write_text('#!' + sys.executable + '\nimport sys,time\nfrom pathlib import Path\nsys.stdout.buffer.write(Path(' + repr(str(payload)) + ').read_bytes() + b"\\0" * 65536)\nsys.stdout.buffer.flush()\ntime.sleep(20)\n')
            fake.chmod(0o700)
            output = root / 'restored'
            process = subprocess.Popen([sys.executable, str(ROOT / 'deploy/state_archive.py'), 'restore', 'systemd', '--snapshot', str(snapshot), '--output', str(output), '--identity', str(identity)], env={**os.environ, 'PATH': str(binary) + os.pathsep + os.environ['PATH']}, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, start_new_session=True)
            try:
                deadline = time.monotonic() + 5
                while not (output / 'etc/lite2api/lite2api.env').exists() and time.monotonic() < deadline:
                    time.sleep(0.01)
                self.assertTrue((output / 'etc/lite2api/lite2api.env').exists(), 'fixture did not reach plaintext restore; process exit=' + str(process.poll()) + (' stderr=' + process.stderr.read().decode() if process.poll() is not None else ''))
                process.terminate()
                process.wait(timeout=5)
                self.assertFalse(output.exists(), 'SIGTERM left plaintext state without a completed restore receipt')
            finally:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait(timeout=5)
                process.stderr.close()

if __name__ == '__main__':
    unittest.main(verbosity=2)
