#!/bin/sh
set -eu

config=/CLIProxyAPI/config.yaml
api_key=$(awk '
  $1 == "api-keys:" { in_keys = 1; next }
  in_keys && $1 == "-" {
    value = $2
    gsub(/^"|"$/, "", value)
    print value
    exit
  }
  in_keys && $0 ~ /^[^[:space:]#]/ { exit }
' "$config")

[ -n "$api_key" ]
status=$(
  printf 'GET /v1/models HTTP/1.1\r\nHost: 127.0.0.1\r\nAuthorization: Bearer %s\r\nConnection: close\r\n\r\n' "$api_key" |
    nc -w 5 127.0.0.1 45682 |
    awk 'NR == 1 { print $2; exit }'
)
[ "$status" = 200 ]
