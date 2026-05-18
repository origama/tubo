# Discovery and Multi-Host Runbook

Questo runbook copre due piani distinti:

1. **stato attuale (as-is)** del progetto, che ora richiede discovery cluster/namespace V2;
2. **target operativo consigliato** per deployment privato NAT/NAT (LM Studio su laptop + Hermes/edge su host remoto).

Per avvio componenti e tunnel p2p sicuro 2+ servizi in forma operativa, usare come riferimento primario:

- `docs/OPERABILITY.md`

## 1) Discovery: stato attuale (as-is)

### 1.1 Pubblicazione (service)

`tubo attach` oggi:

1. crea host libp2p (`p2p.NewHostWithSeedAndPSK`);
2. pubblica `AnnouncementV2` firmato e cifrato sul topic V2 del namespace;
3. include `ServiceName`, `ServiceID`, `Addresses`, membership capability e `ServiceClaim` valida;
4. avvia heartbeat (`HEARTBEAT_INTERVAL`, default `15s`) che ripubblica lo stesso annuncio;
5. tenta connessione ai bootstrap peers (`BOOTSTRAP_PEERS`) e ritenta (`BOOTSTRAP_RETRY_INTERVAL`, default `5s`).
6. se configurato, abilita static AutoRelay verso `RELAY_PEERS` (`ENABLE_AUTORELAY`, `ENABLE_HOLE_PUNCHING`, `FORCE_REACHABILITY_PRIVATE`).

### 1.2 Sottoscrizione e validazione (edge)

`tubo gateway` oggi:

1. crea host libp2p;
2. entra nel topic discovery V2 del namespace;
3. usa `PubSubSubscriber` per:
   - deserializzare annuncio;
   - verificare topic/cluster/namespace;
   - verificare membership capability del namespace e replay nonce;
   - richiedere e verificare una `ServiceClaim` valida per `service_id`, peer, namespace e authority;
   - aggiornare cache discovery.
4. se configurato, tenta connessione ai bootstrap peers (`BOOTSTRAP_PEERS`) e ritenta (`BOOTSTRAP_RETRY_INTERVAL`, default `5s`).

### 1.3 Cache e auto-routing

- Cache keyed per `serviceName` (`internal/discovery/cache.go`).
- Gli edge aggiornano la cache tramite Discovery V2 validata; non accettano `announce_service` sul protocollo query.
- I relay possono mantenere una cache query/sync per supportare `get services` remoti.
- Il TTL effettivo degli annunci V2 è limitato da announcement TTL e scadenza della `ServiceClaim`.
- Su evento `added`, il gateway crea route auto:
  - `hostname = serviceName`
  - `pathPrefix = "/"`
- Su `removed` (expiry), rimuove la route.

Quindi request HTTP con `Host: <serviceName>` viene inoltrata al peer scoperto.

### 1.4 Limiti attuali importanti

1. Un solo `ServiceEntry` per `serviceName` (ultimo annuncio vince).
2. La cache query dei relay resta keyed per `serviceName` e non sostituisce la validazione Discovery V2 degli edge.
3. Se gli indirizzi annunciati non sono raggiungibili, il dial diretto fallisce.
4. La cifratura attuale del payload Discovery V2 deriva la chiave da `cluster_id` + `namespace_id`; questo separa il payload per scope ma **non** fornisce una strong private-namespace metadata boundary se gli ID sono pubblici o indovinabili. Per il target 0.7 vedere `docs/security-model-0.7.md` e il futuro `namespace_discovery_key`.
5. Hole punching/AutoNAT non sono ancora completi nel progetto.
6. La private swarm PSK e supportata tramite env (`LIBP2P_PRIVATE_NETWORK_KEY` oppure `LIBP2P_PRIVATE_NETWORK_KEY_B64`) su `edge`, `service`, `bridge` e `relay`.
7. `LIBP2P_ALLOWED_PEERS` + connection gater sono implementati nel `relay`, ma non ancora enforced end-to-end su tutti i binari.
8. Il vecchio swarm discovery `"/discovery/v1.0"` non e' piu' supportato.

## 2) Obiettivo operativo per deployment NAT/NAT privato

### 2.1 Nodo pubblico controllato obbligatorio

