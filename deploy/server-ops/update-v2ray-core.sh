#!/usr/bin/env bash
set -euo pipefail

umask 077
repo=v2fly/v2ray-core
api_url="https://api.github.com/repos/$repo/releases/latest"
config=/etc/v2ray/config.json
current_link=/usr/local/lib/v2ray-current
tmp_dir=$(mktemp -d /tmp/v2ray-update.XXXXXX)

cleanup() {
    rm -rf -- "$tmp_dir"
}
trap cleanup EXIT

if [[ $(id -u) -ne 0 ]]; then
    echo 'run this updater as root' >&2
    exit 1
fi

for command in awk cmp curl env flock head id install jq ln mkdir mktemp mv \
    readlink rm sha256sum systemctl unzip; do
    command -v "$command" >/dev/null || {
        printf 'Missing required command: %s\n' "$command" >&2
        exit 1
    }
done
[[ -f $config && ! -L $config ]] || {
    echo "V2Fly configuration is missing or unsafe: $config" >&2
    exit 1
}
[[ ! -e $current_link || -L $current_link ]] || {
    echo "V2Fly current pointer is not a symlink: $current_link" >&2
    exit 1
}
exec 9>/run/lock/lite2api-v2ray-update.lock
flock -n 9 || {
    echo 'another V2Fly update is running' >&2
    exit 1
}

tag=${1:-}
if [[ -z $tag ]]; then
    tag=$(curl -fsSL "$api_url" | jq -r '.tag_name')
fi
if [[ ! $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    printf 'Invalid release tag: %s\n' "$tag" >&2
    exit 1
fi

target="/usr/local/lib/v2ray-$tag"
zip="$tmp_dir/v2ray-linux-64.zip"
digest="$tmp_dir/v2ray-linux-64.zip.dgst"
extract="$tmp_dir/extract"
base="https://github.com/$repo/releases/download/$tag"

printf 'Downloading V2Fly %s...\n' "$tag"
curl -fL --retry 3 -o "$zip" "$base/v2ray-linux-64.zip"
curl -fL --retry 3 -o "$digest" "$base/v2ray-linux-64.zip.dgst"

expected=$(awk -F'= ' '/^SHA2-256=/{print $2}' "$digest")
actual=$(sha256sum "$zip" | awk '{print $1}')
if [[ ! $expected =~ ^[0-9A-Fa-f]{64}$ || ${expected,,} != "$actual" ]]; then
    printf 'SHA-256 verification failed.\nExpected: %s\nActual:   %s\n' \
        "$expected" "$actual" >&2
    exit 1
fi
printf 'SHA-256 verified: %s\n' "$actual"

mkdir "$extract"
unzip -q "$zip" -d "$extract"
env V2RAY_LOCATION_ASSET="$extract" "$extract/v2ray" test -c "$config"

if [[ -e $target || -L $target ]]; then
    [[ -d $target && ! -L $target ]] || {
        echo "existing V2Fly release path is unsafe: $target" >&2
        exit 1
    }
    for release_file in v2ray geoip.dat geosite.dat; do
        if [[ ! -f $target/$release_file || -L $target/$release_file ]] || \
            ! cmp -s -- "$extract/$release_file" "$target/$release_file"; then
            echo "existing V2Fly release does not match the verified archive: $target/$release_file" >&2
            exit 1
        fi
    done
    if [[ -f $extract/geoip-only-cn-private.dat ]]; then
        if [[ ! -f $target/geoip-only-cn-private.dat || -L $target/geoip-only-cn-private.dat ]] || \
            ! cmp -s -- "$extract/geoip-only-cn-private.dat" "$target/geoip-only-cn-private.dat"; then
            echo "existing V2Fly optional asset does not match the verified archive" >&2
            exit 1
        fi
    fi
else
    install -d -o root -g root -m 755 "$target"
    install -o root -g root -m 755 "$extract/v2ray" "$target/v2ray"
    install -o root -g root -m 644 \
        "$extract/geoip.dat" "$extract/geosite.dat" "$target/"
    if [[ -f $extract/geoip-only-cn-private.dat ]]; then
        install -o root -g root -m 644 \
            "$extract/geoip-only-cn-private.dat" "$target/"
    fi
fi

old_target=
if [[ -L $current_link ]]; then
    old_target=$(readlink -f "$current_link" 2>/dev/null || true)
    [[ -n $old_target && -d $old_target && -x $old_target/v2ray ]] || {
        echo "existing V2Fly rollback pointer is dangling or incomplete: $current_link" >&2
        exit 1
    }
fi
[[ ! -e $current_link.new && ! -L $current_link.new ]] || {
    echo "stale V2Fly switch pointer exists: $current_link.new" >&2
    exit 1
}
ln -sfn "$target" "$current_link.new"
mv -Tf "$current_link.new" "$current_link"

if ! systemctl restart v2ray || ! systemctl is-active --quiet v2ray; then
    printf 'New core failed; rolling back to %s\n' "$old_target" >&2
    if [[ -n $old_target && -d $old_target ]]; then
        ln -sfn "$old_target" "$current_link.new"
        mv -Tf "$current_link.new" "$current_link"
        systemctl restart v2ray
    else
        rm -f -- "$current_link"
    fi
    exit 1
fi

running=$(/usr/local/lib/v2ray-current/v2ray version | head -n1)
printf 'Upgrade complete: %s\n' "$running"
