# Systemd Deployment Examples

Example systemd unit files for deploying Tubo services.

## Quick Start

1. Copy the desired unit file to `/etc/systemd/system/`
2. Create the config file referenced in the unit (e.g., `/etc/tubo/relay.yaml`)
3. Reload systemd and start the service:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now tubo-relay
```

## Available Units

| File | Description |
|------|-------------|
| `tubo-relay.service` | Public relay/bootstrap node |
| `tubo-grants.service` | Grant service for cluster authority |
| `tubo-attach.service` | Publish a local HTTP service |
| `tubo-connect.service` | Connect to a remote service |
| `tubo-gateway.service` | HTTP gateway/edge node |

## Important: HOME Environment Variable

Systemd does not set `HOME` by default. Without it, Tubo writes process state to `/.tubo-data/` instead of `~/.local/share/tubo/`, causing `tubo ps` to not see systemd-managed processes.

**Always include** in your unit files:

```ini
[Service]
Environment="HOME=/root"
```

Or for non-root users:

```ini
[Service]
User=tubo
Environment="HOME=/home/tubo"
```

## Configuration Files

Create your config files in `/etc/tubo/`. Example locations:

```
/etc/tubo/
├── relay.yaml
├── grants.yaml
├── attach-myapi.yaml
├── connect-myapi.yaml
├── gateway.yaml
└── swarm.key          # Private swarm key (chmod 600)
```

## Logs

By default, logs go to journald:

```bash
journalctl -u tubo-relay -f
```

To write logs to a file, add to the unit:

```ini
StandardOutput=append:/var/log/tubo-relay.log
StandardError=append:/var/log/tubo-relay.log
```

## Process Visibility

After starting services, verify they appear in `tubo ps`:

```bash
tubo ps
```

Expected output:

```
NAME                       COMMAND       SERVICE ID  SCOPE         STATUS   PID   LOCAL                  TARGET
relay-default              relay         -           -             running  1234  /ip4/0.0.0.0/tcp/4001  -
grants-serve-home-default  grants serve  -           home/default  running  1235  /ip4/127.0.0.1/tcp/0   -
```

## Security Recommendations

1. Run services as a dedicated non-root user when possible
2. Protect config files: `chmod 600 /etc/tubo/*.yaml`
3. Protect swarm keys: `chmod 600 /etc/tubo/swarm.key`
4. Use `ProtectSystem=strict` and `ProtectHome=true` for hardening
5. Limit file descriptors with `LimitNOFILE=`

## Troubleshooting

### Service fails to start

```bash
journalctl -u tubo-relay -n 50 --no-pager
```

### Process not visible in `tubo ps`

Check that `HOME` is set correctly in the unit file:

```bash
cat /proc/$(systemctl show tubo-relay -p MainPID --value)/environ | tr '\0' '\n' | grep HOME
```

### Permission denied errors

Ensure the user has access to config files and data directories:

```bash
sudo chown -R tubo:tubo /etc/tubo /var/lib/tubo
```
