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

## Integration Tests (Go)

Package: `tests/integration`

Copre:

- auto-discovery + proxy end-to-end
- streaming request/response large body
- lease expiry con rimozione route
- stripping header hop-by-hop

Esecuzione:

```bash
RUN_INTEGRATION=1 go test -v ./tests/integration
```

Nota: se il daemon Docker e' indisponibile/crasha, i test vengono marcati `SKIP` (errore infrastrutturale), non `FAIL` applicativo.
I comandi `docker compose` interni ai test usano di default `DOCKER_BUILDKIT=0` e `COMPOSE_DOCKER_CLI_BUILD=0`.
