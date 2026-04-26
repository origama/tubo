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
├── service-agent/      # Agent sidecar per servizi origin
├── client-bridge/      # HTTP client → P2P stream bridge
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

## 🚀 Quick Start (Docker Compose)

Prerequisiti: Docker + Docker Compose plugin.

```bash
# Avvio stack minimo (build incluso)
docker compose up -d --build

# Verifica health principali
curl -fsS http://localhost:8443/healthz
curl -fsS http://localhost:8444/healthz
curl -fsS http://localhost:8091/healthz

# Verifica discovery/route sull'admin edge
curl -fsS http://localhost:8444/services
curl -fsS http://localhost:8444/routes

# Chiamata end-to-end via edge gateway
curl -i -H 'Host: myapi' -X POST --data 'hello' \
  'http://localhost:8443/v1/dummy?from=readme'

# Spegnimento
docker compose down
```

## 🧪 Smoke Test E2E

```bash
./tests/smoke-compose.sh
```

Lo script avvia i container, aspetta health + discovery, esegue una richiesta reale
`client -> edge -> service-agent -> dummy-api-server` e fallisce con exit code != 0 se il flusso non e' funzionante.

## 📄 Documentazione

* [Architettura](./ARCHITECTURE.md) — Design dettagliato dei componenti
* [Protocollo Wire](./docs/PROTOCOL.md) — Framing binario, messaggi, streaming
* [Security](./docs/SECURITY.md) — Principi di sicurezza e mitigazioni
