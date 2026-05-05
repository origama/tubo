# Tubo

**Tubo creates private libp2p tunnels for HTTP APIs, services, and AI agents.**

It lets you publish a local HTTP endpoint from one machine and consume it from another machine as if it were local, even when one or both hosts are behind NAT or firewalls. A Tubo network is made of a relay/bootstrap node, one or more attached services, and clients that connect to those services through the swarm.

Tubo is designed for self-hosted, private networks: the libp2p transport is encrypted and authenticated, and an optional private swarm key adds network-level isolation.

## Why Tubo?

- **Publish local services without opening inbound firewall rules**: attach LM Studio, Ollama, internal APIs, dashboards, or agent endpoints.
- **Consume remote services through a local port**: connect to a service by name and talk to `127.0.0.1`.
- **Run your own relay**: no central hosted control plane is required.
- **Use one binary**: relay, attach, connect, gateway, discovery, and process management are all exposed through `tubo`.
- **Keep long-running processes daemonless**: run in the foreground for debugging, or detach with local process state.

## Install

### One-line installer

The installer downloads the latest prebuilt release for your platform, verifies `SHA256SUMS.txt` when possible, and installs `tubo` into `$HOME/.local/bin` by default.

```bash
curl -fsSL https://raw.githubusercontent.com/origama/tubo/main/install.sh | sh
```

Install a specific release:

```bash
curl -fsSL https://raw.githubusercontent.com/origama/tubo/main/install.sh | sh -s -- --version v0.1.3
```

Install somewhere else:

```bash
curl -fsSL https://raw.githubusercontent.com/origama/tubo/main/install.sh | sh -s -- --install-dir /usr/local/bin
```

> Note: while this repository is private, the public raw URL and release download URLs are not accessible without authentication. The installer is intentionally committed now so it is ready as soon as the repository and releases are made public.

### Build from source

```bash
git clone https://github.com/origama/tubo.git
cd tubo
go build -o tubo ./cmd/tubo
./tubo version
```

The project currently uses the Go version declared in [`go.mod`](./go.mod).

## Quick start: relay, attach, connect

This example creates a small Tubo network with three roles:

- a **relay host** reachable by the other nodes;
- a **service host** running an HTTP API, for example LM Studio on `127.0.0.1:1234`;
- a **client host** that opens a local port to that remote service.

### 1. Start the relay host

Generate a private swarm key and start a relay. Replace `RELAY_IP_OR_DNS` with the public IP or DNS name that service/client hosts can reach.

```bash
tubo keygen swarm --out swarm.key

export RELAY_IP="RELAY_IP_OR_DNS"
export RELAY_PEER="$(tubo id from-seed public-relay-seed)"
export RELAY_ADDR="/ip4/${RELAY_IP}/tcp/4001/p2p/${RELAY_PEER}"

tubo relay \
  --listen /ip4/0.0.0.0/tcp/4001 \
  --public-addr /ip4/${RELAY_IP}/tcp/4001 \
  --swarm-key ./swarm.key \
  -d
```

Copy `swarm.key` securely to the service and client hosts. Share `RELAY_ADDR` with them too.

### 2. Attach a service

On the machine that runs the HTTP service:

```bash
tubo join --relay "$RELAY_ADDR" --swarm-key ./swarm.key --check

tubo attach http://127.0.0.1:1234 --name lmstudio -d
```

The service remains published while the detached `attach` process is running.

### 3. Connect from a client

On the client machine:

```bash
tubo join --relay "$RELAY_ADDR" --swarm-key ./swarm.key --check

tubo get services --timeout 10s
tubo describe service/lmstudio

tubo connect lmstudio --local 127.0.0.1:51234
```

Now the remote service is available locally:

```bash
curl http://127.0.0.1:51234/healthz
```

`connect` runs in the foreground so you can see logs and stop it with `Ctrl+C`.

## Common commands

```bash
# Start a relay/bootstrap node
tubo relay -d

# Join an existing Tubo swarm
tubo join --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... --swarm-key ./swarm.key

# Publish a local HTTP endpoint into the swarm
tubo attach http://127.0.0.1:1234 --name lmstudio -d

# List and inspect services in the swarm
tubo get services
tubo describe service/lmstudio
tubo inspect service/lmstudio --json

# Open a local listener to a remote service
tubo connect lmstudio --local 127.0.0.1:51234

# Manage detached local Tubo processes
tubo ps
tubo logs process/attach-lmstudio
tubo stop process/attach-lmstudio
tubo rm --stale
```

Advanced role commands are still available for explicit configuration-file based operation:

```bash
tubo relay run --config relay.yaml
tubo edge run --config edge.yaml
tubo service run --config service.yaml
tubo bridge run --config bridge.yaml
```

## Process model

Tubo does not require a central local daemon. Long-running commands stay in the foreground by default. Commands that support `-d` / `--detach` write local process state under XDG-style directories:

```text
~/.local/share/tubo/processes/
~/.local/share/tubo/logs/
~/.local/share/tubo/run/
```

Use `tubo ps`, `tubo logs`, `tubo stop`, and `tubo rm --stale` to inspect and manage those detached processes.

## Documentation

- [CLI guide](./docs/cli.md)
- [Operational runbook](./docs/OPERABILITY.md)
- [Security notes](./docs/SECURITY.md)
- [Protocol notes](./docs/PROTOCOL.md)
- [Architecture](./docs/ARCHITECTURE.md)
- [Release process](./docs/RELEASING.md)

## For coding agents

Start here before making code changes:

- [AGENTS.md](./AGENTS.md)
- [TASKS.md](./TASKS.md)
- [Versioning policy](./docs/VERSIONING.md)
