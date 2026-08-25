#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

l0_package="$(go list -f '{{.ImportPath}}' ./internal/domain)"
non_stdlib_deps="$(go list -deps -f '{{if and (not .Standard) (ne .ImportPath "github.com/origama/tubo-rebuilt/internal/domain")}}{{.ImportPath}}{{end}}' "$l0_package")"

if [[ -n "$non_stdlib_deps" ]]; then
  echo "[architecture] L0 package $l0_package has forbidden non-stdlib dependencies:" >&2
  printf '%s\n' "$non_stdlib_deps" >&2
  exit 1
fi

forbidden_runtime_symbols='instanceRegistry|SetInstanceRegistry|HandleResumeStreamID'
if grep -R -n -E "$forbidden_runtime_symbols" internal/runtime --include='*.go'; then
  echo "[architecture] forbidden package-global Session dispatch symbol found" >&2
  exit 1
fi

# #28 neutral Carrier seam: Session (secure-connectivity) must not directly
# import libp2p concrete host/network/peer/protocol packages nor *store.Store.
# The guard targets the Session engine file(s) that own the active-carrier
# surface; Session still legitimately consumes the neutral Carrier contract via
# the host adapter at the L1 edge (internal/node).
session_boundary_files='internal/runtime/session.go'
for f in $session_boundary_files; do
  if grep -E '"github.com/libp2p/go-libp2p/core/(host|network|peer|protocol)"' "$f"; then
    echo "[architecture] $f imports forbidden libp2p concrete package" >&2
    exit 1
  fi
  if grep -E '"github.com/origama/tubo-rebuilt/internal/store"' "$f"; then
    echo "[architecture] $f imports *store.Store directly" >&2
    exit 1
  fi
done

# The host-integration adapter (internal/node) realizes the neutral Carrier at
# the L1 edge. It must not import runtime, authority, discovery, store, or CLI.
node_pkg="$(go list -f '{{.ImportPath}}' ./internal/node)"
node_forbidden_deps="$(go list -deps -f '{{if and (not .Standard) (ne .ImportPath "github.com/origama/tubo-rebuilt/internal/node")}}{{.ImportPath}}{{end}}' "$node_pkg" | grep '^github.com/origama/tubo-rebuilt/' || true)"
if [[ -n "$node_forbidden_deps" ]]; then
  echo "[architecture] host-integration adapter internal/node imports forbidden runtime package(s):" >&2
  printf '%s\n' "$node_forbidden_deps" >&2
  exit 1
fi

echo "[architecture] PASS: L0 stdlib-only; runtime dispatch has no forbidden global symbols; Session boundary has no libp2p host/network/peer/protocol or store import; node adapter has no runtime coupling"