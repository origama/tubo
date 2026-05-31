# Tubo

**Tubo creates private libp2p tunnels for HTTP APIs, services, and AI agents.**

It lets you publish a local HTTP endpoint from one machine and consume it from another as if it were local, even when one or both hosts are behind NAT or firewalls. A Tubo network is made of a relay/bootstrap node, one or more attached services, and clients that connect to those services through the swarm.

Tubo is designed for self-hosted, private networks: the libp2p transport is encrypted and authenticated, and an optional private swarm key adds network-level isolation.

## Why Tubo?

- **Publish local services without opening inbound firewall rules** — attach LM Studio, Ollama, internal APIs, dashboards, or agent endpoints.
- **Consume remote services through a local port** — connect to a service by name and talk to `127.0.0.1`.
- **Run your own relay** — no central hosted control plane is required.
- **Use one binary** — relay, attach, connect, gateway, discovery, and process management are all in `tubo`.
- **Keep long-running processes daemonless** — run in the foreground for debugging, or detach with `-d`.

## Install

### One-line installer

```bash
curl -fsSL https://www.tubo.click/install.sh | sh
```

Install a specific release:

```bash
curl -fsSL https://www.tubo.click/install.sh | sh -s -- --version v0.7.0
```

Install to a custom directory:

```bash
curl -fsSL https://www.tubo.click/install.sh | sh -s -- --install-dir /usr/local/bin
```

### Build from source

Requires Go 1.24+.

```bash
git clone https://github.com/origama/tubo.git
cd tubo
go build -o tubo ./cmd/tubo
./tubo version
```

## Quick start: public network (invite-only)

The simplest flow uses the public Tubo relay. No config needed on first run — `attach` and `connect` auto-join on first use.

**Machine A — publish a local service:**

```bash
tubo attach http://127.0.0.1:8080 --name myapp -d
# or shorthand:
tubo attach myapp --port 8080 -d
```

`attach` prints a share invite token:

```
✓ Service published   name=myapp  visibility=unlisted
✓ Share token:  tubo connect --token eyJ...
```

Copy the `tubo connect --token ...` line and give it to whoever needs access.

**Machine B — connect with the token:**

```bash
tubo connect --token eyJ... --local 127.0.0.1:9000
curl http://127.0.0.1:9000/
```

Share tokens are one-time: each token can be redeemed by one client. Generate additional tokens with `tubo share service/myapp`.

## Quick start: private swarm

For stronger isolation, run your own relay with a private swarm key.

**Relay host (needs a public IP, TCP 4001 open):**

```bash
tubo keygen swarm --out swarm.key   # generate once, distribute securely
tubo relay \
  --swarm-key   ./swarm.key \
  --public-addr /ip4/<RELAY_IP>/tcp/4001 \
  -d
# note the printed multiaddr: /ip4/<RELAY_IP>/tcp/4001/p2p/12D3...
```

**Service host:**

```bash
tubo join overlay/manual \
  --relay     /ip4/<RELAY_IP>/tcp/4001/p2p/12D3... \
  --swarm-key ./swarm.key
tubo create cluster/myteam
tubo attach myapp --port 8080 -d
```

**Client host:**

```bash
tubo join overlay/manual \
  --relay     /ip4/<RELAY_IP>/tcp/4001/p2p/12D3... \
  --swarm-key ./swarm.key
tubo join cluster/myteam --token <cluster-invite>
tubo get services
tubo connect myapp --local 127.0.0.1:9000
```

## Common commands

