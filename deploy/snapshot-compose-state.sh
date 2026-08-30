#!/usr/bin/env bash
set -euo pipefail

umask 077

if [[ $# -ne 2 ]]; then
    echo 'usage: snapshot-compose-state.sh OUTPUT_DIRECTORY AGE_RECIPIENT' >&2
    exit 64
fi

output_dir=$1
age_recipient=$2
project_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
environment_file=${LITE2API_COMPOSE_ENV_FILE:-$project_dir/.env}
channel_runtime_dir=${LITE2API_CHANNEL_RUNTIME_DIR:-$project_dir/channels/runtime}
lite2api_volume_override=${LITE2API_DATA_VOLUME:-}
grok_data_volume_override=${GROK2API_DATA_VOLUME:-}
grok_quality_volume_override=${GROK2API_QUALITY_VOLUME:-}
alpine_image=alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce

for command in age docker install mktemp mv sha256sum; do
    command -v "$command" >/dev/null || {
        echo "missing required command: $command" >&2
        exit 1
    }
done
[[ $age_recipient == age1* || $age_recipient == age-plugin-* ]] || {
    echo 'AGE_RECIPIENT must be an age public recipient' >&2
    exit 1
}
[[ -f $environment_file && ! -L $environment_file ]] || {
    echo "Compose environment file is missing or unsafe: $environment_file" >&2
    exit 1
}
[[ ! -e $output_dir && ! -L $output_dir ]] || {
    echo "refusing to overwrite snapshot destination: $output_dir" >&2
    exit 1
}
if [[ -e $channel_runtime_dir ]]; then
    [[ -d $channel_runtime_dir && ! -L $channel_runtime_dir ]] || {
        echo "channel runtime path is not a safe directory: $channel_runtime_dir" >&2
        exit 1
    }
fi

resolve_volume() {
    local label=$1 explicit_name=$2 override_name=$3 required=$4
    if [[ -n $explicit_name ]]; then
        docker volume inspect "$explicit_name" >/dev/null
        printf '%s\n' "$explicit_name"
        return
    fi
    local -a matching_volumes=()
    mapfile -t matching_volumes < <(docker volume ls \
        --filter "label=com.docker.compose.volume=$label" \
        --format '{{.Name}}')
    case ${#matching_volumes[@]} in
        0)
            if [[ $required == true ]]; then
                echo "set $override_name to the exact Compose volume name (none found for $label)" >&2
                exit 1
            fi
            ;;
        1) printf '%s\n' "${matching_volumes[0]}" ;;
        *)
            echo "set $override_name to the exact Compose volume name (multiple found for $label)" >&2
            exit 1
            ;;
    esac
}

lite2api_volume=$(resolve_volume lite2api-data "$lite2api_volume_override" LITE2API_DATA_VOLUME true)
grok_data_volume=$(resolve_volume lite2api-grok-data "$grok_data_volume_override" GROK2API_DATA_VOLUME false)
grok_quality_volume=$(resolve_volume lite2api-grok-quality "$grok_quality_volume_override" GROK2API_QUALITY_VOLUME false)

output_parent=$(dirname -- "$output_dir")
if [[ -e $output_parent ]]; then
    [[ -d $output_parent && ! -L $output_parent ]] || {
        echo "snapshot parent is not a safe directory: $output_parent" >&2
        exit 1
    }
else
    install -d -m 0700 "$output_parent"
fi
stage_dir=$(mktemp -d "$output_parent/.lite2api-snapshot.XXXXXX")
artifacts=()
cleanup() {
    local status=$?
    set +e
    trap - EXIT
    if [[ -d $stage_dir ]]; then
        for artifact in "${artifacts[@]}"; do rm -f -- "$stage_dir/$artifact"; done
        rm -f -- "$stage_dir/SHA256SUMS" "$stage_dir/METADATA"
        rmdir "$stage_dir" 2>/dev/null || true
    fi
    exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

export_volume() {
    local volume=$1 artifact=$2
    artifacts+=("$artifact")
    docker run --rm --network none --read-only --pids-limit 64 \
        --cap-drop ALL --cap-add DAC_READ_SEARCH --security-opt no-new-privileges:true \
        --volume "$volume:/source:ro" \
        "$alpine_image" sh -ec '
            first_link=$(find /source -type l -print -quit)
            [ -z "$first_link" ] || {
                echo "refusing symlinked volume content" >&2
                exit 65
            }
            exec tar -C /source -cf - .
        ' |
        age -r "$age_recipient" -o "$stage_dir/$artifact"
}

export_volume "$lite2api_volume" lite2api-data.tar.age
if [[ -n $grok_data_volume ]]; then
    export_volume "$grok_data_volume" grok2api-data.tar.age
fi
if [[ -n $grok_quality_volume ]]; then
    export_volume "$grok_quality_volume" grok2api-quality.tar.age
fi
if [[ -d $channel_runtime_dir ]]; then
    artifacts+=(channel-runtime.tar.age)
    docker run --rm --network none --read-only --pids-limit 64 \
        --cap-drop ALL --cap-add DAC_READ_SEARCH --security-opt no-new-privileges:true \
        --volume "$channel_runtime_dir:/source:ro" \
        "$alpine_image" sh -ec '
            first_link=$(find /source -type l -print -quit)
            [ -z "$first_link" ] || {
                echo "refusing symlinked channel runtime content" >&2
                exit 65
            }
            exec tar -C /source -cf - .
        ' |
        age -r "$age_recipient" -o "$stage_dir/channel-runtime.tar.age"
fi
artifacts+=(environment.age)
age -r "$age_recipient" -o "$stage_dir/environment.age" "$environment_file"
(
    cd "$stage_dir"
    for artifact in "${artifacts[@]}"; do sha256sum "$artifact"; done >SHA256SUMS
)
printf 'format=lite2api-compose-state-v2\ncreated_utc=%s\nlite2api_volume=%s\ngrok_data_volume=%s\ngrok_quality_volume=%s\nsource_commit=%s\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$lite2api_volume" "$grok_data_volume" "$grok_quality_volume" \
    "$(git -C "$project_dir" rev-parse --verify HEAD 2>/dev/null || printf unknown)" \
    >"$stage_dir/METADATA"
chmod 0600 "$stage_dir"/*
mv -- "$stage_dir" "$output_dir"

printf 'Encrypted Compose core, environment, and available channel state snapshot created: %s\n' "$output_dir"
printf 'Copy it off-host, then run verify-compose-snapshot.sh before relying on it.\n'