Per deployment con nodi potenzialmente dietro NAT, deve esistere almeno un nodo pubblico stabile gestito da noi. Con Discovery V2 il nodo pubblico serve come bootstrap/relay transport, non come discovery swarm router.

Requisiti minimi:

- IP pubblico statico o DNS stabile;
- PeerID stabile;
- porta libp2p TCP aperta (es. `4001/tcp`);
- bootstrap peer della rete;
- relay circuit v2;
- stessa private network config degli altri peer.

Non usare bootstrap peer o relay pubblici di terzi per traffico privato.

Ruolo tipico:

```text
public-node:
- bootstrap peer
- circuit relay v2
- opzionale: AutoNAT service
- opzionale: edge HTTP ingress
```

### 2.2 Separazione bootstrap vs relay

- `bootstrap`: entrare nella rete e trovare peer (control plane).
- `relay`: trasportare traffico quando il direct dial non funziona (data plane).
- `hole punching`: ottimizza verso percorso diretto quando possibile, ma relay resta fallback.

Regola operativa:

1. bootstrap pubblico nostro = necessario per control plane;
2. relay pubblico nostro = necessario per data plane robusto NAT/NAT.

## 3) Private libp2p network (PSK) + autorizzazione peer

### 3.1 Private swarm PSK (target)

Configurazione desiderata:

- `LIBP2P_PRIVATE_NETWORK_KEY=/etc/hermes-p2p/swarm.key`
- oppure `LIBP2P_PRIVATE_NETWORK_KEY_B64=<secret>`

Quando presente, host libp2p deve essere creato con private network (`libp2p.PrivateNetwork(psk)`).

Policy chiave:

- entropia forte;
- distribuzione solo a nodi fidati;
- montata come secret;
- mai committata nel repository;
- ruotabile in caso di compromissione.

### 3.2 Allowlist PeerID (connection-level: implementata su relay/edge/service/bridge)

Configurazione desiderata:

- `LIBP2P_ALLOWED_PEERS=<EDGE_PEER_ID>,<SERVICE_AGENT_PEER_ID>,<RELAY_PEER_ID>,<HERMES_PEER_ID>`

Comportamento richiesto:

1. rifiutare inbound da PeerID non allowlisted;
2. rifiutare outbound verso PeerID non allowlisted;
3. rifiutare annunci discovery firmati da PeerID non allowlisted;
4. rifiutare mapping `ServiceName -> PeerID` non previsto.

Implementazione attuale:

- `ConnectionGater` per livello connessione;
- parser `LIBP2P_ALLOWED_PEERS` e enforcement connessioni su `relay`, `edge`, `service` e `bridge`.

Implementazione ancora necessaria:

- controlli applicativi in discovery handler e stream handler su gateway/agent;
- binding `ServiceName -> PeerID` oltre il semplice gate di connessione.

### 3.3 Binding ServiceName -> PeerID (target)

Esempi config:

- `SERVICE_AUTHZ_lmstudio=<SERVICE_AGENT_PEER_ID>`
- `SERVICE_AUTHZ_hermes=<HERMES_PEER_ID>`

Oppure formato unico:

- `DISCOVERY_SERVICE_ALLOWLIST=lmstudio:<SERVICE_AGENT_PEER_ID>,hermes:<HERMES_PEER_ID>`

Annuncio accettato solo se:

1. `Announcement.PeerID == sender peer`;
2. firma valida;
3. `PeerID` allowlisted;
4. `ServiceName` autorizzato per quel `PeerID`.

## 4) Discovery isolato (no public discovery)

Per questo deployment privato:

1. non usare public DHT;
2. non usare bootstrap peer casuali;
3. non usare relay pubblici esterni;
4. usare solo topic discovery V2 opachi derivati da cluster/namespace;
5. discovery continua con announcement firmati e capability verificate.

## 5) Relay privato, AutoRelay e NAT reachability

### 5.1 Config runtime consigliata (target)

- `BOOTSTRAP_PEERS=/ip4/<PUBLIC_NODE_IP>/tcp/4001/p2p/<PUBLIC_NODE_PEER_ID>`
- `RELAY_PEERS=/ip4/<PUBLIC_NODE_IP>/tcp/4001/p2p/<PUBLIC_NODE_PEER_ID>`
- `ENABLE_RELAY=true`
- `ENABLE_RELAY_SERVICE=true|false`
- `ENABLE_AUTORELAY=true`
- `ENABLE_HOLE_PUNCHING=true`
- `FORCE_REACHABILITY_PRIVATE=true`

