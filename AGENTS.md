# AGENTS.md — Tubo Rebuilt coding-agent contract

This is the required entry point for coding agents.

## Mission

Implement Tubo as a private libp2p connectivity substrate for local HTTP, WebSocket, raw TCP/TLS, and future agent endpoints.

This repository is a greenfield implementation. It intentionally provides no CLI, state, or wire compatibility with the previous Tubo repository. Reuse old code only after proving that it fits this architecture without compatibility adapters or legacy concepts.

## Product model

The canonical ontology is:

```text
Service = local terminal, never a network advertisement
Pipe    = persistent directed connectivity resource
Run     = execution of a primary resource
Session = concrete traversal of one or more pipes
Record  = signed network advertisement for a pipe
```

A pipe always has the form:

```text
Ingress -> Pipe -> Target
```

The target is exactly one `service/...` or `pipe/...`. The ingress is local, gateway, or Tubo/libp2p.

`attach` and `connect` are UX presets, not runtime kinds:

```text
attach  = define local service + define/start published pipe
connect = resolve remote pipe + define/start local pipe
```

Never reintroduce separate service/bridge/client runtimes.

## Core invariants

Before implementation, read `docs/reference/INVARIANTS.md`. In particular:

- only pipes are advertised and remotely addressable;
- `pipe_id` is the secure network identity; names are non-unique labels;
- local service targets never appear in discovery records, invites, logs, or remote traces;
- caches, query peers, relays, and sync peers are not authorities;
- one active authority is allowed per scope;
- each network hop authorizes independently;
- re-export requires explicit `pipe.forward` authority;
- session setup rejects loops and enforces a hop limit;
- logical, authorization, and transport paths are separate concepts;
- stdout/stderr and JSON output contracts are binding;
- configuration is not a mutable database.

## Required reading

1. assigned GitHub Issue and linked issues, once a remote exists;
2. `docs/README.md`;
3. `docs/reference/ARCHITECTURE.md`;
4. `docs/reference/ONTOLOGY.md`;
5. `docs/reference/INVARIANTS.md`;
6. `docs/reference/SECURITY.md`;
7. `docs/reference/PROTOCOL.md` for wire work;
8. `docs/reference/STATE.md` for persistence work;
9. `docs/reference/CLI.md` for commands/output;
10. `docs/testing/ACCEPTANCE_MATRIX.md`;
11. `docs/runbooks/THREE_MACHINE_E2E.md` for distributed behavior;
12. `CHANGELOG.md` and `docs/reference/VERSIONING.md`.

## Architecture boundaries

Prefer these packages:

```text
cmd/tubo/             thin executable
internal/cli/         parsing and presentation
internal/domain/      pure service/pipe/session/grant model
internal/store/       config, transactional state, secrets
internal/node/        libp2p host kernel and module lifecycle
internal/transport/   bootstrap, relay, NAT, reachability
internal/pipe/        pipe plans, adapters, sessions, forwarding
internal/discovery/   signed pipe records, cache, gossip/query/sync
internal/authority/   policies, grants, leases, revocations
internal/runtime/     executor, readiness, registry, stats
```

Dependencies should point inward toward `internal/domain`. Domain code must not import libp2p, CLI, storage, or operating-system process code.

Do not create packages merely to match this map; create them only when a real boundary exists.

## Engineering rules

- Keep each change small and independently verifiable.
- Add characterization/negative tests before security-sensitive or concurrent behavior.
- Use standard library facilities before custom protocol machinery.
- Use one authenticated tunnel handshake followed by raw duplex bytes.
- Keep protocol decoders bounded before allocation.
- Use an embedded transactional store for mutable metadata; keep secrets separate.
- Make writes and detached startup fail closed.
- Never hide persistence failures with in-memory success.
- Do not add a mandatory central daemon.
- Do not build a custom network layer above libp2p.
- Do not add DHT/rendezvous until a concrete issue and threat analysis require it.
- Avoid global mutable state and package-level runtime registries.
- No hidden TODOs: open an issue or implement the behavior.

## Testing contract

Local gate:

```bash
make verify-local
```

Distributed behavioral gate, using isolated state on `tuborelay`, `oripi`, and `macb`:

```bash
make e2e-three-machine-preflight
TUBO_E2E_CONFIRM=three-machine make e2e-three-machine E2E_SCENARIO=private-http
```

The distributed harness must never overwrite `/usr/local/bin/tubo`, existing Tubo configuration, systemd units, or another repository's state. It uses run-specific directories under `/tmp` and `generated/`.

When changing behavior represented in `docs/testing/ACCEPTANCE_MATRIX.md`, update the corresponding local, integration, and three-machine evidence.

## Documentation and release rules

Any CLI, state, security, wire, operational, or test behavior change must update its canonical document in the same PR.

This repository starts at protocol `2.0`. There is no legacy fallback. Protocol changes still follow `docs/reference/VERSIONING.md`.

Never commit secrets, swarm keys, authority keys, pipe-owner keys, access/refresh leases, invite tokens, local state databases, process state, logs, or user-specific paths.
