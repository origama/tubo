# Piano: tubo edge come reverse proxy applicativo

Questo documento riassume il ragionamento sul possibile supporto di `tubo edge` come reverse proxy applicativo con controllo delle rotte.

## Idea generale

`tubo edge` è già vicino a un reverse proxy applicativo.

Oggi il flusso è:

```text
HTTP client -> tubo edge -> routing -> libp2p stream -> tubo service -> upstream HTTP
```

L'edge riceve HTTP, sceglie un service, apre uno stream libp2p e inoltra la richiesta. Questo è già il nucleo di un reverse proxy distribuito.

La proposta è aggregare molti service pubblicizzati nello swarm in un unico albero di URI, simile a nginx/Traefik/Caddy.

Esempio:

```text
https://edge.example.com/lmstudio/...
https://edge.example.com/ollama/...
https://edge.example.com/internal-api/...
```

Ogni prefisso viene inoltrato al service corrispondente.

## Perché ha senso in tubo

`tubo` ha già elementi utili:

- discovery dei service nello swarm;
- service name;
- edge HTTP;
- forwarding request/response;
- admin API;
- topology/config YAML;
- relay libp2p;
- private swarm.

Quindi l'edge può diventare un punto di ingresso HTTP unico per tutti i service privati.

Questo differenzia `tubo` da tunnel più generici come frp/chisel/rathole, che ragionano soprattutto su porte TCP/UDP.

## Modello desiderato

### Route statiche

Configurazione esplicita:

```yaml
routes:
  - name: lmstudio
    match:
      host: edge.example.com
      path_prefix: /lmstudio/
    service: lmstudio
    rewrite:
      strip_prefix: /lmstudio

  - name: internal-api
    match:
      host: edge.example.com
      path_prefix: /internal-api/
    service: internal-api
    rewrite:
      strip_prefix: /internal-api
```

Risultato:

```text
GET /lmstudio/v1/models
  -> service lmstudio
  -> upstream /v1/models

GET /internal-api/users
  -> service internal-api
  -> upstream /users
```

### Auto path tree

Modalità automatica basata sui service scoperti:

```yaml
edge:
  auto_path_tree:
    enabled: true
    base_path: /
    strip_prefix: true
```

Se nello swarm compaiono service:

```text
lmstudio
ollama
grafana
```

l'edge genera automaticamente:

```text
/lmstudio/* -> lmstudio
/ollama/*   -> ollama
/grafana/*  -> grafana
```

Questa è probabilmente la feature più utile per la UX.

## Path rewriting

Il punto più delicato è la riscrittura del path.

Se il client chiama:

```text
/lmstudio/v1/chat/completions
```

spesso il target vuole ricevere:

```text
/v1/chat/completions
```

Quindi serve `strip_prefix`.

Ma non tutti i servizi vogliono stripping. Alcune app sono configurate per vivere sotto un subpath e vogliono vedere il path completo.

Perciò le opzioni dovrebbero includere:

```yaml
rewrite:
  strip_prefix: /lmstudio
```

oppure:

```yaml
rewrite:
  preserve_path: true
```

Dovrebbe valere la regola del longest-prefix match: tra più route compatibili vince quella con prefisso più specifico.

## Header forwarding

Per comportarsi come un reverse proxy serio, l'edge deve gestire correttamente header forwarding:

- `X-Forwarded-For`;
- `X-Forwarded-Host`;
- `X-Forwarded-Proto`;
- `X-Forwarded-Prefix`;
- `X-Real-IP`;
- eventualmente header standard `Forwarded`.

`X-Forwarded-Prefix` è importante per app montate sotto subpath.

Configurazione possibile:

```yaml
reverse_proxy:
  forwarded_headers: true
```

Per route:

```yaml
routes:
  - name: grafana
    match:
      path_prefix: /grafana/
    service: grafana
    rewrite:
      strip_prefix: /grafana
    headers:
      set:
        X-Forwarded-Prefix: /grafana
```

## Discovery metadata e route hints

In futuro i service potrebbero pubblicare anche metadata di routing.

Esempio lato service:

```yaml
service:
  name: lmstudio
  target: http://127.0.0.1:1234
  advertise:
    routes:
      - host: edge.example.com
        path_prefix: /lmstudio/
        strip_prefix: /lmstudio
    tags:
      - ai
```

L'announcement di discovery potrebbe includere route hints:

```text
service_name
peer_id
addresses
ttl
routes
tags
```

Questo consentirebbe ai service di suggerire come vogliono essere esposti.

## Sicurezza delle route automatiche

La parte più rischiosa è permettere ai service di scegliere autonomamente le route.

Un service malevolo potrebbe annunciare:

```text
/
/admin
/api
```

rubando traffico destinato ad altri service.

Per questo conviene separare tre modalità.

### 1. Route statiche amministrate dall'edge

L'edge owner decide tutte le route.

È la modalità più sicura.

### 2. Auto path tree sicuro

