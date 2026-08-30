#!/usr/bin/env bash
set -euo pipefail

source_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
test_root=$(mktemp -d /tmp/lite2api-bootstrap-contract.XXXXXX)
cleanup() {
    local status=$?
    set +e
    trap - EXIT
    case $test_root in
        /tmp/lite2api-bootstrap-contract.*) rm -rf -- "$test_root" ;;
    esac
    exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -p "$test_root/repo/deploy" \
    "$test_root/repo/channels/gemini-web2api" \
    "$test_root/repo/channels/grok2api" \
    "$test_root/repo/channels/cliproxyapi"
cp "$source_root/deploy/bootstrap.sh" "$source_root/deploy/bootstrap-channels.sh" \
    "$test_root/repo/deploy/"
cp "$source_root/channels/gemini-web2api/config.template.json" \
    "$test_root/repo/channels/gemini-web2api/"
cp "$source_root/channels/grok2api/config.template.yaml" \
    "$test_root/repo/channels/grok2api/"
cp "$source_root/channels/cliproxyapi/config.template.yaml" \
    "$test_root/repo/channels/cliproxyapi/"

# Some rootless/id-mapped test sandboxes cannot chown arbitrary numeric IDs.
# Exercise the non-root ownership branch there without changing real identity.
test_path=$PATH
if [[ $(id -u) -eq 0 ]]; then
    mkdir "$test_root/bin"
    # Deliberately preserve these expansions for the generated helper.
    # shellcheck disable=SC2016
    printf '%s\n' '#!/bin/sh' \
        'case ${1:-} in -u|-g) printf "10001\\n";; *) exec /usr/bin/id "$@";; esac' \
        >"$test_root/bin/id"
    chmod 0755 "$test_root/bin/id"
    test_path="$test_root/bin:$PATH"
fi

PATH=$test_path "$test_root/repo/deploy/bootstrap.sh" >/dev/null
PATH=$test_path "$test_root/repo/deploy/bootstrap-channels.sh" >/dev/null
[[ $(stat -c %a "$test_root/repo/.env") == 600 ]]

node - "$test_root/repo" <<'NODE'
const fs = require('node:fs');
const path = require('node:path');
const root = process.argv[2];
const env = Object.fromEntries(fs.readFileSync(path.join(root, '.env'), 'utf8')
  .split(/\r?\n/).filter(line => line.includes('=')).map(line => {
    const split = line.indexOf('=');
    return [line.slice(0, split), line.slice(split + 1)];
  }));
if (env.LITE2API_API_KEYS.length < 32 || env.LITE2API_ADMIN_TOKEN.length < 32) throw new Error('weak core secrets');
for (const name of ['GEMINI_WEB2API_KEY', 'CLIPROXYAPI_KEY', 'CLIPROXYAPI_MANAGEMENT_KEY']) {
  if (!/^[0-9a-f]{64}$/.test(env[name])) throw new Error(`invalid ${name}`);
}
const gemini = JSON.parse(fs.readFileSync(path.join(root, 'channels/runtime/gemini-web2api/config.json')));
if (gemini.host !== '0.0.0.0' || gemini.api_keys[0] !== env.GEMINI_WEB2API_KEY) throw new Error('Gemini config drift');
const grok = fs.readFileSync(path.join(root, 'channels/runtime/grok2api/config.yaml'), 'utf8');
if (!grok.includes('listen: "0.0.0.0:45680"') || !grok.includes(`password: "${env.GROK2API_ADMIN_PASSWORD}"`)) throw new Error('Grok config drift');
const cli = fs.readFileSync(path.join(root, 'channels/runtime/cliproxyapi/config.yaml'), 'utf8');
if (!cli.includes('host: "0.0.0.0"') || !cli.includes('allow-remote: true') || !cli.includes(`- "${env.CLIPROXYAPI_KEY}"`)) throw new Error('CLIProxy config drift');
NODE

sed -i 's/"auth_user": null/"auth_user": "preserve@example.test"/' \
    "$test_root/repo/channels/runtime/gemini-web2api/config.json"
before=$(sha256sum "$test_root/repo/.env" | awk '{print $1}')
PATH=$test_path "$test_root/repo/deploy/bootstrap-channels.sh" >/dev/null
after=$(sha256sum "$test_root/repo/.env" | awk '{print $1}')
[[ $before == "$after" ]]
grep -Fq '"auth_user": "preserve@example.test"' \
    "$test_root/repo/channels/runtime/gemini-web2api/config.json"

cp "$source_root/.env.example" "$test_root/repo/.env"
if PATH=$test_path "$test_root/repo/deploy/bootstrap.sh" >/dev/null 2>&1; then
    echo 'bootstrap accepted placeholder production secrets' >&2
    exit 1
fi

printf 'bootstrap contracts passed\n'
