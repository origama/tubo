# Tests

## Smoke E2E (Docker Compose)

Esegue il percorso minimo completo:

- `p2p-relay`
- `dummy-api-server`
- `edge-gateway`
- `service-agent`
- request HTTP reale via edge

Comando:

```bash
./tests/smoke-compose.sh
```

Di default lo script esegue build/compose con:

- `DOCKER_BUILDKIT=0`
- `COMPOSE_DOCKER_CLI_BUILD=0`

per ridurre crash intermittenti del daemon Docker/BuildKit.

Il test verifica:

- health endpoints up
- discovery cache popolata (`/services`)
- auto-route presente (`/routes`)
- chiamata end-to-end `Host: myapi` con risposta HTTP 200 e payload coerente

## Smoke E2E Relay/NAT-like (Docker Compose con reti isolate)

Simula tre macchine logiche:

- `edge-gateway` su una rete privata dedicata
- `service-agent` + `dummy-api-server` su un'altra rete privata dedicata
- `p2p-relay` collegato ad entrambe le reti

In questo scenario `edge-gateway` e `service-agent` **non condividono una rete Docker**, quindi il direct dial non e' disponibile e il traffico deve passare via relay.

Comando:

```bash
./tests/smoke-compose-relay-nat.sh
```

Il test verifica anche nei log dell'edge che il percorso usato sia `connection_path=relayed`.

## Smoke E2E Private Overlay Multi-Service (Docker Compose con 3 service nodes)

Simula una overlay libp2p privata condivisa da:

- `p2p-relay`
- `edge-gateway`
- `curl-client` sulla stessa rete privata dell'edge
- tre nodi service isolati, ciascuno con:
  - `service-agent-*`
  - `dummy-api-server-*`

Topologia Docker:

- `edge-gateway` e `curl-client` su `edge-private`
- ogni service node su una propria rete privata dedicata
- `p2p-relay` collegato a tutte le reti private
- tutti i peer libp2p usano la stessa private swarm PSK (`LIBP2P_PRIVATE_NETWORK_KEY_B64`)

Il `curl-client` richiama sempre lo **stesso endpoint** dell'edge gateway (`http://edge-gateway:8443/v1/dummy`), cambiando solo l'header `Host` tra `svc-one`, `svc-two` e `svc-three`.

Comando:

```bash
./tests/smoke-compose-private-overlay-multi-service.sh
```

Il test verifica:

- healthcheck di relay, edge e 3 service nodes
- discovery cache con `count=3`
- auto-route per `svc-one`, `svc-two`, `svc-three`
- routing host-based verso i tre backend tramite un solo endpoint edge
- risposta coerente dal backend atteso (`instance`)

## Integration Tests (Go)

Package: `tests/integration`

Copre:

- auto-discovery + proxy end-to-end
- streaming request/response large body
- lease expiry con rimozione route
- stripping header hop-by-hop
- relay fallback tra reti Docker isolate (`docker-compose.nat.yml`)

Esecuzione:

```bash
RUN_INTEGRATION=1 go test -v ./tests/integration
```

Nota: se il daemon Docker e' indisponibile/crasha, i test vengono marcati `SKIP` (errore infrastrutturale), non `FAIL` applicativo.
I comandi `docker compose` interni ai test usano di default `DOCKER_BUILDKIT=0` e `COMPOSE_DOCKER_CLI_BUILD=0`.
