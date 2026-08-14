#!/usr/bin/env sh
set -eu

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
umask 077

if [ ! -f "$project_dir/.env" ]; then
  gateway_key=$(openssl rand -hex 32)
  admin_token=$(openssl rand -hex 32)
  {
    printf 'LITE2API_API_KEYS=%s\n' "$gateway_key"
    printf 'LITE2API_ADMIN_TOKEN=%s\n' "$admin_token"
    printf 'ATOMCODE2API_KEY=%s\n' "${ATOMCODE2API_KEY:-}"
    printf 'DEEPSEEK_API_KEY=%s\n' "${DEEPSEEK_API_KEY:-}"
  } > "$project_dir/.env"
fi

chmod 600 "$project_dir/.env"

printf '%s\n' "Bootstrap complete. Secrets were written to $project_dir/.env and were not printed."
printf '%s\n' "Docker will initialize its private config volume from config.example.json."
printf '%s\n' "Set ATOMCODE2API_KEY/DEEPSEEK_API_KEY in .env, then run: docker compose up -d"
