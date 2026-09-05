#!/usr/bin/env bash
# CLI contract tests: command surface, strict parsing, help, human views,
# stdout/stderr discipline, and JSON shapes. Runs against a fresh TUBO_HOME;
# no network is required. Behavioral proof for attach/share/connect lives in
# tests/integration/private-http.sh.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/tubo-rebuilt-cli.XXXXXX")"
trap 'rm -rf "$WORK_DIR"' EXIT

BIN="$WORK_DIR/tubo"
(cd "$ROOT_DIR" && go build -o "$BIN" ./cmd/tubo)
export TUBO_HOME="$WORK_DIR/home"

# ---- Help surface (no Store/network touched) ----
"$BIN" help >"$WORK_DIR/help.out"
for value in \
  'tubo — private connectivity through composable pipes' \
  'accept     accept an exact invite and persist the durable access relationship' \
  'attach     create or reuse a Service and its Pipe, then run it' \
  'connect    redeem an exact invite (or reuse an accepted binding) and run a local Pipe' \
  'share      mint a one-time invite for an owned Pipe' \
  'get        list resources' \
  'inspect    inspect one resource in detail' \
  'ps         show a compact runtime overview' \
  'top        show live runtime and network metrics' \
  '--quiet'; do
  grep -F -- "$value" "$WORK_DIR/help.out" >/dev/null
done
"$BIN" get --help | grep -E '^  pipes +list Pipes' >/dev/null
"$BIN" get pipes --help | grep -F -- '--wide' >/dev/null
"$BIN" inspect --help | grep -F -- 'detail' >/dev/null
"$BIN" scope --help | grep -F -- 'Subcommands:' >/dev/null
"$BIN" scope invite --help | grep -F -- '--role <value>' >/dev/null
"$BIN" supervise install --help | grep -F -- '--scope <value>' >/dev/null
"$BIN" -h | grep -F -- 'attach' >/dev/null
"$BIN" help get pipes | grep -F -- 'Usage:' >/dev/null
"$BIN" help scope invite | grep -F -- '--role' >/dev/null
if "$BIN" help get garble >"$WORK_DIR/unknown-help.out" 2>&1; then
  echo 'help for unknown subcommand succeeded' >&2; exit 1
fi

# ---- Strict parsing ----
if "$BIN" get pipes --scpoe solar/default >"$WORK_DIR/typo.out" 2>&1; then
  echo 'typo flag silently accepted' >&2; exit 1
fi
grep -F -- 'unknown flag --scpoe' "$WORK_DIR/typo.out" >/dev/null
if "$BIN" frobnicate >"$WORK_DIR/unknown-cmd.out" 2>&1; then
  echo 'unknown command succeeded' >&2; exit 1
fi
grep -F -- 'unknown command "frobnicate"' "$WORK_DIR/unknown-cmd.out" >/dev/null
if "$BIN" attach http://127.0.0.1:1 >"$WORK_DIR/missing-flag.out" 2>&1; then
  echo 'missing required flag succeeded' >&2; exit 1
fi
grep -F -- '--name is required' "$WORK_DIR/missing-flag.out" >/dev/null
if "$BIN" share pipe/a pipe/b >"$WORK_DIR/extra-arg.out" 2>&1; then
  echo 'extra positional succeeded' >&2; exit 1
fi
grep -F -- 'unexpected argument "pipe/b"' "$WORK_DIR/extra-arg.out" >/dev/null
if "$BIN" get pipes --scpoe solar --refresh >"$WORK_DIR/typo2.out" 2>&1; then
  echo 'typo flag with valid neighbor accepted' >&2; exit 1
fi
# Command path must be contiguous; a flag before the subcommand fails instead
# of being silently dropped.
if "$BIN" get --wide pipes >"$WORK_DIR/misplaced1.out" 2>&1; then
  echo 'flag before subcommand accepted' >&2; exit 1
fi
grep -F -- 'flag --wide before subcommand' "$WORK_DIR/misplaced1.out" >/dev/null
if "$BIN" scope --json create solar/default >"$WORK_DIR/misplaced2.out" 2>&1; then
  echo 'flag before scope subcommand accepted' >&2; exit 1
fi
grep -F -- 'flag --json before subcommand' "$WORK_DIR/misplaced2.out" >/dev/null
# Single-dash long forms are never long flags.
if "$BIN" get pipes -json >"$WORK_DIR/shortlong.out" 2>&1; then
  echo '-json accepted as long flag' >&2; exit 1
