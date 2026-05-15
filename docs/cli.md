# tubo CLI

`tubo` e' l'eseguibile principale per avviare i ruoli runtime senza ricordare molte variabili d'ambiente.

## Precedenza configurazione

La configurazione effettiva segue:

```text
flag CLI > env var > config file > default > prompt interattivo
```

Il prompt interattivo e' riservato ai casi TTY, senza `--non-interactive`, e con `CI` diverso da `true`. In modalita' non interattiva i campi required mancanti producono un errore operativo esplicito.

## Comandi principali

La UX primaria di `tubo` e' intent-based:

```text
attach    = pubblica un endpoint HTTP locale nello swarm
connect   = apre un listener HTTP locale verso un servizio remoto
gateway   = avvia un HTTP gateway verso lo swarm
relay     = avvia un relay/bootstrap node
join      = configura questa macchina per uno swarm esistente

get       = lista o recupera risorse
create    = crea risorse locali nella config
share     = crea inviti membership locali per un cluster
grants    = gestisce richieste Publish Grant
describe  = mostra dettagli leggibili
inspect   = mostra dettagli tecnici/raw
watch     = osserva servizi nello swarm

ps        = mostra processi detached locali
logs      = segue o taila i log locali
stop      = ferma un processo detached locale
rm --stale = pulisce state/log di processi terminati
```

I comandi piu' comuni sono:

```bash
tubo relay
tubo join overlay/public
tubo join overlay/manual --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... --swarm-key ./swarm.key
tubo gateway
tubo attach http://127.0.0.1:1234 --name lmstudio
tubo connect lmstudio --local 127.0.0.1:51234
tubo get services
tubo describe service/lmstudio
tubo inspect service/lmstudio --json
tubo watch services
```

`attach` supporta sia la forma esplicita sia lo shorthand name+port; accetta anche `service/<name>` come primo argomento:

```bash
tubo attach --target http://127.0.0.1:1234 --name lmstudio
tubo attach lmstudio --port 1234
```

## Happy path

### Host relay

```bash
tubo relay -d
```

### Host service

```bash
tubo join overlay/manual \
  --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... \
  --swarm-key ./swarm.key

tubo attach http://127.0.0.1:1234 --name lmstudio -d
```

### Host client

```bash
tubo join \
  --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... \
  --swarm-key ./swarm.key

tubo get services
tubo describe service/lmstudio
tubo connect lmstudio --local 127.0.0.1:51234 -d
```

### Host gateway

```bash
tubo gateway --listen :8443 -d
```

I comandi long-running restano in foreground di default. Con `-d` / `--detach` possono essere lasciati in background:

```bash
tubo relay -d
tubo gateway -d
tubo attach http://127.0.0.1:1234 --name lmstudio -d
tubo connect lmstudio --local 127.0.0.1:51234 -d
```

## Join/init implicito e `--no-init`

Se manca la config locale di default:

- `attach`, `connect`, `gateway`, `relay` e i comandi discovery (`get`, `describe`, `inspect`, `watch`) fanno **implicit public join** verso la rete pubblica di default scaricando e verificando il bundle firmato;
- questo significa che, da zero, relay/service/client partono tutti nella stessa swarm key del bundle pubblico;
- in cluster/namespace mode, `attach` crea o riusa una identita' stabile per `(cluster, namespace, service)` (`service_id`, `service_seed`, `service_claim_file`) prima di avviare il runtime;
- senza config esplicita, `attach` genera ancora un seed libp2p unico per processo se non passi `--seed`, evitando PeerID demo condivisi tra macchine diverse;
- `attach` ascolta di default su `/ip4/0.0.0.0/tcp/0` per permettere direct dial/hole punching quando la rete lo consente.

File coinvolti:

```text
~/.config/tubo/config.yaml
~/.config/tubo/swarm.key
```

Per disabilitare esplicitamente il comportamento implicito:

```bash
--no-init
```

In `CI=true`, sia l'implicit public join sia l'init implicito sono disabilitati e il comando fallisce con next steps espliciti invece di creare state locale implicitamente.

## Utility

```bash
tubo keygen swarm --out swarm.key
tubo id from-seed service-lmstudio-seed
tubo config validate --config service.yaml
tubo config print --config service.yaml
tubo doctor --config service.yaml
```

## Init vs Join

`init` crea una nuova configurazione locale; `join` importa la configurazione di uno swarm esistente.

`join` configura localmente questa macchina per usare uno swarm esistente. Non avvia processi in background.

Modalita' default (bundle firmato rete pubblica Tubo):

```bash
tubo join
tubo join tubo-public
```

Modalita' manuale (swarm esistente privato):

```bash
tubo join \
  --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... \
  --swarm-key ./swarm.key
```

Per scripting:

```bash
tubo join \
  --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... \
  --swarm-key ./swarm.key \
  --json
```

