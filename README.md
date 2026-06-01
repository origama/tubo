# Tubo

**Tubo creates private libp2p tunnels for HTTP APIs, raw TCP/TLS services, and AI agents.**

## Install

```bash
curl -fsSL https://www.tubo.click/install.sh | sh
```

Build from source:

```bash
git clone https://github.com/origama/tubo.git
cd tubo
go build -o tubo ./cmd/tubo
```

## Quick start: public invite-only

HTTP service:

```bash
tubo attach myapp --port 8080 -d
# prints a one-time `tubo connect --token ...` command
tubo connect --token eyJ... --local 127.0.0.1:9000
```

Raw TCP / TLS passthrough:

```bash
tubo attach tcp://127.0.0.1:8443 --name tlsdemo -d
tubo connect --token eyJ... --local 127.0.0.1:9443
# local endpoint is tcp://127.0.0.1:9443
```

## Quick start: private swarm

```bash
tubo keygen swarm --out swarm.key
tubo relay --swarm-key ./swarm.key -d
tubo join overlay/manual --relay /ip4/<RELAY_IP>/tcp/4001/p2p/<RELAY_PEER> --swarm-key ./swarm.key
tubo attach myapp --port 8080 -d
tubo connect myapp --local 127.0.0.1:9000
# same flow also works for `tcp://...` targets / TLS passthrough
```

## Documentation

- [Docs index](./docs/README.md)
- [CLI reference](./docs/reference/cli.md)
- [Operational runbook](./docs/runbooks/OPERABILITY.md)
- [Security notes](./docs/reference/SECURITY.md)
- [Security model 0.7](./docs/reference/security-model-0.7.md)
- [Versioning policy](./docs/reference/VERSIONING.md)
- [Release process](./docs/runbooks/RELEASING.md)
- [CHANGELOG.md](./CHANGELOG.md)

## For coding agents

- [AGENTS.md](./AGENTS.md)
- the assigned GitHub Issue and linked subissues
- use the canonical docs above instead of repeating setup/details here
- run `make verify-repo-hygiene` (or `./tests/verify-repo-hygiene.sh`) before opening hygiene/docs PRs
