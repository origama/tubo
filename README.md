# Tubo Rebuilt

Greenfield implementation of Tubo around a service/pipe graph.

> Status: first usable private-HTTP slice implemented. SQLite state, private libp2p relay transport, scoped signed membership, remote publication, verified pipe discovery, authenticated HTTP/WebSocket/raw-TCP tunnels, HTTPS upstreams, detached lifecycle, logs, inventory, and local diagnostics are available. Forwarding, gossip sync, gateway, access refresh/revocation, and direct-path upgrade remain future work.

## Product model

```text
Service = local endpoint
Pipe    = connectivity resource
Session = active traversal
```

A pipe connects one ingress to exactly one target:

```text
Ingress -> Pipe -> Service
Ingress -> Pipe -> Pipe
```

Example:

```text
curl
  -> pipe/lmstudio-local
  -> pipe/lmstudio-team
  -> service/lmstudio
  -> http://127.0.0.1:1234
```

Only the published pipe is discovered and authorized. The origin service stays local and private.

## Intended UX

```bash
# Ergonomic presets
tubo attach http://127.0.0.1:1234 --name lmstudio -d
tubo connect lmstudio --local 127.0.0.1:51234 -d

# Explicit resource form
tubo service create lmstudio --target http://127.0.0.1:1234
tubo pipe create lmstudio-team \
  --ingress tubo \
  --target service/lmstudio \
  --visibility discoverable \
  --access members
tubo start pipe/lmstudio-team -d
```

`attach` and `connect` compile to pipe definitions. They are never runtime kinds.

## Local verification

```bash
make verify-local
./bin/tubo version --json   # after `mkdir -p bin && go build -o bin/tubo ./cmd/tubo`
```

`verify-local` includes an isolated loopback relay/publisher/member/viewer integration scenario. Distributed gate uses isolated three-machine scenario:

```bash
make e2e-three-machine-preflight
TUBO_E2E_CONFIRM=three-machine make e2e-three-machine E2E_SCENARIO=private-http
```

See `docs/runbooks/THREE_MACHINE_E2E.md` before running it.

## Canonical documentation

- [Architecture](docs/reference/ARCHITECTURE.md)
- [Ontology](docs/reference/ONTOLOGY.md)
- [Invariants](docs/reference/INVARIANTS.md)
- [CLI contract](docs/reference/CLI.md)
- [Protocol](docs/reference/PROTOCOL.md)
- [Security](docs/reference/SECURITY.md)
- [State model](docs/reference/STATE.md)
- [Acceptance matrix](docs/testing/ACCEPTANCE_MATRIX.md)
- [Three-machine E2E runbook](docs/runbooks/THREE_MACHINE_E2E.md)
- [Coding-agent contract](AGENTS.md)

## Non-goals

- compatibility with the previous Tubo implementation;
- a mandatory daemon;
- service records in network discovery;
- separate attach/connect runtime stacks;
- treating relays or discovery caches as trust roots;
- a custom network layer above libp2p.
