# AGENTS.md — Entry Point For Coding Agents

Questo file e' il **punto di ingresso canonico** per tutti i coding agents (incluso Codex).

## 1) Missione del progetto

**P2P API Tunnel Platform**: piattaforma self-hosted che inoltra traffico HTTP verso servizi dietro NAT/firewall usando stream libp2p cifrati e discovery distribuito via pubsub firmato.

Flusso base:

```text
Client HTTP -> Edge Gateway -> stream libp2p -> Service Agent -> Origin Service
```

## 2) Stato attuale implementazione (as-is)

Componenti funzionanti:

- `cmd/tubo` (`tubo edge run`): ingress HTTP, discovery subscription, cache locale, auto-route, proxy su stream, relay fallback, admin API.
- `cmd/tubo` (`tubo service run`): annuncio servizio firmato, heartbeat, stream handler, forwarding verso target HTTP locale/remoto.
- `cmd/tubo` (`tubo bridge run`): proxy HTTP client-side verso peer service.
- `cmd/tubo` (`tubo relay run`): nodo pubblico bootstrap/relay v2 con health endpoint, supporto PSK e allowlist PeerID (connection gater).
- `internal/protocol`: framing binario + streaming body bidirezionale.
- `internal/discovery`: announcement signed + TTL cache + eventi add/remove.
- `internal/p2p`: host creation da seed + supporto private swarm PSK + parser allowlist PeerID.

Gap noti:

- allowlist PeerID enforcement completo solo sul relay (non ancora esteso end-to-end a tutti i binari);
- binding `ServiceName -> PeerID` non ancora enforced end-to-end;
- AutoNAT/hole punching non completi;
- diagnostica reachability avanzata non ancora completa.

## 3) Workflow obbligatorio per agent

### 3.1 Prima di iniziare

1. Leggere questo `AGENTS.md`.
2. Leggere `TASKS.md`.
3. Leggere la documentazione rilevante in `docs/`.
4. Se il lavoro tocca release, compatibilita' o protocollo, leggere `docs/VERSIONING.md`.
5. Aggiornare `TASKS.md` segnando il task come `⏳ In progress` (o aggiungendolo se manca).

### 3.2 Durante il lavoro

1. Se cambia comportamento/config/interfaccia, aggiornare **subito** la documentazione in `docs/`.
2. Mantenere coerenza tra codice, env vars e runbook operativi.
3. Non lasciare TODO non tracciati fuori da `TASKS.md`.

### 3.3 Prima di chiudere

1. Eseguire i gate di verifica correnti.
2. Aggiornare `TASKS.md` (stato, note, timestamp).
3. Aggiornare docs toccate dal cambiamento.

## 4) Gate di completion correnti (obbligatori)

Un task e' `DONE` solo se passano:

1. `go test ./...`
2. `./tests/smoke-compose.sh`
3. `RUN_INTEGRATION=1 go test -v ./tests/integration` (raccomandato prima del merge quando Docker e' stabile)

Nota: i test in `tests/integration` saltano (`SKIP`) se il daemon Docker e' indisponibile (errore infrastrutturale).

## 5) Policy documentazione (single source)

Regole:

1. La documentazione tecnica vive in `docs/`.
2. `docs/README.md` e' l'indice canonico della documentazione.
3. I file doc in root (`ARCHITECTURE.md`, `PROTOCOL.md`, `SECURITY.md`) devono essere solo redirect sintetici verso `docs/`.
4. Qualsiasi cambio implementativo deve riflettersi nella doc rilevante nello stesso PR/commit.

## 6) Runbook operativo canonico

Per avvio componenti e creazione tunnel p2p sicuro tra 2+ servizi, riferimento unico:

- `docs/OPERABILITY.md`

Per policy di versioning e compatibilita' tra ruoli/nodi:

- `docs/VERSIONING.md`

In particolare:

- quick start locale con Docker Compose;
- setup private swarm PSK;
- avvio multi-host (`edge` + piu' `service`);
- verifica discovery/routes/proxy end-to-end;
- limitazioni attuali e troubleshooting.

## 7) Task tracking

`TASKS.md` e' la fonte unica dello stato progetto:

- nessun task operativo fuori da `TASKS.md`;
- aggiornamento obbligatorio ad ogni avanzamento rilevante;
- se cambia priorita', aggiornare la sezione "Next Priority".
