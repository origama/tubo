#!/usr/bin/env bash
set -euo pipefail

source "${E2E_ROOT}/lib/common.sh"
source "${E2E_ROOT}/lib/docker.sh"

wait_http_ok_in_actor() {
  local actor="$1"
  local url="$2"
  local tries="${3:-60}"
  local i
  for i in $(seq 1 "$tries"); do
    if exec_actor "$actor" curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

assert_contains() {
  local haystack="$1"
  local needle="$2"
  local message="$3"
  if ! grep -Fq "$needle" <<<"$haystack"; then
    fail "$message"
  fi
}

assert_file_contains() {
  local path="$1"
  local needle="$2"
  local message="$3"
  if ! grep -Fq "$needle" "$path"; then
    fail "$message"
  fi
}
