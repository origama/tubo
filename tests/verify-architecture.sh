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

echo "[architecture] PASS: $l0_package depends only on standard library; runtime dispatch has no forbidden global symbols"
