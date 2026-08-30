#!/bin/sh
set -eu
umask 077

root_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
env_file="$root_dir/.env"
runtime_dir="$root_dir/channels/runtime"
lock_dir="$root_dir/.bootstrap-channels.lock"

if [ ! -f "$env_file" ]; then
  echo "missing $env_file; run deploy/bootstrap.sh first" >&2
  exit 1
fi
if [ -L "$env_file" ]; then
  echo "refusing to update symlinked environment file: $env_file" >&2
  exit 1
fi
if ! mkdir "$lock_dir" 2>/dev/null; then
  echo "another channel bootstrap is running (or remove stale $lock_dir)" >&2
  exit 1
fi
cleanup() {
  [ -z "${env_update_tmp:-}" ] || rm -f -- "$env_update_tmp"
  [ -z "${gemini_tmp:-}" ] || rm -f -- "$gemini_tmp"
  [ -z "${grok_tmp:-}" ] || rm -f -- "$grok_tmp"
  [ -z "${cliproxy_tmp:-}" ] || rm -f -- "$cliproxy_tmp"
  rmdir "$lock_dir" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

assert_safe_directory() {
  path=$1
  [ ! -L "$path" ] || {
    echo "refusing symlinked runtime directory: $path" >&2
    exit 1
  }
  [ ! -e "$path" ] || [ -d "$path" ] || {
    echo "runtime path is not a directory: $path" >&2
    exit 1
  }
}

assert_safe_file() {
  path=$1
  [ ! -L "$path" ] || {
    echo "refusing symlinked runtime configuration: $path" >&2
    exit 1
  }
  [ ! -e "$path" ] || [ -f "$path" ] || {
    echo "runtime configuration is not a regular file: $path" >&2
    exit 1
  }
}

read_env() {
  sed -n "s/^$1=//p" "$env_file" | tail -n 1
}

env_additions=
queue_env() {
  queued_name=$1
  queued_value=$2
  if [ -z "$env_additions" ]; then
    env_additions="$queued_name=$queued_value"
  else
    env_additions="$env_additions
$queued_name=$queued_value"
  fi
}

ensure_hex() {
  name=$1
  bytes=$2
  value=$(read_env "$name")
  if [ -z "$value" ]; then
    value=$(openssl rand -hex "$bytes")
    queue_env "$name" "$value"
  fi
  expected_length=$((bytes * 2))
  case $value in *[!0-9A-Fa-f]*)
    echo "$name must be exactly $expected_length hexadecimal characters; remove it to regenerate" >&2
    exit 1
  esac
  [ "${#value}" -eq "$expected_length" ] || {
    echo "$name must be exactly $expected_length hexadecimal characters; remove it to regenerate" >&2
    exit 1
  }
}

ensure_base64() {
  name=$1
  value=$(read_env "$name")
  if [ -z "$value" ]; then
    value=$(openssl rand -base64 32)
    queue_env "$name" "$value"
  fi
  case $value in *[!A-Za-z0-9+/=]*)
    echo "$name must be a single base64 value; remove it to regenerate" >&2
    exit 1
  esac
  [ "${#value}" -eq 44 ] || {
    echo "$name must encode 32 bytes as base64; remove it to regenerate" >&2
    exit 1
  }
}

ensure_value() {
  name=$1
  default_value=$2
  value=$(read_env "$name")
  if [ -z "$value" ]; then
    queue_env "$name" "$default_value"
  fi
}

