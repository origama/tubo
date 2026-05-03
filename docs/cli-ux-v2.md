# Tubo CLI UX v2

Questo documento raccoglie il design proposto per una nuova UX della CLI di `tubo`, piu' orientata alle intenzioni dell'utente e meno ai ruoli interni dell'implementazione.

L'obiettivo e' mantenere `tubo` come single binary, daemonless by default, ma rendere piu' semplice il flusso quotidiano per:

- pubblicare servizi locali nello swarm;
- consumare servizi remoti come endpoint locali;
- avviare gateway e relay;
- gestire processi long-running senza demone centrale;
- interrogare risorse pubblicate nello swarm con una grammatica coerente.

Questa proposta non rimuove subito i comandi attuali. Li riclassifica come layer avanzato/compatibile, sopra cui introdurre una UX piu' diretta.

---

## Stato implementazione corrente

Gia' implementato nella CLI corrente:

- `attach`, `connect`, `gateway`, `relay`;
- `join`;
- `-d` / `--detach` con state locale XDG-style;
- `ps`, `get processes`, `logs`, `stop`, `rm --stale`, `describe process/...`, `inspect process/...`;
- `get services`, `get service/<name>`, `describe service/<name>`, `inspect service/<name> --json`, `watch services`;
- init implicito locale per `attach`, `gateway` e `relay`.

Ancora fuori scope / futuro in questo documento:

- `get agents`, `get peers`;
- `watch events`;
- integrazione opzionale systemd/launchd.

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
- interrogare risorse pubblicizzate nello swarm con una grammatica stile `kubectl`: `get`, `describe`, `inspect`, `watch`;
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
attach    = pubblica un endpoint locale nello swarm
connect   = apre un tunnel locale verso un servizio dello swarm
gateway   = espone un gateway HTTP verso servizi dello swarm
relay     = avvia un relay/bootstrap node
join      = configura questo host per usare uno swarm esistente
init      = crea una nuova configurazione locale/swarm locale

get       = lista o recupera risorse locali/remoto-swarm
describe  = mostra dettagli leggibili di una risorsa
inspect   = mostra dettagli tecnici/raw di una risorsa
watch     = osserva cambiamenti live

ps        = alias pratico per processi locali detached
logs      = mostra log dei processi detached locali
stop      = ferma processi detached locali
rm        = rimuove state/log locali di processi terminati
```

La parola `mesh` resta utile come concetto architetturale, ma non deve necessariamente essere un namespace primario della CLI.

---

## Grammatica stile kubectl

Per le operazioni di lettura/ispezione, la UX si ispira a `kubectl`:

```bash
tubo get services
tubo get service/lmstudio
tubo describe service/lmstudio
tubo inspect service/lmstudio --json
tubo watch services
```

Questo consente di separare verbo e risorsa:

```text
get       = vista breve/tabellare
describe  = vista umana dettagliata
inspect   = vista tecnica/raw, adatta anche a scripting/debug
watch     = stream di cambiamenti/eventi
```

Risorse possibili:

```text
service / services / svc
agent / agents
peer / peers
process / processes / proc
tunnel / tunnels
gateway / gateways
route / routes
relay / relays
reservation / reservations
circuit / circuits
event / events
capability / capabilities
```

Esempi di ID tipizzati:

```text
service/lmstudio
agent/reviewer.gpubox
peer/12D3...
process/attach-lmstudio
tunnel/lmstudio-51234
gateway/default
relay/default
route/lmstudio
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
| `tubo mesh services` | `tubo get services` | `mesh` non e' necessario come namespace primario. |
| `tubo mesh inspect X` | `tubo describe X` / `tubo inspect X` | `describe` per output umano, `inspect` per output tecnico/raw. |
| `tubo mesh watch` | `tubo watch services` | Watch diventa un verbo top-level. |
| processi in foreground | default | Nessun flag necessario. |
| processi in background | `-d` / `--detach` | Implementato con state locale, senza demone centrale. |

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
id: process/attach-lmstudio
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
id: process/connect-lmstudio-51234
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
  "id": "process/attach-lmstudio",
  "kind": "process",
  "command": "attach",
  "name": "attach-lmstudio",
  "service": "lmstudio",
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

