#!/usr/bin/env bash
set -euo pipefail

umask 077

environment_file=${1:-/etc/lite2api/lite2api.env}
output_file=${2:-/etc/nginx/snippets/lite2api-admin-allowlist.conf}

if [[ $(id -u) -ne 0 ]]; then
    echo 'run this renderer as root' >&2
    exit 1
fi

for command in awk chown install mktemp mv nginx flock dirname; do
    command -v "$command" >/dev/null || {
        echo "missing required command: $command" >&2
        exit 1
    }
done
[[ -f $environment_file && ! -L $environment_file ]] || {
    echo "environment file must be a regular non-symlink: $environment_file" >&2
    exit 1
}
[[ ! -L $output_file ]] || {
    echo "refusing to replace symlinked Nginx allowlist: $output_file" >&2
    exit 1
}
[[ ! -e $output_file || -f $output_file ]] || {
    echo "Nginx allowlist target is not a regular file: $output_file" >&2
    exit 1
}

exec 9>/run/lock/lite2api-nginx-allowlist.lock
flock -n 9 || {
    echo 'another allowlist update is running' >&2
    exit 1
}

allowed_cidrs=$(awk -F= '$1 == "LITE2API_ADMIN_ALLOWED_CIDRS" { value = substr($0, index($0, "=") + 1) } END { print value }' "$environment_file")
[[ -n $allowed_cidrs ]] || {
    echo 'LITE2API_ADMIN_ALLOWED_CIDRS is empty' >&2
    exit 1
}

output_dir=$(dirname -- "$output_file")
if [[ -e $output_dir ]]; then
    [[ -d $output_dir && ! -L $output_dir ]] || {
        echo "Nginx allowlist directory is unsafe: $output_dir" >&2
        exit 1
    }
else
    install -d -o root -g root -m 0755 "$output_dir"
fi
temporary=$(mktemp "$output_dir/.lite2api-admin-allowlist.XXXXXX")
backup=
installed=0

cleanup() {
    local status=$?
    set +e
    trap - EXIT
    [[ -z $temporary ]] || rm -f -- "$temporary"
    if (( status != 0 && installed == 1 )); then
        if [[ -n $backup ]]; then
            mv -- "$backup" "$output_file"
            backup=
        else
            rm -f -- "$output_file"
        fi
        nginx -t >/dev/null 2>&1 || true
    fi
    [[ -z $backup ]] || rm -f -- "$backup"
    exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

printf '# Generated from %s; edit the environment file, not this file.\n' "$environment_file" >"$temporary"
has_ipv4_loopback=0
has_ipv6_loopback=0
declare -A seen=()
IFS=',' read -r -a cidrs <<<"$allowed_cidrs"
for raw_cidr in "${cidrs[@]}"; do
    cidr=${raw_cidr//[[:space:]]/}
    [[ -n $cidr && $cidr =~ ^[0-9A-Fa-f:.]+(/[0-9]{1,3})?$ ]] || {
        echo "invalid admin CIDR: $raw_cidr" >&2
        exit 1
    }
    [[ $cidr != 0.0.0.0/0 && $cidr != ::/0 ]] || {
        echo 'refusing a public Internet admin allowlist' >&2
        exit 1
    }
    [[ -z ${seen[$cidr]:-} ]] || continue
    seen[$cidr]=1
    [[ $cidr == 127.0.0.0/8 || $cidr == 127.0.0.1 || $cidr == 127.0.0.1/32 ]] && has_ipv4_loopback=1
    [[ $cidr == ::1 || $cidr == ::1/128 ]] && has_ipv6_loopback=1
    printf 'allow %s;\n' "$cidr" >>"$temporary"
done
(( has_ipv4_loopback == 1 && has_ipv6_loopback == 1 )) || {
    echo 'admin allowlist must retain IPv4 and IPv6 loopback' >&2
    exit 1
}
printf 'deny all;\n' >>"$temporary"
chmod 0644 "$temporary"

if [[ -e $output_file ]]; then
    backup=$(mktemp "$output_dir/.lite2api-admin-allowlist.backup.XXXXXX")
    install -o root -g root -m 0644 "$output_file" "$backup"
fi
chown root:root "$temporary"
mv -- "$temporary" "$output_file"
temporary=
installed=1
nginx -t
installed=0
printf 'Nginx admin allowlist updated atomically; run systemctl reload nginx after review.\n'
