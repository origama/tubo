# Tests

## Smoke E2E (Docker Compose)

Esegue il percorso minimo completo:

- `dummy-api-server`
- `edge-gateway`
- `service-agent`
- request HTTP reale via edge

Comando:

```bash
./tests/smoke-compose.sh
```

Il test verifica:

- health endpoints up
- discovery cache popolata (`/services`)
- auto-route presente (`/routes`)
- chiamata end-to-end `Host: myapi` con risposta HTTP 200 e payload coerente
