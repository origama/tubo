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

# L1 Reachability boundary (issue #44). The reachability seam may depend on the
# host adapter (internal/node) and its pure bundle subpackage, but must never
# import runtime, authority, discovery, store, or CLI — its type surface is
# transport-only (peer identity/dial addresses in, dial target / node.Carrier
# out) and cannot express authorization. The pure bundle verifier must depend
# on neither the node adapter nor any runtime/authority/store/discovery/CLI
# package, keeping trust pinning isolated to the L1 contract.
reach_root="$(go list -f '{{.ImportPath}}' ./internal/reachability)"
reach_bundle="$(go list -f '{{.ImportPath}}' ./internal/reachability/bundle)"
reach_forbidden="$(go list -deps -f '{{if and (not .Standard)}}{{.ImportPath}}{{end}}' "$reach_root" | grep '^github.com/origama/tubo-rebuilt/' | grep -E 'internal/(runtime|authority|discovery|store|cli)' || true)"
if [[ -n "$reach_forbidden" ]]; then
  echo "[architecture] L1 reachability imports forbidden policy/authority/store package(s):" >&2
  printf '%s\n' "$reach_forbidden" >&2
  exit 1
fi
bundle_forbidden="$(go list -deps -f '{{if and (not .Standard)}}{{.ImportPath}}{{end}}' "$reach_bundle" | grep '^github.com/origama/tubo-rebuilt/' | grep -E 'internal/(node|runtime|authority|discovery|store|cli)|internal/reachability$' || true)"
if [[ -n "$bundle_forbidden" ]]; then
  echo "[architecture] pure bootstrap bundle subpackage imports forbidden concrete packages:" >&2
  printf '%s\n' "$bundle_forbidden" >&2
  exit 1
fi

# Issue #48: receiving-boundary OpenAuthorizer seam. The L2 receiving boundary
# must consume only the neutral openauth contract; it must not import concrete
# L3 authority/revocation or persistence for remote-open validation. The
# transitional Scope-token adapter lives in a separate file
# (scope_token_authorizer.go), which issue #49 replaces with the canonical
# exact-Pipe Access Lease + Exact Pipe Authority.
# shellcheck disable=SC2207
receive_boundary_files='internal/runtime/serve.go'
for f in $receive_boundary_files; do
  if grep -E '"github.com/origama/tubo-rebuilt/internal/(authority|store)"' "$f"; then
    echo "[architecture] $f imports forbidden concrete authority/store for remote-open validation" >&2
    exit 1
  fi
  if grep -E '"github.com/origama/tubo-rebuilt/internal/discovery"' "$f"; then
    echo "[architecture] $f imports forbidden discovery material on receiving boundary" >&2
    exit 1
  fi
done

# The neutral openauth contract must carry no Scope topology and no arbitrary
# metadata map, and must depend only on the standard library so L2 and L3
# converge on the same contract.
openauth_pkg="$(go list -f '{{.ImportPath}}' ./internal/openauth)"
openauth_non_stdlib="$(go list -deps -f '{{if and (not .Standard) (ne .ImportPath "github.com/origama/tubo-rebuilt/internal/openauth")}}{{.ImportPath}}{{end}}' "$openauth_pkg" | grep '^github.com/origama/tubo-rebuilt/' || true)"
if [[ -n "$openauth_non_stdlib" ]]; then
  echo "[architecture] openauth must not depend on concrete repo packages:" >&2
  printf '%s\n' "$openauth_non_stdlib" >&2
  exit 1
fi
if grep -R -n -E 'Scope.*json:"scope|map\[string\]any' internal/openauth --include='*.go'; then
  echo "[architecture] openauth contract must not carry Scope or arbitrary metadata (map[string]any)" >&2
  exit 1
fi

echo "[architecture] PASS: L0 stdlib-only; runtime dispatch has no forbidden global symbols; Session boundary has no libp2p host/network/peer/protocol or store import; node adapter has no runtime coupling; L1 reachability/bundle closure is transport-only without policy/authority/store dependencies; #48 receiving openauth seam imports no concrete authority/store and openauth carries no Scope/metadata map"