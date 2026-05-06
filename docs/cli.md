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
tubo join --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... --swarm-key ./swarm.key
tubo gateway
tubo attach http://127.0.0.1:1234 --name lmstudio
tubo connect lmstudio --local 127.0.0.1:51234
tubo get services
tubo describe service/lmstudio
tubo inspect service/lmstudio --json
tubo watch services
```

Equivalentemente, `attach` supporta anche la forma esplicita con flag:

```bash
tubo attach --target http://127.0.0.1:1234 --name lmstudio
```

## Happy path

### Host relay

```bash
tubo relay -d
```

### Host service

```bash
tubo join \
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

- `attach`, `connect` e `gateway` fanno **implicit public join** verso la rete pubblica di default scaricando e verificando il bundle firmato;
- `relay` continua invece a fare init locale implicito creando una config/swarm key locale.

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

## Advanced role commands

I role commands restano disponibili come compatibility / advanced layer:

```bash
tubo relay run --config relay.yaml
tubo edge run --config edge.yaml
tubo service run --config service.yaml
tubo bridge run --config bridge.yaml
```

Override via flag, per esempio:

```bash
tubo service run \
  --name lmstudio \
  --target http://192.168.1.28:1234 \
  --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3...
```

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

Di default salva:

```text
~/.config/tubo/config.yaml
~/.config/tubo/swarm.key
```

Puoi cambiare directory con `--config-dir`, forzare overwrite con `--force`, oppure fare un check TCP basilare del relay con `--check`.

## Connect

`connect` apre un listener HTTP locale verso un servizio scoperto nello swarm.

Se la config locale di default non esiste ancora, `connect` prova prima a fare implicit public join alla rete pubblica di default.

```bash
tubo connect lmstudio --local 127.0.0.1:51234
```

Se `--local` non e' specificato, sceglie automaticamente una porta libera su `127.0.0.1`.

Per scripting:

```bash
tubo connect lmstudio --json
```

`connect` usa la stessa risoluzione discovery di `get service/<name>`: cache locale quando disponibile, poi remote discovery query verso un bootstrap/relay peer, e solo infine observer effimero live.

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
`get services` riguarda invece le risorse discovery pubblicizzate nello swarm.

## Resource discovery

Con una config locale creata via `join`, puoi ispezionare i servizi pubblicizzati nello swarm:

```bash
tubo get services
tubo get service/lmstudio
tubo describe service/lmstudio
tubo inspect service/lmstudio --json
tubo watch services --timeout 10s
```

Comportamento:

- se trova un edge locale gia' in ascolto sull'admin API, usa la sua cache discovery locale;
- altrimenti prova una remote discovery query verso il primo bootstrap/relay peer disponibile;
- se anche la query remota fallisce o non basta, avvia un observer effimero, si collega allo swarm per un timeout esplicito e poi esce;
- i messaggi di output indicano esplicitamente se sta usando cache locale, query remota, observer live, o fallback tra questi.

Flag utili in questo MVP:

```bash
--config <path>
--timeout 5s
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

## Mapping vecchia UX -> nuova UX

| Attuale | Nuova UX |
|---|---|
| `tubo service run --name X --target URL` | `tubo attach URL --name X` |
| `tubo bridge run ...` | `tubo connect X --local ADDR` |
| `tubo edge run --listen :8443` | `tubo gateway --listen :8443` |
| `tubo relay run` | `tubo relay` |
| config manuale per swarm esistente | `tubo join --relay ... --swarm-key ...` |
| `tubo mesh services` | `tubo get services` |
| `tubo mesh inspect X` | `tubo describe X` / `tubo inspect X --json` |
| `tubo mesh watch` | `tubo watch services` |

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
