# Process supervision for `tubo`

Questo documento definisce la strategia consigliata per integrare `tubo` con supervisor di sistema, mantenendo il modello **daemonless-by-default**.

## Obiettivo

Offrire un percorso opzionale per rendere persistenti dopo reboot i processi long-running di `tubo` (`attach`, `connect`, `gateway`, `relay`) senza introdurre un demone centrale `tubod`.

## Decisione raccomandata

### Modello base

- Il modello primario resta:
  - foreground by default;
  - `-d` / `--detach` per background locale leggero;
  - nessun demone centrale obbligatorio.
- La persistenza tramite supervisor di sistema e' **opzionale**.
- `-d` non viene sostituito: resta il percorso piu' leggero per sviluppo, demo e ambienti non gestiti.

### Prima implementazione raccomandata

**Implementare prima `tubo generate systemd`, non `--install`.**

Motivi:

- e' piu' sicuro e piu' trasparente;
- evita side effects privilegiati o dipendenti dalla macchina;
- evita di dover decidere subito tra user unit e system unit;
- lascia all'operatore il controllo su path del binario, environment, enable/start e logging;
- e' coerente con il modello daemonless: `tubo` genera config, il sistema operativo gestisce il lifecycle.

### Scelta Linux

Per Linux, il target iniziale raccomandato e' **systemd user units**.

Non partire da system unit globali:

- le user unit non richiedono root per il caso comune;
- si adattano meglio a un tool developer/operator self-hosted;
- riducono il rischio di scrivere file in `/etc/systemd/system` o di imporre policy di sistema troppo presto.

### Scelta macOS

Per macOS, la decisione raccomandata e' **rimandare l'automazione `launchd` a una seconda fase**.

Nel frattempo:

- documentare una strategia equivalente con `launchd`;
- non implementare ancora `tubo generate launchd` nella prima PR;
- non implementare ancora `--install` cross-platform.

Motivo: prima conviene stabilizzare naming, metadata e relazione tra `process/...` e unit file sul percorso systemd.

## UX raccomandata

### Fase 1: generazione esplicita

```bash
tubo generate systemd process/attach-lmstudio
```

oppure:

```bash
tubo generate systemd attach --name lmstudio --target http://127.0.0.1:1234
```

Analogamente:

```bash
tubo generate systemd connect lmstudio --local 127.0.0.1:51234
tubo generate systemd gateway --listen :8443
tubo generate systemd relay
```

### Non raccomandato come prima mossa

```bash
tubo attach ... --install --enable
```

Questa UX puo' arrivare in una fase successiva, ma non dovrebbe essere il primo passo implementativo.

## Naming stabile delle unit

Mapping raccomandato:

| Resource ID | systemd user unit |
|---|---|
| `process/attach-lmstudio` | `tubo-attach-lmstudio.service` |
| `process/connect-lmstudio-51234` | `tubo-connect-lmstudio-51234.service` |
| `process/gateway-default` | `tubo-gateway-default.service` |
| `process/relay-default` | `tubo-relay-default.service` |

Regole:

- prefisso fisso `tubo-`;
- base name derivato dal process name locale gia' usato da `-d`;
- suffix `.service` per systemd;
- stesso naming anche se il processo non e' attualmente running.

Questo permette di trattare `process/...` come ID stabile anche quando il lifecycle reale e' delegato al supervisor.

## Interazione con `tubo ps/get processes/logs/stop/describe/inspect`

### Decisione per la prima implementazione

I comandi attuali **continuano a descrivere solo i processi detached locali gestiti da `tubo` via state file**.

Quindi, nella prima fase:

- `tubo ps`
- `tubo get processes`
- `tubo logs`
- `tubo stop`
- `tubo describe process/...`
- `tubo inspect process/...`

**non diventano wrapper impliciti di `systemctl --user` o `launchctl`.**

### Motivazione

- evita behavior sorprendente e platform-specific;
- evita di mescolare file-log locali con journal di systemd;
- evita dipendenze forti da comandi esterni e da permessi utente;
- consente di introdurre systemd in modo incrementale.

### Come si lavora in pratica in fase 1

Per processi installati via unit file:

- gestione lifecycle: `systemctl --user start|stop|restart ...`
- logs: `journalctl --user-unit ...`
- inspect: `systemctl --user status ...`

`process/...` resta comunque l'ID canonico per naming, generazione e documentazione.

