# libp2p API Tunnel Platform

Piattaforma self-hosted per esporre API HTTP dietro NAT/firewall tramite stream libp2p.

## Per coding agents

Entry point obbligatorio:

- [AGENTS.md](./AGENTS.md)

Tracking obbligatorio del lavoro:

- [TASKS.md](./TASKS.md)

## Quick Start locale

```bash
docker compose up -d --build
./tests/smoke-compose.sh
```

## Punto unico per runbook operativo

Per avvio componenti e creazione tunnel p2p sicuro tra due o piu' servizi:

- [docs/OPERABILITY.md](./docs/OPERABILITY.md)

## Documentazione

Indice completo:

- [docs/README.md](./docs/README.md)
