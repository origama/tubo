# Tubo CLI UX v2

Questo documento raccoglie il design discusso per una nuova UX della CLI di `tubo`, piu' orientata alle intenzioni dell'utente e meno ai ruoli interni dell'implementazione.

L'obiettivo e' mantenere `tubo` come single binary, daemonless by default, ma rendere piu' semplice il flusso quotidiano per pubblicare servizi, consumare servizi remoti, gestire processi locali e ispezionare la mesh.

Questa proposta non rimuove subito i comandi attuali. Li riclassifica come layer avanzato/compatibile, sopra cui introdurre una UX piu' diretta.

---

## Obiettivi

La nuova UX dovrebbe permettere di:

- pubblicare un endpoint locale nello swarm con un comando intuitivo;
- aprire localmente un tunnel verso un servizio remoto;
- avviare un gateway HTTP generico verso servizi nello swarm;
- avviare un relay/bootstrap node;
- configurare facilmente un host per usare uno swarm esistente;
- lasciare processi long-running in foreground by default;
- staccare processi in background con `-d` / `--detach` in stile Podman;
- ispezionare processi locali detached con `ps`, `logs`, `stop`, `inspect`;
- ispezionare risorse pubblicizzate nello swarm con `mesh`;
- ridurre il bisogno di scrivere, renderizzare e distribuire manualmente file topology/config per il caso comune.

---

## Principio base

`Daemonless` non significa che non esistono processi long-running.

Significa che non esiste un demone centrale obbligatorio, tipo `dockerd`, che deve essere sempre attivo per usare la CLI.

Invece:

- `tubo attach` avvia un processo che deve restare vivo per pubblicare un servizio;
- `tubo connect` avvia un processo che deve restare vivo per mantenere un listener locale;
- `tubo gateway` avvia un processo gateway HTTP;
- `tubo relay` avvia un processo relay.

Per default questi processi restano in foreground. Con `-d` vengono avviati in background e gestiti tramite state locale.

Questo e' piu' vicino al modello Podman che al modello Docker.

---

## Nuovo modello mentale

```text
attach   = pubblica un endpoint locale nello swarm
connect  = apre un tunnel locale verso un servizio dello swarm
gateway  = espone un gateway HTTP verso servizi dello swarm
relay    = avvia un relay/bootstrap node
join     = configura questo host per usare uno swarm esistente
init     = crea una nuova configurazione locale/swarm locale
ps       = mostra processi tubo detached locali
logs     = mostra log dei processi detached locali
stop     = ferma processi detached locali
inspect  = ispeziona un processo locale o una risorsa mesh
mesh     = mostra risorse pubblicizzate nello swarm
```

---

## Mapping dalla UX attuale alla UX proposta

| UX attuale | Nuova UX proposta | Note |
|---|---|---|
| `tubo service run --name X --target URL` | `tubo attach URL --name X` | Pubblica un endpoint locale nello swarm. |
| `tubo bridge run ...` | `tubo connect X --local ADDR` | Apre un tunnel locale verso un servizio remoto. Richiede evoluzione del bridge/discovery. |
| `tubo edge run --listen :8443` | `tubo gateway --listen :8443` | L'attuale edge e' un gateway HTTP, non una connessione puntuale. |
| `tubo relay run` | `tubo relay` | Forma breve, piu' diretta. |
| topology/config manuale per entrare in uno swarm | `tubo join --relay ... --swarm-key ...` | Importa configurazione di uno swarm esistente. |
| processi in foreground | default | Nessun flag necessario. |
| processi in background | `-d` / `--detach` | Da implementare con state locale, senza demone centrale. |

I comandi esistenti possono restare disponibili come advanced/compatibility commands:

```bash
tubo service run
tubo edge run
tubo bridge run
tubo relay run
tubo config print
tubo config validate
tubo topology render
tubo topology commands
```

---

## Perche' `service` diventa `attach`

Il ruolo attuale `service` prende un `ServiceName` e un `Target`, crea un host libp2p, registra gli stream handler, pubblica announcement nello swarm, mantiene heartbeat e inoltra richieste verso il target HTTP.

Semanticamente, quindi, l'utente sta attaccando un servizio locale alla rete `tubo`.

Comando attuale:

