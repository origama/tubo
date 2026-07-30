#!/usr/bin/env bash
set -euo pipefail

E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$E2E_DIR/lib.sh"

scenario="${1:-}"
E2E_SCENARIO_NAME="$scenario"
load_reporting_scenario() {
  local selected="$1"
  case "$selected" in
    private-http|periodic-sync)
      E2E_SCENARIO_NAME="$selected"
      # shellcheck disable=SC1090
      source "$E2E_DIR/scenarios/$selected.sh"
      ;;
    *) fail 'invalid E2E scenario for preflight reporting' ;;
  esac
}
install_preflight_trap() {
  PERIODIC_PHASE=preflight
  PERIODIC_FAILURE_CLASS=preflight_failure
  on_exit() {
    local status=$?
    trap - EXIT INT TERM
    [[ "$status" -ne 0 && -z "${PERIODIC_FAILURE_CLASS:-}" ]] && PERIODIC_FAILURE_CLASS=internal_error
    finish_mutating_run "$status"
    exit $?
  }
  trap on_exit EXIT INT TERM
}
run_preflight() {
  load_reporting_scenario "${E2E_SCENARIO:-private-http}"
  install_preflight_trap
  preflight
  trap - EXIT INT TERM
}
run_mutating() {
  # Install reporting before confirmation/preflight. Neither phase owns mutable run state.
  load_reporting_scenario "$scenario"
  install_preflight_trap
  confirm_mutation
  preflight
  E2E_MUTATION_STARTED=1
  PERIODIC_PHASE=bootstrap
  PERIODIC_FAILURE_CLASS=""
  "run_${scenario//-/_}"
}
case "$scenario" in
  preflight) run_preflight ;;
  private-http|periodic-sync) run_mutating ;;
  clean)
    confirm_mutation
    [[ -n "${TUBO_E2E_RUN_ID:-}" ]] || fail 'clean requires TUBO_E2E_RUN_ID so another run cannot be removed accidentally'
    cleanup_run
    ;;
  *) fail 'usage: tests/e2e/three-machine/run.sh <preflight|private-http|periodic-sync|clean>' ;;
esac