Per bundle custom:

```bash
tubo join --bundle-url https://example.com/network.bundle
```

Per test/dev prima che il bundle di default sia pubblicato davvero su GitHub Pages, puoi anche forzare l'URL usato da `tubo join` e dall'implicit public join:

```bash
export TUBO_DEFAULT_PUBLIC_BUNDLE_URL=https://example.com/tubo-public.bundle
```

Di default salva:

```text
~/.config/tubo/config.yaml
~/.config/tubo/swarm.key
```

Puoi cambiare directory con `--config-dir`, forzare overwrite con `--force`, oppure fare un check TCP basilare del relay con `--check`.

## Connect

`connect` apre un listener HTTP locale verso un servizio scoperto nello swarm.

Se la config locale di default non esiste ancora, `connect` prova prima a fare implicit public join alla rete pubblica di default.
Anche `get services`, `describe`, `inspect` e `watch` usano lo stesso bootstrap implicito.

```bash
tubo connect lmstudio --local 127.0.0.1:51234
```

Se `--local` non e' specificato, sceglie automaticamente una porta libera su `127.0.0.1`.

Per scripting:

```bash
tubo connect lmstudio --json
```

`connect` usa la stessa risoluzione discovery di `get service/<name>`: cache locale quando disponibile, poi remote discovery query verso un bootstrap/relay peer, e solo infine observer effimero live. Il nome puo' essere passato sia come `lmstudio` sia come `service/lmstudio`, oppure puo' arrivare da `--token <service-share>` senza fare listing dei servizi. Le opzioni `--cluster` e `--namespace` vengono risolte dal config corrente quando presenti o dal token di servizio; `get services` supporta anche `-n/--namespace` e `-A/--all-namespaces` per preparare i futuri lookup scoped.

HTTP normale e WebSocket (`Upgrade: websocket`) sono inoltrati sullo stesso tunnel. Se un servizio pubblicizza solo indirizzi direct loopback/unspecified (`127.0.0.1`, `0.0.0.0`, `::1`), `connect` li ignora per il dial remoto e usa il path relayed. Il client `connect` abilita AutoRelay/hole punching quando la config contiene relay peer; il successo del direct upgrade dipende comunque da NAT/firewall e dagli indirizzi annunciati dal service. Anche quando il path iniziale e' `relayed`, libp2p puo' aprire in seguito una connessione direct tramite hole punching.

## Detached process state

Quando usi `-d`, `tubo` salva state locale in stile daemonless:

```text
~/.local/share/tubo/processes/
~/.local/share/tubo/logs/
~/.local/share/tubo/run/
```

con supporto XDG tramite `XDG_DATA_HOME` quando impostato.

Per persistenza dopo reboot o restart-on-failure tramite supervisor del sistema operativo, vedi anche `docs/PROCESS_SUPERVISORS.md`.

## Processi locali vs risorse nello swarm

Questa distinzione e' importante:

```bash
tubo ps
tubo get processes
```

mostrano processi locali detached di questa macchina.

```bash
tubo get services
tubo describe service/lmstudio
tubo inspect service/lmstudio --json
```

mostrano invece risorse discovery osservate nello swarm.

Esempio: `process/connect-lmstudio-51234` e' il processo locale che mantiene il tunnel; `service/lmstudio` e' la risorsa pubblicizzata dal publisher remoto.

## Process management locale

I processi detached locali possono essere ispezionati e gestiti con:

```bash
tubo ps
tubo get processes
tubo describe process/attach-lmstudio
tubo inspect process/attach-lmstudio --json
tubo logs process/attach-lmstudio
tubo stop process/attach-lmstudio
tubo rm --stale
```

`ps` / `get processes` riguardano i processi locali di questa macchina.
`get services` riguarda invece le risorse discovery pubblicizzate nello swarm. Quando la config locale contiene `current_cluster` / `current_namespace`, questi valori vengono riportati nella scope risolta del comando; puoi sovrascriverli con `--cluster`, `-n/--namespace` e, per le sole liste, `-A/--all-namespaces`. In cluster-mode, la query e la lista sono consentite solo se la capability di membership del namespace lo permette; `-A` richiede capability per ogni namespace o una capability broad con namespace `*`.

## Resource discovery

Con una config locale creata via `join`, puoi ispezionare i servizi pubblicizzati nello swarm:

```bash
tubo get services
tubo get service/lmstudio
tubo describe service/lmstudio
tubo inspect service/lmstudio --json
tubo watch services --timeout 20s
```

Comportamento:

- se trova un edge locale gia' in ascolto sull'admin API, usa la sua cache discovery locale;
- altrimenti prova una remote discovery query verso il primo bootstrap/relay peer disponibile;
- se anche la query remota fallisce o non basta, avvia un observer effimero, si collega allo swarm per un timeout esplicito e poi esce; il default e' pensato per coprire almeno un heartbeat discovery iniziale;
- i messaggi di output indicano esplicitamente se sta usando cache locale, query remota, observer live, o fallback tra questi.

