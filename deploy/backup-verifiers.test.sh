#!/usr/bin/env bash
set -euo pipefail

source_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
test_root=$(mktemp -d /tmp/lite2api-backup-verifiers.XXXXXX)
cleanup() {
    local status=$?
    set +e
    trap - EXIT
    case $test_root in
        /tmp/lite2api-backup-verifiers.*) rm -rf -- "$test_root" ;;
    esac
    exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -p "$test_root/bin"
# Preserve these expansions for the generated fake age executable.
# shellcheck disable=SC2016
printf '%s\n' \
    '#!/bin/sh' \
    'set -eu' \
    '[ "${1:-}" = --decrypt ] && [ "$#" -eq 2 ]' \
    'exec cat -- "$2"' >"$test_root/bin/age"
chmod 0755 "$test_root/bin/age"
export PATH="$test_root/bin:$PATH"

expect_failure() {
    if "$@" >/dev/null 2>&1; then
        echo "expected command to fail: $*" >&2
        exit 1
    fi
}

compose_snapshot="$test_root/compose"
compose_payload="$test_root/compose-payload"
mkdir -p "$compose_snapshot" "$compose_payload"
printf '{}\n' >"$compose_payload/config.json"
tar -C "$compose_payload" -cf "$compose_snapshot/lite2api-data.tar.age" .
printf '%s\n' \
    'LITE2API_API_KEYS=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' \
    'LITE2API_ADMIN_TOKEN=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' \
    >"$compose_snapshot/environment.age"
printf 'format=lite2api-compose-state-v2\n' >"$compose_snapshot/METADATA"
(
    cd "$compose_snapshot"
    sha256sum lite2api-data.tar.age environment.age >SHA256SUMS
)
"$source_root/deploy/verify-compose-snapshot.sh" "$compose_snapshot" >/dev/null

ln -s /etc/passwd "$compose_payload/unsafe-link"
tar -C "$compose_payload" -cf "$compose_snapshot/lite2api-data.tar.age" .
(
    cd "$compose_snapshot"
    sha256sum lite2api-data.tar.age environment.age >SHA256SUMS
)
expect_failure "$source_root/deploy/verify-compose-snapshot.sh" "$compose_snapshot"
rm -f -- "$compose_payload/unsafe-link"

tar -C "$compose_payload" \
    --transform='s#^config[.]json$#../escape#' \
    -cf "$compose_snapshot/lite2api-data.tar.age" config.json
(
    cd "$compose_snapshot"
    sha256sum lite2api-data.tar.age environment.age >SHA256SUMS
)
expect_failure "$source_root/deploy/verify-compose-snapshot.sh" "$compose_snapshot"

tar -C "$compose_payload" -cf "$compose_snapshot/lite2api-data.tar.age" .
printf '%s\n' \
    'LITE2API_API_KEYS=replace-with-a-long-random-gateway-key' \
    'LITE2API_ADMIN_TOKEN=replace-with-a-long-random-admin-token' \
    >"$compose_snapshot/environment.age"
(
    cd "$compose_snapshot"
    sha256sum lite2api-data.tar.age environment.age >SHA256SUMS
)
expect_failure "$source_root/deploy/verify-compose-snapshot.sh" "$compose_snapshot"

systemd_backup="$test_root/systemd"
systemd_payload="$test_root/systemd-payload"
mkdir -p "$systemd_backup" "$systemd_payload/etc/lite2api"
printf '{}\n' >"$systemd_payload/etc/lite2api/config.json"
printf '%s\n' \
    'LITE2API_API_KEYS=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc' \
    'LITE2API_ADMIN_TOKEN=dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd' \
    >"$systemd_payload/etc/lite2api/lite2api.env"
tar -C "$systemd_payload" -cf "$systemd_backup/systemd-state.tar.age" etc/lite2api
printf 'format=lite2api-systemd-state-v1\n' >"$systemd_backup/METADATA"
(
    cd "$systemd_backup"
    sha256sum systemd-state.tar.age >SHA256SUMS
)
"$source_root/deploy/server-ops/verify-systemd-backup.sh" "$systemd_backup" >/dev/null

mkdir -p "$systemd_payload/etc/cliproxyapi"
printf 'host: "127.0.0.1"\n' >"$systemd_payload/etc/cliproxyapi/config.yaml"
tar -C "$systemd_payload" -cf "$systemd_backup/systemd-state.tar.age" etc
(
    cd "$systemd_backup"
    sha256sum systemd-state.tar.age >SHA256SUMS
)
expect_failure "$source_root/deploy/server-ops/verify-systemd-backup.sh" "$systemd_backup"

printf 'backup verifier contracts passed\n'
