"""One reviewed gateway-only deployment; fixed loopback acceptance, automatic rollback."""
import datetime
import hashlib
import json
import os
from pathlib import Path
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

repo = Path('/root/Lite2API')
release = Path('/tmp/lite2api-current-release').read_text().strip()
release = Path(release)
evidence = repo / 'docs/evidence/continuation-2026-10-29'
manifest = json.loads((release / 'release-manifest.json').read_text())
baseline = json.loads((evidence / 'production-before.json').read_text())
sys.path.insert(0, str(release / 'source'))
from deploy.verify_service import verify

def interrupted(_signum, _frame):
    raise KeyboardInterrupt()

signal.signal(signal.SIGTERM, interrupted)
signal.signal(signal.SIGHUP, interrupted)

class AcceptanceDeadline(BaseException):
    pass

def deadline(_signum, _frame):
    raise AcceptanceDeadline()

def bounded_verify(cases):
    signal.signal(signal.SIGALRM, deadline)
    signal.setitimer(signal.ITIMER_REAL, 125)
    try:
        return verify('http://127.0.0.1:45679', cases, environ=env, timeout=120)
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)

def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()

def require(value, message):
    if not value:
        raise RuntimeError(message)

def ctl(action):
    subprocess.run(['systemctl', action, 'lite2api.service'], check=True, timeout=45, capture_output=True)

def service():
    output = subprocess.check_output(['systemctl', 'show', 'lite2api.service', '-p', 'MainPID', '-p', 'ActiveState', '-p', 'SubState', '-p', 'NRestarts'], text=True, timeout=10)
    return dict(line.split('=', 1) for line in output.splitlines())

def request(path, key=''):
    req = urllib.request.Request('http://127.0.0.1:45679' + path, headers={'Authorization': 'Bearer ' + key} if key else {})
    try:
        response = urllib.request.urlopen(req, timeout=5)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, json.load(response)

def live():
    for _ in range(40):
        try:
            if request('/livez')[0] == 200:
                return
        except (OSError, ValueError):
            pass
        time.sleep(.25)
    raise RuntimeError('gateway did not become live')

def replace(source, target):
    fd, temporary = tempfile.mkstemp(prefix='.lite2api-reviewed-', dir=target.parent)
    try:
        with os.fdopen(fd, 'wb') as output, source.open('rb') as input_file:
            shutil.copyfileobj(input_file, output)
            output.flush()
            os.fsync(output.fileno())
        os.chmod(temporary, 0o755)
        os.chown(temporary, 0, 0)
        os.replace(temporary, target)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)

values = {}
for line in Path('/etc/lite2api/lite2api.env').read_text().splitlines():
    for field in shlex.split(line, comments=True):
        key, separator, value = field.partition('=')
        if separator:
            values[key] = value
env = {**os.environ, **values}
binary = Path('/usr/local/bin/lite2api')
require(not binary.is_symlink(), 'binary target must be a regular file')
require(sha(binary) == baseline['services']['lite2api']['installed_sha256'], 'production binary changed since review')
require(sha(release / 'lite2api') == manifest['binary_sha256'], 'candidate digest mismatch')
require(subprocess.check_output([str(release / 'lite2api'), '-version'], text=True).strip() == manifest['version'], 'candidate version mismatch')
subprocess.run([str(release / 'lite2api'), '-check-config', '-config', '/etc/lite2api/config.json'], env=env, check=True, capture_output=True, timeout=15)
require(service()['ActiveState'] == 'active', 'unexpected original service state')
for _ in range(30):
    status, state = request('/admin/api/state', values['LITE2API_ADMIN_TOKEN'])
    require(status == 200, 'cannot verify current in-flight calls')
    if state['stats']['active'] == 0:
        break
    time.sleep(1)
else:
    raise RuntimeError('active calls did not drain within the deployment window')

config_root = Path('/etc/lite2api')
for path in [config_root, *config_root.rglob('*')]:
    require(not path.is_symlink(), 'state snapshot refuses symlinks')
config = json.loads((config_root / 'config.json').read_text())
key_path = Path(config['server'].get('client_keys_path') or 'client_keys.json')
if not key_path.is_absolute():
    key_path = config_root / key_path
external_key = key_path if key_path.is_file() and not key_path.is_relative_to(config_root) else None
if external_key:
    require(not external_key.is_symlink(), 'external key state must be a regular file')

