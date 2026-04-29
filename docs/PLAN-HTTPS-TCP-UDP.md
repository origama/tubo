# Piano: supporto HTTPS, TCP e UDP in tubo

Questo documento riassume il ragionamento sul supporto di HTTPS, TCP e UDP in `tubo`.

## Stato attuale

`tubo` oggi è centrato sul forwarding HTTP request/response:

```text
client HTTP -> tubo edge -> stream libp2p -> tubo service -> upstream HTTP
```

L'edge riceve una richiesta HTTP, ne serializza metodo, path, query, header e body, poi la invia al service tramite stream libp2p. Il service ricostruisce una richiesta HTTP verso il target configurato.

Questo significa che il modello attuale non è un tunnel raw TCP/UDP generico, ma un reverse proxy HTTP distribuito su libp2p.

## HTTPS verso il servizio target

Il supporto base a target HTTPS è probabilmente già presente.

Il service usa `SERVICE_TARGET` / `service.target`, ad esempio:

```yaml
service:
  target: https://internal-api.local:8443
```

Nel forwarding lato service viene costruita la URL upstream e viene usato il client HTTP standard di Go. Quindi, se il target è `https://...`, Go usa TLS automaticamente.

Funziona già nei casi semplici:

- certificato valido;
- hostname coerente con il certificato;
- CA già fidata dal sistema/container;
- nessun mTLS richiesto.

## Limiti HTTPS upstream attuali

Per un uso robusto mancano opzioni operative:

- CA custom;
- override del TLS server name / SNI;
- mTLS client certificate;
- configurazione di timeout;
- opzione esplicita, eventualmente solo per dev, `insecure_skip_verify`;
- validazione in `tubo doctor`.

## Piano HTTPS upstream robusto

Aggiungere configurazione tipo:

```yaml
service:
  target: https://internal-api.local:8443
  tls:
    ca_file: /etc/tubo/ca.pem
    server_name: internal-api.local
    client_cert_file: /etc/tubo/client.crt
    client_key_file: /etc/tubo/client.key
    insecure_skip_verify: false
```

Modifiche necessarie:

1. Estendere `internal/config` con una sezione TLS per `service.target`.
2. Passare la configurazione TLS da `internal/app/service` al forwarding HTTP.
3. Sostituire `http.DefaultClient` con un client costruito da config.
4. Aggiungere validazioni in `tubo config validate` e `tubo doctor`.
5. Aggiungere test con `httptest.NewTLSServer`.
6. Documentare CA custom, mTLS e self-signed.

Onerosità stimata: bassa/media.

## HTTPS sull'edge verso i client

Oggi l'edge espone HTTP plain. HTTPS pubblico può essere ottenuto in due modi.

### Opzione A: TLS terminato davanti a tubo

Esempio:

```text
client HTTPS -> Caddy/nginx/Traefik/LB -> tubo edge HTTP -> libp2p -> service
```

Questa soluzione è già possibile senza modificare `tubo`.

### Opzione B: TLS direttamente in tubo edge

Configurazione desiderata:

```yaml
edge:
  listen: :8443
  tls:
    cert_file: /etc/tubo/tls.crt
    key_file: /etc/tubo/tls.key
```

Modifiche:

1. Estendere config edge.
2. Usare `ListenAndServeTLS` quando cert/key sono configurati.
3. Validare certificati e chiavi in `doctor`.
4. Aggiungere test e docs.

Onerosità stimata: bassa con cert/key statici; media/alta se si aggiunge ACME/Let's Encrypt automatico.

## HTTPS passthrough

HTTPS passthrough significa che l'edge non termina TLS e trasporta byte TCP raw fino al service:

```text
client TLS -> tubo edge TCP listener -> libp2p stream -> tubo service -> backend TCP:443
```

Questo non è supportato dal modello HTTP attuale. Richiede il supporto a tunnel TCP generico.

## TCP generico

Il supporto TCP è fattibile e coerente con libp2p, perché gli stream libp2p sono già:

- bidirezionali;
- ordinati;
- affidabili;
- stream-oriented.

Schema:

```text
client TCP -> edge TCP listener -> libp2p stream -> service TCP dial -> target TCP
```

Esempi d'uso:

```yaml
tcp:
  listeners:
    - listen: 127.0.0.1:15432
      service: postgres

tcp:
  targets:
    - name: postgres
      target: 127.0.0.1:5432
```

Oppure:

```bash
tubo tcp listen --listen 127.0.0.1:15432 --service postgres
```

### Piano TCP

1. Definire un nuovo protocollo libp2p, ad esempio `/tubo/tcp/1.0`.
2. Aggiungere configurazione `tcp.listeners` lato edge/bridge.
3. Aggiungere configurazione `tcp.targets` lato service.
4. Estendere discovery per annunciare capability TCP.
5. Implementare listener TCP lato edge.
6. Aprire stream libp2p verso il service selezionato.
7. Lato service, fare `net.Dial("tcp", target)`.
8. Copiare byte in entrambe le direzioni con `io.Copy`.
9. Gestire chiusure, half-close, deadline e idle timeout.
10. Aggiungere limiti di connessioni e logging.
11. Testare con echo server TCP e servizi reali come Redis/Postgres-like.

Onerosità stimata: media.

## UDP generico

UDP è più complesso perché è datagram-based, mentre libp2p stream è stream-based.

UDP ha semantica diversa:

- messaggi/datagrammi distinti;
- possibile perdita;
- possibile riordinamento;
- assenza di connessione;
- sensibilità a MTU e timing;
- mapping NAT/sessioni applicativo.

Un supporto UDP semplice può essere emulato sopra stream libp2p, ma non sarebbe UDP “puro”.

Schema:

```text
client UDP -> edge UDP socket -> frame datagram -> libp2p stream -> service UDP dial/write -> target UDP
```

### Piano UDP emulato

1. Definire protocollo `/tubo/udp/1.0`.
2. Definire frame datagram con session ID, indirizzo sorgente, payload e metadata.
3. Aggiungere listener UDP lato edge.
4. Mantenere una session table `client addr -> session`.
5. Lato service, inoltrare datagrammi al target UDP.
6. Mappare le risposte dal target verso il client corretto.
7. Gestire timeout sessioni.
8. Limitare dimensione datagrammi.
9. Definire una drop policy in caso di congestione.
10. Testare con echo UDP e DNS-like.

Onerosità stimata: medio/alta.

## Limiti UDP

Buoni casi d'uso iniziali:

- DNS semplice;
- syslog;
- protocolli request/response piccoli;
- UDP echo/test.

Casi rischiosi:

- QUIC;
- WebRTC;
- RTP/media realtime;
- gaming;
- protocolli sensibili a perdita, ordine e timing.

Per questi casi servirebbe un lavoro molto più profondo.

## Roadmap consigliata

1. HTTPS upstream robusto con CA custom e mTLS opzionale.
2. HTTPS edge con cert/key statici.
3. TCP tunnel generico.
4. TLS passthrough/SNI routing sopra TCP.
5. UDP emulato per casi semplici.
6. UDP avanzato solo se emerge un requisito concreto.

## Sintesi

- HTTPS target: già possibile nei casi semplici.
- HTTPS target robusto: modifica piccola/media.
- HTTPS edge: modifica piccola con certificati statici.
- HTTPS passthrough: richiede TCP tunnel.
- TCP generico: fattibile, costo medio, alto valore.
- UDP semplice: fattibile ma più delicato.
- UDP vero/QUIC-grade: oneroso, da rimandare.
