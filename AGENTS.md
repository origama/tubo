# AGENTS.md — Project Specification & Agent Instructions

## What This Project Is

**P2P API Tunnel Platform** — a self-hosted, flat-first system that lets HTTP clients reach services hidden behind NAT/firewalls via encrypted libp2p streams. No central control plane: discovery is distributed through pubsub announcements.

### Core Flow

```
Client HTTP ──→ [Edge Gateway] ──stream libp2p──→ [Connector Agent] ──HTTP──→ Origin Service
                      │                                    │
                      │  pubsub discovery (signed)         │  localhost/unix socket
                      ▼                                    │
              [Discovery Cache] ◄──── announces ──────────┘
```

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Language | Go 1.24+ |
| Networking | libp2p (`go-libp2p` v0.44.0) |
| NAT Traversal | Hole Punching (pion/ice), Relay nodes, STUN/TURN |
| Transport | TCP, QUIC/WebTransport, WebSocket |
| Config | YAML / TOML |
| Admin API | OpenAPI spec |
| Metrics | Prometheus + Uber zap logging |

## Repository Structure

```
cmd/                    # Executable binaries
├── edge-gateway/       # Public HTTP ingress (functional: routing, discovery, relay fallback, admin API)
├── service-agent/      # Connector sidecar (functional: announces, handles streams, forwards to origin)
├── client-bridge/      # Client-side proxy over p2p (functional: direct dial + forwarding)
└── dummy-api-server/   # Mock API for testing

internal/               # Shared libraries
├── p2p/                # Host creation, seed-key identity, stream forwarding helpers
├── protocol/           # Binary wire framing (varint length + type byte + payload)
├── discovery/          # PubSub-based service discovery with signed announcements & TTL cache
├── routing/            # Hostname+path → peer_id matching logic
├── forwarding/         # HTTP ↔ stream hop-by-hop header stripping
├── auth/               # AuthN/AuthZ scaffolding (bearer tokens, peer binding) — NOT WIRED IN
└── observability/      # Logging + metrics setup

deploy/                 # Dockerfiles + docker-compose.yml
docs/                   # Architecture, Protocol spec, Security policy
```

## Wire Protocol (Binary Framing)

Every message on a libp2p stream follows this frame format:

```
[ varint length ] [ 1-byte type ] [ payload... ]
```

| Type Byte | Frame | Direction | Payload Schema |
|-----------|-------|-----------|----------------|
| `0x01` | RequestHeader | Edge → Connector | method, path, query, headers (multi-value), contentLengthHint |
| `0x02` | ResponseHeader | Connector → Edge | statusCode, statusText, headers (multi-value) |
| `0x03` | BodyChunk | Both directions | data ([]byte), isFinal (bool) |
| `0x04` | Error | Either direction | code (int), message (string) |

**Streaming flow:** RequestHeader → BodyChunk* → {isFinal:true} → ResponseHeader → BodyChunk* → {isFinal:true}

See [`docs/PROTOCOL.md`](docs/PROTOCOL.md) for the full spec.

## Code Conventions

1. **Error handling**: Always check errors explicitly. No silent ignores — log or return.
2. **Context propagation**: Every function that can block takes `context.Context` as first arg.
3. **Naming**: Go standard conventions. Package names lowercase, no underscores. Exported types get descriptive names.
4. **Testing**: Every package with logic must have `_test.go` files. Table-driven tests preferred.
5. **Mutex safety**: Prefer `sync.Mutex` over `sync.RWMutex` unless benchmarks prove RWMutex is needed. Read locks in one goroutine + write locks in another = deadlock risk.
6. **libp2p peer IDs**: Never compare `peer.ID.String()` against seed strings — libp2p encodes IDs in base58. Always store and compare the `peer.ID` object directly.
7. **Git commits**: Conventional Commits format: `<type>(<scope>): <description>` with body for details. Types: `fix`, `feat`, `test`, `docs`, `refactor`.

## Security Principles (Zero Trust)

- Every peer authenticates via Ed25519 keypair
- Discovery announcements are cryptographically signed
- Lease + heartbeat expiry prevents stale records
- Rate limiting on pubsub topic
- Replay protection via nonce/timestamp
- Bearer token auth for HTTP ingress
- Peer identity binding (token → specific peer ID)

See [`docs/SECURITY.md`](docs/SECURITY.md) for details.

## Task Tracking

All implementation progress is tracked in **[TASKS.md](./TASKS.md)**. Before starting any work:

1. Read this AGENTS.md to understand the project
2. Check TASKS.md for current status and next steps
3. Update TASKS.md when you complete or change anything

## Completion Gate (Obbligatorio)

Un lavoro e' **DONE** solo se tutti i test sotto passano nello stesso run:

1. `go test ./...`
2. `go test -count=1 ./tests/integration -run TestEdgeAutoDiscoveryAndProxy`
3. `go test -count=1 ./tests/integration -run TestStreamingLargeBodiesNoHang`
4. `go test -count=1 ./tests/integration -run TestLeaseExpiryRemovesServiceAndRoute`
5. `go test -count=1 ./tests/integration -run TestHopByHopHeadersStrippedE2E`

Se `tests/integration` non esiste, va creato prima di chiudere il task.

## Required Integration Assertions

- `TestEdgeAutoDiscoveryAndProxy`: con `dummy-api-server`, `edge-gateway`, `service-agent` avviati, entro 10s `GET /services` (admin edge) deve avere `count=1`, `GET /routes` deve contenere la route auto-creata (`hostname=serviceName`, `path_prefix=/`), e una richiesta `POST` con `Host: <serviceName>` deve tornare `200`.
- `TestStreamingLargeBodiesNoHang`: request body e response body >= 128KiB devono completare entro timeout senza deadlock/hang.
- `TestLeaseExpiryRemovesServiceAndRoute`: fermando il service-agent e aspettando `TTL + grace`, `/services` torna `0`, la route sparisce e una nuova richiesta torna `404` (non route stale).
- `TestHopByHopHeadersStrippedE2E`: header hop-by-hop (`Connection`, `Keep-Alive`, `Transfer-Encoding`, `Upgrade`, `Proxy-Authenticate`, `Proxy-Authorization`, `Te`, `Trailer`) non devono arrivare all'origin.

## Scope Note

AutoNAT, hole punching e Security/Auth (bearer, peer binding, replay protection, rate limiting) restano fuori dal gate MVP finche' non implementati.