rollback = release / ('rollback-' + datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ'))
rollback.mkdir(mode=0o700)
shutil.copy2(binary, rollback / 'lite2api')
unit_digest = sha('/etc/systemd/system/lite2api.service')
env_digest = sha(config_root / 'lite2api.env')
adapter_digest = sha('/usr/local/bin/cliproxyapi')
result = {'observed_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'version': manifest['version'], 'binary_sha256': manifest['binary_sha256'], 'rollback_directory': str(rollback), 'scope': 'gateway_binary_only_existing_systemd_unit_loopback_45679', 'ok': False}
cases = [{'name': operation, 'operation': operation, 'model': 'Shadow', 'expect_account': 'cliproxy-antigravity', 'stream': operation == 'openai.responses'} for operation in ['openai.chat', 'openai.responses', 'anthropic.messages']]
stopped = False
snapshot = False
try:
    stopped = True
    ctl('stop')
    subprocess.run(['cp', '-a', '--', str(config_root), str(rollback / 'etc-lite2api')], check=True, timeout=30)
    if external_key:
        subprocess.run(['cp', '-a', '--', str(external_key), str(rollback / 'external-client-keys')], check=True, timeout=10)
    snapshot = True
    replace(release / 'lite2api', binary)
    ctl('start')
    live()
    accepted = bounded_verify(cases)
    (evidence / 'production-business-acceptance.json').write_text(json.dumps(accepted, indent=2) + '\n')
    require(accepted['ok'], 'production business acceptance failed')
    result['service'] = service()
    result['process_sha256'] = sha('/proc/' + result['service']['MainPID'] + '/exe')
    require(result['process_sha256'] == manifest['binary_sha256'], 'running binary does not match reviewed candidate')
    require(sha('/etc/systemd/system/lite2api.service') == unit_digest, 'unit changed unexpectedly')
    require(sha(config_root / 'lite2api.env') == env_digest, 'environment changed unexpectedly')
    require(sha('/usr/local/bin/cliproxyapi') == adapter_digest, 'adapter binary changed unexpectedly')
    result['ok'] = True
    result['unit_environment_adapter_unchanged'] = True
except BaseException as error:
    signal.setitimer(signal.ITIMER_REAL, 0)
    result['failure_type'] = type(error).__name__
    if stopped:
        try:
            ctl('stop')
            replace(rollback / 'lite2api', binary)
            if snapshot:
                # Keep failed-candidate state private for diagnosis before restoring.
                try:
                    captured = subprocess.run(['cp', '-a', '--', str(config_root), str(rollback / 'failed-candidate-state')], timeout=30, capture_output=True)
                    result['failed_state_captured'] = captured.returncode == 0
                except (OSError, subprocess.SubprocessError):
                    result['failed_state_captured'] = False
                original_files = {p.relative_to(rollback / 'etc-lite2api') for p in (rollback / 'etc-lite2api').rglob('*')}
                for path in sorted(config_root.rglob('*'), key=lambda p: len(p.parts), reverse=True):
                    if path.relative_to(config_root) not in original_files:
                        if path.is_dir():
                            path.rmdir()
                        else:
                            path.unlink()
                subprocess.run(['cp', '-a', '--', str(rollback / 'etc-lite2api') + '/.', str(config_root)], check=True, timeout=30)
                if external_key:
                    subprocess.run(['cp', '-a', '--', str(rollback / 'external-client-keys'), str(external_key)], check=True, timeout=10)
            ctl('start')
            live()
            result['service'] = service()
            require(sha(binary) == baseline['services']['lite2api']['installed_sha256'], 'rollback binary digest mismatch')
            require(sha('/proc/' + result['service']['MainPID'] + '/exe') == sha(binary), 'rollback process digest mismatch')
            restored = bounded_verify([{'name': 'rollback_chat', 'operation': 'openai.chat', 'model': 'Shadow'}])
            (evidence / 'rollback-business-acceptance.json').write_text(json.dumps(restored, indent=2) + '\n')
            require(restored['ok'], 'rollback business acceptance failed')
            result['rolled_back'] = True
        except BaseException as rollback_error:
            result['rollback_failure_type'] = type(rollback_error).__name__

finally:
    (release / 'deployment-manifest.json').write_text(json.dumps(result, indent=2) + '\n')
    (evidence / 'deployment-manifest.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, indent=2))
raise SystemExit(0 if result['ok'] else 1)
