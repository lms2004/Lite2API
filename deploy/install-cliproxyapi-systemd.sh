#!/usr/bin/env bash
set -euo pipefail

umask 077

version=v6.10.9-lite2api.8
commit=785b00c3127eea6aa207f1207ead8a2aa93690a3
build_date=2026-09-01
project_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
source_dir="$project_dir/third_party/cliproxyapi"
lite_env=/etc/lite2api/lite2api.env
binary_target=/usr/local/bin/cliproxyapi
unit_target=/etc/systemd/system/cliproxyapi.service
config_target=/etc/cliproxyapi/config.yaml
adapter_env=/etc/cliproxyapi/cliproxyapi.env
rollback_root=/var/lib/cliproxyapi-rollbacks
maintained_patch_paths=(
    deploy/patches/cliproxyapi-quota-snapshot.patch
    deploy/patches/cliproxyapi-routing-reliability.patch
    deploy/patches/cliproxyapi-auth-refresh.patch
    deploy/patches/cliproxyapi-credential-routing.patch
)
maintained_patch_sha256=(
    bbb9e08f18b210f9ddfbd958ee3cfb84cacc1b8d6b9c02b9c14ac8e23e490a68
    0a388faa429991cccc348bcddc31b0d350ddc21a36681da1bfbca5d052b39f37
    f5e482c127994eb31c9419bbce1e82fff39bfb893ef8c38b14e875b780a330a5
    d0e4923a56dabd12f7cbae421ca9c624bbd4e372fc22b47d95e7644c140bc131
)

if [[ $(id -u) -ne 0 ]]; then
    echo 'run this installer as root' >&2
    exit 1
fi

for command in git openssl curl install systemctl getent groupadd useradd \
    sed awk sha256sum mktemp mv flock chown chmod; do
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

assert_safe_directory_target /etc/cliproxyapi
assert_safe_directory_target /var/lib/cliproxyapi
assert_safe_directory_target /var/lib/cliproxyapi/auths
assert_safe_directory_target "$rollback_root"
assert_safe_file_target "$lite_env"
assert_safe_file_target "$binary_target"
assert_safe_file_target "$unit_target"
assert_safe_file_target "$config_target"
assert_safe_file_target "$adapter_env"
exec 9>/run/lock/lite2api-install.lock
flock -n 9 || {
    echo 'another Lite2API installer is running' >&2
    exit 1
}

[[ -f $lite_env ]] || {
    echo "missing $lite_env; run install-lite2api-systemd.sh first" >&2
    exit 1
}
getent group lite2api >/dev/null || {
    echo 'missing lite2api service group; run install-lite2api-systemd.sh first' >&2
    exit 1
}
[[ -f $source_dir/go.mod ]] || {
    echo 'CLIProxyAPI submodule is not initialized; run:' >&2
    echo '  git submodule update --init third_party/cliproxyapi' >&2
    exit 1
}
main_commit=$(git -C "$project_dir" rev-parse --verify HEAD)
for maintained_patch in "${maintained_patch_paths[@]}"; do
    git -C "$project_dir" cat-file -e "$main_commit:$maintained_patch" || {
        echo "selected Lite2API commit is missing maintained patch: $maintained_patch" >&2
        exit 1
    }
done

actual_commit=$(git -C "$source_dir" rev-parse HEAD)
[[ $actual_commit == "$commit" ]] || {
    echo "CLIProxyAPI source is $actual_commit, expected $commit" >&2
    exit 1
}
git -C "$source_dir" cat-file -e "$commit^{commit}"

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

get_env_value() {
    local file=$1 name=$2
    [[ -f $file ]] || return 0
    sed -n "s/^${name}=//p" "$file" | tail -n1
}

upsert_lite_env() {
    local name=$1 value=$2 temporary
    temporary=$(mktemp "${lite_env}.tmp.XXXXXX")
    awk -F= -v name="$name" '$1 != name { print }' "$lite_env" >"$temporary"
    printf '%s=%s\n' "$name" "$value" >>"$temporary"
    chown root:lite2api "$temporary"
    chmod 0640 "$temporary"
    mv -f -- "$temporary" "$lite_env"
}

read_routing_strategy() {
    local file=$1
    [[ -f $file ]] || return 0
    awk '
        $1 == "routing:" { in_routing = 1; next }
        in_routing && $1 == "strategy:" {
            value = tolower($2)
            gsub(/[^a-z-]/, "", value)
            print value
            exit
        }
        in_routing && $0 ~ /^[^[:space:]#]/ { exit }
    ' "$file"
}

api_key=$(get_env_value "$lite_env" CLIPROXYAPI_KEY)
management_key=$(get_env_value "$lite_env" CLIPROXYAPI_MANAGEMENT_KEY)
if [[ -z $management_key ]]; then
    management_key=$(get_env_value "$adapter_env" MANAGEMENT_PASSWORD)
fi
[[ -n $api_key ]] || api_key=$(openssl rand -hex 32)
[[ -n $management_key ]] || management_key=$(openssl rand -hex 32)
cliproxy_routing_strategy=$(read_routing_strategy "$config_target")
case $cliproxy_routing_strategy in
    round-robin|fill-first) ;;
    *) cliproxy_routing_strategy=round-robin ;;