### 5.2 Ruoli per nodo

Nodo pubblico:

- `ENABLE_RELAY_SERVICE=true`
- `ENABLE_AUTONAT_SERVICE=true`

Nodi dietro NAT:

- `ENABLE_RELAY=true`
- `ENABLE_AUTORELAY=true`
- `ENABLE_HOLE_PUNCHING=true`
- `FORCE_REACHABILITY_PRIVATE=true`

### 5.3 Relay statici (target)

Per ambienti privati usare relay statici configurati, non discovery generico relay.

Esempio:

- `RELAY_PEERS=/ip4/<PUBLIC_NODE_IP>/tcp/4001/p2p/<PUBLIC_NODE_PEER_ID>`

## 6) Diagnostica reachability (target)

Endpoint suggeriti:

- `GET /p2p/status`
- `GET /p2p/peers`
- `GET /p2p/relays`
- `GET /p2p/reachability`

Output minimo utile:

```json
{
  "peer_id": "...",
  "listen_addrs": [],
  "observed_addrs": [],
  "reachability": "private|public|unknown",
  "connected_peers": [],
  "relay_peers": [],
  "using_private_network": true,
  "allowed_peers_count": 3
}
```

## 7) Error taxonomy per HTTP 502 (target)

Quando il gateway non riesce a forwardare verso service scoperto, distinguere almeno:

1. `discovery_missing`
2. `peer_not_allowed`
3. `peer_not_connected`
4. `dial_failed`
5. `stream_open_failed`
6. `relay_unavailable`
7. `service_expired`
8. `target_unreachable_from_agent`

In log 502 includere almeno:

- `serviceName`
- target `PeerID`
- known addresses
- relay addresses
- tipo connessione (`direct|relayed|none`)
- last announcement timestamp
- last dial error

## 8) Runbook NAT/NAT privato (LM Studio + Hermes)

Topologia di riferimento:

```text
                    Internet
                       |
              +----------------+
              | public node    |
              | bootstrap      |
              | relay v2       |
              | AutoNAT svc    |
              +----------------+
                ^            ^
                | outbound   | outbound
                |            |
+-------------------+    +-------------------+
| laptop LM Studio  |    | Hermes / gateway  |
| service NAT |    | edge NAT          |
+-------------------+    +-------------------+
```

Flusso:

1. service connette outbound al public node;
2. edge connette outbound al public node;
3. service pubblica announcement firmato;
4. edge riceve e valida;
5. edge crea route `Host=lmstudio`;
6. Hermes chiama edge;
7. edge apre stream verso service;
8. se direct dial fallisce, usa relay;
9. se hole punching riesce, stream successivi possono andare diretti.

## 9) Configurazione esempio completa (target)

### 9.1 Public node

```bash
NODE_SEED=public-relay-seed \
LIBP2P_PRIVATE_NETWORK_KEY=/etc/hermes-p2p/swarm.key \
LIBP2P_ALLOWED_PEERS=<EDGE_PEER_ID>,<SERVICE_AGENT_PEER_ID>,<PUBLIC_NODE_PEER_ID> \
P2P_LISTEN=/ip4/0.0.0.0/tcp/4001 \
ENABLE_RELAY_SERVICE=true \
ENABLE_AUTONAT_SERVICE=true \
go run ./cmd/tubo relay run
```

### 9.2 Edge gateway dietro NAT

```bash
EDGE_LISTEN=:8443 \
EDGE_ADMIN_LISTEN=127.0.0.1:8444 \
EDGE_P2P_LISTEN=/ip4/0.0.0.0/tcp/4001 \
EDGE_SEED=edge-seed \
LIBP2P_PRIVATE_NETWORK_KEY=/etc/hermes-p2p/swarm.key \
LIBP2P_ALLOWED_PEERS=<PUBLIC_NODE_PEER_ID>,<SERVICE_AGENT_PEER_ID>,<EDGE_PEER_ID> \
BOOTSTRAP_PEERS=/ip4/<PUBLIC_NODE_IP>/tcp/4001/p2p/<PUBLIC_NODE_PEER_ID> \
RELAY_PEERS=/ip4/<PUBLIC_NODE_IP>/tcp/4001/p2p/<PUBLIC_NODE_PEER_ID> \
ENABLE_AUTORELAY=true \
ENABLE_HOLE_PUNCHING=true \
FORCE_REACHABILITY_PRIVATE=true \
go run ./cmd/tubo edge run
```

