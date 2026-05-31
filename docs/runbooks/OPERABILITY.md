# Operability Runbook

Questo documento e' il riferimento operativo canonico per:

1. avviare i componenti;
2. creare un tunnel p2p funzionante;
3. creare un tunnel p2p **sicuro** (private swarm PSK) tra due o piu' servizi.

## 1) Prerequisiti

- Go 1.24+
- Docker + Docker Compose plugin
- rete raggiungibile tra nodi (almeno outbound dai nodi service verso il nodo edge/bootstrap)

## 2) Componenti runtime reali

Nuova UX consigliata: `tubo` con intent-based command espliciti:

```bash
tubo relay --config relay.yaml
tubo gateway --config edge.yaml
tubo attach --config service.yaml
```

Ruoli disponibili tramite `tubo`:

- `relay` (bootstrap + relay v2 + health endpoint); ora e' solo trasporto/bootstrap, non un router discovery
- `gateway` (ingress HTTP + discovery consumer)
- `attach` (publisher + stream handler verso servizio origin)
- in configurazioni cluster-aware, `gateway`/`attach`/observer selezionano un topic discovery V2 opaco derivato da `current_cluster` + `current_namespace`
- il vecchio discovery swarm `/discovery/v1.0` e' stato rimosso: usa solo la discovery basata su cluster/namespace + capability
- `bridge` rimane disponibile come logica client-side, ma il comando runtime storico `bridge run` non e' piu' supportato

## 3) Quick Start locale (Docker Compose)

Da root repository:

```bash
docker compose up -d --build
```

Verifiche minime:

```bash
curl -fsS http://127.0.0.1:8443/healthz
curl -fsS http://127.0.0.1:8444/healthz
curl -fsS http://127.0.0.1:8091/healthz
curl -fsS http://127.0.0.1:8092/healthz
curl -fsS http://127.0.0.1:8444/services
curl -fsS http://127.0.0.1:8444/routes
```

Test end-to-end consigliato:

```bash
./tests/smoke-compose.sh
```

## 4) Tunnel p2p sicuro (private swarm PSK)

### 4.1 Generazione chiave swarm

Genera `swarm.key` (formato libp2p pnet):

```bash
# nuovo metodo consigliato
tubo keygen swarm --out swarm.key
chmod 600 swarm.key

# equivalente manuale
KEY_HEX="$(openssl rand -hex 32)"
cat > swarm.key <<EOF_KEY
/key/swarm/psk/1.0.0/
/base16/
${KEY_HEX}
EOF_KEY
chmod 600 swarm.key
```

Distribuire `swarm.key` **solo** ai nodi fidati. Non committare nel repository.

Per esempi YAML completi (relay, edge, service, bridge) e `tests/e2e/compose/tubo/compose.yml`, vedi [`reference/cli.md`](../reference/cli.md). Nei cluster-aware setup, il flusso locale consigliato e': `tubo create cluster/...`, `tubo create namespace/...`, `tubo create service/...`, poi `tubo use ...`, `tubo share service/...` e `tubo attach ...` / `tubo connect --token ...`. Quando il namespace e' discovery-enabled (`connect_policy: namespace_members`), puoi anche invitare Bob con `tubo share cluster/home --namespace <ns> --role member`, fargli fare `tubo join cluster/home --token ...`, poi usare `tubo get services` e `tubo connect <service>` by name nello stesso scope; un invite `--role viewer` puo' listare ma non ottenere lease di connect. Per il bundle pubblico, `tubo join`/`tubo attach`/`tubo connect` da config pulita installano `home/default` e i metadata del cluster pubblico, cosi' il publish grant listener puo' auto-approvare il flusso semplificato senza richiedere un join cluster esplicito. Lo share token e' ora un `ShareInvite` bearer connect-only: non autorizza listing generico, risolve il `service_id` esatto, non sostituisce la membership capability, e lato server e' one-time al momento del primo successful lease/session redemption (non al primo HTTP request). `tubo share revoke <share-invite>` puo' bloccarne la redemption locale, mentre `tubo revoke invite|session|service-access|publish ...` aggiorna lo store revoche issuer-side usato da `grants serve`. Il modello attuale assume un solo issuer attivo per scope; ha piani HA/consensus per issuer sono rinviati a un design futuro. Per `get services -A` o namespace aggiuntivi, assicurati che ogni namespace abbia la sua `membership_capability_file` (oppure una capability broad con namespace `*`). I join via invite salvano anche un grant firmato che autorizza le query sul nodo remoto.

Nota operativa per `tubo-public`: puoi avere piu' relay pubblici senza problemi, ma oggi e' raccomandato avere **un solo Grant Service autorevole per ogni cluster/namespace pubblico** (per esempio `home/default`). I relay gestiscono solo reachability/trasporto; invece grant service multipli con store indipendenti possono approvare contemporaneamente lo stesso `service name` per peer diversi, creando split-brain e risultati discovery non deterministici. In breve: **multi-relay ok, single grant service per authority scope**.

