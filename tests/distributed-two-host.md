# Distributed 2-host smoke testbench

Questo smoke test usa **2 macchine reali**:

- `edge` sulla macchina locale/agent host (`172.236.202.99` di default)
- `relay` obbligatoriamente sulla macchina remota (`root@172-232-189-160.ip.linodeusercontent.com`)
- `service` + `dummy-api-server` co-hosted sulla macchina remota

## Perche' questa topologia

Con solo due macchine non possiamo avere `edge`, `relay` e `service` tutti separati come nel compose NAT.
Questa variante forza comunque un percorso distribuito utile:

- l'`edge` gira davvero su un host separato;
- il `relay` gira davvero sull'host remoto;
- il `service` remoto e' forzato a usare `p2p_listen=/ip4/127.0.0.1/tcp/40123` + `force_reachability: private`, quindi non e' direttamente dialabile dall'edge;
- il traffico deve quindi passare via relay.

In pratica non e' un test 3-host puro, ma e' un buon surrogate relay-first distribuito con solo 2 macchine.

## Prerequisiti

Locale:

- Go toolchain
- `curl`
- `ssh` + `scp`
- accesso SSH root alla macchina relay

Remoto:

- Linux amd64 compatibile
- `curl`
- porta `4001/tcp` aperta verso Internet

## Esecuzione

```bash
./tests/smoke-distributed-two-host.sh
```

Default importanti:

- `REMOTE_HOST=root@172-232-189-160.ip.linodeusercontent.com`
- `REMOTE_RELAY_IP=172.232.189.160`
- `EDGE_HOST_IP=172.236.202.99`
- `SERVICE_NAME=myapi`

## Variabili utili

- `KEEP_RUNNING=1` lascia i processi attivi per debug
- `RUN_DIR=...` cambia la directory locale generata
- `REMOTE_BASE_DIR=...` cambia la directory remota temporanea
- `EDGE_HTTP_LISTEN=127.0.0.1:18443`
- `EDGE_ADMIN_LISTEN=127.0.0.1:18444`

Esempio:

```bash
KEEP_RUNNING=1 ./tests/smoke-distributed-two-host.sh
```

## Verifiche eseguite

Lo script:

1. compila `tubo` e `dummy-api-server` in locale;
2. genera una PSK temporanea;
3. genera config YAML per `edge`, `relay`, `service`;
4. copia binari + config sul relay host;
5. avvia `relay`, `service`, `dummy-api-server` in remoto;
6. avvia `edge` in locale;
7. aspetta health + discovery + route;
8. esegue una request HTTP vera con `Host: myapi`;
9. controlla nei log dell'edge `connection_path=relayed`.

## Idea migliore con sole 2 macchine?

Sì, ma e' molto vicina a questa:

- tieni `edge` da una parte;
- sull'altra macchina tieni `relay` e `service`;
- **lega il service a loopback** per impedire direct dial pubblico;
- lascia il relay pubblico.

Questo e' il compromesso migliore se vuoi verificare davvero:

- control plane distribuito;
- discovery reale;
- forwarding HTTP reale;
- relay-first effettivo;
- debug semplice via SSH su una sola macchina remota.

L'unica alternativa leggermente migliore, sempre con 2 macchine, e' mettere il `service` dentro una network namespace o VM privata sulla macchina relay per isolarlo ancora di piu'. Ma come primo bench operativo, questo smoke e' gia' abbastanza buono.
