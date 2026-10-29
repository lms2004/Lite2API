#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
    echo 'usage: verify-systemd-backup.sh BACKUP_DIRECTORY' >&2
    exit 64
fi

backup_dir=$1
age_identity_args=()
if [[ -n ${AGE_IDENTITY_FILE:-} ]]; then
    [[ -f $AGE_IDENTITY_FILE && ! -L $AGE_IDENTITY_FILE ]] || { echo 'unsafe age identity file' >&2; exit 1; }
    age_identity_args=(--identity "$AGE_IDENTITY_FILE")
fi
decrypt() { age --decrypt "${age_identity_args[@]}" "$1"; }
for command in age grep sha256sum tar; do
    command -v "$command" >/dev/null || {
        echo "missing required command: $command" >&2
        exit 1
    }
done
for file in systemd-state.tar.age SHA256SUMS METADATA; do
    [[ -f $backup_dir/$file && ! -L $backup_dir/$file ]] || {
        echo "backup artifact is missing or unsafe: $file" >&2
        exit 1
    }
done
grep -Eq '^format=lite2api-systemd-state-v[12]$' "$backup_dir/METADATA"

(
    cd "$backup_dir"
    [[ $(wc -l <SHA256SUMS) -eq 1 ]]
    grep -Eq '^[0-9a-f]{64}  systemd-state[.]tar[.]age$' SHA256SUMS
    sha256sum --check SHA256SUMS
)
if grep -Fxq 'format=lite2api-systemd-state-v2' "$backup_dir/METADATA"; then
    project_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
    decrypt "$backup_dir/systemd-state.tar.age" | python3 "$project_dir/deploy/state_archive.py" check --require-manifest
fi
archive_listing=$(decrypt "$backup_dir/systemd-state.tar.age" | tar -tf -)
while IFS= read -r archive_path; do
    [[ $archive_path != /* && $archive_path != .. && $archive_path != ../* && \
        $archive_path != */../* && $archive_path != */.. ]] || {
        echo "unsafe path in encrypted archive: $archive_path" >&2
        exit 1
    }
done <<<"$archive_listing"
archive_verbose=$(decrypt "$backup_dir/systemd-state.tar.age" | tar -tvf -)
while read -r mode _; do
    case ${mode:0:1} in
        -|d) ;;
        *)
            echo "unsafe member type in encrypted archive: $mode" >&2
            exit 1
            ;;
    esac
done <<<"$archive_verbose"
grep -Fxq 'etc/lite2api/config.json' <<<"$archive_listing"
grep -Fxq 'etc/lite2api/lite2api.env' <<<"$archive_listing"
if grep -Eq '^(etc/cliproxyapi|var/lib/cliproxyapi/auths)(/|$)' <<<"$archive_listing"; then
    grep -Fxq 'etc/cliproxyapi/config.yaml' <<<"$archive_listing"
    grep -Fxq 'etc/cliproxyapi/cliproxyapi.env' <<<"$archive_listing"
    grep -Eq '^var/lib/cliproxyapi/auths/?$' <<<"$archive_listing"
fi
decrypt "$backup_dir/systemd-state.tar.age" |
    tar -xOf - etc/lite2api/lite2api.env |
    awk -F= '
        $1 == "LITE2API_API_KEYS" || $1 == "LITE2API_ADMIN_TOKEN" {
            value = substr($0, index($0, "=") + 1)
            lower = tolower(value)
            if (length(value) >= 32 && value !~ /[[:space:]]/ &&
                lower !~ /^replace[-_]with/ && lower !~ /^change[-_]me/) {
                valid[$1] = 1
            }
        }
        END {
            exit !(valid["LITE2API_API_KEYS"] && valid["LITE2API_ADMIN_TOKEN"])
        }
    '
printf 'Backup checksum, encrypted archive, and Lite2API state structure verified.\n'
