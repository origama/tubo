# Tests

## Smoke E2E CLI UX v2 (local, without Docker)

Validates the main documented happy paths for the new user-facing CLI using only local processes, dynamic ports, temporary directories, and a local mock HTTP server.

Covers:

- `relay -d`
- `join`
- `attach -d`
- `ps` / `get processes` / `describe process/...` / `inspect process/... --json` / `logs` / `stop` / `rm --stale`
- `get services` without a local gateway (ephemeral observer)
- `get service/<name>` / `describe service/<name>` / `inspect service/<name> --json`
- `connect <service-name>` + real HTTP request
- `gateway -d` + real HTTP request with `Host:`
- foreground-by-default smoke for `attach` without `-d`

Command:

```bash
./tests/smoke-cli-ux.sh
```

Set `KEEP_WORK=1` to preserve the temporary working directory for debugging.

## Smoke E2E (Docker Compose)

Runs the minimum complete path:

- `relay`
- `dummy-api-server`
- `edge`
- `service`
- real HTTP request via edge

Command:

```bash
./tests/smoke-compose.sh
```

By default the script runs build/compose with:

- `DOCKER_BUILDKIT=0`
- `COMPOSE_DOCKER_CLI_BUILD=0`

to reduce intermittent Docker daemon/BuildKit crashes.

The test verifies:

- health endpoints up
- populated discovery cache (`/services`)
- auto-route present (`/routes`)
- end-to-end `Host: myapi` call with HTTP 200 response and consistent payload

## Smoke E2E tubo UX (Docker Compose)

Verifies the new UX with the single `tubo` image and YAML config:

- `tubo relay --config /etc/tubo/relay.yaml`
- `tubo gateway --config /etc/tubo/edge.yaml`
- `tubo attach --config /etc/tubo/service.yaml`

Command:

```bash
./tests/smoke-compose-tubo.sh
```

The script prepares `generated/integration/tubo/*.yaml`, starts `tests/e2e/compose/tubo/compose.yml`, waits for health/discovery/routes, and performs an end-to-end request through edge.

## Archived: Relay/NAT-like (Docker Compose with isolated networks)

The relay-first scenario on isolated Docker networks has been archived in:

- `tests/archive/compose/relay-nat/compose.yml`

The manual benchmark/perf flows that use it point to that archive.

## Smoke E2E Private Overlay Multi-Service (Docker Compose with 3 service nodes)

Simulates a private shared libp2p overlay with:

- `relay`
- `edge`
- `curl-client` on the same private network as edge
- three isolated service nodes, each with:
  - `service-*`
  - `dummy-api-server-*`

Docker topology:

- `edge` and `curl-client` on `edge-private`
- each service node on its own dedicated private network
- `relay` connected to all private networks
- all libp2p peers use the same private swarm PSK (`LIBP2P_PRIVATE_NETWORK_KEY_B64`)

The `curl-client` always calls the **same edge gateway endpoint** (`http://edge:8443/v1/dummy`), changing only the `Host` header between `svc-one`, `svc-two`, and `svc-three`.

Command:

```bash
./tests/smoke-compose-private-overlay-multi-service.sh
```

The test verifies:

- health checks for relay, edge, and 3 service nodes
- discovery cache with `count=3`
- auto-routes for `svc-one`, `svc-two`, `svc-three`
- host-based routing to the three backends through a single edge endpoint
- consistent response from the expected backend (`instance`)

## Smoke E2E Distributed 2-host (local edge + remote relay)

Real smoke on 2 machines:

- `edge` on the local machine (`172.236.202.99` by default)
- `relay` on the remote machine (`172.232.189.160`)
- `service` + `dummy-api-server` co-hosted on the remote machine

Command:

```bash
./tests/smoke-distributed-two-host.sh
```

The remote `service` is forced to use `p2p_listen=/ip4/127.0.0.1/tcp/40123` and `force_reachability: private`, so edge cannot perform a public direct dial and must go through relay.

Operational details: `tests/distributed-two-host.md`

## Smoke E2E Linode/Terraform (3-host multi-region)

Distributed provisioning via Terraform:

- public `relay` on Linode
- `edge` on Linode with closed ingress (SSH-only, NAT-like)
- `service` on Linode with closed ingress (SSH-only, NAT-like)

Terraform stack:

- `infra/terraform/linode-distributed/`

Smoke harness:

```bash
./tests/smoke-terraform-linode.sh
```

The smoke reads IPs from `terraform output`, uploads binaries + config to the nodes, and verifies the relay-first path by checking `connection_path=relayed` in edge logs.

## Smoke mixed-version on Linode/Terraform

Validates compatibility between different `tubo` binary versions on the real multi-host bench.

Command:

```bash
./tests/smoke-terraform-linode-mixed-version.sh
```

By default the script builds:

- the current binary from `main`
- a legacy binary from ref `c9bbb1f` (pre-protocol 1.1 hello handshake)

Covered scenarios:

- current edge -> legacy service (fallback `/p2p-tunnel/1.0`)
- legacy edge -> current service (current service accepts legacy)
- current edge -> current service (negotiates `/p2p-tunnel/1.1`)

The script also uses protocol debug/admin endpoints when available to save operational compatibility evidence.

## TCP raw throughput benchmark (Docker)

Docker-based raw TCP throughput benchmark for `service_kind=tcp` with both explicit direct and explicit relayed paths.

Harness:

- `tests/perf/tcpraw/`

Commands:

```bash
./tests/perf/tcpraw/run.sh
./tests/perf/tcpraw/run.sh --validate
./tests/perf/tcpraw/run.sh --duration 10
```

Artifacts are written under:

- `tests/perf/tcpraw/results/`
- `tests/perf/tcpraw/results/runs/<timestamp>/`

The harness records the selected Tubo path (`direct` or `relayed`) from `tubo connect`, collects `iperf3 --json` output for forward / reverse / parallel runs, and saves attach/connect/relay logs plus lightweight container CPU samples.

## Persistent performance benchmark on Linode/Terraform

Uses the multi-region testbed created by Terraform, keeps remote processes active for the duration of the benchmark, and saves comparable historical results in:

- `tests/perf/results/linode-terraform/<timestamp>/report.json`
- `tests/perf/results/linode-terraform/<timestamp>/summary.md`
- `tests/perf/results/linode-terraform/latest.json`
- `tests/perf/results/linode-terraform/latest.md`

Command:

```bash
python3 ./tests/perf/run_linode_terraform_perf.py
```

## Integration Tests (Go)

Package: `tests/integration`

Covers:

- auto-discovery + end-to-end proxy
- streaming request/response large body
- lease expiry with route removal
- hop-by-hop header stripping
- raw TCP echo with large payload and concurrent connections
- HTTPS passthrough over `service_kind=tcp`
- archived relay-first scenario (`tests/archive/compose/relay-nat/compose.yml`)

Run:

```bash
RUN_INTEGRATION=1 go test -v ./tests/integration
```

Note: part of the integration coverage is now pure Go and does not require Docker (for example TCP echo / HTTPS passthrough); if the Docker daemon is unavailable or crashes, Docker-dependent tests are marked `SKIP` (infrastructure error), not application `FAIL`.
Internal `docker compose` commands used by the tests default to `DOCKER_BUILDKIT=0` and `COMPOSE_DOCKER_CLI_BUILD=0`.
