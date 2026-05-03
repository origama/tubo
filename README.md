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

Con `tubo` locale, la UX primaria e' intent-based:

```bash
go build -o tubo ./cmd/tubo

# host relay
tubo relay -d

# host service
tubo join --relay /ip4/RELAY_IP/tcp/4001/p2p/RELAY_PEER --swarm-key ./swarm.key
tubo attach http://127.0.0.1:1234 --name lmstudio -d

# host client
tubo join --relay /ip4/RELAY_IP/tcp/4001/p2p/RELAY_PEER --swarm-key ./swarm.key
tubo get services
tubo describe service/lmstudio
tubo connect lmstudio --local 127.0.0.1:51234 -d
```

Per il layer advanced / compatibility restano disponibili anche i role commands:

```bash
tubo relay run --config relay.yaml
tubo edge run --config edge.yaml
tubo service run --config service.yaml
tubo bridge run --config bridge.yaml
```

Per una topologia locale renderizzata da file:

```bash
tubo keygen swarm --out swarm.key
tubo init topology --out topology.yaml
tubo topology render --config topology.yaml --out generated
```

Per la guida CLI completa vedi anche `docs/cli.md`.

## Punto unico per runbook operativo

Per avvio componenti e creazione tunnel p2p sicuro tra due o piu' servizi:

- [docs/OPERABILITY.md](./docs/OPERABILITY.md)

## Documentazione

Indice completo:

- [docs/README.md](./docs/README.md)