La precedenza della configurazione e':

```text
flag CLI > env var > config file > default > interactive
```

### 4.2 Variabili supportate (implementate)

- `LIBP2P_PRIVATE_NETWORK_KEY=/path/to/swarm.key`
- `LIBP2P_PRIVATE_NETWORK_KEY_B64=<base64_32_bytes>`

Se valorizzate, host libp2p viene creato con private network PSK.

## 5) Test reale a 3 macchine (laptop NAT + edge NAT + relay pubblico)

### 5.1 Avvia relay (host pubblico stabile)

```bash
NODE_SEED=public-relay-seed \
P2P_LISTEN=/ip4/0.0.0.0/tcp/4001 \
RELAY_HEALTH_LISTEN=127.0.0.1:8092 \
ENABLE_RELAY_SERVICE=true \
ENABLE_AUTONAT_SERVICE=true \
ENABLE_DISCOVERY_PUBSUB=true \
FORCE_REACHABILITY_PUBLIC=true \
PRINT_RUN_COMMANDS=true \
LIBP2P_PRIVATE_NETWORK_KEY=/etc/p2p/swarm.key \
go run ./cmd/tubo relay
```

Il relay stampa nei log:

1. il proprio `peer_id`;
2. gli indirizzi libp2p disponibili;
3. un blocco `startup command hints` con `BOOTSTRAP_PEERS` e `RELAY_PEERS` gia' valorizzati per `edge` e `service`.

Se l'indirizzo pubblico non viene inferito correttamente, forzarlo:

```bash
RELAY_PUBLIC_ADDR=/ip4/<RELAY_PUBLIC_IP>/tcp/4001
```

Se `RELAY_PUBLIC_ADDR` non include `/p2p/<PEER_ID>`, il relay aggiunge automaticamente il proprio PeerID nei comandi suggeriti.

Porte firewall minime sul relay:

1. `tcp/4001` (obbligatoria, bootstrap + relay circuit v2)
2. `tcp/8092` (opzionale, health check)
3. `tcp/22` (SSH gestione)

### 5.2 Avvia edge

Nel caso NAT/NAT, l'edge deve poter usare il relay pubblico come unico peer statico. Non e' necessario esporre l'edge come bootstrap peer per i service.

```bash
EDGE_LISTEN=:8443 \
EDGE_ADMIN_LISTEN=127.0.0.1:8444 \
EDGE_P2P_LISTEN=/ip4/0.0.0.0/tcp/4001 \
EDGE_SEED=edge-seed \
BOOTSTRAP_PEERS=/ip4/<RELAY_PUBLIC_IP>/tcp/4001/p2p/<RELAY_PEER_ID> \
RELAY_PEERS=/ip4/<RELAY_PUBLIC_IP>/tcp/4001/p2p/<RELAY_PEER_ID> \
LIBP2P_PRIVATE_NETWORK_KEY=/etc/p2p/swarm.key \
go run ./cmd/tubo gateway
```

Recuperare `peer_id` edge dai log (`edge gateway peer_id=...`).

### 5.3 Avvia service sul laptop (LM Studio)

Nel caso NAT/NAT, il service deve usare il relay pubblico come `BOOTSTRAP_PEERS` e `RELAY_PEERS`. Non usare l'edge come bootstrap peer se l'edge e' dietro NAT.

```bash
SERVICE_NAME=lmstudio \
SERVICE_TARGET=http://192.168.1.28:1234 \
SERVICE_P2P_LISTEN=/ip4/0.0.0.0/tcp/40123 \
NODE_SEED=service-lmstudio-seed \
LIBP2P_PRIVATE_NETWORK_KEY=/etc/p2p/swarm.key \
BOOTSTRAP_PEERS=/ip4/<RELAY_PUBLIC_IP>/tcp/4001/p2p/<RELAY_PEER_ID> \
RELAY_PEERS=/ip4/<RELAY_PUBLIC_IP>/tcp/4001/p2p/<RELAY_PEER_ID> \
ENABLE_AUTORELAY=true \
ENABLE_HOLE_PUNCHING=true \
FORCE_REACHABILITY_PRIVATE=true \
HEARTBEAT_INTERVAL=5s \
go run ./cmd/tubo attach
```

### 5.4 Verifica discovery e route sul nodo edge

```bash
curl -fsS http://127.0.0.1:8444/services
curl -fsS http://127.0.0.1:8444/routes
```

Atteso: `count >= 1` e route auto-creata per `lmstudio`.

### 5.5 Esegui la query reale dal client sull'host edge