read_routing_strategy() {
  file=$1
  [ -f "$file" ] || return 0
  awk '
    $1 == "routing:" { in_routing = 1; next }
    in_routing && $1 == "strategy:" {
      value = tolower($2)
      gsub(/[^a-z-]/, "", value)
      print value
      exit
    }
    in_routing && $0 ~ /^[^[:space:]#]/ { exit }
  ' "$file"
}

ensure_hex GEMINI_WEB2API_KEY 32
ensure_hex GROK2API_ADMIN_PASSWORD 24
ensure_hex GROK2API_JWT_SECRET 32
ensure_base64 GROK2API_CREDENTIAL_KEY
ensure_hex CLIPROXYAPI_KEY 32
ensure_hex CLIPROXYAPI_MANAGEMENT_KEY 32

if [ "$(id -u)" -eq 0 ]; then
  default_runtime_uid=10001
  default_runtime_gid=10001
else
  default_runtime_uid=$(id -u)
  default_runtime_gid=$(id -g)
fi
ensure_value CHANNEL_RUNTIME_UID "$default_runtime_uid"
ensure_value CHANNEL_RUNTIME_GID "$default_runtime_gid"

if [ -n "$env_additions" ]; then
  env_update_tmp=$(mktemp "$root_dir/.env.channels.XXXXXX")
  awk '1' "$env_file" > "$env_update_tmp"
  printf '%s\n' "$env_additions" >> "$env_update_tmp"
  chmod 0600 "$env_update_tmp"
  if [ "$(id -u)" -eq 0 ]; then
    chown --reference="$env_file" "$env_update_tmp"
  fi
  mv -f "$env_update_tmp" "$env_file"
  env_update_tmp=
fi
chmod 0600 "$env_file"

runtime_uid=$(read_env CHANNEL_RUNTIME_UID)
runtime_gid=$(read_env CHANNEL_RUNTIME_GID)
case $runtime_uid in ''|*[!0-9]*) echo "CHANNEL_RUNTIME_UID must be numeric" >&2; exit 1;; esac
case $runtime_gid in ''|*[!0-9]*) echo "CHANNEL_RUNTIME_GID must be numeric" >&2; exit 1;; esac
if [ "$runtime_uid" -eq 0 ] || [ "$runtime_gid" -eq 0 ]; then
  echo "channel containers must use a non-root UID and GID" >&2
  exit 1
fi
if [ "$(id -u)" -ne 0 ] && { [ "$runtime_uid" != "$(id -u)" ] || [ "$runtime_gid" != "$(id -g)" ]; }; then
  echo "non-root bootstrap requires CHANNEL_RUNTIME_UID/GID to match the current user" >&2
  exit 1
fi

cliproxy_config="$runtime_dir/cliproxyapi/config.yaml"
gemini_config="$runtime_dir/gemini-web2api/config.json"
grok_config="$runtime_dir/grok2api/config.yaml"
for directory in "$runtime_dir" "$runtime_dir/gemini-web2api" "$runtime_dir/grok2api" \
  "$runtime_dir/cliproxyapi" "$runtime_dir/cliproxyapi/auths"; do
  assert_safe_directory "$directory"
done
assert_safe_file "$gemini_config"
assert_safe_file "$grok_config"
assert_safe_file "$cliproxy_config"
cliproxy_routing_strategy=$(read_routing_strategy "$cliproxy_config")
case $cliproxy_routing_strategy in
  round-robin|fill-first) ;;
  *) cliproxy_routing_strategy=round-robin ;;
esac

mkdir -p "$runtime_dir/gemini-web2api" "$runtime_dir/grok2api" \
  "$runtime_dir/cliproxyapi/auths"
chmod 700 "$runtime_dir" "$runtime_dir/gemini-web2api" "$runtime_dir/grok2api" \
  "$runtime_dir/cliproxyapi" "$runtime_dir/cliproxyapi/auths"

gemini_tmp=$(mktemp "$runtime_dir/gemini-web2api/.config.json.XXXXXX")
grok_tmp=$(mktemp "$runtime_dir/grok2api/.config.yaml.XXXXXX")
cliproxy_tmp=$(mktemp "$runtime_dir/cliproxyapi/.config.yaml.XXXXXX")

# Existing private configs may contain manually imported Gemini cookies or
# channel-specific settings. Use them as the migration source, preserving those
# fields while synchronizing managed keys and container-listen fields. Templates
# are used solely for first bootstrap.
gemini_source="$root_dir/channels/gemini-web2api/config.template.json"
grok_source="$root_dir/channels/grok2api/config.template.yaml"
cliproxy_source="$root_dir/channels/cliproxyapi/config.template.yaml"
[ ! -f "$gemini_config" ] || gemini_source=$gemini_config
[ ! -f "$grok_config" ] || grok_source=$grok_config
[ ! -f "$cliproxy_config" ] || cliproxy_source=$cliproxy_config