```bash
tubo service run --name lmstudio --target http://127.0.0.1:1234
```

Nuova UX:

```bash
tubo attach http://127.0.0.1:1234 --name lmstudio
```

oppure:

```bash
tubo attach --target http://127.0.0.1:1234 --name lmstudio
```

---

## Perche' `edge` non dovrebbe diventare `connect`

L'attuale `edge` non si limita a connettersi a un singolo servizio.

Fa da gateway HTTP:

- avvia un server HTTP;
- avvia una admin API;
- entra nello swarm;
- sottoscrive discovery;
- mantiene route table;
- riceve richieste HTTP;
- risolve `host/path -> service/peer`;
- apre stream diretti o via relay;
- gestisce retry, stale route e relay recovery;
- proxy-a request/response.

Quindi `connect` sarebbe fuorviante come alias diretto di `edge`.

La proposta e':

```bash
tubo gateway --listen :8443
```

per l'attuale edge.

Il comando `connect`, invece, dovrebbe essere una UX nuova o evoluta dal bridge:

```bash
tubo connect lmstudio --local 127.0.0.1:51234
```

cioe': rendi disponibile localmente il servizio remoto `lmstudio`.

---

## Foreground by default

I comandi long-running devono restare in foreground se l'utente non chiede esplicitamente il detach.

Esempio:

```bash
tubo attach http://127.0.0.1:1234 --name lmstudio
```

Output possibile:

```text
attaching service "lmstudio"
target: http://127.0.0.1:1234
peer: 12D3...
status: published
```

Il processo resta attivo e stampa log in console fino a `Ctrl+C`.

Questo e' il comportamento piu' semplice per sviluppo, debug e demo.

---

## Detach mode

Con `-d` o `--detach`, il processo viene lasciato in background.

Esempio:

```bash
tubo attach http://127.0.0.1:1234 --name lmstudio -d
```

Output possibile:

```text
attached service "lmstudio"
id: attach/lmstudio
pid: 18422
logs: ~/.local/share/tubo/logs/attach-lmstudio.log
```

Altro esempio:

```bash
tubo connect lmstudio --local 127.0.0.1:51234 -d
```

Output possibile:

```text
connected service "lmstudio"
id: connect/lmstudio-51234
local: http://127.0.0.1:51234
pid: 18480
logs: ~/.local/share/tubo/logs/connect-lmstudio-51234.log
```

---

## State locale per processi detached

Senza demone centrale, `tubo` puo' gestire i processi detached tramite file locali.

Path proposti, seguendo XDG quando possibile:

```text
~/.local/share/tubo/processes/
~/.local/share/tubo/logs/
~/.local/share/tubo/run/
```

Esempio state file:

```json
{
  "id": "attach/lmstudio",
  "kind": "attach",
  "name": "lmstudio",
  "pid": 18422,
  "started_at": "2026-05-02T12:00:00Z",
  "target": "http://127.0.0.1:1234",
  "log_file": "~/.local/share/tubo/logs/attach-lmstudio.log",
  "status_addr": "127.0.0.1:8091"
}
```

`tubo ps` legge questi file, verifica se il PID e' vivo e mostra lo stato.

---

## Process management locale

### `tubo ps`

Mostra i processi `tubo` detached avviati su questa macchina.

```bash
tubo ps
```

Output target:

```text
ID                         KIND      NAME        STATUS    PID     LOCAL                  TARGET
attach/lmstudio            attach    lmstudio    running   18422   -                      http://127.0.0.1:1234
connect/lmstudio-51234     connect   lmstudio    running   18480   127.0.0.1:51234        lmstudio
gateway/default            gateway   -           running   18520   :8443                  swarm
relay/default              relay     -           running   18590   /ip4/0.0.0.0/tcp/4001  -
```

Flag utili:

```bash
tubo ps --all
tubo ps --json
tubo ps --kind attach
```

### `tubo logs`

```bash
tubo logs attach/lmstudio
```

Follow:

```bash
tubo logs -f attach/lmstudio
```

Tail:

```bash
tubo logs --tail 100 gateway/default
```

### `tubo stop`

```bash
tubo stop attach/lmstudio
tubo stop connect/lmstudio-51234
tubo stop gateway/default
```

Tutti:

