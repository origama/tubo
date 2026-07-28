# Ontology

## Service

A service is a local terminal known only to its owner machine.

Examples:

```text
service/lmstudio  -> http://127.0.0.1:1234
service/postgres  -> tcp://127.0.0.1:5432
service/reviewer  -> A2A endpoint
```

A service is declarative, persistent, and inert. It is never advertised and does not become a runtime merely because it exists. Its local target is secret operational metadata.

## Pipe

A pipe is a persistent, directed connectivity resource:

```text
Ingress -> Pipe -> Target
```

Every pipe has exactly one target, either `service/...` or `pipe/...`.

Ingress kinds:

- `local`: local TCP/HTTP listener;
- `tubo`: authenticated libp2p listener, optionally advertised;
- `http-route`: a gateway host/path route.

A pipe owns network identity, publication, access policy, lifecycle, aggregate status, and statistics.

Examples:

```text
Tubo ingress -> pipe/lmstudio-team -> service/lmstudio
local :51234 -> pipe/lmstudio-local -> remote pipe/pip_123
Tubo ingress -> pipe/reexport -> remote pipe/pip_123
```

## Run

A run is the execution of one primary resource. It contains PID, readiness, status, log, control endpoint, and aggregate counters. It does not own durable pipe identity or policy.

`attach` and `connect` may create runs, but they are not run kinds. The primary reference remains `pipe/...`.

## Session

A session is one concrete traversal of a pipe graph. It carries:

- session ID;
- immediate caller and next hop;
- logical hop list;
- per-hop authorization result;
- per-hop transport route (`direct`, `relayed`, upgraded);
- timestamps and byte counters;
- terminal/refresh status.

One running pipe can have zero or many sessions, each with a different transport route.

## Pipe record

A pipe record is a signed, scoped, expiring advertisement of a published pipe. It contains enough information to resolve and authorize the pipe without exposing its local target.

A cache stores records. A query returns records. A relay may forward encrypted/opaque records. None becomes an authority by doing so.

## Scope and overlay

Scope is always:

```text
cluster + namespace
```

An overlay is the transport context: swarm isolation, bootstrap peers, and relay peers. Overlay reachability does not imply scope membership, discovery visibility, or connect authorization.

## Identity vocabulary

- `pipe_id`: stable cryptographic identity and authorization audience;
- `peer_id`: current libp2p runtime identity hosting a pipe;
- `display_name`: non-unique user-facing label;
- `service/...`: local reference with no network authority;
- `session_id`: one connection, not a resource identity.

## Path vocabulary

Three path types are distinct:

1. logical: `pipe/client -> pipe/proxy -> pipe/origin -> service/local`;
2. authorization: grants and policy decisions at each hop;
3. transport: direct or relay path used for each network hop.

A command must not report one as though it proves another.
