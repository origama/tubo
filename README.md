# libp2p API Tunnel Platform

Piattaforma self-hosted per esporre API HTTP dietro NAT/firewall tramite stream libp2p.

Nota sicurezza: il canale libp2p tra `edge` e `service` e' gia' cifrato e autenticato anche quando il traffico passa via relay. La private swarm PSK, quando usata, aggiunge isolamento della rete oltre alla cifratura del trasporto. Vedi anche `docs/SECURITY.md`.

## Per coding agents

Entry point obbligatorio:

- [AGENTS.md](./AGENTS.md)

Tracking obbligatorio del lavoro:

- [TASKS.md](./TASKS.md)

Policy di versioning:

- [docs/VERSIONING.md](./docs/VERSIONING.md)

## Quick Start locale

Con Docker Compose:

```bash
docker compose up -d --build
./tests/smoke-compose.sh
```

Con `tubo` locale:

```bash
go build ./cmd/tubo
tubo keygen swarm --out swarm.key
tubo init topology --out topology.yaml
tubo topology render --config topology.yaml --out generated
tubo relay run --config generated/relay.yaml
tubo edge run --config generated/edge.yaml
tubo service run --config generated/lmstudio.yaml
```

Per un service tipico:

```bash
tubo service run --config service.yaml
# oppure
tubo service run --name lmstudio --target http://192.168.1.28:1234 --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3...
```

## Punto unico per runbook operativo

Per avvio componenti e creazione tunnel p2p sicuro tra due o piu' servizi:

- [docs/OPERABILITY.md](./docs/OPERABILITY.md)

## Documentazione

Indice completo:

- [docs/README.md](./docs/README.md)