```bash
tubo stop --all
```

### `tubo inspect`

```bash
tubo inspect attach/lmstudio
```

Dovrebbe mostrare:

- ID processo;
- PID;
- stato;
- config effettiva mascherata;
- peer ID;
- target;
- listener locali;
- path log;
- eventuale health/status runtime.

### `tubo rm`

Rimuove state/log locali di processi terminati.

```bash
tubo rm --stale
tubo rm connect/lmstudio-51234
```

---

## `ps` vs `mesh`

Questa distinzione e' centrale.

```bash
tubo ps
```

mostra cosa gira localmente su questa macchina.

```bash
tubo mesh list
```

mostra cosa e' pubblicizzato nello swarm.

Esempio:

- `tubo ps` puo' mostrare `connect/lmstudio-51234`, cioe' il processo locale che mantiene aperto un tunnel;
- `tubo mesh list` puo' mostrare `service lmstudio`, cioe' il servizio remoto pubblicato nello swarm.

Sono due piani diversi.

---

## Mesh discovery

La nuova UX dovrebbe includere comandi per vedere le risorse nello swarm.

### `tubo mesh list`

```bash
tubo mesh list
```

Output target:

```text
KIND      NAME                STATUS    PATH       PEER
service   lmstudio            online    relayed    12D3...
service   ollama              online    direct     12D3...
agent     reviewer.gpubox     online    relayed    12D3...
agent     builder.linode      busy      direct     12D3...
```

### `tubo mesh services`

```bash
tubo mesh services
```

Output target:

```text
NAME        STATUS    PATH       CAPABILITIES
lmstudio    online    relayed    model.openai-compatible
ollama      online    direct     model.ollama
```

### `tubo mesh agents`

Futuro, quando saranno introdotti agent announcement.

```bash
tubo mesh agents
```

Output target:

```text
NAME               STATUS    CAPABILITIES
reviewer.gpubox    online    agent.code_review, tool.go_test
builder.linode     busy      tool.docker_build, tool.go_test
```

### `tubo mesh find`

```bash
tubo mesh find --capability model.openai-compatible
tubo mesh find --capability agent.code_review
tubo mesh find --kind service
tubo mesh find --kind agent
```

### `tubo mesh inspect`

```bash
tubo mesh inspect lmstudio
```

Output target:

```yaml
kind: service
name: lmstudio
peer_id: 12D3...
status: online
addresses:
  - /ip4/.../p2p-circuit/p2p/12D3...
capabilities:
  - model.openai-compatible
ttl: 30s
expires_in: 24s
```

### Nota implementativa

Oggi l'edge espone un endpoint `/services`, ma restituisce solo un conteggio. Per `mesh list` serve esporre o ottenere una lista completa di discovery entries, con almeno:

- service name;
- peer ID;
- addresses;
- TTL;
- registered/age/expires;
- eventuale kind/capabilities future.

---

## `init` vs `join`

Questa distinzione va chiarita bene.

```text
init = crea una nuova configurazione locale / nuovo swarm locale
join = importa/configura uno swarm esistente su questa macchina
```

### `tubo init`

Serve quando parti da zero.

```bash
tubo init
```

Crea una configurazione locale e, se serve, una swarm key:

```text
~/.config/tubo/config.yaml
~/.config/tubo/swarm.key
```

Output possibile:

```text
initialized tubo config
config: ~/.config/tubo/config.yaml
swarm key: ~/.config/tubo/swarm.key

next:
  tubo attach http://127.0.0.1:1234 --name lmstudio
  tubo relay -d
```

### `tubo join`

Serve quando hai gia' uno swarm da usare.

```bash
tubo join \
  --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... \
  --swarm-key ./swarm.key
```

Questo non avvia un processo. Salva localmente le informazioni necessarie:

- swarm key;
- relay peers;
- bootstrap peers;
- eventuali default di rete.

Output possibile:

```text
joined swarm config
relay: /ip4/1.2.3.4/tcp/4001/p2p/12D3...
swarm key installed: ~/.config/tubo/swarm.key

next:
  tubo mesh list
  tubo attach http://127.0.0.1:1234 --name my-service
  tubo connect lmstudio
```

### Init implicito

