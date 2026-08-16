#!/usr/bin/env bash
set -uo pipefail

failures=0

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

for service in nginx v2ray lite2api cliproxyapi; do
    check "$service active" systemctl is-active --quiet "$service"
    check "$service enabled" systemctl is-enabled --quiet "$service"
done

check 'certbot timer active' systemctl is-active --quiet certbot.timer
check 'certbot timer enabled' systemctl is-enabled --quiet certbot.timer
check 'Nginx configuration' nginx -t
check 'V2Fly configuration' env V2RAY_LOCATION_ASSET=/usr/local/lib/v2ray-current \
    /usr/local/lib/v2ray-current/v2ray test -c /etc/v2ray/config.json
check 'Lite2API production configuration' /usr/local/bin/lite2api \
    -check-config -config /etc/lite2api/config.json
check 'public health endpoint' curl -fsS --max-time 10 -o /dev/null \
    https://sub2api.foresights.top/health
check 'OAuth adapter loopback endpoint' curl -fsS --max-time 5 -o /dev/null \
    http://127.0.0.1:45682/

cliproxy_management_key=$(sed -n 's/^CLIPROXYAPI_MANAGEMENT_KEY=//p' \
    /etc/lite2api/lite2api.env | tail -n1)
cliproxy_api_key=$(sed -n 's/^CLIPROXYAPI_KEY=//p' \
    /etc/lite2api/lite2api.env | tail -n1)
lite2api_admin_token=$(sed -n 's/^LITE2API_ADMIN_TOKEN=//p' \
    /etc/lite2api/lite2api.env | tail -n1)
check 'OAuth adapter management authentication' curl -fsS --max-time 5 -o /dev/null \
    -H "Authorization: Bearer $cliproxy_management_key" \
    http://127.0.0.1:45682/v0/management/auth-files
check 'OAuth adapter model authentication' curl -fsS --max-time 5 -o /dev/null \
    -H "Authorization: Bearer $cliproxy_api_key" \
    http://127.0.0.1:45682/v1/models

oauth_account_state=$(curl -fsS --max-time 5 \
    -H "Authorization: Bearer $lite2api_admin_token" \
    http://127.0.0.1:45679/admin/api/oauth/accounts || true)
if command -v jq >/dev/null && jq -e '
    (.data | length >= 1) and
    (any(.data[]; .ready == true)) and
    (all(.data[]; has("provider") and has("identity") and
      (.quota_windows | type == "array") and
      (all(.quota_windows[]; has("kind") and has("observed_at") and has("source") and
        (has("used_percentage") or has("remaining") or has("reset_at") or has("status")))) and
      (has("token") | not) and (has("access_token") | not) and
      (has("refresh_token") | not) and (has("cookie") | not) and
      (has("headers") | not) and (has("path") | not)))
' >/dev/null <<<"$oauth_account_state"; then
    printf '[OK]   OAuth credential pool and quota snapshots visible and sanitized\n'
else
    printf '[FAIL] OAuth credential pool is empty, unavailable or unsafe\n' >&2
    failures=$((failures + 1))
fi

adapter_state=$(curl -fsS --max-time 5 \
    -H "Authorization: Bearer $lite2api_admin_token" \
    http://127.0.0.1:45679/admin/api/adapters || true)
if command -v jq >/dev/null && jq -e '
    (.data | length >= 20) and
    ([.data[] | select((.operations | length) == 0)] | length == 0) and
    ([.data[] | select(.install_status == "installed") |
      select(.runtime_status != "running" and .runtime_status != "stopped")] | length == 0)
' >/dev/null <<<"$adapter_state"; then
    printf '[OK]   adapter catalog, operation types and runtime probes\n'
else
    printf '[FAIL] adapter catalog or runtime probe state is invalid\n' >&2
    failures=$((failures + 1))
fi

api_status=$(curl -sS --max-time 10 -o /dev/null -w '%{http_code}' \
    https://sub2api.foresights.top/lite/v1/models || true)
if [[ $api_status == 401 ]]; then
    printf '[OK]   unauthenticated API rejected with 401\n'
else
    printf '[FAIL] unauthenticated API returned %s\n' "$api_status" >&2
    failures=$((failures + 1))
fi

resolved_ip=$(dig +short sub2api.foresights.top A | tail -n1)
if [[ $resolved_ip == 64.83.25.68 ]]; then
    printf '[OK]   DNS resolves to %s\n' "$resolved_ip"
else
    printf '[FAIL] DNS resolves to %s\n' "${resolved_ip:-nothing}" >&2
    failures=$((failures + 1))
fi

check 'TLS certificate valid for at least 14 days' openssl x509 \
    -checkend 1209600 -noout \
    -in /etc/letsencrypt/live/sub2api.foresights.top/cert.pem

cc=$(sysctl -n net.ipv4.tcp_congestion_control)
qdisc=$(sysctl -n net.core.default_qdisc)
if [[ $cc == bbr && $qdisc == fq ]]; then
    printf '[OK]   congestion control is bbr/fq\n'
else
    printf '[FAIL] congestion control is %s/%s\n' "$cc" "$qdisc" >&2
    failures=$((failures + 1))
fi

printf '\nListeners:\n'
ss -lntp | grep -E ':(80|443|10086|45679|45682|39264)\b' || true

printf '\nResource summary:\n'
free -h
df -h /

printf '\nService resource usage (current, no polling agent):\n'
systemctl show lite2api cliproxyapi \
    --property=Id,MemoryCurrent,TasksCurrent,CPUUsageNSec --no-pager

printf '\nEndpoint timing (connect / first byte / total seconds):\n'
curl -fsS --max-time 10 -o /dev/null \
    -w 'public health: %{time_connect} / %{time_starttransfer} / %{time_total}\n' \
    https://sub2api.foresights.top/health || true
curl -fsS --max-time 5 -o /dev/null \
    -H "Authorization: Bearer $lite2api_admin_token" \
    -w 'adapter catalog (cached): %{time_connect} / %{time_starttransfer} / %{time_total}\n' \
    http://127.0.0.1:45679/admin/api/adapters || true
curl -fsS --max-time 5 -o /dev/null \
    -H "Authorization: Bearer $cliproxy_api_key" \
    -w 'OAuth model list: %{time_connect} / %{time_starttransfer} / %{time_total}\n' \
    http://127.0.0.1:45682/v1/models || true

exit "$failures"
