#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

section() {
  printf '\n== %s ==\n' "$1"
}

show_block() {
  local text="$1"
  if [[ -n "$text" ]]; then
    printf '%s\n' "$text"
  else
    printf '(none)\n'
  fi
}

section "TASKS references"
tasks_hits="$(rg -n -S --glob '!docs/archive/**' --glob '!generated/**' --glob '!tests/verify-repo-hygiene.sh' --glob '!.git/**' 'TASKS\.md|tasks\.md' . 2>/dev/null || true)"
show_block "$tasks_hits"

section "topology references"
topology_hits="$(rg -n -S --glob '!docs/archive/**' --glob '!generated/**' --glob '!tests/verify-repo-hygiene.sh' --glob '!.git/**' 'topology' . 2>/dev/null || true)"
show_block "$topology_hits"

section "root docker-compose files"
compose_files="$(find . -maxdepth 1 -type f -name 'docker-compose*.yml' | sort)"
compose_count="$(printf '%s\n' "$compose_files" | sed '/^$/d' | wc -l | tr -d ' ')"
printf 'count: %s\n' "$compose_count"
show_block "$compose_files"

section "skipped test locations"
skip_hits="$(rg -n -S --glob '!docs/archive/**' --glob '!generated/**' --glob '!.git/**' --glob '!**/vendor/**' 't\.Skipf?\(|Skipf?\(' tests cmd internal 2>/dev/null || true)"
show_block "$(printf '%s\n' "$skip_hits" | sed -n '1,40p')"
if [[ -n "$skip_hits" ]]; then
  skip_count="$(printf '%s\n' "$skip_hits" | sed '/^$/d' | wc -l | tr -d ' ')"
  printf '... total matches: %s\n' "$skip_count"
else
  printf '... total matches: 0\n'
fi

section "docs line counts"
docs=(
  README.md
  docs/README.md
  docs/reference/cli.md
  docs/reference/PROTOCOL.md
  docs/reference/SECURITY.md
  docs/reference/security-model-0.7.md
  docs/reference/VERSIONING.md
  docs/runbooks/OPERABILITY.md
  docs/runbooks/RELEASING.md
)
for doc in "${docs[@]}"; do
  if [[ -f "$doc" ]]; then
    printf '%6s %s\n' "$(wc -l < "$doc" | tr -d ' ')" "$doc"
  fi
done
