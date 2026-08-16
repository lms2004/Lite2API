#!/bin/sh
set -eu
umask 077

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
env_file="$root_dir/.env"
runtime_dir="$root_dir/channels/runtime"

if [ ! -f "$env_file" ]; then
  echo "missing $env_file; run deploy/bootstrap.sh first" >&2
  exit 1
fi

read_env() {
  sed -n "s/^$1=//p" "$env_file" | tail -n 1
}

ensure_hex() {
  name=$1
  bytes=$2
  value=$(read_env "$name")
  if [ -z "$value" ]; then
    value=$(openssl rand -hex "$bytes")
    printf '%s=%s\n' "$name" "$value" >> "$env_file"
  fi
}

ensure_base64() {
  name=$1
  value=$(read_env "$name")
  if [ -z "$value" ]; then
    value=$(openssl rand -base64 32)
    printf '%s=%s\n' "$name" "$value" >> "$env_file"
  fi
}

ensure_hex GEMINI_WEB2API_KEY 32
ensure_hex GROK2API_ADMIN_PASSWORD 24
ensure_hex GROK2API_JWT_SECRET 32
ensure_base64 GROK2API_CREDENTIAL_KEY
ensure_hex CLIPROXYAPI_KEY 32
ensure_hex CLIPROXYAPI_MANAGEMENT_KEY 32

gemini_key=$(read_env GEMINI_WEB2API_KEY)
grok_admin=$(read_env GROK2API_ADMIN_PASSWORD)
grok_jwt=$(read_env GROK2API_JWT_SECRET)
grok_credential=$(read_env GROK2API_CREDENTIAL_KEY)
cliproxy_key=$(read_env CLIPROXYAPI_KEY)

mkdir -p "$runtime_dir/gemini-web2api" "$runtime_dir/grok2api" \
  "$runtime_dir/cliproxyapi/auths"
chmod 700 "$runtime_dir" "$runtime_dir/gemini-web2api" "$runtime_dir/grok2api" \
  "$runtime_dir/cliproxyapi" "$runtime_dir/cliproxyapi/auths"

sed "s|__API_KEY__|$gemini_key|g" \
  "$root_dir/channels/gemini-web2api/config.template.json" \
  > "$runtime_dir/gemini-web2api/config.json"

sed \
  -e "s|__ADMIN_PASSWORD__|$grok_admin|g" \
  -e "s|__JWT_SECRET__|$grok_jwt|g" \
  -e "s|__CREDENTIAL_KEY__|$grok_credential|g" \
  "$root_dir/channels/grok2api/config.template.yaml" \
  > "$runtime_dir/grok2api/config.yaml"

sed "s|REPLACE_WITH_RANDOM_KEY|$cliproxy_key|g" \
  "$root_dir/channels/cliproxyapi/config.template.yaml" \
  > "$runtime_dir/cliproxyapi/config.yaml"

chmod 600 "$env_file" "$runtime_dir/gemini-web2api/config.json" \
  "$runtime_dir/grok2api/config.yaml" "$runtime_dir/cliproxyapi/config.yaml"
if [ "$(id -u)" -eq 0 ]; then
  chown -R 10001:10001 "$runtime_dir/gemini-web2api" "$runtime_dir/cliproxyapi"
fi
echo "channel runtime configuration is ready"