Flag utili in questo MVP:

```bash
--config <path>
--timeout 20s
--live
--cached-only
--json
```

`config print` maschera i segreti (`private_key_b64`) e non stampa il contenuto di `swarm.key`.

## Esempi LM Studio / Ollama

LM Studio pubblicato nello swarm:

```bash
tubo attach http://127.0.0.1:1234 --name lmstudio -d
tubo get services
tubo describe service/lmstudio
```

Ollama pubblicato nello swarm:

```bash
tubo attach http://127.0.0.1:11434 --name ollama -d
tubo get services
tubo describe service/ollama
```

## Init

```bash
tubo init relay --out relay.yaml
tubo init edge --out edge.yaml
tubo init service --out service.yaml
tubo init topology --out topology.yaml
```

I file esistenti non sono sovrascritti senza `--force`.

## Esempio swarm.key

```bash
tubo keygen swarm --out swarm.key
chmod 600 swarm.key
```

Il file generato usa il formato libp2p pnet:

```text
/key/swarm/psk/1.0.0/
/base16/
<32 byte random in hex>
```

## Config service

```yaml
role: service
node:
  seed: service-lmstudio-seed
  p2p_listen: /ip4/0.0.0.0/tcp/40123
network:
  private_key_file: /etc/p2p/swarm.key
  bootstrap_peers:
    - /ip4/1.2.3.4/tcp/4001/p2p/12D3...
  relay_peers:
    - /ip4/1.2.3.4/tcp/4001/p2p/12D3...
  autorelay: true
  hole_punching: true
  force_reachability: private
service:
  name: lmstudio
  target: http://192.168.1.28:1234
health_listen: 127.0.0.1:8091
heartbeat_interval: 5s
```

## Config edge

```yaml
role: edge
node:
  seed: edge-seed
  p2p_listen: /ip4/0.0.0.0/tcp/4001
network:
  private_key_file: /etc/p2p/swarm.key
  bootstrap_peers: [/ip4/1.2.3.4/tcp/4001/p2p/12D3...]
  relay_peers: [/ip4/1.2.3.4/tcp/4001/p2p/12D3...]
edge:
  listen: :8443
  admin_listen: 127.0.0.1:8444
  direct_stream_timeout: 750ms
```

## Config relay

```yaml
role: relay
node:
  seed: public-relay-seed
  p2p_listen: /ip4/0.0.0.0/tcp/4001
network:
  private_key_file: /etc/p2p/swarm.key
relay:
  public_addr: /ip4/1.2.3.4/tcp/4001
  health_listen: 127.0.0.1:8092
  enable_relay_service: true
  enable_autonat_service: true
  enable_discovery_pubsub: true
  force_reachability_public: true
```

## Config bridge

```yaml
role: bridge
node:
  seed: bridge-demo-seed
  p2p_listen: /ip4/127.0.0.1/tcp/0
network:
  private_key_file: /etc/p2p/swarm.key
bridge:
  listen: 127.0.0.1:18081
  service_seed: service-lmstudio-seed
  service_p2p_listen: /ip4/127.0.0.1/tcp/40123
```

## Config resource model (Phase 1)

La configurazione supporta un modello risorse minimale per overlay, cluster e namespace, ma il runtime continua a leggere `network:` come source of truth operativo.

```yaml
current_overlay: public
current_cluster: home
current_namespace: default

overlays:
  public:
    relays: []
    bootstrap_peers: []
    swarm_key_file: ""

clusters:
  home:
    cluster_id: ""
    authority_public_key: ""
    capabilities: []
    namespaces:
      default: {}

network:
  private_key_file: /etc/p2p/swarm.key
  bootstrap_peers:
    - /ip4/1.2.3.4/tcp/4001/p2p/12D3...
  relay_peers:
    - /ip4/1.2.3.4/tcp/4001/p2p/12D3...
```

`current_overlay` materializza i campi overlay in `network:` quando il file usa il nuovo layout; le writers di `join` e bundle firmati scrivono entrambi i formati per compatibilità. `tubo join overlay/public` è la forma esplicita del join pubblico; `tubo join overlay/manual --relay ... --swarm-key ...` è la forma esplicita del join manuale/legacy. Quando la config corrente porta un cluster con identity metadata (`cluster_id` + `authority_public_key` + membership grant/capability), il runtime discovery usa un topic V2 opaco derivato da `current_cluster/current_namespace` e valida topic/scope, membership capability e replay nonce; le config senza questi metadati non supportano più discovery runtime.

## Local resource CLI (Phase 2a)

