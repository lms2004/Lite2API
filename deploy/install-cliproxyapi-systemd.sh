#!/usr/bin/env bash
set -euo pipefail

umask 077

version=v6.10.9-lite2api.4
commit=785b00c3127eea6aa207f1207ead8a2aa93690a3
build_date=2026-08-16
project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
source_dir="$project_dir/third_party/cliproxyapi"
lite_env=/etc/lite2api/lite2api.env
service_file="$project_dir/deploy/cliproxyapi.service"
config_template="$project_dir/channels/cliproxyapi/config.template.yaml"
quota_patch="$project_dir/deploy/patches/cliproxyapi-quota-snapshot.patch"

if [[ $(id -u) -ne 0 ]]; then
    echo 'run this installer as root' >&2
    exit 1
fi

for command in git go openssl curl install systemctl getent groupadd useradd \
    sed awk sha256sum mktemp; do
    command -v "$command" >/dev/null || {
        echo "missing required command: $command" >&2
        exit 1
    }
done

if [[ ! -f $lite_env ]]; then
    echo "missing $lite_env; deploy Lite2API first" >&2
    exit 1
fi

if [[ ! -f $source_dir/go.mod ]]; then
    echo 'CLIProxyAPI submodule is not initialized; run:' >&2
    echo '  git submodule update --init third_party/cliproxyapi' >&2
    exit 1
fi

if [[ ! -f $quota_patch ]]; then
    echo "missing maintained CLIProxyAPI patch: $quota_patch" >&2
    exit 1
fi

actual_commit=$(git -C "$source_dir" rev-parse HEAD)
if [[ $actual_commit != "$commit" ]]; then
    echo "CLIProxyAPI source is $actual_commit, expected $commit" >&2
    exit 1
fi

if git -C "$source_dir" apply --reverse --check "$quota_patch" >/dev/null 2>&1; then
    : # Maintained patch is already present in this working tree.
elif git -C "$source_dir" apply --check "$quota_patch" >/dev/null 2>&1; then
    git -C "$source_dir" apply "$quota_patch"
else
    echo 'CLIProxyAPI source does not cleanly match the maintained quota patch' >&2
    exit 1
fi

get_env_value() {
    local file=$1 name=$2
    [[ -f $file ]] || return 0
    sed -n "s/^${name}=//p" "$file" | tail -n1
}

upsert_env() {
    local file=$1 name=$2 value=$3 temporary
    temporary=$(mktemp "${file}.tmp.XXXXXX")
    awk -F= -v name="$name" '$1 != name { print }' "$file" >"$temporary"
    printf '%s=%s\n' "$name" "$value" >>"$temporary"
    chown root:root "$temporary"
    chmod 600 "$temporary"
    mv -f "$temporary" "$file"
}

api_key=$(get_env_value "$lite_env" CLIPROXYAPI_KEY)
management_key=$(get_env_value "$lite_env" CLIPROXYAPI_MANAGEMENT_KEY)
if [[ -z $management_key ]]; then
    management_key=$(get_env_value /etc/cliproxyapi/cliproxyapi.env MANAGEMENT_PASSWORD)
fi
[[ -n $api_key ]] || api_key=$(openssl rand -hex 32)
[[ -n $management_key ]] || management_key=$(openssl rand -hex 32)

binary_tmp=$(mktemp)
config_tmp=$(mktemp)
environment_tmp=$(mktemp)
trap 'rm -f "$binary_tmp" "$config_tmp" "$environment_tmp"' EXIT

(
    cd "$source_dir"
    GOTOOLCHAIN=auto go build -trimpath \
        -ldflags="-s -w -X main.Version=$version -X main.Commit=$commit -X main.BuildDate=$build_date" \
        -o "$binary_tmp" ./cmd/server/
)

sed \
    -e "s|REPLACE_WITH_RANDOM_KEY|$api_key|g" \
    -e 's|auth-dir: "/run/cliproxyapi/auths"|auth-dir: "/var/lib/cliproxyapi/auths"|' \
    "$config_template" >"$config_tmp"
printf 'MANAGEMENT_PASSWORD=%s\n' "$management_key" >"$environment_tmp"

getent group cliproxyapi >/dev/null || groupadd --system cliproxyapi
id cliproxyapi >/dev/null 2>&1 || useradd --system --gid cliproxyapi \
    --home-dir /var/lib/cliproxyapi --shell /usr/sbin/nologin cliproxyapi

install -d -o root -g cliproxyapi -m 750 /etc/cliproxyapi
install -d -o cliproxyapi -g cliproxyapi -m 700 \
    /var/lib/cliproxyapi /var/lib/cliproxyapi/auths
install -o root -g root -m 755 "$binary_tmp" /usr/local/bin/cliproxyapi
install -o root -g cliproxyapi -m 640 "$config_tmp" /etc/cliproxyapi/config.yaml
install -o root -g cliproxyapi -m 640 "$environment_tmp" /etc/cliproxyapi/cliproxyapi.env
install -o root -g root -m 644 "$service_file" /etc/systemd/system/cliproxyapi.service

upsert_env "$lite_env" CLIPROXYAPI_KEY "$api_key"
upsert_env "$lite_env" CLIPROXYAPI_MANAGEMENT_KEY "$management_key"
upsert_env "$lite_env" CLIPROXYAPI_MANAGEMENT_URL http://127.0.0.1:45682

systemctl daemon-reload
systemctl enable --now cliproxyapi.service
systemctl restart cliproxyapi.service
systemctl try-restart lite2api.service

for _ in {1..20}; do
    if curl -fsS --max-time 2 -o /dev/null http://127.0.0.1:45682/; then
        break
    fi
    sleep 0.5
done

curl -fsS --max-time 5 -o /dev/null \
    -H "Authorization: Bearer $management_key" \
    http://127.0.0.1:45682/v0/management/auth-files
curl -fsS --max-time 5 -o /dev/null \
    -H "Authorization: Bearer $api_key" \
    http://127.0.0.1:45682/v1/models
systemctl is-active --quiet cliproxyapi.service

printf 'CLIProxyAPI %s installed from %s\n' "$version" "$commit"
printf 'Binary SHA-256: '
sha256sum /usr/local/bin/cliproxyapi | awk '{print $1}'
printf 'OAuth adapter health and both authentication paths passed.\n'
printf 'Sanitized in-memory quota snapshot patch: applied\n'
