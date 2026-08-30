#!/usr/bin/env sh
set -eu

project_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
umask 077
environment_file="$project_dir/.env"
lock_dir="$project_dir/.bootstrap.lock"

command -v openssl >/dev/null 2>&1 || {
  echo 'openssl is required' >&2
  exit 1
}
[ ! -L "$environment_file" ] || {
  echo "refusing symlinked environment file: $environment_file" >&2
  exit 1
}
[ ! -e "$environment_file" ] || [ -f "$environment_file" ] || {
  echo "environment path is not a regular file: $environment_file" >&2
  exit 1
}
if ! mkdir "$lock_dir" 2>/dev/null; then
  echo "another bootstrap is running (or remove stale $lock_dir)" >&2
  exit 1
fi
environment_tmp=
cleanup() {
  [ -z "$environment_tmp" ] || rm -f -- "$environment_tmp"
  rmdir "$lock_dir" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

if [ ! -f "$environment_file" ]; then
  gateway_key=$(openssl rand -hex 32)
  admin_token=$(openssl rand -hex 32)
  environment_tmp=$(mktemp "$project_dir/.env.tmp.XXXXXX")
  {
    printf 'LITE2API_API_KEYS=%s\n' "$gateway_key"
    printf 'LITE2API_ADMIN_TOKEN=%s\n' "$admin_token"
    printf 'LITE2API_ADMIN_AUTO_LOGIN=false\n'
    printf 'LITE2API_ADMIN_ALLOWED_CIDRS=127.0.0.0/8,::1/128\n'
    printf 'LITE2API_TRUSTED_PROXY_CIDRS=127.0.0.0/8,::1/128\n'
    printf 'ATOMCODE2API_KEY=%s\n' "${ATOMCODE2API_KEY:-}"
    printf 'DEEPSEEK_API_KEY=%s\n' "${DEEPSEEK_API_KEY:-}"
  } > "$environment_tmp"
  chmod 0600 "$environment_tmp"
  mv -f "$environment_tmp" "$environment_file"
  environment_tmp=
fi

chmod 0600 "$environment_file"

read_env() {
  sed -n "s/^$1=//p" "$environment_file" | tail -n 1
}
validate_bootstrap_secret() {
  secret_name=$1
  secret_value=$(read_env "$secret_name")
  [ "${#secret_value}" -ge 32 ] || {
    echo "$secret_name must be a non-empty secret of at least 32 characters" >&2
    exit 1
  }
  case $secret_value in
    replace-with-*|REPLACE_WITH_*|*[[:space:]]*)
      echo "$secret_name still contains a placeholder or unsafe whitespace" >&2
      exit 1
      ;;
  esac
}
validate_bootstrap_secret LITE2API_API_KEYS
validate_bootstrap_secret LITE2API_ADMIN_TOKEN

printf '%s\n' "Bootstrap complete. Secrets in $project_dir/.env are present, private, and were not printed."
printf '%s\n' "Docker will initialize its private config volume from config.example.json."
printf '%s\n' "Set ATOMCODE2API_KEY/DEEPSEEK_API_KEY in .env, then run: docker compose up -d"