Per ridurre attrito, alcuni comandi possono fare init implicito se manca config locale.

Esempio:

```bash
tubo attach http://127.0.0.1:1234 --name lmstudio
```

Se non esiste config:

```text
no tubo config found
created local config: ~/.config/tubo/config.yaml
created private swarm key: ~/.config/tubo/swarm.key

attaching service "lmstudio"
```

Flag utile:

```bash
--no-init
```

per fallire invece di creare config automaticamente, utile in CI o ambienti controllati.

---

## Happy path: creare uno swarm e pubblicare LM Studio

### Host relay

```bash
tubo relay -d
```

Se non esiste config, il comando puo' fare init implicito e stampare un comando `join` da condividere.

Output target:

```text
relay running
id: relay/default
peer: 12D3...
addr: /ip4/1.2.3.4/tcp/4001/p2p/12D3...

share this with other nodes:
  tubo join --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... --swarm-key ./swarm.key
```

### Host con LM Studio

```bash
tubo join --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... --swarm-key ./swarm.key
tubo attach http://127.0.0.1:1234 --name lmstudio -d
```

### Host client

```bash
tubo join --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... --swarm-key ./swarm.key
tubo mesh services
tubo connect lmstudio --local 127.0.0.1:51234
```

Poi il client usa:

```text
http://127.0.0.1:51234
```

come se LM Studio fosse locale.

---

## Happy path: Ollama remoto

### Host remoto con Ollama

```bash
tubo attach http://127.0.0.1:11434 --name ollama -d
```

### Host client

```bash
tubo connect ollama --local 127.0.0.1:11434 -d
```

Poi:

```bash
curl http://127.0.0.1:11434/api/tags
```

---

## Happy path: gateway HTTP generico

Invece di aprire un tunnel locale per un singolo servizio, si puo' avviare un gateway HTTP.

```bash
tubo gateway --listen :8443 -d
```

Il gateway riceve richieste HTTP e instrada verso servizi scoperti nello swarm.

Esempio concettuale:

```bash
curl -H 'Host: lmstudio' http://gateway-host:8443/v1/models
```

oppure, se il routing supporta host/path specifici:

```bash
curl http://gateway-host:8443/lmstudio/v1/models
```

Il dettaglio esatto del routing dipende dalla route table e dalla futura UX routing.

---

## Happy path: agenti nella mesh

Questa e' una direzione futura, ma la CLI dovrebbe lasciare spazio al concetto.

Un agente potrebbe pubblicarsi come risorsa nello swarm:

```bash
tubo attach http://127.0.0.1:7777 \
  --name reviewer.gpubox \
  --kind agent \
  --capability agent.code_review \
  --capability tool.go_test
```

Poi altri agenti o utenti potrebbero trovarlo:

```bash
tubo mesh agents
tubo mesh find --capability agent.code_review
tubo connect reviewer.gpubox
```

La differenza concettuale:

```text
service = endpoint passivo richiamabile
agent   = entita' attiva che puo' ricevere task, collaborare, delegare
```

Questo apre scenari come:

- remote model providers;
- agent delegation;
- build/test workers;
- browser/tool workers;
- agenti vicini ai dati;
- private MCP-like service mesh.

Questa parte richiede discovery metadata, capabilities e policy. Non deve bloccare il primo MVP della CLI.

---

## Use case coperti bene dalla nuova UX

### 1. Pubblicare un servizio HTTP locale

```bash
tubo attach http://127.0.0.1:1234 --name lmstudio
```

Sostituisce il caso comune di `tubo service run`.

### 2. Consumare un servizio remoto come locale

```bash
tubo connect lmstudio --local 127.0.0.1:51234
```

Questo e' uno dei principali miglioramenti di UX rispetto al modello attuale.

### 3. Avviare un gateway HTTP

```bash
tubo gateway --listen :8443
```

Nome piu' chiaro per l'attuale ruolo `edge`.

### 4. Avviare un relay

```bash
tubo relay -d
```

Forma breve e piu' naturale dell'attuale `tubo relay run`.

### 5. Primo utilizzo locale

```bash
tubo attach http://127.0.0.1:1234 --name lmstudio
```

con init implicito se manca config.

### 6. Entrare in uno swarm esistente

```bash
tubo join --relay ... --swarm-key ...
```

