#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

for path in README.md AGENTS.md docs/README.md docs/reference/INVARIANTS.md docs/runbooks/THREE_MACHINE_E2E.md; do
  [[ -s "$path" ]] || { echo "missing required file: $path" >&2; exit 1; }
done

if git grep -nE 'tubo-share-invite-v[0-9]+\.|BEGIN (OPENSSH |RSA |EC )?PRIVATE KEY|/key/swarm/psk/1\.0\.0/' -- ':!tests/verify-repo-hygiene.sh' ':!docs/reference/SECURITY.md'; then
  echo 'possible committed secret or live token' >&2
  exit 1
fi

echo '[hygiene] PASS'
