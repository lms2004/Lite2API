#!/usr/bin/env bash
set -euo pipefail

umask 077

project_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
install_root=${LITE2API_INSTALL_ROOT:-}
if [[ -n $install_root ]]; then
    [[ $install_root =~ ^/[A-Za-z0-9/._-]+$ && $install_root != / && -d $install_root && ! -L $install_root ]] || { echo 'unsafe isolated install root' >&2; exit 1; }
    install_root=${install_root%/}
fi
config_dir=$install_root/etc/lite2api
config_file=$config_dir/config.json
environment_file=$config_dir/lite2api.env
binary_target=$install_root/usr/local/bin/lite2api
unit_target=$install_root/etc/systemd/system/lite2api.service
state_dir=$install_root/var/lib/lite2api
rollback_root=$install_root/var/lib/lite2api-rollbacks
acceptance_cases=${LITE2API_ACCEPTANCE_CASES:-$config_dir/acceptance.json}

if [[ $(id -u) -ne 0 ]]; then
    echo 'run this installer as root' >&2
    exit 1
fi

for command in git install mktemp mv systemctl curl sha256sum openssl getent \
    groupadd useradd awk sed flock chown chmod cp python3 timeout; do
    command -v "$command" >/dev/null || {
        echo "missing required command: $command" >&2
        exit 1
    }
done

assert_safe_file_target() {
    local path=$1
    [[ ! -L $path ]] || {
        echo "refusing symlinked deployment target: $path" >&2
        exit 1
    }
    [[ ! -e $path || -f $path ]] || {
        echo "deployment target is not a regular file: $path" >&2
        exit 1
    }
}

assert_safe_directory_target() {
    local path=$1
    [[ ! -L $path ]] || {
        echo "refusing symlinked deployment directory: $path" >&2
        exit 1
    }
    [[ ! -e $path || -d $path ]] || {
        echo "deployment directory is not a directory: $path" >&2
        exit 1
    }
}

assert_safe_directory_target "$config_dir"
assert_safe_directory_target "$state_dir"
assert_safe_directory_target "$rollback_root"
assert_safe_file_target "$config_file"
assert_safe_file_target "$environment_file"
assert_safe_file_target "$binary_target"
assert_safe_file_target "$unit_target"
install -d -m 0755 "$install_root/run/lock"
exec 9>"$install_root/run/lock/lite2api-install.lock"
flock -n 9 || {
    echo 'another Lite2API installer is running' >&2
    exit 1
}

expected_go_version=go1.26.5
go_bin=${GO_BIN:-$(command -v go || true)}
[[ -x $go_bin ]] || {
    echo "Go $expected_go_version is required; set GO_BIN to its absolute path" >&2
    exit 1
}

go_version=$(GOTOOLCHAIN=local "$go_bin" env GOVERSION)
if [[ $go_version != "$expected_go_version" ]]; then
    echo "$go_bin is $go_version; exact toolchain $expected_go_version is required" >&2
    exit 1
fi

commit=$(git -C "$project_dir" rev-parse --verify HEAD)
commit_short=${commit:0:12}
commit_date=$(git -C "$project_dir" show -s --format=%cs "$commit" | tr -d -)
version="deployed-$commit_date-$commit_short"
build_root=$(mktemp -d /tmp/lite2api-build.XXXXXX)
build_tree=$build_root/tree
binary_tmp=$(mktemp /tmp/lite2api-binary.XXXXXX)
environment_tmp=
config_tmp=
rollback_dir=
deployment_started=0
was_active=0
was_enabled=0
had_binary=0
had_unit=0
had_config_dir=0
had_state_dir=0
external_key_path=
had_external_key=0
service_quiesced=0
acceptance_tool=$build_tree/deploy/verify_service.py

