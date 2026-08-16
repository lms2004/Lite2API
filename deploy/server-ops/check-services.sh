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

for service in nginx v2ray lite2api; do
    check "$service active" systemctl is-active --quiet "$service"
    check "$service enabled" systemctl is-enabled --quiet "$service"
done

check 'certbot timer active' systemctl is-active --quiet certbot.timer
check 'certbot timer enabled' systemctl is-enabled --quiet certbot.timer
check 'Nginx configuration' nginx -t
check 'V2Fly configuration' env V2RAY_LOCATION_ASSET=/usr/local/lib/v2ray-current \
    /usr/local/lib/v2ray-current/v2ray test -c /etc/v2ray/config.json
check 'public health endpoint' curl -fsS --max-time 10 \
    https://sub2api.foresights.top/health

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
ss -lntp | grep -E ':(80|443|10086|45679|39264)\b' || true

printf '\nResource summary:\n'
free -h
df -h /

exit "$failures"
