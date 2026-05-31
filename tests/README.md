# Tests

## Smoke E2E CLI UX v2 (locale, senza Docker)

Valida i principali happy path documentati per la nuova CLI user-facing usando solo processi locali, porte dinamiche, directory temporanee e mock HTTP server locale.

Copre:

- `relay -d`
- `join`
- `attach -d`
- `ps` / `get processes` / `describe process/...` / `inspect process/... --json` / `logs` / `stop` / `rm --stale`
- `get services` senza gateway locale (observer effimero)
- `get service/<name>` / `describe service/<name>` / `inspect service/<name> --json`
- `connect <service-name>` + richiesta HTTP reale
- `gateway -d` + richiesta HTTP reale con `Host:`
- smoke di foreground-by-default per `attach` senza `-d`

Comando:

```bash
./tests/smoke-cli-ux.sh
```

Imposta `KEEP_WORK=1` per preservare la working directory temporanea in caso di debug.

## Smoke E2E (Docker Compose)

Esegue il percorso minimo completo:

- `relay`
- `dummy-api-server`
- `edge`
- `service`
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

## Smoke E2E tubo UX (Docker Compose)

Verifica la nuova UX con immagine unica `tubo` e config YAML:

- `tubo relay --config /etc/tubo/relay.yaml`
- `tubo gateway --config /etc/tubo/edge.yaml`
- `tubo attach --config /etc/tubo/service.yaml`

Comando:

```bash
./tests/smoke-compose-tubo.sh
```

Lo script prepara `generated/integration/tubo/*.yaml`, avvia `tests/e2e/compose/tubo/compose.yml`, attende health/discovery/route e fa una richiesta end-to-end via edge.

## Archiviato: Relay/NAT-like (Docker Compose con reti isolate)

Lo scenario relay-first su reti Docker isolate e' stato archiviato in:

- `tests/archive/compose/relay-nat/compose.yml`

I benchmark/perf manuali che lo usano puntano a quell'archivio.

## Smoke E2E Private Overlay Multi-Service (Docker Compose con 3 service nodes)

Simula una overlay libp2p privata condivisa da:

- `relay`
- `edge`
- `curl-client` sulla stessa rete privata dell'edge
- tre nodi service isolati, ciascuno con:
  - `service-*`
  - `dummy-api-server-*`

Topologia Docker:

- `edge` e `curl-client` su `edge-private`
- ogni service node su una propria rete privata dedicata
- `relay` collegato a tutte le reti private
- tutti i peer libp2p usano la stessa private swarm PSK (`LIBP2P_PRIVATE_NETWORK_KEY_B64`)

Il `curl-client` richiama sempre lo **stesso endpoint** dell'edge gateway (`http://edge:8443/v1/dummy`), cambiando solo l'header `Host` tra `svc-one`, `svc-two` e `svc-three`.

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

## Smoke E2E Distributed 2-host (edge locale + relay remoto)

Smoke reale su 2 macchine:

- `edge` sulla macchina locale (`172.236.202.99` di default)
- `relay` sulla macchina remota (`172.232.189.160`)
- `service` + `dummy-api-server` co-hosted sulla macchina remota

Comando:

```bash
./tests/smoke-distributed-two-host.sh
```

Il `service` remoto viene forzato a usare `p2p_listen=/ip4/127.0.0.1/tcp/40123` e `force_reachability: private`, cosi' l'edge non puo' fare direct dial pubblico e deve passare via relay.

Dettagli operativi: `tests/distributed-two-host.md`

## Smoke E2E Linode/Terraform (3 host multi-region)

Provisioning distribuito tramite Terraform:

- `relay` pubblico su Linode
- `edge` su Linode con ingress chiuso (SSH-only, NAT-like)
- `service` su Linode con ingress chiuso (SSH-only, NAT-like)

Terraform stack:

- `infra/terraform/linode-distributed/`

Smoke harness:

```bash
./tests/smoke-terraform-linode.sh
```

Lo smoke legge gli IP da `terraform output`, carica binari + config sui nodi e verifica il percorso relay-first controllando `connection_path=relayed` nei log edge.

## Smoke mixed-version su Linode/Terraform

Valida compatibilita' tra binari `tubo` di versioni diverse sul bench multi-host reale.

Comando:

```bash
./tests/smoke-terraform-linode-mixed-version.sh
```

Di default lo script costruisce:

- il binario corrente da `main`
- un binario legacy dal ref `c9bbb1f` (pre-protocol 1.1 hello handshake)

Scenari coperti:

- edge corrente -> service legacy (fallback `/p2p-tunnel/1.0`)
- edge legacy -> service corrente (service corrente accetta legacy)
- edge corrente -> service corrente (negoziazione `/p2p-tunnel/1.1`)

Lo script usa anche gli endpoint di debug/admin del protocollo quando disponibili per salvare evidenza operativa della compatibilita'.

## Performance benchmark persistente su Linode/Terraform

Usa il testbed multi-region creato da Terraform, lascia i processi remoti attivi per la durata del benchmark e salva risultati storici confrontabili in:

- `tests/perf/results/linode-terraform/<timestamp>/report.json`
- `tests/perf/results/linode-terraform/<timestamp>/summary.md`
- `tests/perf/results/linode-terraform/latest.json`
- `tests/perf/results/linode-terraform/latest.md`

Comando:

```bash
python3 ./tests/perf/run_linode_terraform_perf.py
```

## Integration Tests (Go)

Package: `tests/integration`

Copre:

- auto-discovery + proxy end-to-end
- streaming request/response large body
- lease expiry con rimozione route
- stripping header hop-by-hop
- scenario relay-first archiviato (`tests/archive/compose/relay-nat/compose.yml`)

Esecuzione:

```bash
RUN_INTEGRATION=1 go test -v ./tests/integration
```

Nota: se il daemon Docker e' indisponibile/crasha, i test vengono marcati `SKIP` (errore infrastrutturale), non `FAIL` applicativo.
I comandi `docker compose` interni ai test usano di default `DOCKER_BUILDKIT=0` e `COMPOSE_DOCKER_CLI_BUILD=0`.