cleanup() {
    local status=$?
    set +e
    trap - EXIT
    if (( status != 0 && deployment_started == 1 )); then
        echo "installation failed; restoring the previous Lite2API release" >&2
        systemctl stop lite2api.service >/dev/null 2>&1 || true
        rollback_files_ok=1
        rm -rf -- "$config_dir" "$state_dir"
        if (( had_config_dir == 1 )); then cp -a -- "$rollback_dir/config" "$config_dir" || rollback_files_ok=0; fi
        if (( had_state_dir == 1 )); then cp -a -- "$rollback_dir/state" "$state_dir" || rollback_files_ok=0; fi
        if [[ -n $external_key_path ]]; then
            if (( had_external_key == 1 )); then
                cp -a -- "$rollback_dir/external-client-keys" "$external_key_path" || rollback_files_ok=0
            else
                rm -f -- "$external_key_path" || rollback_files_ok=0
            fi
        fi
        if (( had_binary == 1 )); then
            install -o root -g root -m 755 "$rollback_dir/lite2api" "$binary_target"
        else
            rm -f -- "$binary_target"
        fi
        if (( had_unit == 1 )); then
            install -o root -g root -m 644 "$rollback_dir/lite2api.service" "$unit_target"
        else
            rm -f -- "$unit_target"
        fi
        systemctl daemon-reload >/dev/null 2>&1 || true
        if (( was_enabled == 1 )); then
            systemctl enable lite2api.service >/dev/null 2>&1 || true
        else
            systemctl disable lite2api.service >/dev/null 2>&1 || true
        fi
        if (( was_active == 1 )); then
            if systemctl start lite2api.service >/dev/null 2>&1; then
                if [[ -f $acceptance_cases && -f $environment_file ]]; then
                    python3 "$acceptance_tool" --url "$acceptance_url" --cases "$acceptance_cases" --env-file "$environment_file" --timeout 90 --output "$rollback_dir/rollback-acceptance.json" || rollback_files_ok=0
                else
                    echo 'rollback process restarted; business acceptance was not configured' >&2
                fi
            else
                rollback_files_ok=0
            fi
        fi
        (( rollback_files_ok == 1 )) || echo 'rollback is incomplete or business acceptance failed; inspect the retained snapshot' >&2
        echo "rollback artifacts: $rollback_dir" >&2
    elif (( status != 0 && service_quiesced == 1 && was_active == 1 )); then
        # Snapshot failure happened before mutation, but the old process must
        # still be returned to its original active state.
        systemctl start lite2api.service >/dev/null 2>&1 || echo 'could not restart the unchanged prior service' >&2
    fi
    git -C "$project_dir" worktree remove --force "$build_tree" >/dev/null 2>&1 || true
    [[ -z $environment_tmp ]] || rm -f -- "$environment_tmp"
    [[ -z $config_tmp ]] || rm -f -- "$config_tmp"
    rm -f -- "$binary_tmp"
    rm -f -- "$build_root/lite2api.service"
    rmdir "$build_root" >/dev/null 2>&1 || true
    exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# Build only the committed tree. Untracked or dirty developer files can never
# be mislabeled as the selected Git revision.
git -C "$project_dir" worktree add --detach "$build_tree" "$commit" >/dev/null
service_file="$build_tree/deploy/lite2api.service"
config_template="$build_tree/config.example.json"
[[ -f $service_file && -f $config_template && -f $acceptance_tool ]] || {
    echo 'selected commit is missing deployment assets' >&2
    exit 1
}

(
    cd "$build_tree"
    GOTOOLCHAIN=local CGO_ENABLED=0 "$go_bin" build -buildvcs=false -trimpath \
        -ldflags="-s -w -X main.version=$version" \
        -o "$binary_tmp" ./cmd/lite2api
)

# Validate the release's business scope before changing the running service.
candidate_config=$config_file
[[ -f $candidate_config ]] || candidate_config=$config_template
enabled_sources=$(python3 -c 'import json,sys; print(int(any(a.get("enabled", False) for a in json.load(open(sys.argv[1]))["accounts"])))' "$candidate_config")
acceptance_url=${LITE2API_ACCEPTANCE_URL:-$(python3 -c 'import json,sys; v=json.load(open(sys.argv[1]))["server"]["listen"]; host,port=v.rsplit(":",1); host=host.strip("[]"); host="127.0.0.1" if host in ("","0.0.0.0") else host; print("http://"+("["+host+"]" if ":" in host else host)+":"+port)' "$candidate_config")}
if (( enabled_sources == 1 )); then
    [[ -f $acceptance_cases && ! -L $acceptance_cases ]] || { echo 'configured sources require LITE2API_ACCEPTANCE_CASES or /etc/lite2api/acceptance.json' >&2; exit 1; }
fi

# Capture a quiescent recovery point before *any* configuration mutation.
install -d -o root -g root -m 0700 "$rollback_root"
rollback_dir=$(mktemp -d "$rollback_root/$(date -u +%Y%m%dT%H%M%SZ)-$commit_short.XXXXXX")
if systemctl is-active --quiet lite2api.service; then was_active=1; fi
if systemctl is-enabled --quiet lite2api.service; then was_enabled=1; fi
if [[ -e $binary_target ]]; then had_binary=1; cp -a -- "$binary_target" "$rollback_dir/lite2api"; fi
if [[ -e $unit_target ]]; then had_unit=1; cp -a -- "$unit_target" "$rollback_dir/lite2api.service"; fi
if [[ -f $config_file ]]; then
    external_key_path=$(python3 -c 'import json,os,sys; p=json.load(open(sys.argv[1]))["server"].get("client_keys_path") or "client_keys.json"; print(os.path.normpath(sys.argv[2]+p if os.path.isabs(p) else os.path.join(os.path.dirname(sys.argv[1]),p)))' "$config_file" "$install_root")
    case "$external_key_path" in "$config_dir"/*|"$state_dir"/*) external_key_path= ;; esac
    [[ -z $external_key_path ]] || assert_safe_file_target "$external_key_path"
fi
if (( was_active == 1 )); then systemctl stop lite2api.service; service_quiesced=1; fi
if [[ -d $config_dir ]]; then had_config_dir=1; cp -a -- "$config_dir" "$rollback_dir/config"; fi
if [[ -d $state_dir ]]; then had_state_dir=1; cp -a -- "$state_dir" "$rollback_dir/state"; fi
if [[ -n $external_key_path && -f $external_key_path ]]; then had_external_key=1; cp -a -- "$external_key_path" "$rollback_dir/external-client-keys"; fi
deployment_started=1

service_owner=lite2api
if [[ -z $install_root ]]; then
    getent group lite2api >/dev/null || groupadd --system lite2api
    id lite2api >/dev/null 2>&1 || useradd --system --gid lite2api --home-dir "$state_dir" --shell /usr/sbin/nologin lite2api
else
    service_owner=root
    install -d -m 0755 "$(dirname "$binary_target")" "$(dirname "$unit_target")"
    rendered_service=$build_root/lite2api.service
    sed -e "s#/etc/lite2api#$config_dir#g" -e "s#/usr/local/bin/lite2api#$binary_target#g" -e 's/^User=.*/User=root/' -e 's/^Group=.*/Group=root/' "$service_file" >"$rendered_service"
    service_file=$rendered_service
fi
# The service must create temporary config/key/log files in this directory,
# while the root-owned EnvironmentFile must not be replaceable by that service.
# Sticky ownership gives both properties without changing established paths.
install -d -o root -g "$service_owner" -m 1770 "$config_dir"
install -d -o "$service_owner" -g "$service_owner" -m 0700 "$state_dir"

if [[ ! -f $environment_file ]]; then
    api_key=$(openssl rand -hex 32)
    admin_token=$(openssl rand -hex 32)
    environment_tmp=$(mktemp "$config_dir/.lite2api.env.XXXXXX")
    printf '%s\n' \
        "LITE2API_API_KEYS=$api_key" \
        "LITE2API_ADMIN_TOKEN=$admin_token" \
        'LITE2API_ADMIN_AUTO_LOGIN=false' \
        'LITE2API_ADMIN_ALLOWED_CIDRS=127.0.0.0/8,::1/128' \
        'LITE2API_TRUSTED_PROXY_CIDRS=127.0.0.0/8,::1/128' \
        'ATOMCODE2API_KEY=' \
        'DEEPSEEK_API_KEY=' \
        'GROK2API_KEY=' \
        'GEMINI_WEB2API_KEY=' \
        'CLIPROXYAPI_KEY=' \
        'CLIPROXYAPI_MANAGEMENT_KEY=' >"$environment_tmp"
    chown root:"$service_owner" "$environment_tmp"
    chmod 0640 "$environment_tmp"
    mv -f -- "$environment_tmp" "$environment_file"
    environment_tmp=
else
    # Do not dereference a path raced into a symlink. Once root owns the entry,
    # the sticky directory prevents the service account from replacing it.
    chown -h root:"$service_owner" "$environment_file"
    assert_safe_file_target "$environment_file"
    chmod 0640 "$environment_file"
fi

if [[ ! -f $config_file ]]; then
    config_tmp=$(mktemp "$config_dir/.config.json.XXXXXX")
    install -o root -g root -m 0600 "$config_template" "$config_tmp"
    mv -f -- "$config_tmp" "$config_file"
    config_tmp=
else
    # Temporarily protect the entry before any root operation that dereferences
    # it; hand ownership back only after the regular-file check and chmod.
    chown -h root:"$service_owner" "$config_file"
    assert_safe_file_target "$config_file"
    chmod 0600 "$config_file"
fi
chown -h "$service_owner:$service_owner" "$config_file"

# Export only the two bootstrap values required for validation; never execute
# the environment file as shell code.
LITE2API_API_KEYS=$(sed -n 's/^LITE2API_API_KEYS=//p' "$environment_file" | tail -n1)
LITE2API_ADMIN_TOKEN=$(sed -n 's/^LITE2API_ADMIN_TOKEN=//p' "$environment_file" | tail -n1)
export LITE2API_API_KEYS LITE2API_ADMIN_TOKEN
[[ -n $LITE2API_API_KEYS && -n $LITE2API_ADMIN_TOKEN ]] || {
    echo "$environment_file must define non-empty gateway and admin keys" >&2
    exit 1
}
"$binary_tmp" -check-config -config "$config_file"

install -o root -g root -m 0755 "$binary_tmp" "$binary_target"
install -o root -g root -m 0644 "$service_file" "$unit_target"
systemctl daemon-reload
systemctl enable lite2api.service >/dev/null
systemctl restart lite2api.service

healthy=0
for _ in {1..20}; do
    # A fresh, fail-closed configuration has no enabled upstream and therefore
    # reports /health=503 by design. Deployment success only requires that the
    # validated process is live; readiness is an operator traffic-policy check.
    if curl -fsS --max-time 2 -o /dev/null "$acceptance_url/livez"; then
        healthy=1
        break
    fi
    sleep 0.25
done
(( healthy == 1 ))
systemctl is-active --quiet lite2api.service
curl -fsS --max-time 5 -o /dev/null "$acceptance_url/livez"
if (( enabled_sources == 1 )); then
    python3 "$acceptance_tool" --url "$acceptance_url" --cases "$acceptance_cases" --env-file "$environment_file" --timeout 90 --output "$rollback_dir/release-acceptance.json"
fi
deployment_started=0

printf 'Lite2API %s installed from committed tree %s.\n' "$version" "$commit"
if (( enabled_sources == 1 )); then printf 'Configured business calls and access boundary passed.\n'; else printf 'Bootstrap is live; business acceptance is pending until a source is configured.\n'; fi
printf 'Binary SHA-256: '
sha256sum "$binary_target" | awk '{print $1}'
printf 'Rollback snapshot: %s\n' "$rollback_dir"
