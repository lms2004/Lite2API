import datetime
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess

repo = Path('/root/Lite2API')
evidence = repo / 'docs/evidence/continuation-2026-10-29'
files = subprocess.check_output(['git', 'ls-files', '-co', '--exclude-standard'], cwd=repo, text=True).splitlines()
paths = [p for p in sorted(set(files)) if p.startswith(('cmd/', 'internal/', 'deploy/', 'tools/')) and '__pycache__' not in p and (repo / p).is_file()]
paths += ['go.mod', 'Makefile']
hashes = {p: hashlib.sha256((repo / p).read_bytes()).hexdigest() for p in sorted(paths)}
digest = hashlib.sha256(json.dumps(hashes, sort_keys=True).encode()).hexdigest()
release = Path('/root/lite2api-release-continuation-' + digest[:12])
release.mkdir(mode=0o700)
source = release / 'source'
source.mkdir()
for relative in sorted(paths):
    src, dst = repo / relative, source / relative
    if src.is_symlink():
        raise RuntimeError('release input is a symlink: ' + relative)
    dst.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(src, dst)
version = 'continuation-20261029-' + digest[:12]
env = {**os.environ, 'GOTOOLCHAIN': 'local', 'CGO_ENABLED': '0', 'GOMAXPROCS': '2'}
command = ['go', 'build', '-p', '2', '-trimpath', '-buildvcs=false', '-ldflags=-s -w -X main.version=' + version, '-o', str(release / 'lite2api'), './cmd/lite2api']
subprocess.run(command, cwd=source, env=env, check=True)
manifest = {'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'git_base': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=repo, text=True).strip(), 'working_tree_modified': True, 'version': version, 'source_tree_sha256': digest, 'files': hashes, 'binary_sha256': hashlib.sha256((release / 'lite2api').read_bytes()).hexdigest(), 'build_command': command, 'go_version': subprocess.check_output(['go', 'version'], text=True).strip(), 'release_directory': str(release)}
assert subprocess.check_output([str(release / 'lite2api'), '-version'], text=True).strip() == version
(release / 'release-manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
(evidence / 'release-manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
Path('/tmp/lite2api-current-release').write_text(str(release))
print(json.dumps({k: v for k, v in manifest.items() if k != 'files'}, indent=2))
