# libp2p API Tunnel Platform

Piattaforma di trasporto API nativa su libp2p. Permette a client e servizi di operare dietro NAT, supportando HTTP e streaming robusti con isolamento per tenant — **senza alcun control plane centrale**.

## 🎯 Obiettivo

Costruire un sistema self-hosted open-source basato sul protocollo P2P nativo (libp2p), dove ogni nodo è autonomo e la discovery avviene in modo distribuito tramite pubsub.

## 🏗️ Architettura — Flat-First (No Control Plane)

Il design è **flat-first**: nessun server centrale coordina il sistema. I componenti comunicano direttamente tra loro:

* **Edge Gateway** — Ingress HTTP/HTTPS pubblico. Riceve richieste, risolve il peer di destinazione tramite discovery cache distribuita, apre uno stream libp2p e inoltra la richiesta.
* **Connector Agent** — Reside accanto al servizio origin (behind NAT). Si annuncia sulla rete pubsub, riceve stream dagli Edge Gateway e li forward sul localhost/unix socket del servizio locale.
* **Discovery via Pubsub** — I Connector pubblicano annunci firmati su un topic pubsub condiviso. Gli Edge Gateway sottoscrivono il topic e mantengono una cache locale di servizi disponibili. Lease + heartbeat garantiscono la freschezza dei record.
* **Relay (fallback)** — Quando il direct dial fallisce (NAT simmetrico, firewall), il traffico viene instradato attraverso nodi relay pubblici.

```
Client HTTP ──→ [Edge Gateway] ──stream libp2p──→ [Connector Agent] ──HTTP──→ Origin Service
                      │                                    │
                      │  pubsub discovery (annunci)        │  localhost/unix socket
                      ▼                                    │
              [Discovery Cache] ◄──── annuncia ───────────┘
```

## 🛠️ Tech Stack

* **Linguaggio Primario:** Go
* **Networking:** libp2p Go stack (`libp2p/go-libp2p`)
* **Configurazione:** YAML / TOML
* **API di Gestione:** OpenAPI (per Edge Gateway admin)

## 🧱 Struttura del Monorepo

```
cmd/                    # Binari eseguibili
├── edge-gateway/       # HTTP ingress + routing + forwarding
├── connector/          # Agent sidecar per servizi origin
└── dummy-api-server/   # Servizio mock per testing

internal/               # Librerie condivise
├── p2p/                # Host creation, libp2p setup
├── protocol/           # Wire protocol (framing binario)
├── discovery/          # Pubsub-based service discovery
├── routing/            # Hostname+path → peer_id matching
├── forwarding/         # HTTP ↔ stream forwarding
├── auth/               # AuthN/AuthZ (bearer tokens, peer binding)
└── observability/      # Logging + metrics

deploy/                 # Docker Compose, Dockerfiles
docs/                   # Documentazione architettura e protocollo
```

## 🛣️ Roadmap & Progress

Tutto il lavoro di implementazione è tracciato in [TASKS.md](./TASKS.md).
Le specifiche del progetto per agenti AI sono in [AGENTS.md](./AGENTS.md).

Per una panoramica rapida:

| Fase | Componente | Stato |
|------|-----------|-------|
| 0 | Decisione architetturale (flat-first) | ✅ Completato |
| 1 | Protocollo wire (framing binario + streaming) | ✅ Completato |
| 2 | Discovery via pubsub (annunci firmati, lease, heartbeat) | ✅ Completato |
| 3 | Edge Gateway (HTTP ingress + routing + forwarding) | 🔲 Da fare |
| 4 | Connector Agent (pubsub announcement + stream handler + localhost forward) | ⏳ In lavorazione |
| 5 | Relay fallback (bootstrap nodes, NAT traversal) | 🔲 Da fare |
| 6 | Security & Auth (bearer token, peer binding, tenant isolation, replay protection) | 🔲 Da fare |
| 7 | Testing completo (unit + integration + E2E docker-compose) | 🔲 Da fare |

Consulta [TASKS.md](./TASKS.md) per i dettagli granulari di ogni fase.

## 🚀 Quick Start (Testing)

```bash
# Build tutti i binari
go build ./cmd/...

# Avvia il servizio mock
./dummy-api-server --port 8081

# Avvia il connector agent (si connette al servizio locale)
./service-agent --origin-addr localhost:8081 --service-id myapi

# Avvia l'edge gateway
./edge-gateway --listen :8080

# Testa la connessione
curl http://localhost:8080/myapi/health
```

> ⚠️ L'Edge Gateway è attualmente uno stub — il forwarding completo è in fase di sviluppo. Vedi [TASKS.md](./TASKS.md) per i dettagli.

## 📄 Documentazione

* [Architettura](docs/ARCHITECTURE.md) — Design dettagliato dei componenti
* [Protocollo Wire](docs/PROTOCOL.md) — Framing binario, messaggi, streaming
* [Security](docs/SECURITY.md) — Principi di sicurezza e mitigazioni