fi
grep -F -- 'unknown flag -json' "$WORK_DIR/shortlong.out" >/dev/null
# A value flag must not consume another flag as its value.
if "$BIN" attach http://127.0.0.1:1 --name --json >"$WORK_DIR/flagvalue.out" 2>&1; then
  echo '--name --json accepted' >&2; exit 1
fi
grep -F -- '--name requires a value' "$WORK_DIR/flagvalue.out" >/dev/null
# Duplicate flags are rejected.
if "$BIN" attach http://127.0.0.1:1 --name y -d -d >"$WORK_DIR/dupflags.out" 2>&1; then
  echo 'duplicate -d -d accepted' >&2; exit 1
fi
grep -F -- 'duplicate flag -d' "$WORK_DIR/dupflags.out" >/dev/null
# Accidental long forms of short flags are rejected.
if "$BIN" attach http://127.0.0.1:1 --name y --d >"$WORK_DIR/dlong.out" 2>&1; then
  echo '--d accepted as long flag' >&2; exit 1
fi
grep -F -- 'unknown flag --d' "$WORK_DIR/dlong.out" >/dev/null

# ---- Version / keygen / overlay ----
"$BIN" version --json >"$WORK_DIR/version.json"
expected_protocol="$(tr -d '[:space:]' <"$ROOT_DIR/PROTOCOL_VERSION")"
"$BIN" keygen swarm --out "$WORK_DIR/swarm.key" --json >/dev/null
"$BIN" join overlay --relay /ip4/127.0.0.1/tcp/4001/p2p/12D3KooWSoLbunv7D2wE4QFsRAdZR8oqSD2ruhh18gPN1A9d3Rnv --swarm-key "$WORK_DIR/swarm.key" --json >/dev/null

# ---- Resources ----
"$BIN" scope create solar/default --json >/dev/null
"$BIN" scope use solar/default --json >/dev/null
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
grep -F -- 'conflict:' "$WORK_DIR/conflict.out" >/dev/null

# ---- get pipes cache flags ----
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
  grep -F -- 'invalid_input: --live is obsolete; use --refresh' "$WORK_DIR/pipes-live.out" >/dev/null
done

# ---- Human views ----
grep -E '^NAME[[:space:]]+ID[[:space:]]+STATUS' "$WORK_DIR/pipes.out" >/dev/null
grep -F -- 'ACCESS' "$WORK_DIR/pipes.out" >/dev/null
"$BIN" get services >"$WORK_DIR/services.out"
grep -E '^NAME[[:space:]]+KIND[[:space:]]+TARGET' "$WORK_DIR/services.out" >/dev/null
grep -F -- 'echo' "$WORK_DIR/services.out" >/dev/null
"$BIN" get scopes >"$WORK_DIR/scopes.out"
grep -E '^SCOPE[[:space:]]+ROLE[[:space:]]+CURRENT' "$WORK_DIR/scopes.out" >/dev/null
grep -F -- 'solar/default' "$WORK_DIR/scopes.out" >/dev/null
"$BIN" get overlays >"$WORK_DIR/overlays.out"
grep -F -- 'overlay/default' "$WORK_DIR/overlays.out" >/dev/null
"$BIN" inspect service/echo >"$WORK_DIR/inspect-service.out"
grep -F -- 'Service:' "$WORK_DIR/inspect-service.out" >/dev/null
grep -F -- 'Target' "$WORK_DIR/inspect-service.out" >/dev/null
"$BIN" inspect pipe/local-echo >"$WORK_DIR/inspect-pipe.out"
grep -F -- 'Pipe:' "$WORK_DIR/inspect-pipe.out" >/dev/null
grep -F -- 'ID' "$WORK_DIR/inspect-pipe.out" >/dev/null
grep -F -- 'Status' "$WORK_DIR/inspect-pipe.out" >/dev/null
grep -F -- 'Target' "$WORK_DIR/inspect-pipe.out" >/dev/null

# ---- JSON shapes ----
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
if "$BIN" logs pipe/local-echo --json -f >"$WORK_DIR/logs-json-follow.out" 2>&1; then
  echo 'logs --json -f unexpectedly succeeded' >&2; exit 1