L'edge decide automaticamente il prefisso a partire dal service name:

```text
/<service-name>/
```

Il service non sceglie path arbitrari.

Questa modalità è sicura e comoda.

### 3. Route hints approvati

Il service pubblica suggerimenti, ma l'edge li accetta solo se policy consente.

Esempio:

```yaml
edge:
  route_policy:
    allow_service_hints: true
    allowed_prefixes:
      - /services/
      - /api/
```

Oppure policy per service:

```yaml
edge:
  route_policy:
    services:
      lmstudio:
        allowed_prefixes:
          - /lmstudio/
      internal-api:
        allowed_prefixes:
          - /internal-api/
```

## Controllo delle rotte

Un reverse proxy applicativo dovrebbe offrire controllo esplicito su:

- host match;
- path prefix match;
- service target;
- rewrite path;
- header set/add/remove;
- priorità;
- timeout per route;
- max body size;
- auth per route;
- allowed methods;
- rate limit;
- visibility/admin state;
- route dinamiche da discovery;
- route statiche da config.

Esempio configurazione evoluta:

```yaml
reverse_proxy:
  forwarded_headers: true
  auto_path_tree:
    enabled: true
    base_path: /
    strip_prefix: true

routes:
  - name: lmstudio
    priority: 100
    match:
      host: api.example.com
      path_prefix: /lmstudio/
      methods: [GET, POST]
    service: lmstudio
    rewrite:
      strip_prefix: /lmstudio
    timeout: 60s
    max_body_size: 100MiB
```

## Admin API e diagnosi

L'edge dovrebbe esporre le route effettive via admin API.

Endpoint possibili:

```text
GET /routes
GET /routes/effective
GET /services
GET /route-match?host=...&path=...
```

Informazioni utili:

- route name;
- source: static, auto_path_tree, discovery_hint;
- service;
- peer selezionato;
- match host/path;
- rewrite applicato;
- stato healthy/unhealthy;
- ultimo announcement;
- eventuali collisioni.

Questo è importante perché con route dinamiche serve capire perché una richiesta viene inviata a un certo service.

## Problemi da gestire

Le difficoltà principali sono:

1. Path rewrite corretto.
2. Collisioni tra route.
3. Longest-prefix match.
4. Sicurezza delle route annunciate dai service.
5. Header `X-Forwarded-*` corretti.
6. WebSocket/HTTP upgrade.
7. Streaming request/response.
8. Timeout e cancellazione request.
9. Compatibilità con app non progettate per subpath.
10. Debug delle route effettive.

## WebSocket e HTTP upgrade

Molte applicazioni moderne usano WebSocket o HTTP upgrade.

Il modello HTTP attuale andrebbe verificato/esteso per supportare:

- `Connection: Upgrade`;
- `Upgrade: websocket`;
- streaming full-duplex dopo handshake;
- gestione corretta degli header hop-by-hop.

Questa parte può essere una fase separata.

## Piano incrementale

### Fase 1: route statiche con path prefix

Obiettivo:

```yaml
routes:
  - path_prefix: /lmstudio/
    service: lmstudio
    strip_prefix: /lmstudio
```

Modifiche:

1. Estendere config.
2. Aggiungere route table con host/path prefix.
3. Implementare longest-prefix match.
4. Applicare rewrite prima del forwarding.
5. Testare match e rewrite.
6. Documentare.

Onerosità: bassa/media.

### Fase 2: auto path tree da discovery

Obiettivo:

```text
/<service-name>/* -> service-name
```

Modifiche:

1. Config `edge.auto_path_tree`.
2. Generare route dinamiche dai service scoperti.
3. Rimuovere route quando il service scade.
4. Sanitizzare service name.
5. Gestire collisioni.
6. Esportare route effettive da admin API.

Onerosità: media.

### Fase 3: route hints nei service announcement

Obiettivo: permettere ai service di suggerire route.

Modifiche:

1. Estendere schema announcement.
2. Firmare/validare i nuovi campi.
3. Policy edge per accettare/rifiutare hints.
4. Compatibilità con announcement vecchi.
5. Admin diagnostics.

Onerosità: media.

### Fase 4: reverse proxy features avanzate

Aggiungere gradualmente:

- auth per route;
- ACL;
- rate limiting;
- max body size;
- timeout per route;
- header manipulation;
- WebSocket;
- TLS edge;
- health checking;
- observability.

Onerosità: media/alta, ma incrementale.

## Conclusione

Il supporto dell'edge come reverse proxy applicativo è una direzione molto coerente per `tubo`.

La versione più utile e semplice sarebbe:

```text
https://edge.example.com/<service-name>/...
```

con auto-discovery dei service e strip opzionale del prefisso.

Questo trasformerebbe lo swarm in un unico endpoint HTTP navigabile e renderebbe `tubo` un:

```text
reverse proxy p2p self-hosted con service discovery libp2p
```

È una feature più distintiva del semplice TCP tunneling, perché valorizza l'architettura già presente in `tubo`.
