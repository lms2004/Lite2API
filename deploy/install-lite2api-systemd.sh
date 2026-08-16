#!/usr/bin/env bash
set -euo pipefail

umask 077

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
service_file="$project_dir/deploy/lite2api.service"
config_file=/etc/lite2api/config.json
binary_target=/usr/local/bin/lite2api

if [[ $(id -u) -ne 0 ]]; then
    echo 'run this installer as root' >&2
    exit 1
fi

for command in git install mktemp systemctl curl sha256sum; do
    command -v "$command" >/dev/null || {
        echo "missing required command: $command" >&2
        exit 1
    }
done

[[ -f $config_file ]] || {
    echo "missing $config_file" >&2
    exit 1
}
[[ -f $service_file ]] || {
    echo "missing $service_file" >&2
    exit 1
}

go_bin=${GO_BIN:-}
if [[ -z $go_bin && -x /root/toolchains/go1.24.4/bin/go ]]; then
    go_bin=/root/toolchains/go1.24.4/bin/go
fi
if [[ -z $go_bin ]]; then
    go_bin=$(command -v go || true)
fi
[[ -x $go_bin ]] || {
    echo 'Go 1.23 or newer is required; set GO_BIN to its absolute path' >&2
    exit 1
}

go_version=$(GOTOOLCHAIN=local "$go_bin" env GOVERSION)
go_minor=${go_version#go1.}
go_minor=${go_minor%%.*}
if [[ ! $go_minor =~ ^[0-9]+$ || $go_minor -lt 23 ]]; then
    echo "$go_bin is $go_version; Go 1.23 or newer is required" >&2
    exit 1
fi

commit=$(git -C "$project_dir" rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
version="deployed-$(date -u +%Y%m%d)-$commit"
binary_tmp=$(mktemp)
trap 'rm -f "$binary_tmp"' EXIT

(
    cd "$project_dir"
    GOTOOLCHAIN=local "$go_bin" build -trimpath \
        -ldflags="-s -w -X main.version=$version" \
        -o "$binary_tmp" ./cmd/lite2api
)

"$binary_tmp" -check-config -config "$config_file"
install -o root -g root -m 755 "$binary_tmp" "$binary_target"
install -o root -g root -m 644 "$service_file" /etc/systemd/system/lite2api.service

systemctl daemon-reload
systemctl enable lite2api.service >/dev/null
systemctl restart lite2api.service

for _ in {1..20}; do
    if curl -fsS --max-time 2 -o /dev/null http://127.0.0.1:45679/health; then
        break
    fi
    sleep 0.25
done

systemctl is-active --quiet lite2api.service
curl -fsS --max-time 5 -o /dev/null http://127.0.0.1:45679/health

printf 'Lite2API %s installed and healthy.\n' "$version"
printf 'Binary SHA-256: '
sha256sum "$binary_target" | awk '{print $1}'
