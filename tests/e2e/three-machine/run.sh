#!/usr/bin/env bash
set -euo pipefail

E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$E2E_DIR/lib.sh"

scenario="${1:-}"
case "$scenario" in
  preflight)
    preflight
    ;;
  private-http)
    confirm_mutation
    preflight
    trap 'status=$?; collect_artifacts; if [[ "${KEEP_WORK:-0}" != "1" ]]; then cleanup_run; else log "preserved run: $LOCAL_ROOT and $REMOTE_ROOT"; fi; exit $status' EXIT INT TERM
    # shellcheck disable=SC1091
    source "$E2E_DIR/scenarios/private-http.sh"
    run_private_http
    ;;
  clean)
    confirm_mutation
    [[ -n "${TUBO_E2E_RUN_ID:-}" ]] || fail 'clean requires TUBO_E2E_RUN_ID so another run cannot be removed accidentally'
    cleanup_run
    ;;
  *)
    fail 'usage: tests/e2e/three-machine/run.sh <preflight|private-http|clean>'
    ;;
esac