### 9.3 Service-agent laptop (LM Studio)

```bash
SERVICE_NAME=lmstudio \
SERVICE_TARGET=http://192.168.1.28:1234 \
SERVICE_P2P_LISTEN=/ip4/0.0.0.0/tcp/40123 \
NODE_SEED=laptop-lmstudio-seed \
LIBP2P_PRIVATE_NETWORK_KEY=/etc/hermes-p2p/swarm.key \
LIBP2P_ALLOWED_PEERS=<PUBLIC_NODE_PEER_ID>,<EDGE_PEER_ID>,<SERVICE_AGENT_PEER_ID> \
BOOTSTRAP_PEERS=/ip4/<PUBLIC_NODE_IP>/tcp/4001/p2p/<PUBLIC_NODE_PEER_ID> \
RELAY_PEERS=/ip4/<PUBLIC_NODE_IP>/tcp/4001/p2p/<PUBLIC_NODE_PEER_ID> \
ENABLE_AUTORELAY=true \
ENABLE_HOLE_PUNCHING=true \
FORCE_REACHABILITY_PRIVATE=true \
HEARTBEAT_INTERVAL=5s \
go run ./cmd/tubo service run
```

## 10) Test di accettazione richiesti (target)

1. peer senza PSK non si connette;
2. peer con PSK ma PeerID non allowlisted e rifiutato;
3. peer allowlisted ma `ServiceName` non autorizzato e rifiutato;
4. announcement con firma non valida e rifiutato;
5. service dietro NAT scoperto via relay;
6. edge apre stream via relay;
7. route `Host=lmstudio` creata dopo discovery valido;
8. route `Host=lmstudio` rimossa dopo expiry;
9. `502` include/logga motivo corretto quando relay non disponibile;
10. nessun nodo usa public DHT o relay esterni.

## 11) Livello di sicurezza ottenuto (target)

La private swarm con PSK isola la rete libp2p da peer esterni che non possiedono la chiave, ma non sostituisce autorizzazione applicativa.

Per questo il deployment deve usare livelli multipli:

1. PSK private network;
2. PeerID allowlist;
3. discovery announcement firmati;
4. binding `ServiceName -> PeerID` autorizzato;
5. relay/bootstrap controllati da noi;
6. no public DHT;
7. no relay pubblici di terzi.

Limite importante:

Se la PSK viene compromessa, va ruotata su tutti i nodi. La PeerID allowlist riduce il rischio ma non elimina la necessita di rotazione PSK.

## 12) Scenario LM Studio + Hermes: comandi minimi (as-is, oggi)

Questa sezione resta utile finche le feature target sopra non sono tutte implementate.

### 12.1 Edge su Linode

```bash
EDGE_LISTEN=:8443 \
EDGE_ADMIN_LISTEN=127.0.0.1:8444 \
EDGE_P2P_LISTEN=/ip4/0.0.0.0/tcp/4001 \
EDGE_SEED=edge-linode-seed \
go run ./cmd/tubo edge run
```

### 12.2 Service-agent su laptop

```bash
SERVICE_P2P_LISTEN=/ip4/0.0.0.0/tcp/40123 \
SERVICE_TARGET=http://192.168.1.28:1234 \
NODE_SEED=laptop-lmstudio-seed \
SERVICE_NAME=lmstudio \
HEARTBEAT_INTERVAL=5s \
BOOTSTRAP_PEERS=/ip4/<LINODE_PUBLIC_IP>/tcp/4001/p2p/<EDGE_PEER_ID> \
go run ./cmd/tubo service run
```

### 12.3 Query da Hermes

```bash
curl -sS -H 'Host: lmstudio' http://<EDGE_IP>:8443/v1/models
```

```bash
curl -sS \
  -H 'Host: lmstudio' \
  -H 'Content-Type: application/json' \
  -d '{"model":"local-model","messages":[{"role":"user","content":"ciao"}]}' \
  http://<EDGE_IP>:8443/v1/chat/completions
```