`ps` e' un alias pratico di:

```bash
tubo get processes
```

Output target:

```text
NAME                    COMMAND   STATUS    PID     LOCAL                  TARGET
attach-lmstudio          attach    running   18422   -                      http://127.0.0.1:1234
connect-lmstudio-51234   connect   running   18480   127.0.0.1:51234        lmstudio
gateway-default          gateway   running   18520   :8443                  swarm
relay-default            relay     running   18590   /ip4/0.0.0.0/tcp/4001  -
```

Flag utili:

```bash
tubo ps --all
tubo ps --json
tubo ps --kind attach
```

### `tubo get processes`

```bash
tubo get processes
```

Output equivalente a `tubo ps`, ma coerente con la grammatica resource-based.

### `tubo logs`

```bash
tubo logs attach-lmstudio
```

oppure, con ID tipizzato:

```bash
tubo logs process/attach-lmstudio
```

Follow:

```bash
tubo logs -f process/attach-lmstudio
```

Tail:

```bash
tubo logs --tail 100 process/gateway-default
```

### `tubo stop`

```bash
tubo stop process/attach-lmstudio
tubo stop process/connect-lmstudio-51234
tubo stop process/gateway-default
```

Alias breve ammesso se non ambiguo:

```bash
tubo stop attach-lmstudio
```

### `tubo describe process/...`

```bash
tubo describe process/attach-lmstudio
```

Dovrebbe mostrare una vista umana:

```yaml
Name: attach-lmstudio
Kind: process
Command: attach
Status: running
PID: 18422
Target: http://127.0.0.1:1234
Service: lmstudio
Log file: ~/.local/share/tubo/logs/attach-lmstudio.log
State file: ~/.local/share/tubo/processes/attach-lmstudio.json
```

### `tubo inspect process/...`

```bash
tubo inspect process/attach-lmstudio --json
```

Dovrebbe mostrare lo state tecnico/raw, adatto a debugging e scripting.

### `tubo rm`

Rimuove state/log locali di processi terminati.

```bash
tubo rm --stale
```

---

## Processi locali vs risorse nello swarm

Questa distinzione e' centrale.

```bash
tubo ps
# oppure

tubo get processes
```

mostra cosa gira localmente su questa macchina.

```bash
tubo get services
```

mostra servizi pubblicizzati nello swarm.

Esempio:

- `tubo ps` puo' mostrare `process/connect-lmstudio-51234`, cioe' il processo locale che mantiene aperto un tunnel;
- `tubo get services` puo' mostrare `service/lmstudio`, cioe' il servizio remoto pubblicato nello swarm.

Sono due piani diversi.

---

## Discovery delle risorse nello swarm

La UX primaria non usa piu' `tubo mesh ...` come namespace principale.

Usa invece:

```bash
tubo get services
tubo get agents
tubo get peers
tubo describe service/lmstudio
tubo inspect service/lmstudio --json
tubo watch services
```

### Come fa `get services` a vedere lo swarm?

`tubo get services` non puo' vedere magicamente lo swarm da fuori. Deve partecipare alla rete o usare una cache locale.

Comportamento raccomandato:

1. Se esiste un processo locale gia' connesso allo swarm, usare la sua cache/admin API quando disponibile.
2. Se non esiste una cache locale, avviare un observer effimero:
   - carica config locale da `join`/`init`;
   - carica swarm key;
   - crea un host libp2p temporaneo;
   - si connette a bootstrap/relay peer;
   - ascolta discovery per un timeout esplicito;
   - stampa le risorse osservate;
   - esce.