esac

build_root=$(mktemp -d /tmp/cliproxyapi-build.XXXXXX)
build_tree=$build_root/tree
main_build_root=$(mktemp -d /tmp/lite2api-assets.XXXXXX)
main_tree=$main_build_root/tree
binary_tmp=$(mktemp /tmp/cliproxyapi-binary.XXXXXX)
config_tmp=$(mktemp /tmp/cliproxyapi-config.XXXXXX)
environment_tmp=$(mktemp /tmp/cliproxyapi-env.XXXXXX)
secrets_tmp=$(mktemp /tmp/cliproxyapi-secrets.XXXXXX)
management_header_tmp=$(mktemp /tmp/cliproxyapi-management-header.XXXXXX)
api_header_tmp=$(mktemp /tmp/cliproxyapi-api-header.XXXXXX)
rollback_dir=
deployment_started=0
was_active=0
was_enabled=0
lite_was_active=0
had_binary=0
had_unit=0
had_config=0
had_adapter_env=0

cleanup() {
    local status=$?
    set +e
    trap - EXIT
    if (( status != 0 && deployment_started == 1 )); then
        echo 'installation failed; restoring the previous CLIProxyAPI release' >&2
        systemctl stop cliproxyapi.service >/dev/null 2>&1 || true
        for item in \
            "binary:$had_binary:$rollback_dir/cliproxyapi:$binary_target:0755:root:root" \
            "unit:$had_unit:$rollback_dir/cliproxyapi.service:$unit_target:0644:root:root" \
            "config:$had_config:$rollback_dir/config.yaml:$config_target:0660:root:cliproxyapi" \
            "environment:$had_adapter_env:$rollback_dir/cliproxyapi.env:$adapter_env:0640:root:cliproxyapi"; do
            IFS=: read -r _ existed backup target mode owner group <<<"$item"
            if [[ $existed == 1 ]]; then
                install -o "$owner" -g "$group" -m "$mode" "$backup" "$target"
            else
                rm -f -- "$target"
            fi
        done
        install -o root -g lite2api -m 0640 "$rollback_dir/lite2api.env" "$lite_env"
        systemctl daemon-reload >/dev/null 2>&1 || true
        if (( was_enabled == 1 )); then
            systemctl enable cliproxyapi.service >/dev/null 2>&1 || true
        else
            systemctl disable cliproxyapi.service >/dev/null 2>&1 || true
        fi
        if (( was_active == 1 )); then
            systemctl start cliproxyapi.service >/dev/null 2>&1 || true
        fi
        if (( lite_was_active == 1 )); then
            systemctl restart lite2api.service >/dev/null 2>&1 || true
        fi
        echo "rollback artifacts: $rollback_dir" >&2
    fi
    git -C "$source_dir" worktree remove --force "$build_tree" >/dev/null 2>&1 || true
    git -C "$project_dir" worktree remove --force "$main_tree" >/dev/null 2>&1 || true
    rm -f -- "$binary_tmp" "$config_tmp" "$environment_tmp" "$secrets_tmp" \
        "$management_header_tmp" "$api_header_tmp"
    rmdir "$build_root" >/dev/null 2>&1 || true
    rmdir "$main_build_root" >/dev/null 2>&1 || true
    exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# A detached worktree guarantees that unrelated dirty or untracked submodule
# files can neither enter the binary nor masquerade as the fixed revision.
git -C "$project_dir" worktree add --detach "$main_tree" "$main_commit" >/dev/null
git -C "$source_dir" worktree add --detach "$build_tree" "$commit" >/dev/null
maintained_patches=(
    "$main_tree/deploy/patches/cliproxyapi-quota-snapshot.patch"
    "$main_tree/deploy/patches/cliproxyapi-routing-reliability.patch"
    "$main_tree/deploy/patches/cliproxyapi-auth-refresh.patch"
    "$main_tree/deploy/patches/cliproxyapi-credential-routing.patch"
)
config_template="$main_tree/channels/cliproxyapi/config.template.yaml"
service_file="$main_tree/deploy/cliproxyapi.service"
for index in "${!maintained_patches[@]}"; do
    actual_patch_sha256=$(sha256sum "${maintained_patches[$index]}" | awk '{print $1}')
    [[ $actual_patch_sha256 == "${maintained_patch_sha256[$index]}" ]] || {
        echo "maintained patch digest mismatch: ${maintained_patches[$index]}" >&2
        exit 1
    }
done
for maintained_patch in "${maintained_patches[@]}"; do
    git -C "$build_tree" apply --unidiff-zero --check "$maintained_patch"
    git -C "$build_tree" apply --unidiff-zero "$maintained_patch"
done
git -C "$build_tree" diff --check
(
    cd "$build_tree"
    GOTOOLCHAIN=local "$go_bin" build -buildvcs=false -trimpath \
        -ldflags="-s -w -X main.Version=$version -X main.Commit=$commit -X main.BuildDate=$build_date" \
        -o "$binary_tmp" ./cmd/server/
)

printf 'CLIPROXYAPI_KEY=%s\nMANAGEMENT_PASSWORD=%s\n' "$api_key" "$management_key" >"$secrets_tmp"
awk -v secrets_file="$secrets_tmp" -v routing_strategy="$cliproxy_routing_strategy" '
    function replace_all(line, token, value, pos) {
        while ((pos = index(line, token)) > 0) {
            line = substr(line, 1, pos - 1) value substr(line, pos + length(token))
        }
        return line
    }
    BEGIN {
        while ((getline entry < secrets_file) > 0) {
            pos = index(entry, "=")
            if (pos > 1) values[substr(entry, 1, pos - 1)] = substr(entry, pos + 1)
        }
        close(secrets_file)
    }
    {
        line = replace_all($0, "REPLACE_WITH_RANDOM_KEY", values["CLIPROXYAPI_KEY"])
        if (line == "auth-dir: \"/run/cliproxyapi/auths\"") line = "auth-dir: \"/var/lib/cliproxyapi/auths\""
        if (line == "  strategy: \"round-robin\"") line = "  strategy: \"" routing_strategy "\""
        print line
    }
' "$config_template" >"$config_tmp"
printf 'MANAGEMENT_PASSWORD=%s\n' "$management_key" >"$environment_tmp"
printf 'Authorization: Bearer %s\n' "$management_key" >"$management_header_tmp"
printf 'Authorization: Bearer %s\n' "$api_key" >"$api_header_tmp"

getent group cliproxyapi >/dev/null || groupadd --system cliproxyapi
id cliproxyapi >/dev/null 2>&1 || useradd --system --gid cliproxyapi \
    --home-dir /var/lib/cliproxyapi --shell /usr/sbin/nologin cliproxyapi
install -d -o root -g cliproxyapi -m 0750 /etc/cliproxyapi
install -d -o cliproxyapi -g cliproxyapi -m 0700 \
    /var/lib/cliproxyapi /var/lib/cliproxyapi/auths
# The adapter can write /var/lib/cliproxyapi. Store rollback material in a
# separate root-only tree so the running service cannot tamper with recovery.
install -d -o root -g root -m 0700 "$rollback_root"
rollback_dir=$(mktemp -d "$rollback_root/$(date -u +%Y%m%dT%H%M%SZ)-${commit:0:12}.XXXXXX")
chown root:root "$rollback_dir"
chmod 0700 "$rollback_dir"

if systemctl is-active --quiet cliproxyapi.service; then was_active=1; fi
if systemctl is-enabled --quiet cliproxyapi.service; then was_enabled=1; fi
if systemctl is-active --quiet lite2api.service; then lite_was_active=1; fi
if [[ -e $binary_target ]]; then had_binary=1; install -m 0755 "$binary_target" "$rollback_dir/cliproxyapi"; fi
if [[ -e $unit_target ]]; then had_unit=1; install -m 0644 "$unit_target" "$rollback_dir/cliproxyapi.service"; fi
if [[ -e $config_target ]]; then had_config=1; install -m 0600 "$config_target" "$rollback_dir/config.yaml"; fi
if [[ -e $adapter_env ]]; then had_adapter_env=1; install -m 0600 "$adapter_env" "$rollback_dir/cliproxyapi.env"; fi
install -m 0600 "$lite_env" "$rollback_dir/lite2api.env"

deployment_started=1
install -o root -g root -m 0755 "$binary_tmp" "$binary_target"
install -o root -g cliproxyapi -m 0660 "$config_tmp" "$config_target"
install -o root -g cliproxyapi -m 0640 "$environment_tmp" "$adapter_env"
install -o root -g root -m 0644 "$service_file" "$unit_target"
upsert_lite_env CLIPROXYAPI_KEY "$api_key"
upsert_lite_env CLIPROXYAPI_MANAGEMENT_KEY "$management_key"
upsert_lite_env CLIPROXYAPI_MANAGEMENT_URL http://127.0.0.1:45682

systemctl daemon-reload
systemctl enable cliproxyapi.service >/dev/null
systemctl restart cliproxyapi.service
systemctl try-restart lite2api.service

healthy=0
for _ in {1..20}; do
    if curl -fsS --max-time 2 -o /dev/null http://127.0.0.1:45682/healthz; then
        healthy=1
        break
    fi
    sleep 0.5
done
(( healthy == 1 ))
curl -fsS --max-time 5 -o /dev/null -H "@$management_header_tmp" \
    http://127.0.0.1:45682/v0/management/auth-files
curl -fsS --max-time 5 -o /dev/null -H "@$api_header_tmp" \
    http://127.0.0.1:45682/v1/models
systemctl is-active --quiet cliproxyapi.service
deployment_started=0

printf 'CLIProxyAPI %s installed from exact revision %s\n' "$version" "$commit"
printf 'Binary SHA-256: '
sha256sum "$binary_target" | awk '{print $1}'
printf 'OAuth adapter health and both authentication paths passed.\n'
printf 'Maintained quota, routing, and auth-refresh reliability patches: applied\n'
printf 'Rollback snapshot: %s\n' "$rollback_dir"
