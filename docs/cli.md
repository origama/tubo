# tubo CLI

`tubo` e' l'eseguibile principale per avviare i ruoli runtime senza ricordare molte variabili d'ambiente.

## Precedenza configurazione

La configurazione effettiva segue:

```text
flag CLI > env var > config file > default > prompt interattivo
```

Il prompt interattivo e' riservato ai casi TTY, senza `--non-interactive`, e con `CI` diverso da `true`. In modalita' non interattiva i campi required mancanti producono un errore operativo esplicito.

## Comandi principali

La UX base espone comandi orientati all'intento:

```bash
tubo relay --config relay.yaml
tubo join --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... --swarm-key ./swarm.key
tubo gateway --config edge.yaml
tubo attach http://127.0.0.1:1234 --name lmstudio
tubo connect lmstudio --local 127.0.0.1:51234
```

Equivalentemente, `attach` supporta anche la forma esplicita con flag:

```bash
tubo attach --target http://127.0.0.1:1234 --name lmstudio
```

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

## Join

`join` configura localmente questa macchina per usare uno swarm esistente. Non avvia processi in background.

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

Di default salva:

```text
~/.config/tubo/config.yaml
~/.config/tubo/swarm.key
```

Puoi cambiare directory con `--config-dir`, forzare overwrite con `--force`, oppure fare un check TCP basilare del relay con `--check`.

`init` crea una nuova configurazione locale; `join` importa la configurazione di uno swarm esistente.

## Connect

`connect` apre un listener HTTP locale verso un servizio scoperto nello swarm.

```bash
tubo connect lmstudio --local 127.0.0.1:51234
```

Se `--local` non e' specificato, sceglie automaticamente una porta libera su `127.0.0.1`.

Per scripting:

```bash
tubo connect lmstudio --json
```

`connect` usa la stessa risoluzione discovery di `get service/<name>`: cache locale quando disponibile, altrimenti observer effimero live.

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
- altrimenti avvia un observer effimero, si collega allo swarm per un timeout esplicito e poi esce.

Flag utili in questo MVP:

```bash
--config <path>
--timeout 5s
--live
--cached-only
--json
```

`config print` maschera i segreti (`private_key_b64`) e non stampa il contenuto di `swarm.key`.

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
