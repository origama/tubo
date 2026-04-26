# Operability Runbook

Questo documento e' il riferimento operativo canonico per:

1. avviare i componenti;
2. creare un tunnel p2p funzionante;
3. creare un tunnel p2p **sicuro** (private swarm PSK) tra due o piu' servizi.

## 1) Prerequisiti

- Go 1.24+
- Docker + Docker Compose plugin
- rete raggiungibile tra nodi (almeno outbound dai nodi service verso il nodo edge/bootstrap)

## 2) Componenti runtime reali (oggi)

- `p2p-relay` (bootstrap + relay v2 + health endpoint)
- `edge-gateway` (ingress HTTP + discovery consumer)
- `service-agent` (publisher + stream handler verso servizio origin)
- opzionale `client-bridge` (proxy client-side)

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
KEY_HEX="$(openssl rand -hex 32)"
cat > swarm.key <<EOF_KEY
/key/swarm/psk/1.0.0/
/base16/
${KEY_HEX}
EOF_KEY
chmod 600 swarm.key
```

Distribuire `swarm.key` **solo** ai nodi fidati. Non committare nel repository.

### 4.2 Variabili supportate (implementate)

- `LIBP2P_PRIVATE_NETWORK_KEY=/path/to/swarm.key`
- `LIBP2P_PRIVATE_NETWORK_KEY_B64=<base64_32_bytes>`

Se valorizzate, host libp2p viene creato con private network PSK.

## 5) Setup multi-host: relay pubblico + edge + 2 servizi

### 5.1 Avvia p2p-relay (host pubblico stabile)

```bash
NODE_SEED=public-relay-seed \
P2P_LISTEN=/ip4/0.0.0.0/tcp/4001 \
RELAY_HEALTH_LISTEN=127.0.0.1:8092 \
ENABLE_RELAY_SERVICE=true \
ENABLE_AUTONAT_SERVICE=true \
FORCE_REACHABILITY_PUBLIC=true \
LIBP2P_PRIVATE_NETWORK_KEY=/etc/p2p/swarm.key \
go run ./cmd/p2p-relay
```

Recuperare `peer_id` relay dai log (`peer_id=...`).

### 5.2 Avvia edge-gateway

```bash
EDGE_LISTEN=:8443 \
EDGE_ADMIN_LISTEN=127.0.0.1:8444 \
EDGE_P2P_LISTEN=/ip4/0.0.0.0/tcp/4001 \
EDGE_SEED=edge-seed \
BOOTSTRAP_PEERS=/ip4/<RELAY_PUBLIC_IP>/tcp/4001/p2p/<RELAY_PEER_ID> \
RELAY_PEERS=/ip4/<RELAY_PUBLIC_IP>/tcp/4001/p2p/<RELAY_PEER_ID> \
LIBP2P_PRIVATE_NETWORK_KEY=/etc/p2p/swarm.key \
go run ./cmd/edge-gateway
```

Recuperare `peer_id` edge dai log (`edge gateway peer_id=...`).

### 5.3 Avvia service-agent #1 (es. lmstudio)

```bash
SERVICE_NAME=lmstudio \
SERVICE_TARGET=http://192.168.1.28:1234 \
SERVICE_P2P_LISTEN=/ip4/0.0.0.0/tcp/40123 \
NODE_SEED=service-lmstudio-seed \
LIBP2P_PRIVATE_NETWORK_KEY=/etc/p2p/swarm.key \
BOOTSTRAP_PEERS=/ip4/<RELAY_PUBLIC_IP>/tcp/4001/p2p/<RELAY_PEER_ID>,/ip4/<EDGE_IP>/tcp/4001/p2p/<EDGE_PEER_ID> \
HEARTBEAT_INTERVAL=5s \
go run ./cmd/service-agent
```

### 5.4 Avvia service-agent #2 (es. internal-api)

```bash
SERVICE_NAME=internal-api \
SERVICE_TARGET=http://127.0.0.1:9000 \
SERVICE_P2P_LISTEN=/ip4/0.0.0.0/tcp/40124 \
NODE_SEED=service-internal-api-seed \
LIBP2P_PRIVATE_NETWORK_KEY=/etc/p2p/swarm.key \
BOOTSTRAP_PEERS=/ip4/<RELAY_PUBLIC_IP>/tcp/4001/p2p/<RELAY_PEER_ID>,/ip4/<EDGE_IP>/tcp/4001/p2p/<EDGE_PEER_ID> \
HEARTBEAT_INTERVAL=5s \
go run ./cmd/service-agent
```

### 5.5 Verifica discovery e routing

Dal nodo edge:

```bash
curl -fsS http://127.0.0.1:8444/services
curl -fsS http://127.0.0.1:8444/routes
```

Atteso: route auto-create per `lmstudio` e `internal-api`.

### 5.6 Query verso i servizi attraverso edge

```bash
curl -sS -H 'Host: lmstudio' http://<EDGE_IP>:8443/v1/models
```

```bash
curl -sS -H 'Host: internal-api' http://<EDGE_IP>:8443/healthz
```

## 6) Aggiungere un nuovo servizio al tunnel

Pattern:

1. nuovo `service-agent` con `SERVICE_NAME` univoco;
2. stesso `LIBP2P_PRIVATE_NETWORK_KEY` della swarm;
3. `BOOTSTRAP_PEERS` verso relay pubblico (e opzionalmente edge);
4. verifica comparsa route su `GET /routes`;
5. chiamare edge con `Host: <SERVICE_NAME>`.

## 7) Stato sicurezza: cosa e' implementato vs target

Implementato oggi:

- discovery announcement firmati;
- private swarm PSK (env key path o b64).
- binary `p2p-relay` con relay service + AutoNAT service.
- parser allowlist PeerID (`LIBP2P_ALLOWED_PEERS`) + connection gater sul relay.

Target ancora da implementare:

- allowlist PeerID enforcement completo su edge-gateway/service-agent/client-bridge;
- binding `ServiceName -> PeerID` enforcement;
- diagnostica reachability/AutoNAT completa.

## 8) Troubleshooting rapido

Se `502` da edge:

1. controllare che servizio sia presente in `GET /services`;
2. controllare route in `GET /routes`;
3. verificare `BOOTSTRAP_PEERS` e `EDGE_PEER_ID` corretti;
4. verificare che tutti i nodi usino la stessa PSK (o nessuna PSK in locale);
5. controllare log `service-agent` per raggiungibilita' `SERVICE_TARGET`.
