#!/usr/bin/env python3
"""Create, check and restore bounded state archives without executing their contents."""
import argparse
import datetime
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import signal
import subprocess
import sys
import tarfile

MANIFEST = "lite2api-state-manifest.json"
MAX_BYTES = 2 << 30
MAX_FILES = 20000


def digest_file(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def member_name(name):
    value = PurePosixPath(name)
    if value.is_absolute() or ".." in value.parts or len(name.encode()) > 1024:
        raise ValueError("unsafe archive path")
    return str(value)


def state_paths(root, paths):
    entries = {}
    for name in paths:
        path = root / member_name(name)
        if not path.exists():
            continue
        candidates = [path]
        if path.is_dir():
            candidates += list(path.rglob("*"))
        for candidate in candidates:
            if candidate.is_symlink() or candidate.resolve() != candidate:
                raise ValueError("state source contains a symbolic link")
            if not candidate.is_file() and not candidate.is_dir():
                raise ValueError("state source contains a special file")
            entries[str(candidate.relative_to(root))] = candidate
            if len(entries) > MAX_FILES:
                raise ValueError("state has too many entries")
    return entries


def create(root, paths):
    root = root.resolve()
    config_path = root / "etc/lite2api/config.json"
    cfg = json.loads(config_path.read_text())
    key_name = cfg.get("server", {}).get("client_keys_path") or "client_keys.json"
    key_path = PurePosixPath(key_name)
    if not key_path.is_absolute():
        key_path = PurePosixPath("/etc/lite2api") / key_path
    if ".." in key_path.parts:
        key_path = PurePosixPath(os.path.normpath(str(key_path)))
    key_member = str(key_path).lstrip("/")
    if (root / key_member).exists():
        paths.append(key_member)
    entries = state_paths(root, paths)
    before = {name: digest_file(path) for name, path in entries.items() if path.is_file()}
    if "etc/lite2api/config.json" not in before or "etc/lite2api/lite2api.env" not in before:
        raise ValueError("core state is incomplete")
    manifest = {"version": 1, "client_keys_path": key_member, "client_keys_present": key_member in before, "files": before}
    count = sum(path.stat().st_size for path in entries.values() if path.is_file())
    if count > MAX_BYTES:
        raise ValueError("state exceeds 2 GiB archive budget")
    with tarfile.open(fileobj=sys.stdout.buffer, mode="w|", dereference=True) as archive:
        for name, path in sorted(entries.items()):
            archive.add(path, arcname=name, recursive=False)
        encoded = json.dumps(manifest, sort_keys=True).encode()
        info = tarfile.TarInfo(MANIFEST)
        info.mode, info.size = 0o600, len(encoded)
        archive.addfile(info, io.BytesIO(encoded))
    after_entries = state_paths(root, paths)
    after = {name: digest_file(path) for name, path in after_entries.items() if path.is_file()}
    if before != after:
        raise ValueError("state changed while being archived; quiesce writes and retry")


def read_archive(stream, output=None, require_manifest=False):
    seen, digests = set(), {}
    total, manifest = 0, None
    with tarfile.open(fileobj=stream, mode="r|") as archive:
        for entry in archive:
            name = member_name(entry.name)
            if name == "RESTORE.json":
                raise ValueError("archive conflicts with reserved restore report")
            if name == "." and entry.isdir():
                continue
            if name in seen:
                raise ValueError("duplicate archive member")
            seen.add(name)
            if len(seen) > MAX_FILES or entry.size < 0 or not (entry.isdir() or entry.isreg()):
                raise ValueError("unsafe archive member type or count")
            total += entry.size
            if total > MAX_BYTES:
                raise ValueError("archive exceeds 2 GiB restore budget")
            target = output / name if output else None
            if entry.isdir():
                if output:
                    target.mkdir(parents=True, exist_ok=True, mode=0o700)
                continue
            body = archive.extractfile(entry)
            if name == MANIFEST:
                if entry.size > 8 << 20:
                    raise ValueError("state manifest is too large")
                manifest = json.load(body)
                continue
            digest = hashlib.sha256()
            if output:
                target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                destination = open(target, "xb", opener=lambda p, flags: os.open(p, flags, 0o600))
            else:
                destination = None
            try:
                while chunk := body.read(1 << 20):
                    digest.update(chunk)
                    if destination:
                        destination.write(chunk)
            finally:
                if destination:
                    destination.close()
            digests[name] = digest.hexdigest()
    if require_manifest and not isinstance(manifest, dict):
        raise ValueError("required state manifest is missing")
    if manifest is not None:
        if not isinstance(manifest, dict):
            raise ValueError("state manifest must be an object")
        if manifest.get("version") != 1 or manifest.get("files") != digests:
            raise ValueError("state content does not match manifest")
        if manifest.get("client_keys_present") and manifest.get("client_keys_path") not in digests:
            raise ValueError("managed client key metadata is missing")
    if output and manifest:
        (output / MANIFEST).write_text(json.dumps(manifest, indent=2) + "\n")
        (output / MANIFEST).chmod(0o600)
    return manifest, digests


def restore(snapshot, output, identity, kind):
    output = Path(os.path.abspath(output))
    if any(parent.is_symlink() for parent in output.parents):
        raise ValueError("restore destination ancestry contains a symbolic link")
    if output.exists() or output.is_symlink() or snapshot.is_symlink() or not identity.is_file() or identity.is_symlink():
        raise ValueError("restore requires a new destination and safe snapshot/identity")
    if kind == "systemd":
        required = {"systemd-state.tar.age"}
        allowed = required
        metadata_format = {"lite2api-systemd-state-v1", "lite2api-systemd-state-v2"}
    else:
        required = {"lite2api-data.tar.age", "environment.age"}
        allowed = required | {"grok2api-data.tar.age", "grok2api-quality.tar.age", "channel-runtime.tar.age"}
        metadata_format = {"lite2api-compose-state-v2"}
    for name in ("METADATA", "SHA256SUMS"):
        if not (snapshot / name).is_file() or (snapshot / name).is_symlink():
            raise ValueError("unsafe snapshot metadata")
    metadata = dict(line.split("=", 1) for line in (snapshot / "METADATA").read_text().splitlines() if "=" in line)
    if metadata.get("format") not in metadata_format:
        raise ValueError("unsupported snapshot format")
    artifacts = {}
    for line in (snapshot / "SHA256SUMS").read_text().splitlines():
        digest, name = line.split("  ", 1)
        if name not in allowed or name in artifacts or len(digest) != 64 or any(c not in "0123456789abcdef" for c in digest):
            raise ValueError("unsafe checksum manifest")
        path = snapshot / name
        if path.is_symlink() or not path.is_file() or digest_file(path) != digest:
            raise ValueError("snapshot checksum mismatch")
        artifacts[name] = path
    if not required <= artifacts.keys():
        raise ValueError("required snapshot artifact is missing")
    output.mkdir(mode=0o700)
    try:
        summaries = []
        for name, path in artifacts.items():
            process = subprocess.Popen(["age", "--decrypt", "--identity", str(identity), str(path)], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
            try:
                if name == "environment.age":
                    destination = output / "environment"
                    with open(destination, "xb", opener=lambda p, flags: os.open(p, flags, 0o600)) as sink:
                        total = 0
                        while chunk := process.stdout.read(65536):
                            total += len(chunk)
                            if total > 1 << 20:
                                raise ValueError("environment exceeds restore budget")
                            sink.write(chunk)
                else:
                    directory = output if kind == "systemd" else output / name.removesuffix(".tar.age")
                    directory.mkdir(exist_ok=True, mode=0o700)
                    manifest, members = read_archive(process.stdout, directory, metadata.get("format") == "lite2api-systemd-state-v2")
                    if kind == "systemd" and not {"etc/lite2api/config.json", "etc/lite2api/lite2api.env"} <= members.keys():
                        raise ValueError("restored core state is incomplete")
                    if kind == "compose" and name == "lite2api-data.tar.age" and "config.json" not in members:
                        raise ValueError("restored gateway volume is incomplete")
                    summaries.append({"artifact": name, "files": len(members), "manifest_verified": manifest is not None, "client_keys_present": manifest.get("client_keys_present") if manifest else None})
                if process.wait(timeout=10) != 0:
                    raise ValueError("snapshot decryption failed")
            finally:
                process.stdout.close()
                if process.poll() is None:
                    process.kill()
                    process.wait(timeout=5)
        created = None
        try:
            created = datetime.datetime.fromisoformat(metadata.get("created_utc", "").replace("Z", "+00:00")).isoformat()
        except ValueError:
            pass
        report = {"ok": True, "format": metadata["format"], "backup_created_at": created, "artifacts": summaries, "status": "material_restored_business_acceptance_required", "pending_checks": ["reconcile_post_backup_permission_and_channel_changes", "source_authorization", "required_client_calls"]}
        (output / "RESTORE.json").write_text(json.dumps(report, indent=2) + "\n")
        (output / "RESTORE.json").chmod(0o600)
        return report
    except BaseException:
        shutil.rmtree(output)
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    create_parser = sub.add_parser("create")
    create_parser.add_argument("--root", default="/")
    create_parser.add_argument("paths", nargs="+")
    inventory = sub.add_parser("inventory")
    inventory.add_argument("paths", nargs="+")
    check = sub.add_parser("check")
    check.add_argument("--require-manifest", action="store_true")
    restore_parser = sub.add_parser("restore")
    restore_parser.add_argument("kind", choices=("systemd", "compose"))
    restore_parser.add_argument("--snapshot", required=True)
    restore_parser.add_argument("--output", required=True)
    restore_parser.add_argument("--identity", required=True)
    args = parser.parse_args()
    os.umask(0o077)
    def timed_out(_signum, _frame):
        raise TimeoutError("state operation deadline")
    signal.signal(signal.SIGALRM, timed_out)
    def interrupted(signum, _frame):
        raise SystemExit(128 + signum)
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGHUP, interrupted)
    signal.alarm(300)
    try:
        if args.action == "create":
            create(Path(args.root), args.paths)
        elif args.action == "inventory":
            entries = state_paths(Path("/"), [str(Path(p).absolute()).lstrip("/") for p in args.paths])
            print(json.dumps({name: digest_file(path) for name, path in sorted(entries.items()) if path.is_file()}, sort_keys=True))
        elif args.action == "check":
            read_archive(sys.stdin.buffer, require_manifest=args.require_manifest)
        else:
            print(json.dumps(restore(Path(args.snapshot), Path(args.output), Path(args.identity), args.kind)))
    except (OSError, ValueError, TypeError, KeyError, tarfile.TarError, subprocess.SubprocessError):
        print("state archive operation failed; check archive integrity, required material and quiescence", file=sys.stderr)
        raise SystemExit(1)
    finally:
        signal.alarm(0)


if __name__ == "__main__":
    main()
