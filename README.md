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

Architecture-driven **A0 — Layered Architecture Foundation** has begun. First implemented slice establishes pure L0 Service/Pipe validation and dependency guard; remaining A0 runtime, authorization, reachability, persistence, and use-case work is tracked in GitHub issues.

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
