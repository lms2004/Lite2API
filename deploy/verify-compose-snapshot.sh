#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
    echo 'usage: verify-compose-snapshot.sh SNAPSHOT_DIRECTORY' >&2
    exit 64
fi

snapshot_dir=$1
for command in age sha256sum tar; do
    command -v "$command" >/dev/null || {
        echo "missing required command: $command" >&2
        exit 1
    }
done
for file in lite2api-data.tar.age environment.age SHA256SUMS METADATA; do
    [[ -f $snapshot_dir/$file && ! -L $snapshot_dir/$file ]] || {
        echo "snapshot artifact is missing or unsafe: $file" >&2
        exit 1
    }
done
grep -Fxq 'format=lite2api-compose-state-v2' "$snapshot_dir/METADATA"

declare -A allowed_artifacts=(
    [lite2api-data.tar.age]=1
    [environment.age]=1
    [grok2api-data.tar.age]=1
    [grok2api-quality.tar.age]=1
    [channel-runtime.tar.age]=1
)
declare -A listed_artifacts=()
while read -r digest artifact extra; do
    [[ $digest =~ ^[0-9a-f]{64}$ && -z ${extra:-} && -n ${allowed_artifacts[$artifact]:-} ]] || {
        echo "unsafe checksum manifest entry: ${artifact:-missing}" >&2
        exit 1
    }
    [[ -z ${listed_artifacts[$artifact]:-} ]] || {
        echo "duplicate checksum manifest entry: $artifact" >&2
        exit 1
    }
    [[ -f $snapshot_dir/$artifact && ! -L $snapshot_dir/$artifact ]] || {
        echo "listed snapshot artifact is missing or unsafe: $artifact" >&2
        exit 1
    }
    listed_artifacts[$artifact]=1
done <"$snapshot_dir/SHA256SUMS"
[[ -n ${listed_artifacts[lite2api-data.tar.age]:-} && -n ${listed_artifacts[environment.age]:-} ]]

validate_archive_paths() {
    local entry normalized
    while IFS= read -r entry; do
        normalized=${entry#./}
        [[ $normalized != /* && $normalized != .. && $normalized != ../* && \
            $normalized != */../* && $normalized != */.. ]] || {
            echo "unsafe path in encrypted archive: $entry" >&2
            return 1
        }
    done
}

validate_archive_types() {
    local mode
    while read -r mode _; do
        case ${mode:0:1} in
            -|d) ;;
            *)
                echo "unsafe member type in encrypted archive: $mode" >&2
                return 1
                ;;
        esac
    done
}

(
    cd "$snapshot_dir"
    sha256sum --check SHA256SUMS
)
for artifact in lite2api-data.tar.age grok2api-data.tar.age grok2api-quality.tar.age channel-runtime.tar.age; do
    if [[ -n ${listed_artifacts[$artifact]:-} ]]; then
        age --decrypt "$snapshot_dir/$artifact" | tar -tf - | validate_archive_paths
        age --decrypt "$snapshot_dir/$artifact" | tar -tvf - | validate_archive_types
    fi
done
lite2api_listing=$(age --decrypt "$snapshot_dir/lite2api-data.tar.age" | tar -tf -)
grep -Eq '^([.]/)?config[.]json$' <<<"$lite2api_listing"
if [[ -n ${listed_artifacts[channel-runtime.tar.age]:-} ]]; then
    channel_listing=$(age --decrypt "$snapshot_dir/channel-runtime.tar.age" | tar -tf -)
    for required_channel_path in \
        cliproxyapi/config.yaml gemini-web2api/config.json grok2api/config.yaml; do
        grep -Eq "^([.]/)?${required_channel_path//./[.]}$" <<<"$channel_listing"
    done
fi
age --decrypt "$snapshot_dir/environment.age" |
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
printf 'Snapshot checksums, encrypted Compose state archives, and required environment keys verified.\n'
