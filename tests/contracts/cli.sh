#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/tubo-rebuilt-cli.XXXXXX")"
trap 'rm -rf "$WORK_DIR"' EXIT

BIN="$WORK_DIR/tubo"
(cd "$ROOT_DIR" && go build -o "$BIN" ./cmd/tubo)

"$BIN" help >"$WORK_DIR/help.out"
for value in \
  'Service = local terminal; never advertised' \
  'Pipe    = persistent directed connectivity resource' \
  'tubo attach' \
  'tubo connect' \
  'tubo get pipes'; do
  grep -F "$value" "$WORK_DIR/help.out" >/dev/null
 done

"$BIN" version --json >"$WORK_DIR/version.json"
python3 - "$WORK_DIR/version.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as f:
    value = json.load(f)
assert value["product_version"] == "0.1.0-dev", value
assert value["protocol_version"] == "2.0", value
PY

if "$BIN" attach http://127.0.0.1:1 --name demo >"$WORK_DIR/unimplemented.out" 2>&1; then
  echo "bootstrap attach unexpectedly succeeded" >&2
  exit 1
fi
grep -F 'not implemented in the bootstrap repository' "$WORK_DIR/unimplemented.out" >/dev/null

echo '[contract] CLI bootstrap contract PASS'
