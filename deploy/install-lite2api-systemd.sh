#!/usr/bin/env bash
set -euo pipefail

umask 077

project_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
config_dir=/etc/lite2api
config_file=$config_dir/config.json
environment_file=$config_dir/lite2api.env
binary_target=/usr/local/bin/lite2api
unit_target=/etc/systemd/system/lite2api.service
state_dir=/var/lib/lite2api
rollback_root=/var/lib/lite2api-rollbacks

if [[ $(id -u) -ne 0 ]]; then
    echo 'run this installer as root' >&2
    exit 1
fi

for command in git install mktemp mv systemctl curl sha256sum openssl getent \
    groupadd useradd awk sed flock chown chmod; do
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
exec 9>/run/lock/lite2api-install.lock
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

cleanup() {
    local status=$?
    set +e
    trap - EXIT
    if (( status != 0 && deployment_started == 1 )); then
        echo "installation failed; restoring the previous Lite2API release" >&2
        systemctl stop lite2api.service >/dev/null 2>&1 || true
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
            systemctl start lite2api.service >/dev/null 2>&1 || true
        fi
        echo "rollback artifacts: $rollback_dir" >&2
    fi
    git -C "$project_dir" worktree remove --force "$build_tree" >/dev/null 2>&1 || true
    [[ -z $environment_tmp ]] || rm -f -- "$environment_tmp"
    [[ -z $config_tmp ]] || rm -f -- "$config_tmp"
    rm -f -- "$binary_tmp"
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
[[ -f $service_file && -f $config_template ]] || {
    echo 'selected commit is missing deployment assets' >&2
    exit 1
}

(
    cd "$build_tree"
    GOTOOLCHAIN=local "$go_bin" build -buildvcs=false -trimpath \
        -ldflags="-s -w -X main.version=$version" \
        -o "$binary_tmp" ./cmd/lite2api
)

getent group lite2api >/dev/null || groupadd --system lite2api
id lite2api >/dev/null 2>&1 || useradd --system --gid lite2api \
    --home-dir "$state_dir" --shell /usr/sbin/nologin lite2api
# The service must create temporary config/key/log files in this directory,
# while the root-owned EnvironmentFile must not be replaceable by that service.
# Sticky ownership gives both properties without changing established paths.
install -d -o root -g lite2api -m 1770 "$config_dir"
install -d -o lite2api -g lite2api -m 0700 "$state_dir"

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
    chown root:lite2api "$environment_tmp"
    chmod 0640 "$environment_tmp"
    mv -f -- "$environment_tmp" "$environment_file"
    environment_tmp=
else
    # Do not dereference a path raced into a symlink. Once root owns the entry,
    # the sticky directory prevents the service account from replacing it.
    chown -h root:lite2api "$environment_file"
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
    chown -h root:lite2api "$config_file"
    assert_safe_file_target "$config_file"
    chmod 0600 "$config_file"
fi
chown -h lite2api:lite2api "$config_file"

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

# Keep rollback material outside the service-owned state directory. A process
# running as lite2api must not be able to delete or replace its recovery point.
install -d -o root -g root -m 0700 "$rollback_root"
# mktemp prevents two rapid retries from reusing and overwriting one snapshot.
rollback_dir=$(mktemp -d "$rollback_root/$(date -u +%Y%m%dT%H%M%SZ)-$commit_short.XXXXXX")
chown root:root "$rollback_dir"
chmod 0700 "$rollback_dir"
if systemctl is-active --quiet lite2api.service; then was_active=1; fi
if systemctl is-enabled --quiet lite2api.service; then was_enabled=1; fi
if [[ -e $binary_target ]]; then
    had_binary=1
    install -o root -g root -m 0755 "$binary_target" "$rollback_dir/lite2api"
fi
if [[ -e $unit_target ]]; then
    had_unit=1
    install -o root -g root -m 0644 "$unit_target" "$rollback_dir/lite2api.service"
fi

deployment_started=1
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
    if curl -fsS --max-time 2 -o /dev/null http://127.0.0.1:45679/livez; then
        healthy=1
        break
    fi
    sleep 0.25
done
(( healthy == 1 ))
systemctl is-active --quiet lite2api.service
curl -fsS --max-time 5 -o /dev/null http://127.0.0.1:45679/livez
deployment_started=0

printf 'Lite2API %s installed from committed tree %s and is healthy.\n' "$version" "$commit"
printf 'Binary SHA-256: '
sha256sum "$binary_target" | awk '{print $1}'
printf 'Rollback snapshot: %s\n' "$rollback_dir"
