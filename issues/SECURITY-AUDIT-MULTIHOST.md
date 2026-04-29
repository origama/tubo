# Security audit notes: multi-host, multi-service tubo deployment

Branch: `security/multihost-testbench`

Scope: security testbench for a private libp2p swarm with one relay, one edge and multiple published services on isolated Docker networks.

The goal is to keep reproducible tests for known weaknesses so they can be fixed over time.

## Testbench

Files:

- `tests/security/docker-compose.security-multihost.yml`
- `tests/security/security-multihost-testbench.sh`

Run:

```bash
./tests/security/security-multihost-testbench.sh
```

Useful options:

```bash
KEEP_STACK=1 ./tests/security/security-multihost-testbench.sh
COMPOSE_CMD="docker compose" ./tests/security/security-multihost-testbench.sh
```

The script writes an artifact report to:

```text
tests/security/artifacts/security-multihost-report.txt
```

## Topology under test

```text
Docker host
  |
  | published ports 18080(edge ingress), 18444(edge admin)
  v
edge-net: curl-client + edge, with host-published ingress/admin ports
  |
relay connected to edge-net and all isolated service networks
  |
svc-one-net: service-one -> dummy-api-server-one
svc-two-net: service-two -> dummy-api-server-two
attacker-net: attacker-service -> dummy-api-server-attacker
rogue-net: rogue-svc-one -> dummy-api-server-attacker
```

All libp2p peers intentionally share the same private swarm key in this testbench. That models an insider or compromised node inside the private overlay.

## Findings tracked by the testbench

### SEC-001: unauthenticated admin API route injection

Severity: high

The edge admin API exposes `POST /add_route` without authentication.

A client that can reach the admin listener can add an arbitrary route, for example:

```text
Host: admin-hijack -> service attacker
```

Impact:

- attacker can publish a malicious service;
- attacker can add a route pointing to it;
- traffic for an arbitrary hostname can be routed to the attacker-controlled service.

Expected mitigation:

- admin API must bind to loopback by default;
- require authentication for mutating endpoints;
- preferably disable admin mutation endpoints unless explicitly enabled;
- add audit logs for route changes.

### SEC-002: admin API exposed on host port in insecure deployments

Severity: high

The testbench intentionally publishes the admin API on `127.0.0.1:18444` to prove reachability from the Docker host.

In production, exposing the admin API externally would be critical because it currently has no auth.

Expected mitigation:

- never publish admin port by default in production compose;
- document admin bind best practice;
- add auth before exposing admin API;
- add `tubo doctor` warning when admin listener is non-loopback or published.

### SEC-003: duplicate service-name takeover by swarm insider

Severity: high

A peer with the private swarm key can announce the same service name as a legitimate service.

The discovery cache is keyed by service name and stores one mutable registration per service:

```text
service name -> peer ID
```

A rogue peer can therefore announce `svc-one` and potentially become the resolved backend for traffic addressed to `svc-one`.

Impact:

- malicious service impersonation;
- data exfiltration;
- request tampering;
- denial of service by flapping announcements.

Expected mitigation:

- enforce `ServiceName -> PeerID` binding at the edge;
- optionally pin allowed peer IDs per service in config;
- reject service-name collisions unless policy explicitly allows multiple replicas;
- if replicas are allowed, require an authorized peer set per service;
- include collision events in admin diagnostics.

### SEC-004: ingress has no client authentication

Severity: medium/high depending on deployment

The edge ingress accepts anonymous HTTP requests for any published service reachable through Host routing.

This may be intended for public services, but it is unsafe as a default for private/internal APIs.

Expected mitigation:

- add optional bearer token / mTLS / OIDC / basic auth at edge;
- allow per-route auth policy;
- default examples should make auth posture explicit;
- add rate limiting.

### SEC-005: no enforced service identity policy

Severity: high

The route table and discovery cache are based on service names. The current design does not enforce that a route to `svc-one` can only resolve to a specific peer ID or authorized peer set.

This is the design-level root cause behind duplicate service-name takeover.

Expected mitigation:

- policy model:

```yaml
services:
  svc-one:
    allowed_peers:
      - 12D3KooW...
```

- route model should optionally pin peer identity:

```yaml
routes:
  - hostname: svc-one
    service: svc-one
    allowed_peers:
      - 12D3KooW...
```

- discovery should reject unauthorized announcements before updating cache.

## Additional concerns not yet automated

These should be added to the testbench later:

1. Invalid/mismatched private swarm key should not discover or route traffic.
2. `LIBP2P_ALLOWED_PEERS` should be enforced consistently on relay, edge, service and bridge.
3. Replay protection for signed announcements: nonce/timestamp/sequence should prevent old announcements from being replayed.
4. Announcement TTL bounds: reject extreme TTLs if route expiry ever starts trusting peer-provided TTL.
5. Request size limits: edge should reject excessive bodies before consuming memory/bandwidth.
6. Header spoofing: sanitize or set `X-Forwarded-*` consistently.
7. Host header normalization: reject invalid/ambiguous hostnames.
8. Admin API CSRF risk if admin listener is reachable from a browser context.
9. Rate limits for discovery pubsub and edge ingress.
10. Secret handling: examples should not reuse fixed PSKs outside test fixtures.

## Current intended status

These tests are expected to expose weaknesses today. They are not yet pass/fail security gates.

As fixes land, each finding should be converted into a regression assertion where the secure behavior is expected.
