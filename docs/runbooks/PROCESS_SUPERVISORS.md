# Process supervision for `tubo`

This document defines the recommended strategy for integrating `tubo` with system supervisors while keeping the **daemonless-by-default** model.

## Goal

Provide an optional path to make `tubo` long-running processes (`attach`, `connect`, `gateway`, `relay`) persistent across reboot without introducing a central `tubod` daemon.

## Recommended decision

### Base model

- The primary model remains:
  - foreground by default;
  - `-d` / `--detach` for lightweight local background mode;
  - no mandatory central daemon.
- System-supervisor persistence is **optional**.
- `-d` is not replaced: it remains the lightest path for development, demos, and unmanaged environments.

### First recommended implementation

**Implement `tubo generate systemd` first, not `--install`.**

Reasons:

- it is safer and more transparent;
- it avoids privileged or machine-dependent side effects;
- it avoids choosing too early between user units and system units;
- it leaves the operator in control of binary path, environment, enable/start, and logging;
- it matches the daemonless model: `tubo` generates config, the OS manages the lifecycle.

### Linux choice

For Linux, the recommended initial target is **systemd user units**.

Do not start with global system units:

- user units do not require root for the common case;
- they fit better with a self-hosted developer/operator tool;
- they reduce the risk of writing to `/etc/systemd/system` or imposing system policy too early.

### macOS choice

For macOS, the recommended decision is to **defer `launchd` automation to a second phase**.

In the meantime:

- document an equivalent strategy with `launchd`;
- do not implement `tubo generate launchd` in the first PR;
- do not implement cross-platform `--install` yet.

Reason: first stabilize naming, metadata, and the relationship between `process/...` and unit files on the systemd path.

## Recommended UX

### Phase 1: explicit generation

```bash
tubo generate systemd process/attach-lmstudio
```

or:

```bash
tubo generate systemd attach --name lmstudio --target http://127.0.0.1:1234
```

Similarly:

```bash
tubo generate systemd connect lmstudio --local 127.0.0.1:51234
tubo generate systemd gateway --listen :8443
tubo generate systemd relay
```

### Not recommended as a first step

```bash
tubo attach ... --install --enable
```

That UX may come later, but it should not be the first implementation step.

## Stable unit naming

Recommended mapping:

| Resource ID | systemd user unit |
|---|---|
| `process/attach-lmstudio` | `tubo-attach-lmstudio.service` |
| `process/connect-lmstudio-51234` | `tubo-connect-lmstudio-51234.service` |
| `process/gateway-default` | `tubo-gateway-default.service` |
| `process/relay-default` | `tubo-relay-default.service` |

Rules:

- fixed `tubo-` prefix;
- base name derived from the local process name already used by `-d`;
- `.service` suffix for systemd;
- same naming even if the process is not running yet.

This allows `process/...` to be treated as a stable ID even when the real lifecycle is delegated to a supervisor.

## Interaction with `tubo ps/get processes/logs/stop/describe/inspect`

After the inventory and process registry refactor, these commands describe locally registered Tubo runtimes (foreground or detached) when Tubo knows state/PID metadata.

It remains true that:

- they do not become implicit wrappers around `systemctl --user` or `launchctl`;
- externally managed processes may appear only if they use the same data/config context and register compatible metadata;
- logs are tailable only when Tubo knows an owned log file.

### Why

- avoids surprising platform-specific behavior;
- avoids mixing local log files with systemd journal;
- avoids strong dependencies on external commands and user permissions;
- allows incremental systemd adoption.

### How this works in practice at this stage

For processes installed via unit files or other external supervisors:

- lifecycle management: `systemctl --user start|stop|restart ...` (or equivalent);
- logs: `journalctl --user-unit ...` when no Tubo-owned log file exists;
- inspect: `systemctl --user status ...`.

`process/...` remains the canonical ID for naming, generation, and documentation.

### Possible future evolution

If a unified view of external supervisors is needed later, we can still add an explicit mode, for example:

- `tubo get processes --supervised`
- `tubo describe process/attach-lmstudio` showing `supervisor=systemd`
- `tubo logs process/attach-lmstudio --systemd`

## Recommended files and metadata

### Generated output

For Linux user units, output target:

```text
~/.config/systemd/user/tubo-attach-lmstudio.service
```

Recommended unit template:

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

### Recommended sidecar metadata

Even if `tubo ps` does not integrate systemd yet, it is useful to plan for a future-readable sidecar, for example:

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

Recommended path:

```text
~/.local/share/tubo/processes/attach-lmstudio.supervisor.json
```

This sidecar does not imply that the process was started with `-d`; it only serves as metadata for a future integration.

## macOS / launchd strategy

Decision recommended for this issue:

- document `launchd`;
- do not implement generator/install automation yet;
- use the same `process/...` naming semantics.

Suggested equivalent naming:

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

## Operational examples

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

## Summary for implementation

1. Keep the base model daemonless.
2. Add `tubo generate systemd` first.
3. Keep `-d` as the lightweight detached mode.
4. Do not implement `--install` first.
5. Keep `launchd` as a later phase.
6. `process/...` remains the canonical ID for installed services too.
7. Local `ps/logs/stop/inspect` read locally registered Tubo runtimes; for supervised services without Tubo-owned log files, use the OS-native tools.
8. Optional supervisor integration is a future enhancement, not part of the core.
