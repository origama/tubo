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

- AutoNAT/hole punching non completi;
- diagnostica reachability avanzata non ancora completa.

## 3) Workflow obbligatorio per agent

### 3.1 Prima di iniziare

1. Leggere questo `AGENTS.md`.
2. Leggere la GitHub issue assegnata e le issue collegate.
3. Verificare lo stato reale del codice sul branch corrente.
4. Leggere la documentazione rilevante in `docs/`.
5. Se il lavoro tocca release, compatibilita' o protocollo, leggere `docs/VERSIONING.md`.
6. Se trovi lavoro non tracciato, apri o aggiorna una GitHub issue invece di usare tracker locali.

### 3.2 Durante il lavoro

1. Se cambia comportamento/config/interfaccia, aggiornare **subito** la documentazione in `docs/`.
2. Mantenere coerenza tra codice, env vars e runbook operativi.
3. Non lasciare TODO operativi non tracciati: devono avere una GitHub issue o essere risolti nel PR.

### 3.3 Prima di chiudere

1. Eseguire i gate di verifica correnti.
2. Aggiornare la GitHub issue con stato, evidenza, test eseguiti e follow-up.
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
3. Qualsiasi cambio implementativo deve riflettersi nella doc rilevante nello stesso PR/commit.
4. I documenti sotto `docs/obsoletes/` sono storico non canonico e non devono guidare implementazioni nuove.

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

GitHub Issues sono la fonte canonica per:

- stato del lavoro;
- scope implementativo;
- acceptance criteria;
- priorita';
- evidenza di verifica;
- follow-up.

Non usare file locali come tracker paralleli. Lo storico migrato dal vecchio tracker e' conservato in #180 solo come snapshot di migrazione.

## 8) GitHub issue / PR labels

Per triage e priorita', issue e PR devono usare label GitHub coerenti.

### 8.1 Tipo

- `bug`
- `performance`
- `security`
- `docs`
- `test`
- `infra`
- `release`
- `planning`
- `enhancement`

### 8.2 Area

- `area:edge`
- `area:service`
- `area:relay`
- `area:protocol`
- `area:testbench`
- `area:cli`
- `area:docs`
- `area:linode`

### 8.3 Priorita'

- `prio:high`
- `prio:medium`
- `prio:low`

### 8.4 Stato / rischio

- `needs-triage`
- `investigation`
- `blocked`
- `breaking-compat`
- `good-first-release-candidate`

### 8.5 Regola pratica

Ogni issue/PR dovrebbe avere almeno:

1. una label di **tipo**;
2. una label di **area** (una o piu' se serve);
3. una label di **priorita'** quando la priorita' e' nota.

Usare anche `investigation`, `blocked` o `breaking-compat` quando servono a chiarire il rischio o lo stato del lavoro.