Dopo il nuovo model locale puoi ispezionare, creare, invitare e selezionare overlay, cluster e namespace già presenti nella config:

```bash
tubo get overlays
tubo get clusters
tubo get namespaces

tubo create cluster/home
tubo create namespace/observability
tubo create service/myapi

tubo share cluster/home --permission member
tubo share cluster/home --role grant-requester --grant-peer /ip4/1.2.3.4/tcp/4001/p2p/12D3...
tubo share service/myapi --expires 1h
tubo join cluster/home --token <cluster-invite>

tubo describe overlay/public
tubo describe cluster/home
tubo describe namespace/default

tubo use overlay/public
tubo use cluster/home
tubo use namespace/default
```

Note:

- `get overlays` e `get clusters` leggono solo la config locale.
- `get namespaces` usa il `current_cluster` corrente.
- `create cluster/...` genera un authority keypair locale, scrive un `cluster_id`, imposta `authority_public_key`, crea il namespace `default` e salva una capability di membership locale senza stampare segreti.
- `create namespace/...` richiede un `current_cluster` valido, aggiunge il namespace al cluster corrente, rende esplicito il nuovo `current_namespace` e materializza una capability di membership firmata per quel namespace.
- `create service/...` richiede un `current_cluster` e `current_namespace`, genera un `ServiceID` deterministico per `(cluster, namespace, name)`, firma una `ServiceClaim` locale e salva il claim su disco per `attach`/Discovery V2.
- `attach` in cluster/namespace mode materializza automaticamente una identita' servizio stabile se manca: il `service_id` resta deterministico per scope/nome, mentre il `service_seed` viene generato una sola volta e salvato nel config locale (`0600`).
- prima di avviare il runtime, `attach` risolve l'autorizzazione di pubblicazione: usa una `ServiceClaim` valida esistente, la firma localmente se il nodo possiede `authority_private_key_file`, oppure invia/polla una Publish Grant request se il servizio ha `grant_service_peer`; la pubblicazione procede solo dopo una `ServiceClaim` valida.
- `share cluster/...` usa la chiave authority locale per emettere un invito firmato, include namespace/expiry/grant data e stampa un comando `tubo join ...` copiabile; `--role grant-requester --grant-peer ...` emette un invito senza diritti publish diretti ma con metadata per richiedere una Publish Grant.
- `share service/...` usa la chiave authority locale per emettere un token connect-only, firma un `ConnectCapability` per il servizio, risolve il cluster/namespace corrente o esplicito (`--cluster`/`--namespace`) e stampa un comando `tubo connect --token ...` copiabile; in namespace-v2 il bridge converte poi il grant in un connect proof on-stream.
- `join cluster/... --token ...` e `join <cluster-invite>` verificano l'invito e salvano metadata del cluster + grant nel config locale senza toccare il runtime.
- `describe overlay/...`, `describe cluster/...` e `describe namespace/...` mostrano solo metadata locale e non stampano segreti.
- `use` aggiorna solo il file di config locale; non avvia o ferma processi runtime.
- `--json` resta disponibile per `get` e per i nuovi flussi locali quando utile.

## Publish Grants

Authority nodes can start the MVP grant protocol listener and review local requests:

```bash
tubo grants serve --cluster home --namespace default
tubo grants pending
tubo grants describe gr_123
tubo grants approve gr_123 --ttl 168h
tubo grants deny gr_123

tubo grants request service/myapi --peer /ip4/1.2.3.4/tcp/4001/p2p/12D3...
tubo grants request service/myapi --poll
# if joined with a grant-requester invite, --peer can be omitted
tubo grants history
```

The listener uses `/tubo/grants/1.0`, stores pending requests under the local Tubo data dir, derives requester PeerID from the libp2p stream, and never signs a `ServiceClaim` automatically. Approval is explicit and signs a service-scoped `ServiceClaim` with the local authority key. `attach` also uses the saved `grant_service_peer`/`grant_request_id` metadata to submit or poll before service publication; denied, expired, or still-pending grants stop publication.

## Topology

```yaml
swarm:
  key_file: ./swarm.key
nodes:
  relay:
    role: relay
    seed: public-relay-seed
    public_addr: /ip4/1.2.3.4/tcp/4001
  edge:
    role: edge
    seed: edge-seed
    listen: :8443
    admin_listen: 127.0.0.1:8444
    relay: relay
  lmstudio:
    role: service
    seed: service-lmstudio-seed
    service_name: lmstudio
    target: http://192.168.1.28:1234
    relay: relay
```

Se un nodo dichiara `relay: relay`, il render risolve il relay in `/p2p/<peer_id>` e popola automaticamente `network.bootstrap_peers` e `network.relay_peers` per edge/service.

Generazione:

```bash
tubo topology render --config topology.yaml --out generated
tubo topology commands --config topology.yaml
```
