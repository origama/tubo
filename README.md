# Tubo

Tubo is a secure peer-to-peer connectivity and collaboration platform.

```text
local service -> Tubo attach -> exact invite -> clean Tubo installation
              -> short-lived Access Lease -> authorized encrypted connectivity
```

Tubo grows from exact sharing into scoped collaboration, private/hybrid infrastructure, persistent service connectivity, signed Pipe discovery, gateways, catalogs, MCP, A2A, agent discovery, and capability search. It is not primarily a VPN, reverse proxy, service mesh, or centralized tunnel broker.

## Model

- **Service:** local HTTP, TCP, or Unix endpoint. Never automatically discoverable.
- **Pipe:** stable directed resource, `Ingress -> Pipe -> Target`; target is Service or Pipe.
- **Session:** live traversal with byte-flow and continuity semantics.
- **Invite:** exact bootstrap/redemption credential.
- **Access Lease:** short-lived cryptographic authorization for one remote Pipe open.
- **Scope:** optional collaboration/security domain, not prerequisite for connectivity.
- **Descriptor:** higher-level metadata, such as AgentDescriptor, referencing a Pipe.

Every remote Pipe open requires verified short-lived Access Lease. Pipe ID, route, record, invite, or membership alone never authorize. Public bootstrap/relay provides reachability, never ambient authorization.

## Development status

Pre-A0 implementation is permanently preserved as **prototype reference baseline**: [`prototype-v2-final`](docs/implementation/PROTOTYPE_BASELINE.md), commit `637c3859051103c844a042dac04c40b7ce0627e1`. Code, tests, and docs at tag preserve prototype behavior, security learning, and failure modes. Main progressively diverges during A0 and later phases; prototype internals are not target architecture or compatibility constraint.

Architecture-driven **A0 — Layered Architecture Foundation** is complete (epic #18): the exact minimal sharing path — attach a local Service, share it as an exact invite, accept on a clean installation, connect through a short-lived Access Lease with renewal and revocation — is implemented end to end on the target architecture, with Journey A durable remote-Pipe binding including invite expiry, owner/consumer restarts, revocation, and re-authorization proven by scenario tests (`make verify-local`). Supported-path correctness fixes continue on main (issue #76); A1 (Scope Authority and deferred-capability reintroduction) is tracked in the roadmap and issues.

## Minimal use case (exact sharing)

Owner machine (service is local and stays local):

```bash
tubo attach http://127.0.0.1:8080 --name origin      # define local Service
tubo start pipe/origin                                  # run the Pipe (zero-config reachability via public relay)
tubo share pipe/origin                               # prints a one-time exact invite
```

Consumer machine (clean Tubo installation):

```bash
tubo connect --token "$INVITE" --local 127.0.0.1:9001   # redeem invite, run local ingress
curl http://127.0.0.1:9001/                             # authorized encrypted connectivity
```

The invite is redeemed exactly once and becomes a durable accepted binding; later `tubo connect --remote <pipe-id>` reuses it, renewing short-lived Access Leases per open. `tubo revoke pipe-access pipe/origin` on the owner immediately fences old authorizations; a fresh share re-authorizes. `tubo revoke invite <jti-or-full-token>` fences a not-yet-redeemed invite.

See [CLI reference](docs/reference/CLI.md).

## Start here

- [Coding-agent contract](AGENTS.md)
- [High-level design](docs/reference/HIGH_LEVEL_DESIGN.md)
- [Implementation boundaries](docs/reference/ARCHITECTURE.md)
- [A0–A4 roadmap](docs/implementation/ROADMAP.md)
- [Prototype baseline](docs/implementation/PROTOTYPE_BASELINE.md)
- [Architecture acceptance](docs/testing/ARCHITECTURE_ACCEPTANCE.md)
- [Documentation map](docs/README.md)

## Verification

```bash
go test ./...
make verify-local
```

Distributed tests require isolated state and explicit confirmation. Read [three-machine E2E runbook](docs/runbooks/THREE_MACHINE_E2E.md) before running them.
