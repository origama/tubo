#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORKDIR="${TUBO_PERF_WORKDIR:-$ROOT/generated/perf/tcpraw}"
mkdir -p "$WORKDIR"
export TUBO_REPO_ROOT="$ROOT"
export TUBO_PERF_WORKDIR="$WORKDIR"
export DOCKER_BUILDKIT=0
export COMPOSE_DOCKER_CLI_BUILD=0
export COMPOSE_PROJECT_NAME=tubo-tcpraw
exec docker compose -f "$ROOT/tests/perf/tcpraw/docker-compose.yml" up -d --build "$@"