fi
grep -F -- '--json cannot be combined with -f' "$WORK_DIR/logs-json-follow.out" >/dev/null
"$BIN" -v ps --json >"$WORK_DIR/verbose-ps.json" 2>"$WORK_DIR/verbose.err"
python3 - "$WORK_DIR/services.json" "$WORK_DIR/scopes.json" "$WORK_DIR/inspect-service.json" "$WORK_DIR/config-valid.json" "$WORK_DIR/peers.json" "$WORK_DIR/doctor.json" <<'PY'
import json, sys
services, scopes, service, config, peers, doctor = [json.load(open(path, encoding="utf-8")) for path in sys.argv[1:]]
assert services["items"][0]["ref"] == "service/echo"
assert services["items"][0]["kind"] == "http"
assert services["items"][0]["target"] == "http://127.0.0.1:18080"
assert scopes["items"][0]["current"] is True
assert service["target"] == "http://127.0.0.1:18080"
assert config["status"] == "valid"
assert config["config"]["swarm_key_file"] == "<configured>"
assert peers["items"][0]["name"] == "relay"
assert peers["items"][0]["source"] == "alias"
assert peers["items"][0]["connected"] is False
assert doctor["status"] in {"ok", "warning"}
PY
grep -E '^PEER[[:space:]]+ID' "$WORK_DIR/peers.out" >/dev/null
grep -E 'OBSERVED[[:space:]]+BY' "$WORK_DIR/peers.out" >/dev/null
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
grep -F -- '<configured>' "$WORK_DIR/config-print.yaml" >/dev/null
if grep -F "$WORK_DIR/swarm.key" "$WORK_DIR/config-print.yaml" >/dev/null; then echo 'config print leaked key path' >&2; exit 1; fi
grep -F -- 'runtime marker' "$WORK_DIR/logs.out" >/dev/null
grep -F -- 'debug: command=ps' "$WORK_DIR/verbose.err" >/dev/null

# ---- JSON single-value and no stderr contamination ----
if "$BIN" get services --json 2>"$WORK_DIR/get-services.err" | python3 -c 'import json,sys; json.load(sys.stdin)'; then
  :
else
  echo 'get services --json not single JSON value' >&2; exit 1
fi
python3 - "$WORK_DIR/get-services.err" <<'PY'
import sys
assert open(sys.argv[1]).read().strip() == "", "stderr leaked for --json command"
PY

# ---- Runtime views ----
"$BIN" ps >"$WORK_DIR/ps.out"
"$BIN" top --once >"$WORK_DIR/top.out"
"$BIN" top --json >"$WORK_DIR/top.json"
grep -E '^REF[[:space:]]+KIND[[:space:]]+STATUS' "$WORK_DIR/ps.out" >/dev/null
grep -F -- 'Tubo runtime' "$WORK_DIR/top.out" >/dev/null
if grep -E 'NET[[:space:]]+RX' "$WORK_DIR/ps.out" >/dev/null; then
  echo 'ps leaked top metrics into compact view' >&2; exit 1
fi
python3 - "$WORK_DIR/top.json" <<'PY'
import json, sys
value = json.load(open(sys.argv[1], encoding="utf-8"))
assert "observed_at" in value
assert isinstance(value["items"], list)
PY

# ---- Removal ----
"$BIN" rm pipe/local-echo --json >"$WORK_DIR/rm-pipe.json"
"$BIN" rm service/echo --json >"$WORK_DIR/rm-service.json"
python3 - "$WORK_DIR/rm-pipe.json" "$WORK_DIR/rm-service.json" <<'PY'
import json,sys
for path in sys.argv[1:]: assert json.load(open(path))["status"] == "removed"
PY

# ---- Empty-state hints ----
if "$BIN" rm >"$WORK_DIR/rm-noargs.out" 2>&1; then
  echo 'bare rm succeeded' >&2; exit 1
fi
grep -F -- 'rm requires a resource ref or --stale' "$WORK_DIR/rm-noargs.out" >/dev/null
if "$BIN" start "pipe/x" --json >"$WORK_DIR/start-json.out" 2>&1; then
  echo 'foreground start --json succeeded' >&2; exit 1
fi
grep -F -- 'requires -d' "$WORK_DIR/start-json.out" >/dev/null
"$BIN" get services >"$WORK_DIR/services-empty.out"
grep -F -- 'No services.' "$WORK_DIR/services-empty.out" >/dev/null

# Overlay empty-state against a fresh home (the main home has one configured).
mkdir -p "$WORK_DIR/fresh-home"
TUBO_HOME="$WORK_DIR/fresh-home" "$BIN" get overlays >"$WORK_DIR/overlays-empty.out"
grep -F -- 'No overlay configured.' "$WORK_DIR/overlays-empty.out" >/dev/null

echo '[contract] CLI resource contract PASS'