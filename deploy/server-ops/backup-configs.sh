#!/usr/bin/env bash
set -euo pipefail

umask 077
backup_dir=/root/server-backups
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
archive="$backup_dir/server-config-$timestamp.tar.gz"

install -d -o root -g root -m 700 "$backup_dir"

tar -czf "$archive" \
    /etc/v2ray/config.json \
    /etc/systemd/system/v2ray.service.d/override.conf \
    /etc/sysctl.d/90-v2ray-network.conf \
    /etc/nginx/sites-available/sub2api.conf \
    /etc/lite2api/config.json \
    /etc/lite2api/lite2api.env \
    /etc/systemd/system/lite2api.service \
    /etc/cliproxyapi/config.yaml \
    /etc/cliproxyapi/cliproxyapi.env \
    /etc/systemd/system/cliproxyapi.service \
    /var/lib/cliproxyapi/auths \
    /etc/letsencrypt/renewal/sub2api.foresights.top.conf \
    /etc/letsencrypt/renewal-hooks/deploy/reload-nginx \
    /root/v2ray-client.json \
    /root/v2ray-share.txt

sha256sum "$archive" >"$archive.sha256"
chmod 600 "$archive" "$archive.sha256"
printf 'Created %s\n' "$archive"
printf 'This archive contains credentials and must remain private.\n'