Sostituisce molti passaggi manuali di config per il caso comune.

### 7. Gestione processi locali

```bash
tubo ps
tubo logs -f attach/lmstudio
tubo stop attach/lmstudio
```

Rende usabili i processi detached senza demone centrale.

### 8. Discovery mesh

```bash
tubo mesh services
tubo mesh inspect lmstudio
```

Permette di capire cosa e' disponibile nello swarm.

---

## Use case che restano advanced o fuori dal primo MVP

La nuova UX non deve eliminare tutti i comandi attuali. Alcuni use case restano meglio serviti dal layer advanced.

### 1. Topologie dichiarative multi-nodo

Oggi esistono comandi come:

```bash
tubo topology render --config topology.yaml --out generated
tubo topology commands --config topology.yaml
```

Questi restano utili per:

- demo ripetibili;
- CI;
- test distribuiti;
- deployment multi-host;
- ambienti lab;
- configurazioni deterministicamente rigenerate.

La nuova UX e' piu' imperativa e human-friendly. La topology YAML resta advanced.

### 2. Config file espliciti

Comandi come:

```bash
tubo config validate --config service.yaml
tubo config print --config service.yaml
tubo doctor --config service.yaml
```

restano utili per:

- produzione;
- troubleshooting;
- CI;
- review della config effettiva;
- ambienti non interattivi.

### 3. Peer ID deterministici

```bash
tubo id from-seed service-lmstudio-seed
```

Resta utile per:

- allowlist;
- topology;
- peer ID stabili;
- test ripetibili.

### 4. Key management esplicito

```bash
tubo keygen swarm --out swarm.key
```

Resta utile per:

- generazione offline;
- secret manager;
- distribuzione controllata;
- rotazione chiavi;
- produzione.

### 5. Tuning avanzato relay

La UX semplice:

```bash
tubo relay -d
```

non copre tutta la configurazione avanzata:

- max reservations;
- max reservations per IP/ASN;
- max circuits per peer;
- buffer size;
- reservation TTL;
- limit duration;
- data limit;
- AutoNAT;
- discovery pubsub router.

Questi restano via config/flag avanzati.

### 6. Tuning avanzato attach/service

La UX semplice:

```bash
tubo attach http://127.0.0.1:1234 --name lmstudio
```

non copre tutti i dettagli:

- p2p listen addr;
- seed;
- heartbeat interval;
- force reachability;
- autorelay;
- hole punching;
- health listen;
- private key base64/file.

Questi restano flag/config avanzati.

### 7. Gateway routing avanzato

La UX semplice:

```bash
tubo gateway --listen :8443
```

non copre necessariamente:

- route custom;
- admin listen;
- direct stream timeout;
- manual route add;
- policy/tenant routing futuro.

Restano via config/admin API/flag avanzati.

### 8. Bridge raw by peer/seed

L'attuale bridge puo' connettersi tramite peer addr o seed. La nuova UX `connect <service>` si basa invece su discovery by service name.

Casi raw/direct restano advanced, ad esempio in futuro:

```bash
tubo connect --peer /ip4/.../p2p/12D3... --local 127.0.0.1:51234
```

### 9. CI e automazione non interattiva

La nuova UX deve restare scriptabile, ma in CI spesso servono:

- `--config` esplicito;
- `--no-init`;
- output `--json`;
- errori se manca config;
- nessuna scrittura automatica in home.

### 10. Persistence gestita dal sistema

`-d` lascia processi in background, ma non fornisce:

- restart al reboot;
- restart on failure;
- logging di sistema;
- dependency management.

Per questo e' utile una futura integrazione opzionale con systemd/launchd.

Esempi possibili:

```bash
tubo attach http://127.0.0.1:1234 --name lmstudio --install --enable
tubo generate systemd attach/lmstudio
```

Questa e' una capability opzionale, non parte del nucleo daemonless.

---

## Comandi one-shot futuri

Oltre a `connect`, potrebbe essere utile avere comandi one-shot.

### `tubo call`

Chiama un servizio senza aprire un tunnel persistente:

```bash
tubo call lmstudio /v1/models
tubo call ollama /api/tags
```

Utile per script, agenti e debug.

### `tubo open`

