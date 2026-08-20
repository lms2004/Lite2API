#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly OUTPUT_DIR="${OUTPUT_DIR:-$REPOSITORY_ROOT/evidence/admin-screenshots}"
readonly ADMIN_URL="${ADMIN_URL:-http://127.0.0.1:45679/admin}"
readonly CHROMIUM_BIN="${CHROMIUM_BIN:-chromium}"

mkdir -p "$OUTPUT_DIR"

capture() {
  local name=$1
  local width=$2
  local height=$3
  local view=$4

  echo "capturing $name (${width}x${height})"
  "$CHROMIUM_BIN" \
    --headless=new \
    --no-sandbox \
    --disable-gpu \
    --disable-dev-shm-usage \
    --hide-scrollbars \
    --force-device-scale-factor=1 \
    --window-size="${width},${height}" \
    --virtual-time-budget=7000 \
    --screenshot="$OUTPUT_DIR/${name}.png" \
    "$ADMIN_URL#$view"
}

# This list is intentionally sequential. The production host has 1 GiB RAM;
# parallel Chromium process trees can exhaust RAM and swap in under a minute.
capture monitor 1440 1000 monitor
capture routes 1440 1000 routes
capture accounts 1440 1000 accounts
capture keys 1440 1000 keys
capture adapters 1440 1000 adapters
capture prompt-test 1440 1000 prompt-test
capture monitor-mobile 390 844 monitor
capture routes-mobile 390 844 routes
capture accounts-mobile 390 844 accounts
capture keys-mobile 390 844 keys

echo "screenshots written to $OUTPUT_DIR"
