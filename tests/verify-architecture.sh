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

# Step 8A: L4 use-case boundary (internal/usecase). Use cases compose narrow
# injected capabilities; they must never import store, runtime, discovery,
# authority, CLI, node, engine, or concrete reachability. Only L0 domain,
# accesslease/exactauthority/localkey (L3 owner-local lifecycle), openauth, and
# stdlib are permitted. Contracts expose no *store.Store anywhere.
usecase_pkg="$(go list -f '{{.ImportPath}}' ./internal/usecase)"
usecase_forbidden="$(go list -deps -f '{{if and (not .Standard)}}{{.ImportPath}}{{end}}' "$usecase_pkg" | grep '^github.com/origama/tubo-rebuilt/' | grep -E 'internal/(store|runtime|discovery|authority|cli|node|engine|reachability)' || true)"
if [[ -n "$usecase_forbidden" ]]; then
  echo "[architecture] L4 usecase imports forbidden concrete package(s):" >&2
  printf '%s\n' "$usecase_forbidden" >&2
  exit 1
fi
if grep -R -n -E 'store\.Store|\*store\.Store' internal/usecase --include='*.go' | grep -v '_test.go' | grep -vE ':[0-9]+:.*//' >/dev/null; then
  echo "[architecture] L4 usecase contract exposes *store.Store" >&2
  exit 1
fi
# The new narrow ports live beside the consumer and must remain generic-free:
# no Repository[T], UnitOfWork, or giant Storage interface in usecase.
if grep -R -n -E 'Repository\[|UnitOfWork|type Storage interface' internal/usecase --include='*.go' | grep -v '_test.go' >/dev/null; then
  echo "[architecture] L4 usecase must define narrow consumer-owned ports only" >&2
  exit 1
fi

# #52/#54 owner-bound Exact Authority lifecycle: internal/accesslease remains
# pure/stdlib-only value contract. Step 6B1b adds only owner-local private-key
# lifecycle packages: internal/localkey (stdlib-only Ed25519 key-file primitive)
# and internal/exactauthority (may depend only on accesslease + localkey). No
# Scope, runtime receiving path, reachability, discovery, store/schema, CLI,
# concrete multiaddrs, libp2p, or old authority token implementation may enter
# these packages.
accesslease_pkg="$(go list -f '{{.ImportPath}}' ./internal/accesslease)"
accesslease_non_stdlib="$(go list -deps -f '{{if and (not .Standard) (ne .ImportPath "github.com/origama/tubo-rebuilt/internal/accesslease")}}{{.ImportPath}}{{end}}' "$accesslease_pkg")"
if [[ -n "$accesslease_non_stdlib" ]]; then
  echo "[architecture] internal/accesslease must be stdlib-only (rejecting ALL non-stdlib deps, including third-party):" >&2
  printf '%s\n' "$accesslease_non_stdlib" >&2
  exit 1
fi
localkey_pkg="$(go list -f '{{.ImportPath}}' ./internal/localkey)"
localkey_non_stdlib="$(go list -deps -f '{{if and (not .Standard) (ne .ImportPath "github.com/origama/tubo-rebuilt/internal/localkey")}}{{.ImportPath}}{{end}}' "$localkey_pkg")"
if [[ -n "$localkey_non_stdlib" ]]; then
  echo "[architecture] internal/localkey must be stdlib-only:" >&2
  printf '%s\n' "$localkey_non_stdlib" >&2
  exit 1
fi
exactauthority_pkg="$(go list -f '{{.ImportPath}}' ./internal/exactauthority)"
exactauthority_forbidden="$(go list -deps -f '{{if and (not .Standard)}}{{.ImportPath}}{{end}}' "$exactauthority_pkg" | grep '^github.com/origama/tubo-rebuilt/' | grep -Ev 'internal/(accesslease|localkey|exactauthority)$' || true)"
if [[ -n "$exactauthority_forbidden" ]]; then
  echo "[architecture] internal/exactauthority imports forbidden repo package(s):" >&2
  printf '%s\n' "$exactauthority_forbidden" >&2
  exit 1
fi
for f in $(find internal/accesslease internal/localkey internal/exactauthority -name '*.go' ! -name '*_test.go'); do
  if grep -qE 'Scope.*json:"scope|map\[string\]any' "$f"; then
    echo "[architecture] $f carries Scope or arbitrary metadata (map[string]any)" >&2
    exit 1
  fi
  if grep -E '"github.com/origama/tubo-rebuilt/internal/(runtime|reachability|discovery|store|cli|authority)"|"github.com/multiformats|go-libp2p' "$f"; then
    echo "[architecture] $f imports forbidden runtime/reachability/discovery/store/authority/libp2p type" >&2
    exit 1
  fi
done

# Step 7: Pipe Engine (L2 connectivity composition) must not import concrete
# store, discovery, authority, Scope/CLI packages. It may depend on domain (L0)
# and node (L1 Carrier), and on the openauth seam via the consumer contract.
engine_pkg="$(go list -f '{{.ImportPath}}' ./internal/engine)"
engine_forbidden="$(go list -deps -f '{{if and (not .Standard)}}{{.ImportPath}}{{end}}' "$engine_pkg" | grep '^github.com/origama/tubo-rebuilt/' | grep -E 'internal/(store|discovery|authority|cli|exactauthority|localkey|accesslease|openauth)' || true)"
if [[ -n "$engine_forbidden" ]]; then
  echo "[architecture] Step 7 engine imports forbidden package(s):" >&2
  printf '%s\n' "$engine_forbidden" >&2
  exit 1
fi

echo "[architecture] PASS: L0 stdlib-only; runtime dispatch has no forbidden global symbols; Session boundary has no libp2p host/network/peer/protocol or store import; node adapter has no runtime coupling; L1 reachability/bundle closure is transport-only without policy/authority/store dependencies; #48 receiving openauth seam imports no concrete authority/store and openauth carries no Scope/metadata map; #52 accesslease remains stdlib-only canonical bounded base64url contract; #54 localkey/exactauthority add only owner-local secure key lifecycle with no Scope/runtime/reachability/discovery/store/authority/CLI/libp2p/multiaddr wiring; Step 7 engine has no store/discovery/authority/Scope/CLI dependency; Step 8A usecase boundary imports no store/runtime/discovery/authority/CLI/node/engine/reachability and exposes no *store.Store or generic repository framework"
