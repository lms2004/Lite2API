#!/usr/bin/env bash
set -euo pipefail

umask 077

if [[ $# -ne 2 ]]; then
    echo 'usage: backup-configs.sh OUTPUT_DIRECTORY AGE_RECIPIENT' >&2
    exit 64
fi
if [[ $(id -u) -ne 0 ]]; then
    echo 'run this backup as root' >&2
    exit 1
fi

output_dir=$1
age_recipient=$2
for command in age find install mktemp mv sha256sum tar; do
    command -v "$command" >/dev/null || {
        echo "missing required command: $command" >&2
        exit 1
    }
done
[[ $age_recipient == age1* || $age_recipient == age-plugin-* ]] || {
    echo 'AGE_RECIPIENT must be an age public recipient' >&2
    exit 1
}
[[ ! -e $output_dir && ! -L $output_dir ]] || {
    echo "refusing to overwrite backup destination: $output_dir" >&2
    exit 1
}

required_core_paths=(
    /etc/lite2api/config.json
    /etc/lite2api/lite2api.env
)
for required_path in "${required_core_paths[@]}"; do
    [[ -f $required_path && ! -L $required_path ]] || {
        echo "required Lite2API state is missing or unsafe: $required_path" >&2
        exit 1
    }
done

# Archive the whole private configuration directory. Besides config.json and
# lite2api.env it contains client_keys.json and bounded request logs, both of
# which were silently omitted by the former file-by-file list.
configured_paths=(
    /etc/lite2api
    /etc/systemd/system/lite2api.service
    /etc/cliproxyapi
    /etc/systemd/system/cliproxyapi.service
    /var/lib/cliproxyapi/auths
    /etc/nginx/snippets/lite2api-admin-allowlist.conf
)

cliproxy_present=0
for cliproxy_path in /etc/systemd/system/cliproxyapi.service /etc/cliproxyapi /var/lib/cliproxyapi/auths; do
    [[ ! -e $cliproxy_path && ! -L $cliproxy_path ]] || cliproxy_present=1
done
if (( cliproxy_present == 1 )); then
    [[ -f /etc/cliproxyapi/config.yaml && ! -L /etc/cliproxyapi/config.yaml ]] || {
        echo 'installed OAuth adapter config is incomplete or unsafe' >&2
        exit 1
    }
    [[ -f /etc/cliproxyapi/cliproxyapi.env && ! -L /etc/cliproxyapi/cliproxyapi.env ]] || {
        echo 'installed OAuth adapter environment is incomplete or unsafe' >&2
        exit 1
    }
    [[ -d /var/lib/cliproxyapi/auths && ! -L /var/lib/cliproxyapi/auths ]] || {
        echo 'installed OAuth adapter auth directory is incomplete or unsafe' >&2
        exit 1
    }
fi
archive_paths=()
for absolute_path in "${configured_paths[@]}"; do
    [[ -e $absolute_path ]] || continue
    [[ ! -L $absolute_path ]] || {
        echo "refusing symlinked backup source: $absolute_path" >&2
        exit 1
    }
    if [[ -d $absolute_path ]] && [[ -n $(find "$absolute_path" -type l -print -quit) ]]; then
        echo "refusing nested symlink in backup source: $absolute_path" >&2
        exit 1
    fi
    archive_paths+=("${absolute_path#/}")
done
(( ${#archive_paths[@]} > 0 )) || {
    echo 'no Lite2API systemd state was found' >&2
    exit 1
}

output_parent=$(dirname -- "$output_dir")
if [[ -e $output_parent ]]; then
    [[ -d $output_parent && ! -L $output_parent ]] || {
        echo "backup parent is not a safe directory: $output_parent" >&2
        exit 1
    }
else
    install -d -o root -g root -m 0700 "$output_parent"
fi
stage_dir=$(mktemp -d "$output_parent/.lite2api-systemd-backup.XXXXXX")
cleanup() {
    local status=$?
    set +e
    trap - EXIT
    if [[ -d $stage_dir ]]; then
        rm -f -- "$stage_dir/systemd-state.tar.age" "$stage_dir/SHA256SUMS" "$stage_dir/METADATA"
        rmdir "$stage_dir" 2>/dev/null || true
    fi
    exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

tar -C / -cf - -- "${archive_paths[@]}" |
    age -r "$age_recipient" -o "$stage_dir/systemd-state.tar.age"
(
    cd "$stage_dir"
    sha256sum systemd-state.tar.age >SHA256SUMS
)
printf 'created_utc=%s\nformat=lite2api-systemd-state-v1\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$stage_dir/METADATA"
chmod 0600 "$stage_dir"/*
mv -- "$stage_dir" "$output_dir"

printf 'Encrypted systemd state backup created: %s\n' "$output_dir"
printf 'Copy it off-host and run verify-systemd-backup.sh.\n'