```bash
# ── Network setup ──────────────────────────────────────────────────────────
tubo join                                     # join the public Tubo network
tubo join overlay/manual \
  --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... \
  --swarm-key ./swarm.key                     # join a private swarm

# ── Runtime roles ──────────────────────────────────────────────────────────
tubo relay -d                                 # start a relay/bootstrap node
tubo gateway --listen :8443 -d               # start an HTTP gateway
tubo attach myapp --port 8080 -d             # publish a local service
tubo connect --token eyJ... \
  --local 127.0.0.1:9000                     # connect via invite token
tubo connect myapp --local 127.0.0.1:9000    # connect by name (collab namespaces)

# ── Discovery ──────────────────────────────────────────────────────────────
tubo get services
tubo describe service/myapp
tubo inspect service/myapp --json
tubo watch services --timeout 30s

# ── Clusters, namespaces, sharing ──────────────────────────────────────────
tubo create cluster/myteam
tubo create namespace/production
tubo create service/myapp
tubo share cluster/myteam --role member       # invite a team member
tubo share cluster/myteam --role viewer
tubo share service/myapp --expires 1h         # one-time connect token
tubo join cluster/myteam --token <invite>
tubo use cluster/myteam
tubo use namespace/production

# ── Publish grants (non-authority nodes) ───────────────────────────────────
tubo grants serve --cluster myteam --namespace default -d
tubo grants pending
tubo grants approve gr_123 --ttl 168h
tubo grants request service/myapp --poll

# ── Revocation ─────────────────────────────────────────────────────────────
tubo revoke invite <token>
tubo revoke session <session-id>
tubo revoke service-access myapp
tubo revoke publish myapp

# ── Process management ─────────────────────────────────────────────────────
tubo ps
tubo logs process/attach-myapp
tubo stop process/attach-myapp
tubo rm --stale

# ── Utilities ──────────────────────────────────────────────────────────────
tubo keygen swarm --out swarm.key
tubo id from-seed my-seed
tubo config validate
tubo config print
tubo doctor
tubo version
```

## LM Studio / Ollama

```bash
# Publish LM Studio (port 1234)
tubo attach lmstudio --port 1234 -d

# Publish Ollama (port 11434)
tubo attach ollama --port 11434 -d

# Connect from another machine
tubo connect --token eyJ... --local 127.0.0.1:51234

# Use the OpenAI-compatible endpoint as if it were local
curl http://127.0.0.1:51234/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"local-model","messages":[{"role":"user","content":"Hello"}]}'
```

## Process model

Tubo does not require a central daemon. Long-running commands stay in the foreground by default; use `-d` / `--detach` to run in the background. Detached process state is stored under XDG-style directories:

```text
~/.config/tubo/            — configuration, swarm key, cluster/namespace state
~/.local/share/tubo/processes/  — detached process metadata
~/.local/share/tubo/logs/       — process log files
~/.local/share/tubo/run/        — PID files
```

Use `tubo ps`, `tubo logs`, `tubo stop`, and `tubo rm --stale` to manage detached processes.

## Configuration precedence

```
CLI flag  >  env var  >  config file  >  default  >  interactive prompt
```

Interactive prompts are disabled when `CI=true` or `--non-interactive` is set.

Key environment variables:

| Variable | Description |
|---|---|
| `LIBP2P_PRIVATE_NETWORK_KEY` | Path to swarm key file |
| `LIBP2P_PRIVATE_NETWORK_KEY_B64` | Base64-encoded 32-byte PSK |
| `LIBP2P_ALLOWED_PEERS` | Comma-separated PeerID allowlist |
| `TUBO_DEFAULT_PUBLIC_BUNDLE_URL` | Override the default public bundle URL |
| `CI` | Set to `true` to disable interactive prompts and implicit join |

## Documentation

Full documentation is at **[www.tubo.click/docs/](https://www.tubo.click/docs/)**.

Key references in this repository:

- [docs/cli.md](./docs/cli.md) — full CLI reference
- [docs/OPERABILITY.md](./docs/OPERABILITY.md) — operational runbook (relay setup, multi-host, private swarm)
- [docs/security-model-0.7.md](./docs/security-model-0.7.md) — security model and trust chain
- [docs/VERSIONING.md](./docs/VERSIONING.md) — versioning and compatibility policy
- [docs/RELEASING.md](./docs/RELEASING.md) — release process
- [CHANGELOG.md](./CHANGELOG.md) — release history

## For coding agents

Start here before making code changes:

- [AGENTS.md](./AGENTS.md)
- the assigned GitHub Issue and linked subissues
- [docs/VERSIONING.md](./docs/VERSIONING.md) when touching protocol, release, or compatibility behavior
