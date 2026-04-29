# tubo CLI

`tubo` e' l'eseguibile principale per avviare i ruoli runtime senza ricordare molte variabili d'ambiente.

## Precedenza configurazione

La configurazione effettiva segue:

```text
flag CLI > env var > config file > default > prompt interattivo
```

Il prompt interattivo e' riservato ai casi TTY, senza `--non-interactive`, e con `CI` diverso da `true`. In modalita' non interattiva i campi required mancanti producono un errore operativo esplicito.

## Comandi principali

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

`topology.yaml` descrive una rete logica i cui nodi normalmente girano su macchine separate. Non assume Docker Compose: `tubo topology render` genera un file YAML per ogni nodo e un `RUNBOOK.md` con indirizzi peer e comandi da eseguire/copiare sulle singole macchine.

```yaml
swarm:
  key_file: /etc/tubo/swarm.key
nodes:
  relay:
    role: relay
    seed: public-relay-seed
    p2p_listen: /ip4/0.0.0.0/tcp/4001
    public_addr: /ip4/1.2.3.4/tcp/4001
  edge:
    role: edge
    seed: edge-seed
    p2p_listen: /ip4/0.0.0.0/tcp/4001
    listen: :8443
    admin_listen: 127.0.0.1:8444
    relay: relay
  lmstudio:
    role: service
    seed: service-lmstudio-seed
    p2p_listen: /ip4/0.0.0.0/tcp/40123
    service_name: lmstudio
    target: http://192.168.1.28:1234
    relay: relay
```

Generazione:

```bash
tubo topology render --config topology.yaml --out generated
tubo topology commands --config topology.yaml --out generated
```

Output tipico:

```text
generated/relay.yaml
generated/edge.yaml
generated/lmstudio.yaml
generated/RUNBOOK.md
```

I riferimenti come `relay: relay` vengono risolti in multiaddr `/p2p/...` usando `public_addr` e il PeerID deterministico derivato dal seed del nodo relay.