L'output deve sempre dire chiaramente quale modalita' sta usando.

Esempio con cache locale:

```text
using local cache from process/gateway-default
observing swarm for 5s to discover fresh announcements...
```

Esempio senza cache locale:

```text
no local cache found
starting temporary observer for 10s...
```

Flag utili:

```bash
tubo get services --cached-only
tubo get services --live
tubo get services --timeout 15s
tubo get services --json
```

### `tubo get services`

```bash
tubo get services
```

Output target:

```text
NAME        STATUS    PATH       PEER       CAPABILITIES
lmstudio    online    relayed    12D3...    model.openai-compatible
ollama      online    direct     12D3...    model.ollama
```

Alias possibile:

```bash
tubo get svc
```

### `tubo get service/lmstudio`

```bash
tubo get service/lmstudio
```

Output breve:

```text
NAME        STATUS    PATH       PEER       TTL
lmstudio    online    relayed    12D3...    24s
```

### `tubo describe service/lmstudio`

```bash
tubo describe service/lmstudio
```

Output umano dettagliato:

```yaml
Name: lmstudio
Kind: service
Status: online
Peer ID: 12D3...
Path: relayed
TTL: 30s
Expires in: 24s
Capabilities:
  - model.openai-compatible
Addresses:
  - /ip4/.../p2p-circuit/p2p/12D3...
Observed from:
  - local cache: process/gateway-default
  - live discovery: 5s
```

### `tubo inspect service/lmstudio`

```bash
tubo inspect service/lmstudio --json
```

Dovrebbe produrre una vista tecnica/raw, adatta a debug e automazione.

### `tubo get agents`

Futuro, quando saranno introdotti agent announcement.

```bash
tubo get agents
```

Output target:

```text
NAME               STATUS    PEER       CAPABILITIES
reviewer.gpubox    online    12D3...    agent.code_review, tool.go_test
builder.linode     busy      12D3...    tool.docker_build, tool.go_test
```

### `tubo watch services`

```bash
tubo watch services
```

Output target:

```text
watching services...
using local cache: process/gateway-default
also observing swarm live

ADDED     service/lmstudio       peer=12D3... path=relayed
ADDED     service/ollama         peer=12D3... path=direct
REMOVED   service/old-service
```

### `tubo watch events`

Futuro, se verra' introdotto uno stream eventi generalizzato oltre ai servizi.

```bash
tubo watch events
```

Output target:

```text
ADDED     service/lmstudio
ADDED     agent/reviewer.gpubox
UPDATED   service/ollama
REMOVED   peer/12D3...
```

### Nota implementativa

L'edge admin ora espone `/services` con `count` e `items[]`, quindi `get services` puo' usare una cache locale reale quando un gateway locale e' gia' attivo. In assenza di cache, la CLI avvia un observer effimero con timeout esplicito.

---

## Risoluzione ambiguita' in `describe` e `inspect`

`inspect` e `describe` possono riferirsi sia a processi locali sia a risorse nello swarm.

Per evitare ambiguita', si raccomanda l'uso di ID tipizzati:

```bash
tubo describe service/lmstudio
tubo describe process/attach-lmstudio
tubo inspect agent/reviewer.gpubox
tubo inspect peer/12D3...
```

Se l'utente usa un nome non tipizzato:

```bash
tubo inspect lmstudio
```

`tubo` puo' auto-risolvere solo se il match e' univoco.

Se e' ambiguo:

```text
ambiguous resource "lmstudio"

matches:
  service/lmstudio
  process/attach-lmstudio

try:
  tubo inspect service/lmstudio
  tubo inspect process/attach-lmstudio
```

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
  tubo get services
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
id: process/relay-default
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
tubo get services
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
tubo get services
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
tubo get agents
tubo get agents --capability agent.code_review
tubo describe agent/reviewer.gpubox
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
tubo logs -f process/attach-lmstudio
tubo stop process/attach-lmstudio
```

Rende usabili i processi detached senza demone centrale.

### 8. Discovery delle risorse nello swarm

```bash
tubo get services
tubo describe service/lmstudio
tubo watch services
```

Permette di capire cosa e' disponibile nello swarm, senza richiedere necessariamente un gateway long-running.

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
tubo generate systemd process/attach-lmstudio
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

tubo get <resource>
tubo describe <resource>
tubo inspect <resource>
tubo watch <resource>

tubo ps
tubo logs
tubo stop
tubo rm

tubo call
tubo doctor
tubo version

tubo config ...
tubo keygen ...
tubo id ...
tubo topology ...
```

La UX primaria non richiede piu' un namespace `mesh`. Se in futuro serve, `tubo mesh ...` puo' restare come namespace advanced o interno, ma il quickstart dovrebbe usare `get`, `describe`, `inspect`, `watch`.

---

## MVP suggerito

Ordine consigliato / stato attuale:

1. introdurre alias intent-based semplici — fatto;
2. implementare `join` come config import — fatto;
3. implementare `-d/--detach` — fatto;
4. implementare `ps/logs/stop/inspect/rm` — fatto;
5. implementare `connect <service>` by discovery — fatto;
6. implementare `get services`, `describe service/...`, `watch services` — fatto;
7. aggiungere init implicito — fatto;
8. aggiornare docs/README — documentato con #47;
9. investigare systemd/launchd — rimane #48.

---

## Issue tracking

Il lavoro e' tracciato nella epic:

```text
#39 — Epic: CLI UX v2 — attach/connect/gateway, daemonless detach e mesh commands
```

Sub-issue operative esistenti:

```text
#40 — CLI UX v2: implementare comandi intent-based attach, gateway e relay breve
#41 — CLI UX v2: implementare tubo connect per tunnel locale by service name
#42 — CLI UX v2: implementare daemonless -d/--detach mode
#43 — CLI UX v2: aggiungere process management locale ps/logs/stop/inspect/rm
#44 — CLI UX v2: implementare tubo join per configurare uno swarm esistente
#45 — CLI UX v2: aggiungere init implicito locale quando manca la config
#46 — CLI UX v2: aggiungere resource discovery commands
#47 — CLI UX v2: documentare nuova UX e compatibilita' con role commands
#48 — CLI UX v2: investigare integrazione systemd/launchd per processi persistenti
```

Le sub-issue #40, #41, #42, #43, #44, #45 e #46 sono state poi implementate in questa forma resource-oriented.

---

## Decisioni chiave

1. `service -> attach` ha senso semanticamente.
2. `edge -> connect` non e' corretto come rename diretto.
3. L'attuale edge dovrebbe diventare `gateway` nella UX user-facing.
4. `connect` dovrebbe essere un local tunnel verso un service name.
5. `join` configura uno swarm esistente, non avvia processi.
6. `init` crea nuovo contesto locale; puo' essere implicito per ridurre attrito.
7. `ps` e `get services` devono restare concetti separati: processi locali vs risorse nello swarm.
8. `get services` deve spiegare se usa cache locale, observer effimero o entrambi.
9. `-d` deve essere daemonless, non dipendere da un `tubod`.
10. systemd/launchd sono integrazioni opzionali per persistenza, non requisiti della CLI base.
11. I comandi role-based attuali restano come advanced/compatibility layer.
12. Il namespace `mesh` non e' necessario nella UX primaria; si preferisce `get/describe/inspect/watch`.

---

## Open questions

- Quando introdurre `kind=agent` e `capabilities` negli announcement?
- Come collegare `agent_name`, `service_name`, `peer_id` e policy?
- Quanto mantenere in vita i vecchi role commands nella documentazione principale?
- Come presentare in futuro `get peers` / `get agents` senza sovraccaricare il quickstart?
- Quale integrazione opzionale offrire per systemd/launchd senza indebolire il modello daemonless?