### Evoluzione futura possibile

In una fase successiva si puo' aggiungere una vista unificata, ad esempio:

- `tubo get processes --supervised`
- `tubo describe process/attach-lmstudio` che mostri anche `supervisor=systemd`
- `tubo logs process/attach-lmstudio --systemd`

Ma non e' raccomandato farlo nel primo passo.

## File e metadata raccomandati

### Output generato

Per Linux user units, output target:

```text
~/.config/systemd/user/tubo-attach-lmstudio.service
```

Unit template raccomandato:

```ini
[Unit]
Description=tubo attach lmstudio
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/absolute/path/to/tubo attach http://127.0.0.1:1234 --name lmstudio
Restart=on-failure
RestartSec=2s
WorkingDirectory=%h
Environment=XDG_CONFIG_HOME=%h/.config
Environment=XDG_DATA_HOME=%h/.local/share

[Install]
WantedBy=default.target
```

### Metadata sidecar consigliato

Anche se `tubo ps` non integra ancora systemd, e' utile prevedere un sidecar leggibile da tooling futuro, per esempio:

```json
{
  "id": "process/attach-lmstudio",
  "supervisor": "systemd-user",
  "unit": "tubo-attach-lmstudio.service",
  "generated_at": "2026-05-03T00:00:00Z",
  "command": [
    "/absolute/path/to/tubo",
    "attach",
    "http://127.0.0.1:1234",
    "--name",
    "lmstudio"
  ]
}
```

Path consigliato:

```text
~/.local/share/tubo/processes/attach-lmstudio.supervisor.json
```

Questo sidecar non implica che il processo sia stato avviato con `-d`; serve solo come metadata per una futura integrazione.

## Strategia macOS / launchd

Decisione raccomandata per questa issue:

- documentare `launchd`;
- non implementare ancora generator/install automatico;
- usare la stessa semantica di naming di `process/...`.

Naming equivalente suggerito:

```text
io.origama.tubo.attach-lmstudio
io.origama.tubo.connect-lmstudio-51234
io.origama.tubo.gateway-default
io.origama.tubo.relay-default
```

Plist sketch:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>io.origama.tubo.attach-lmstudio</string>
    <key>ProgramArguments</key>
    <array>
      <string>/absolute/path/to/tubo</string>
      <string>attach</string>
      <string>http://127.0.0.1:1234</string>
      <string>--name</string>
      <string>lmstudio</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
  </dict>
</plist>
```

## Esempi operativi

### Attach

```bash
tubo generate systemd attach --name lmstudio --target http://127.0.0.1:1234 \
  > ~/.config/systemd/user/tubo-attach-lmstudio.service
systemctl --user daemon-reload
systemctl --user enable --now tubo-attach-lmstudio.service
```

### Connect

```bash
tubo generate systemd connect lmstudio --local 127.0.0.1:51234 \
  > ~/.config/systemd/user/tubo-connect-lmstudio-51234.service
systemctl --user daemon-reload
systemctl --user enable --now tubo-connect-lmstudio-51234.service
```

### Gateway

```bash
tubo generate systemd gateway --listen :8443 \
  > ~/.config/systemd/user/tubo-gateway-default.service
systemctl --user daemon-reload
systemctl --user enable --now tubo-gateway-default.service
```

### Relay

```bash
tubo generate systemd relay \
  > ~/.config/systemd/user/tubo-relay-default.service
systemctl --user daemon-reload
systemctl --user enable --now tubo-relay-default.service
```

## Decisioni finali per #48

1. Integrazione supervisor **opzionale**; non sostituisce `-d`.
2. Prima implementazione raccomandata: **`tubo generate systemd`**.
3. Linux: partire da **systemd user units**.
4. macOS: **launchd documentato ma rimandato** come implementazione.
5. `--install` / `--enable` non sono il primo passo raccomandato.
6. `process/...` resta l'ID canonico anche per servizi installati.
7. I comandi locali `ps/logs/stop/inspect` restano inizialmente limitati ai processi detached locali; per i servizi supervisionati si usano gli strumenti nativi del sistema operativo.

## Possibile roadmap successiva

1. implementare `tubo generate systemd ...`;
2. aggiungere metadata sidecar per unit generate;
3. valutare `tubo generate launchd ...`;
4. solo dopo valutare `--install` / `--enable`;
5. solo dopo valutare una vista unificata `tubo get processes --supervised`.
