#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/tubo-rebuilt-cli.XXXXXX")"
trap 'rm -rf "$WORK_DIR"' EXIT

BIN="$WORK_DIR/tubo"
(cd "$ROOT_DIR" && go build -o "$BIN" ./cmd/tubo)
export TUBO_HOME="$WORK_DIR/home"

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
expected_protocol="$(tr -d '[:space:]' <"$ROOT_DIR/PROTOCOL_VERSION")"
"$BIN" keygen swarm --out "$WORK_DIR/swarm.key" --json >/dev/null
"$BIN" join overlay --relay /ip4/127.0.0.1/tcp/4001/p2p/12D3KooWSoLbunv7D2wE4QFsRAdZR8oqSD2ruhh18gPN1A9d3Rnv --swarm-key "$WORK_DIR/swarm.key" --json >/dev/null
"$BIN" scope create solar/default --json >/dev/null
"$BIN" scope use solar/default --json >"$WORK_DIR/scope-use.json"
"$BIN" service create echo --target http://127.0.0.1:18080 --json >"$WORK_DIR/service.json"
"$BIN" pipe create local-echo --ingress local://127.0.0.1:18081 --target pipe/pip_remote --json >"$WORK_DIR/pipe-1.json"
"$BIN" pipe create local-echo --ingress local://127.0.0.1:18081 --target pipe/pip_remote --json >"$WORK_DIR/pipe-2.json"

python3 - "$WORK_DIR/version.json" "$WORK_DIR/service.json" "$WORK_DIR/pipe-1.json" "$WORK_DIR/pipe-2.json" "$expected_protocol" <<'PY'
import json, sys
version, service, first, second = [json.load(open(path, encoding="utf-8")) for path in sys.argv[1:5]]
assert version["product_version"] == "0.1.0-dev", version
assert version["protocol_version"] == sys.argv[5] == "2.1", version
assert service == {"ref": "service/echo", "status": "created"}, service
assert first["id"] == second["id"], (first, second)
assert second["status"] == "reused", second
PY

if "$BIN" service create echo --target http://127.0.0.1:18082 >"$WORK_DIR/conflict.out" 2>&1; then
  echo "conflicting service unexpectedly succeeded" >&2
  exit 1
fi
grep -F 'conflict:' "$WORK_DIR/conflict.out" >/dev/null

"$BIN" get pipes >"$WORK_DIR/pipes.out"
"$BIN" get pipes --json >"$WORK_DIR/pipes.json"
"$BIN" get pipes --cached-only --json >"$WORK_DIR/pipes-cached.json"
if "$BIN" get pipes --refresh --cached-only >"$WORK_DIR/pipes-conflict.out" 2>&1; then
  echo 'conflicting pipe cache flags unexpectedly succeeded' >&2
  exit 1
fi
grep -F -- '--refresh and --cached-only cannot be used together' "$WORK_DIR/pipes-conflict.out" >/dev/null
for live_args in '--live' '--live ignored' '--live=ignored'; do
  if "$BIN" get pipes $live_args >"$WORK_DIR/pipes-live.out" 2>&1; then
    echo 'obsolete --live unexpectedly succeeded' >&2
    exit 1
  fi
  grep -F 'invalid_input: --live is obsolete; use --refresh' "$WORK_DIR/pipes-live.out" >/dev/null
done
"$BIN" get services --json >"$WORK_DIR/services.json"
"$BIN" get scopes --json >"$WORK_DIR/scopes.json"
"$BIN" inspect service/echo --json >"$WORK_DIR/inspect-service.json"
"$BIN" config validate --json >"$WORK_DIR/config-valid.json"
"$BIN" config print >"$WORK_DIR/config-print.yaml"
"$BIN" peers alias 12D3KooWSoLbunv7D2wE4QFsRAdZR8oqSD2ruhh18gPN1A9d3Rnv --name relay --note local --json >"$WORK_DIR/peer.json"
"$BIN" get peers >"$WORK_DIR/peers.out"
"$BIN" get peers --json >"$WORK_DIR/peers.json"
"$BIN" doctor --json >"$WORK_DIR/doctor.json"
mkdir -p "$TUBO_HOME/logs"
printf 'runtime marker\n' >"$TUBO_HOME/logs/pipe-local-echo.log"
"$BIN" logs pipe/local-echo --tail 1 >"$WORK_DIR/logs.out"
"$BIN" -v ps --json >"$WORK_DIR/verbose-ps.json" 2>"$WORK_DIR/verbose.err"
python3 - "$WORK_DIR/services.json" "$WORK_DIR/scopes.json" "$WORK_DIR/inspect-service.json" "$WORK_DIR/config-valid.json" "$WORK_DIR/peers.json" "$WORK_DIR/doctor.json" <<'PY'
import json, sys
services, scopes, service, config, peers, doctor = [json.load(open(path, encoding="utf-8")) for path in sys.argv[1:]]
assert services["items"][0]["ref"] == "service/echo"
assert scopes["items"][0]["current"] is True
assert service["target"] == "http://127.0.0.1:18080"
assert config["status"] == "valid"
assert config["config"]["swarm_key_file"] == "<configured>"
assert peers["items"][0]["name"] == "relay"
assert peers["items"][0]["source"] == "alias"
assert peers["items"][0]["connected"] is False
assert doctor["status"] in {"ok", "warning"}
PY
grep -F 'PATH SOURCE' "$WORK_DIR/pipes.out" >/dev/null
grep -F 'OBSERVED BY' "$WORK_DIR/peers.out" >/dev/null
python3 - "$WORK_DIR/pipes.json" "$WORK_DIR/pipes-cached.json" <<'PY'
import json,sys
for path in sys.argv[1:]:
    value=json.load(open(path))
    items=value["items"]
    assert len(items)==1
    assert items[0]["source"]=="local"
    assert items[0]["status"]=="defined"
    assert items[0]["path"]=="unknown"
    assert items[0]["target"]=="pipe/pip_remote"
    assert value["scopes"] == []
PY
grep -F '<configured>' "$WORK_DIR/config-print.yaml" >/dev/null
if grep -F "$WORK_DIR/swarm.key" "$WORK_DIR/config-print.yaml" >/dev/null; then echo 'config print leaked key path' >&2; exit 1; fi
grep -F 'runtime marker' "$WORK_DIR/logs.out" >/dev/null
grep -F 'debug: command=ps' "$WORK_DIR/verbose.err" >/dev/null

"$BIN" ps >"$WORK_DIR/ps.out"
"$BIN" top --once >"$WORK_DIR/top.out"
"$BIN" top --json >"$WORK_DIR/top.json"
grep -F $'REF  KIND  STATUS' "$WORK_DIR/ps.out" >/dev/null
grep -F 'Tubo runtime' "$WORK_DIR/top.out" >/dev/null
python3 - "$WORK_DIR/top.json" <<'PY'
import json, sys
value = json.load(open(sys.argv[1], encoding="utf-8"))
assert "observed_at" in value
assert isinstance(value["items"], list)
PY

"$BIN" rm pipe/local-echo --json >"$WORK_DIR/rm-pipe.json"
"$BIN" rm service/echo --json >"$WORK_DIR/rm-service.json"
python3 - "$WORK_DIR/rm-pipe.json" "$WORK_DIR/rm-service.json" <<'PY'
import json,sys
for path in sys.argv[1:]: assert json.load(open(path))["status"] == "removed"
PY

echo '[contract] CLI resource contract PASS'
