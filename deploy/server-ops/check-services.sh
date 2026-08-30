#!/usr/bin/env bash
set -uo pipefail

umask 077

environment_file=${LITE2API_ENV_FILE:-/etc/lite2api/lite2api.env}
config_file=${LITE2API_CONFIG_FILE:-/etc/lite2api/config.json}
public_origin=${LITE2API_PUBLIC_ORIGIN:-}
public_api_base=${LITE2API_PUBLIC_API_BASE:-}
expected_public_ip=${LITE2API_EXPECTED_PUBLIC_IP:-}
tls_certificate=${LITE2API_TLS_CERTIFICATE:-}
require_bbr=${LITE2API_REQUIRE_BBR:-false}
require_oauth_ready=${LITE2API_REQUIRE_OAUTH_READY:-false}
require_data_plane_ready=${LITE2API_REQUIRE_DATA_PLANE_READY:-false}
nginx_allowlist=${LITE2API_NGINX_ALLOWLIST:-/etc/nginx/snippets/lite2api-admin-allowlist.conf}
failures=0

header_dir=$(mktemp -d /tmp/lite2api-service-check.XXXXXX)
# Invoked indirectly by the signal/EXIT trap.
# shellcheck disable=SC2317
cleanup() {
    rm -f -- "$header_dir/cliproxy-management.header" \
        "$header_dir/cliproxy-api.header" "$header_dir/lite2api-admin.header"
    rmdir "$header_dir" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

check() {
    local label=$1
    shift
    if "$@"; then
        printf '[OK]   %s\n' "$label"
    else
        printf '[FAIL] %s\n' "$label" >&2
        failures=$((failures + 1))
    fi
}

skip() {
    printf '[SKIP] %s\n' "$1"
}

env_value() {
    local name=$1
    [[ -f $environment_file ]] || return 0
    sed -n "s/^${name}=//p" "$environment_file" | tail -n1
}

service_exists() {
    systemctl cat "$1" >/dev/null 2>&1
}

for command in curl grep jq sed ss systemctl; do
    if ! command -v "$command" >/dev/null; then
        printf '[FAIL] required command missing: %s\n' "$command" >&2
        failures=$((failures + 1))
    fi
done
if (( failures > 0 )); then
    exit "$failures"
fi

if [[ ! -f $environment_file || -L $environment_file ]]; then
    printf '[FAIL] environment file missing or unsafe: %s\n' "$environment_file" >&2
    exit 1
fi

for service in lite2api cliproxyapi; do
    if service_exists "$service.service"; then
        check "$service active" systemctl is-active --quiet "$service.service"
        check "$service enabled" systemctl is-enabled --quiet "$service.service"
    elif [[ $service == lite2api ]]; then
        printf '[FAIL] lite2api.service is not installed\n' >&2
        failures=$((failures + 1))
    else
        skip 'cliproxyapi.service is not installed (OAuth adapter optional)'
    fi
done

if service_exists nginx.service && command -v nginx >/dev/null; then
    check 'Nginx active' systemctl is-active --quiet nginx.service
    check 'Nginx configuration' nginx -t
else
    skip 'Nginx is not installed'
fi
if service_exists certbot.timer; then
    check 'certbot timer active' systemctl is-active --quiet certbot.timer
    check 'certbot timer enabled' systemctl is-enabled --quiet certbot.timer
fi
if service_exists v2ray.service && [[ -x /usr/local/lib/v2ray-current/v2ray && -f /etc/v2ray/config.json ]]; then
    check 'V2Fly active' systemctl is-active --quiet v2ray.service
    check 'V2Fly configuration' env V2RAY_LOCATION_ASSET=/usr/local/lib/v2ray-current \
        /usr/local/lib/v2ray-current/v2ray test -c /etc/v2ray/config.json
fi

check 'Lite2API production configuration' /usr/local/bin/lite2api \
    -check-config -config "$config_file"
check 'Lite2API loopback liveness' curl -fsS --max-time 5 -o /dev/null \
    http://127.0.0.1:45679/livez
if [[ $require_data_plane_ready == true ]]; then
    check 'Lite2API data-plane readiness' curl -fsS --max-time 5 -o /dev/null \
        http://127.0.0.1:45679/readyz
else
    skip 'Lite2API data-plane readiness (set LITE2API_REQUIRE_DATA_PLANE_READY=true to enforce)'
fi

cliproxy_management_key=$(env_value CLIPROXYAPI_MANAGEMENT_KEY)
cliproxy_api_key=$(env_value CLIPROXYAPI_KEY)
lite2api_admin_token=$(env_value LITE2API_ADMIN_TOKEN)
printf 'Authorization: Bearer %s\n' "$cliproxy_management_key" >"$header_dir/cliproxy-management.header"
printf 'Authorization: Bearer %s\n' "$cliproxy_api_key" >"$header_dir/cliproxy-api.header"
printf 'Authorization: Bearer %s\n' "$lite2api_admin_token" >"$header_dir/lite2api-admin.header"
chmod 0600 "$header_dir"/*.header

if service_exists cliproxyapi.service; then
    if [[ -z $cliproxy_management_key || -z $cliproxy_api_key ]]; then
        printf '[FAIL] OAuth adapter keys are missing from %s\n' "$environment_file" >&2
        failures=$((failures + 1))
    else
        check 'OAuth adapter liveness' curl -fsS --max-time 5 -o /dev/null \
            http://127.0.0.1:45682/healthz
        check 'OAuth adapter management authentication' curl -fsS --max-time 5 -o /dev/null \
            -H "@$header_dir/cliproxy-management.header" \
            http://127.0.0.1:45682/v0/management/auth-files
        check 'OAuth adapter model authentication' curl -fsS --max-time 5 -o /dev/null \
            -H "@$header_dir/cliproxy-api.header" \
            http://127.0.0.1:45682/v1/models
    fi
fi

if [[ -n $lite2api_admin_token ]]; then
    if service_exists cliproxyapi.service || [[ -n $cliproxy_management_key ]]; then
        oauth_account_state=$(curl -fsS --max-time 5 \
            -H "@$header_dir/lite2api-admin.header" \
            http://127.0.0.1:45679/admin/api/oauth/accounts || true)
        oauth_filter='(.data | type == "array") and (all(.data[];
          has("provider") and has("identity") and (.quota_windows | type == "array") and
          (all(.quota_windows[]; has("kind") and has("observed_at") and has("source") and
            (has("used_percentage") or has("remaining") or has("reset_at") or has("status")))) and
          (has("token") | not) and (has("access_token") | not) and
          (has("refresh_token") | not) and (has("cookie") | not) and
          (has("headers") | not) and (has("path") | not)))'
        if [[ $require_oauth_ready == true ]]; then
            oauth_filter="($oauth_filter) and (.data | length >= 1) and (any(.data[]; .ready == true))"
        fi
        if jq -e "$oauth_filter" >/dev/null <<<"$oauth_account_state"; then
            printf '[OK]   OAuth credential schema and redaction boundary\n'
        else
            printf '[FAIL] OAuth credential response is unavailable or unsafe\n' >&2
            failures=$((failures + 1))
        fi
    else
        skip 'OAuth credential schema (adapter not configured)'
    fi

    adapter_state=$(curl -fsS --max-time 5 \
        -H "@$header_dir/lite2api-admin.header" \
        http://127.0.0.1:45679/admin/api/adapters || true)
    if jq -e '
      (.data | type == "array") and
      (all(.data[]; (.operations | type == "array") and
        (.install_status != "installed" or
          (.runtime_status == "running" or .runtime_status == "stopped"))))
    ' >/dev/null <<<"$adapter_state"; then
        printf '[OK]   adapter catalog schema and runtime states\n'
    else
        printf '[FAIL] adapter catalog or runtime state is invalid\n' >&2
        failures=$((failures + 1))
    fi
else
    printf '[FAIL] LITE2API_ADMIN_TOKEN is empty\n' >&2
    failures=$((failures + 1))
fi

api_status=$(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' \
    http://127.0.0.1:45679/v1/models || true)
if [[ $api_status == 401 ]]; then
    printf '[OK]   unauthenticated local API rejected with 401\n'
else
    printf '[FAIL] unauthenticated local API returned %s\n' "$api_status" >&2
    failures=$((failures + 1))
fi

if [[ -f $nginx_allowlist ]]; then
    configured_cidrs=$(env_value LITE2API_ADMIN_ALLOWED_CIDRS)
    allowlist_ok=1
    expected_allow_count=0
    declare -A expected_allows=()
    IFS=',' read -r -a cidrs <<<"$configured_cidrs"
    for raw_cidr in "${cidrs[@]}"; do
        cidr=${raw_cidr//[[:space:]]/}
        [[ -n $cidr ]] || continue
        [[ -z ${expected_allows[$cidr]:-} ]] || continue
        expected_allows[$cidr]=1
        expected_allow_count=$((expected_allow_count + 1))
        grep -Fqx "allow $cidr;" "$nginx_allowlist" || allowlist_ok=0
    done
    actual_allow_count=$(grep -Ec '^allow [^;]+;$' "$nginx_allowlist" || true)
    (( expected_allow_count > 0 && actual_allow_count == expected_allow_count )) || allowlist_ok=0
    grep -Fqx 'deny all;' "$nginx_allowlist" || allowlist_ok=0
    if (( allowlist_ok == 1 )); then
        printf '[OK]   Nginx and Lite2API admin allowlists agree\n'
    else
        printf '[FAIL] Nginx allowlist has drifted from LITE2API_ADMIN_ALLOWED_CIDRS\n' >&2
        failures=$((failures + 1))
    fi
fi

if [[ -n $public_origin ]]; then
    public_origin=${public_origin%/}
    check 'public health endpoint' curl -fsS --max-time 10 -o /dev/null \
        "$public_origin/health"
fi
if [[ -n $public_api_base ]]; then
    public_api_base=${public_api_base%/}
    public_status=$(curl -sS --max-time 10 -o /dev/null -w '%{http_code}' \
        "$public_api_base/models" || true)
    if [[ $public_status == 401 ]]; then
        printf '[OK]   unauthenticated public API rejected with 401\n'
    else
        printf '[FAIL] unauthenticated public API returned %s\n' "$public_status" >&2
        failures=$((failures + 1))
    fi
fi
if [[ -n $expected_public_ip && -n $public_origin ]] && command -v dig >/dev/null; then
    public_host=${public_origin#*://}
    public_host=${public_host%%/*}
    public_host=${public_host%%:*}
    resolved_ip=$(dig +short "$public_host" A | tail -n1)
    if [[ $resolved_ip == "$expected_public_ip" ]]; then
        printf '[OK]   DNS resolves to %s\n' "$resolved_ip"
    else
        printf '[FAIL] DNS resolves to %s, expected %s\n' "${resolved_ip:-nothing}" "$expected_public_ip" >&2
        failures=$((failures + 1))
    fi
fi
if [[ -n $tls_certificate ]]; then
    check 'TLS certificate valid for at least 14 days' openssl x509 \
        -checkend 1209600 -noout -in "$tls_certificate"
fi
if [[ $require_bbr == true ]]; then
    cc=$(sysctl -n net.ipv4.tcp_congestion_control)
    qdisc=$(sysctl -n net.core.default_qdisc)
    if [[ $cc == bbr && $qdisc == fq ]]; then
        printf '[OK]   congestion control is bbr/fq\n'
    else
        printf '[FAIL] congestion control is %s/%s\n' "$cc" "$qdisc" >&2
        failures=$((failures + 1))
    fi
fi

printf '\nListeners:\n'
ss -lntp | grep -E ':(80|443|45679|45680|45681|45682)\b' || true

printf '\nService resource usage:\n'
systemctl show lite2api cliproxyapi \
    --property=Id,MemoryCurrent,TasksCurrent,CPUUsageNSec --no-pager 2>/dev/null || true

printf '\nEndpoint timing (connect / first byte / total seconds):\n'
curl -fsS --max-time 5 -o /dev/null \
    -H "@$header_dir/lite2api-admin.header" \
    -w 'adapter catalog: %{time_connect} / %{time_starttransfer} / %{time_total}\n' \
    http://127.0.0.1:45679/admin/api/adapters || true
if service_exists cliproxyapi.service && [[ -n $cliproxy_api_key ]]; then
    curl -fsS --max-time 5 -o /dev/null \
        -H "@$header_dir/cliproxy-api.header" \
        -w 'OAuth models: %{time_connect} / %{time_starttransfer} / %{time_total}\n' \
        http://127.0.0.1:45682/v1/models || true
fi

exit "$failures"