Apre un servizio nel browser o crea un tunnel temporaneo:

```bash
tubo open dashboard
```

Se non esiste una connessione locale, puo' crearne una temporanea.

### `tubo mesh watch`

Mostra eventi live dello swarm:

```bash
tubo mesh watch
```

---

## Compatibilita' e migrazione

La nuova UX dovrebbe essere introdotta senza rompere gli utenti attuali.

Comandi attuali da mantenere almeno nella prima fase:

```bash
tubo service run
tubo edge run
tubo bridge run
tubo relay run
tubo config print
tubo config validate
tubo doctor
tubo topology render
tubo topology commands
tubo keygen swarm
tubo id from-seed
```

La documentazione dovrebbe presentarli come:

```text
Advanced role commands
```

oppure:

```text
Compatibility layer
```

La nuova UX diventa il percorso principale nel README e nel quickstart.

---

## Possibile gerarchia finale CLI

```text
tubo init
tubo join

tubo attach
tubo connect
tubo gateway
tubo relay

tubo ps
tubo logs
tubo stop
tubo inspect
tubo rm

tubo mesh list
tubo mesh services
tubo mesh agents
tubo mesh find
tubo mesh inspect
tubo mesh watch

tubo call
tubo doctor
tubo version

tubo config ...
tubo keygen ...
tubo id ...
tubo topology ...
```

---

## MVP suggerito

Ordine consigliato:

1. introdurre alias intent-based semplici:
   - `attach`;
   - `gateway`;
   - short `relay`;
2. implementare `join` come config import;
3. implementare `-d/--detach`;
4. implementare `ps/logs/stop/inspect/rm`;
5. implementare `connect <service>` by discovery;
6. implementare `mesh list/services/inspect`;
7. aggiungere init implicito;
8. aggiornare docs/README;
9. investigare systemd/launchd.

---

## Issue tracking

Il lavoro e' tracciato nella epic:

```text
#39 — Epic: CLI UX v2 — attach/connect/gateway, daemonless detach e mesh commands
```

Sub-issue operative:

```text
#40 — CLI UX v2: implementare comandi intent-based attach, gateway e relay breve
#41 — CLI UX v2: implementare tubo connect per tunnel locale by service name
#42 — CLI UX v2: implementare daemonless -d/--detach mode
#43 — CLI UX v2: aggiungere process management locale ps/logs/stop/inspect/rm
#44 — CLI UX v2: implementare tubo join per configurare uno swarm esistente
#45 — CLI UX v2: aggiungere init implicito locale quando manca la config
#46 — CLI UX v2: aggiungere mesh discovery commands
#47 — CLI UX v2: documentare nuova UX e compatibilita' con role commands
#48 — CLI UX v2: investigare integrazione systemd/launchd per processi persistenti
```

---

## Decisioni chiave

1. `service -> attach` ha senso semanticamente.
2. `edge -> connect` non e' corretto come rename diretto.
3. L'attuale edge dovrebbe diventare `gateway` nella UX user-facing.
4. `connect` dovrebbe essere un local tunnel verso un service name.
5. `join` configura uno swarm esistente, non avvia processi.
6. `init` crea nuovo contesto locale; puo' essere implicito per ridurre attrito.
7. `ps` e `mesh` devono restare concetti separati.
8. `-d` deve essere daemonless, non dipendere da un `tubod`.
9. systemd/launchd sono integrazioni opzionali per persistenza, non requisiti della CLI base.
10. I comandi role-based attuali restano come advanced/compatibility layer.

---

## Open questions

- `connect` deve essere implementato evolvendo `bridge` o creando un mini-edge locale?
- `mesh list` deve avviare un nodo discovery temporaneo o interrogare un processo locale esistente?
- Come gestire il timeout discovery nei comandi one-shot?
- Quale formato usare per lo state locale dei processi detached?
- Come gestire collisioni di ID, ad esempio due `attach` con lo stesso nome?
- Quali comandi devono supportare init implicito?
- In CI, l'init implicito deve essere disabilitato di default?
- Quando introdurre `kind=agent` e `capabilities` negli announcement?
- Come collegare `agent_name`, `service_name`, `peer_id` e policy?
- Quanto mantenere in vita i vecchi role commands nella documentazione principale?
