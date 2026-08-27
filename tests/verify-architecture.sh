#!/usr/bin/env bash
# Architecture dependency-edge harness — A0 Step 9
#
# This script delegates to the Go-based architecture checker.
# The Go harness (tests/archcheck/) runs go list -deps for all internal
# packages and checks every resolved dependency against declarative rules.
#
# See tests/archcheck/rules.yaml for the full rule set.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

exec go run ./tests/archcheck
