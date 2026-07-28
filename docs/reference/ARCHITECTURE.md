# Architecture

## Goal

Tubo is a daemonless-by-default libp2p connectivity substrate. Commands compile resource intent into a runtime plan built from shared modules.

```text
CLI -> use case -> RuntimePlan -> Node Kernel -> modules
```

There are no independent service, bridge, or connect applications.

## Layers

```text
cmd/tubo
  |
internal/cli             parsing, output, command use cases
  |
internal/runtime         plan execution, readiness, registry, stats
  |          |           |             |
internal/pipe   internal/discovery   internal/authority
  |          |           |             |
internal/node -------- internal/transport
  |
libp2p

internal/store <------- use cases and runtimes
internal/domain <------ imported by all inner layers
```

`internal/domain` is pure and imports no libp2p, storage, CLI, or OS lifecycle package.

## Node Kernel

One kernel owns:

- libp2p identity and host construction;
- private swarm PSK and connection gating;
- bootstrap connectivity;
- static relay reservations and renewal;
- hole punching and reachability transitions;
- protocol registration;
- lifecycle and graceful shutdown;
- health, status, and stats endpoints.

Modules register capabilities against a kernel. Capability does not imply authority.

## Pipe Engine

The pipe engine composes ingress and target adapters.

Ingress adapters:

```text
LocalTCP
LocalHTTP
GatewayRoute
TuboStream
```

Target adapters:

```text
HTTPService
TCPService
UnixService
LocalPipe
RemotePipe
```

Canonical plans:

| Intent | Ingress | Target |
|---|---|---|
| attach HTTP | TuboStream | HTTPService |
| attach TCP | TuboStream | TCPService |
| connect | LocalTCP | RemotePipe |
| gateway route | GatewayRoute | RemotePipe |
| re-export | TuboStream | RemotePipe |

All transfer logic converges on a full-duplex byte copier after a common authenticated handshake.

## Discovery

A publisher signs one `PipeRecord`. The same envelope is used for:

- namespace-secret-backed GossipSub;
- direct announcement/query;
- cache storage;
- peer sync;
- opaque relay forwarding.

Consumers decrypt when required and verify the authority, pipe owner, scope, grant, expiry, and replay state before cache insertion. CLI views are projections, not alternate data models.

## Authority

One logical authority per scope owns policy and mutable issuer state. It issues:

- membership grants;
- pipe publish grants;
- forwarding grants;
- one-time share invites;
- short access leases;
- longer refresh leases.

Connect lease issuance is not delegated to published pipe runtimes. This keeps one issuer class and one refresh route.

## Store

The preferred implementation is an embedded transactional database in WAL mode for mutable metadata. Static config and secrets stay separate. Storage APIs expose domain transactions rather than generic tables to callers.

## Daemonless runtime

Foreground is the default. `-d` starts a child and commits run metadata only after readiness. OS supervisors are optional. There is no required `tubod`.

## Failure boundaries

- Stream/parser failures affect one session.
- Pipe authorization failure degrades or blocks one pipe, not all local resources.
- Discovery query/cache failure does not create trust.
- Relay failure may degrade sessions but does not alter authority state.
- Store failure prevents mutation success.
- A runtime may continue with already committed, still-valid credentials but may not invent or silently persist replacements.