awk -v env_file="$env_file" '
  function replace_all(line, token, value, pos) {
    while ((pos = index(line, token)) > 0) {
      line = substr(line, 1, pos - 1) value substr(line, pos + length(token))
    }
    return line
  }
  BEGIN {
    while ((getline entry < env_file) > 0) {
      pos = index(entry, "=")
      if (pos > 1) values[substr(entry, 1, pos - 1)] = substr(entry, pos + 1)
    }
    close(env_file)
  }
  {
    line = replace_all($0, "__API_KEY__", values["GEMINI_WEB2API_KEY"])
    if (line ~ /^  "host": "127[.]0[.]0[.]1",$/) line = "  \"host\": \"0.0.0.0\","
    if (line ~ /^  "api_keys": \[/) line = "  \"api_keys\": [\"" values["GEMINI_WEB2API_KEY"] "\"],"
    print line
  }
' "$gemini_source" > "$gemini_tmp"

awk -v env_file="$env_file" '
  function replace_all(line, token, value, pos) {
    while ((pos = index(line, token)) > 0) {
      line = substr(line, 1, pos - 1) value substr(line, pos + length(token))
    }
    return line
  }
  BEGIN {
    while ((getline entry < env_file) > 0) {
      pos = index(entry, "=")
      if (pos > 1) values[substr(entry, 1, pos - 1)] = substr(entry, pos + 1)
    }
    close(env_file)
  }
  {
    line = replace_all($0, "__ADMIN_PASSWORD__", values["GROK2API_ADMIN_PASSWORD"])
    line = replace_all(line, "__JWT_SECRET__", values["GROK2API_JWT_SECRET"])
    line = replace_all(line, "__CREDENTIAL_KEY__", values["GROK2API_CREDENTIAL_KEY"])
    if (line ~ /^  listen: "127[.]0[.]0[.]1:45680"$/) line = "  listen: \"0.0.0.0:45680\""
    if (line ~ /^  jwtSecret:/) line = "  jwtSecret: \"" values["GROK2API_JWT_SECRET"] "\""
    if (line ~ /^  credentialEncryptionKey:/) line = "  credentialEncryptionKey: \"" values["GROK2API_CREDENTIAL_KEY"] "\""
    if (line ~ /^  password:/) line = "  password: \"" values["GROK2API_ADMIN_PASSWORD"] "\""
    print line
  }
' "$grok_source" > "$grok_tmp"

awk -v env_file="$env_file" -v routing_strategy="$cliproxy_routing_strategy" '
  function replace_all(line, token, value, pos) {
    while ((pos = index(line, token)) > 0) {
      line = substr(line, 1, pos - 1) value substr(line, pos + length(token))
    }
    return line
  }
  BEGIN {
    while ((getline entry < env_file) > 0) {
      pos = index(entry, "=")
      if (pos > 1) values[substr(entry, 1, pos - 1)] = substr(entry, pos + 1)
    }
    close(env_file)
  }
  {
    line = replace_all($0, "REPLACE_WITH_RANDOM_KEY", values["CLIPROXYAPI_KEY"])
    if (line == "api-keys:") {
      print line
      print "  - \"" values["CLIPROXYAPI_KEY"] "\""
      skip_api_keys = 1
      next
    }
    if (skip_api_keys && (line ~ /^[[:space:]]/ || line ~ /^$/ || line ~ /^#/)) next
    skip_api_keys = 0
    if (line == "host: \"127.0.0.1\"") line = "host: \"0.0.0.0\""
    if (line == "  allow-remote: false") line = "  allow-remote: true"
    if (line == "  strategy: \"round-robin\"") line = "  strategy: \"" routing_strategy "\""
    print line
  }
' "$cliproxy_source" > "$cliproxy_tmp"

chmod 600 "$env_file" "$gemini_tmp" "$grok_tmp" "$cliproxy_tmp"
mv -f "$gemini_tmp" "$gemini_config"
mv -f "$grok_tmp" "$grok_config"
mv -f "$cliproxy_tmp" "$cliproxy_config"
gemini_tmp=
grok_tmp=
cliproxy_tmp=
if [ "$(id -u)" -eq 0 ]; then
  chown -R "$runtime_uid:$runtime_gid" "$runtime_dir/gemini-web2api" \
    "$runtime_dir/grok2api" "$runtime_dir/cliproxyapi"
fi
echo "channel runtime configuration is ready"