```bash
curl -sS \
  -H 'Host: lmstudio' \
  -H 'Content-Type: application/json' \
  -d '{"model":"google/gemma-4-e2b","system_prompt":"You answer only in rhymes.","input":"What is your favorite color?"}' \
  http://127.0.0.1:8443/api/v1/chat
```

Atteso: `HTTP 200` e body JSON restituito da LM Studio.

### 5.6 Smoke distribuito con Terraform su 3 Linode multi-region

Per un bench distribuito repeatable su cloud, e' disponibile anche uno stack Terraform + smoke harness:

- Terraform: `infra/terraform/linode-distributed/`
- doc: `docs/runbooks/LINODE_TERRAFORM_TESTBENCH.md`
- smoke: `./tests/smoke-terraform-linode.sh`

La topologia usa:

- `relay` pubblico
- `edge` NAT-like (SSH-only, ingress chiuso)
- `service` NAT-like (SSH-only, ingress chiuso)

Poiche' edge e service sono volutamente chiusi in ingresso, la verifica HTTP viene eseguita dall'interno dell'host edge via SSH.

### 5.7 Smoke distribuito con sole 2 macchine

Se hai solo 2 macchine reali disponibili, il compromesso operativo consigliato e':

- `edge` sulla macchina A;
- `relay` sulla macchina B (pubblica, obbligatoria);
- `service` + servizio di esempio sulla stessa macchina B;
- `service` bindato su loopback (`/ip4/127.0.0.1/tcp/40123`) con `force_reachability: private` per impedire il direct dial pubblico.

Questo produce comunque un bench **relay-first distribuito** utile, anche se non e' un 3-host puro.

Smoke script dedicato:

```bash
./tests/smoke-distributed-two-host.sh
```

Dettagli: `tests/distributed-two-host.md`

## 6) Aggiungere un servizio ulteriore sullo stesso tunnel

Pattern:

1. nuovo `service` con `SERVICE_NAME` univoco;
2. stesso `LIBP2P_PRIVATE_NETWORK_KEY` della swarm;
3. `BOOTSTRAP_PEERS` verso relay pubblico;
4. `RELAY_PEERS` verso relay pubblico + `ENABLE_AUTORELAY=true`;
5. verifica comparsa route su `GET /routes`;
6. chiamare edge con `Host: <SERVICE_NAME>`.

Esempio servizio aggiuntivo:

```bash
SERVICE_NAME=internal-api \
SERVICE_TARGET=http://127.0.0.1:9000 \
SERVICE_P2P_LISTEN=/ip4/0.0.0.0/tcp/40124 \
NODE_SEED=service-internal-api-seed \
LIBP2P_PRIVATE_NETWORK_KEY=/etc/p2p/swarm.key \
BOOTSTRAP_PEERS=/ip4/<RELAY_PUBLIC_IP>/tcp/4001/p2p/<RELAY_PEER_ID> \
RELAY_PEERS=/ip4/<RELAY_PUBLIC_IP>/tcp/4001/p2p/<RELAY_PEER_ID> \
ENABLE_AUTORELAY=true \
ENABLE_HOLE_PUNCHING=true \
FORCE_REACHABILITY_PRIVATE=true \
HEARTBEAT_INTERVAL=5s \
go run ./cmd/tubo attach
```

## 7) Stato sicurezza: cosa e' implementato vs target

Implementato oggi:

- discovery announcement firmati;
- private swarm PSK (env key path o b64);
- binary `relay` con relay service + AutoNAT service + router GossipSub discovery;
- parser allowlist PeerID (`LIBP2P_ALLOWED_PEERS`) + connection gater su relay, edge, service e bridge.

Target ancora da implementare:

- binding `ServiceName -> PeerID` enforcement applicativo oltre al controllo di connessione;
- diagnostica reachability/AutoNAT completa.

## 8) Troubleshooting rapido

Se `502` da edge:

1. controllare che servizio sia presente in `GET /services`;
2. controllare route in `GET /routes`;
3. verificare `BOOTSTRAP_PEERS` e `EDGE_PEER_ID` corretti;
4. verificare che tutti i nodi usino la stessa PSK (o nessuna PSK in locale);
5. controllare log `service` per raggiungibilita' `SERVICE_TARGET`.

Log utili attesi:

1. `relay`: `startup command hints`, `relay_addr`, connessioni `relay p2p connected/disconnected`.
2. `edge`: `proxy request`, `route matched`, `resolved`, `relay fallback`, `proxy completed`.
3. `service`: `service upstream request`, `service upstream response`, `service stream completed`.
4. `bridge`: `bridge request`, `bridge completed`.

Se un `GET` o una richiesta senza body resta appesa, verificare che il client stia usando una versione con final body chunk vuoto: il service deve vedere `service stream completed`, non solo `service upstream request`.
