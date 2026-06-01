# Plan: tubo edge as an application reverse proxy

This document summarizes the reasoning behind possible support for `tubo edge` as an application reverse proxy with route control.

## General idea

`tubo edge` is already close to an application reverse proxy.

Today the flow is:

```text
HTTP client -> tubo edge -> routing -> libp2p stream -> tubo service -> upstream HTTP
```

The edge receives HTTP, chooses a service, opens a libp2p stream, and forwards the request. That is already the core of a distributed reverse proxy.

The proposal is to aggregate many services advertised in the swarm into a single URI tree, similar to nginx/Traefik/Caddy.

Example:

```text
https://edge.example.com/lmstudio/...
https://edge.example.com/ollama/...
https://edge.example.com/internal-api/...
```

Each prefix is forwarded to the corresponding service.

## Why this makes sense in tubo

`tubo` already has useful pieces:

- service discovery in the swarm;
- service names;
- HTTP edge;
- request/response forwarding;
- admin API;
- topology/config YAML;
- libp2p relay;
- private swarm.

So the edge can become a single HTTP entry point for all private services.

This differentiates `tubo` from more generic tunnels such as frp/chisel/rathole, which mainly think in terms of TCP/UDP ports.

## Desired model

### Static routes

Explicit configuration:

```yaml
routes:
  - name: lmstudio
    match:
      host: edge.example.com
      path_prefix: /lmstudio/
    service: lmstudio
    rewrite:
      strip_prefix: /lmstudio

  - name: internal-api
    match:
      host: edge.example.com
      path_prefix: /internal-api/
    service: internal-api
    rewrite:
      strip_prefix: /internal-api
```

Result:

```text
GET /lmstudio/v1/models
  -> service lmstudio
  -> upstream /v1/models

GET /internal-api/users
  -> service internal-api
  -> upstream /users
```

### Auto path tree

Automatic mode based on discovered services:

```yaml
edge:
  auto_path_tree:
    enabled: true
    base_path: /
    strip_prefix: true
```

If the swarm contains services:

```text
lmstudio
ollama
grafana
```

the edge automatically generates:

```text
/lmstudio/* -> lmstudio
/ollama/*   -> ollama
/grafana/*  -> grafana
```

This is probably the single most useful UX feature.

## Path rewriting

The trickiest part is path rewriting.

If the client calls:

```text
/lmstudio/v1/chat/completions
```

the target often wants to receive:

```text
/v1/chat/completions
```

So `strip_prefix` is needed.

But not all services want stripping. Some apps are designed to live under a subpath and want to see the full path.

So the options should include:

```yaml
rewrite:
  strip_prefix: /lmstudio
```

or:

```yaml
rewrite:
  preserve_path: true
```

The longest-prefix match rule should apply: among compatible routes, the one with the most specific prefix wins.

## Header forwarding

To behave like a serious reverse proxy, the edge must handle header forwarding correctly:

- `X-Forwarded-For`;
- `X-Forwarded-Host`;
- `X-Forwarded-Proto`;
- `X-Forwarded-Prefix`;
- `X-Real-IP`;
- optionally the standard `Forwarded` header.

`X-Forwarded-Prefix` is important for apps mounted under a subpath.

Possible configuration:

```yaml
reverse_proxy:
  forwarded_headers: true
```

Per-route:

```yaml
routes:
  - name: grafana
    match:
      path_prefix: /grafana/
    service: grafana
    rewrite:
      strip_prefix: /grafana
    headers:
      set:
        X-Forwarded-Prefix: /grafana
```

## Discovery metadata and route hints

In the future, services could also publish routing metadata.

Example on the service side:

```yaml
service:
  name: lmstudio
  target: http://127.0.0.1:1234
  advertise:
    routes:
      - host: edge.example.com
        path_prefix: /lmstudio/
        strip_prefix: /lmstudio
    tags:
      - ai
```

The discovery announcement could include route hints:

```text
service_name
peer_id
addresses
ttl
routes
tags
```

This would let services suggest how they want to be exposed.

## Route safety

The riskiest part is allowing services to choose routes autonomously.

A malicious service could announce:

```text
/
/admin
/api
```

and hijack traffic intended for other services.

For that reason, it is better to separate three modes.

### 1. Static routes managed by the edge

The edge owner decides all routes.

This is the safest mode.

### 2. Safe auto path tree

The edge automatically decides the prefix from the service name:

```text
/<service-name>/
```

The service does not choose arbitrary paths.

This is safe and convenient.

### 3. Approved route hints

The service publishes hints, but the edge accepts them only if policy allows it.

Example:

```yaml
edge:
  route_policy:
    allow_service_hints: true
    allowed_prefixes:
      - /services/
      - /api/
```

Or per-service policy:

```yaml
edge:
  route_policy:
    services:
      lmstudio:
        allowed_prefixes:
          - /lmstudio/
      internal-api:
        allowed_prefixes:
          - /internal-api/
```

## Route control

An application reverse proxy should offer explicit control over:

- host match;
- path prefix match;
- service target;
- path rewrite;
- header set/add/remove;
- priority;
- route timeout;
- max body size;
- route auth;
- allowed methods;
- rate limiting;
- visibility/admin state;
- dynamic routes from discovery;
- static routes from config.

Example of an advanced configuration:

```yaml
reverse_proxy:
  forwarded_headers: true
  auto_path_tree:
    enabled: true
    base_path: /
    strip_prefix: true

routes:
  - name: lmstudio
    priority: 100
    match:
      host: api.example.com
      path_prefix: /lmstudio/
      methods: [GET, POST]
    service: lmstudio
    rewrite:
      strip_prefix: /lmstudio
    timeout: 60s
    max_body_size: 100MiB
```

## Admin API and diagnostics

The edge should expose the effective routes through its admin API.

Possible endpoints:

```text
GET /routes
GET /routes/effective
GET /services
GET /route-match?host=...&path=...
```

Useful information:

- route name;
- source: static, auto_path_tree, discovery_hint;
- service;
- selected peer;
- host/path match;
- applied rewrite;
- healthy/unhealthy state;
- last announcement;
- any collisions.

This matters because dynamic routes require understanding why a request is routed to a specific service.

## Problems to handle

The main difficulties are:

1. correct path rewriting.
2. route collisions.
3. longest-prefix match.
4. security of routes announced by services.
5. correct `X-Forwarded-*` headers.
6. WebSocket/HTTP upgrade.
7. streaming request/response.
8. request timeout and cancellation.
9. compatibility with apps not designed for subpaths.
10. debugging the effective routes.

## WebSocket and HTTP upgrade

Many modern apps use WebSocket or HTTP upgrade.

The current HTTP model should be verified/extended to support:

- `Connection: Upgrade`;
- `Upgrade: websocket`;
- full-duplex streaming after handshake;
- correct handling of hop-by-hop headers.

This can be a separate phase.

## Incremental plan

### Phase 1: static routes with path prefix

Goal:

```yaml
routes:
  - path_prefix: /lmstudio/
    service: lmstudio
    strip_prefix: /lmstudio
```

Changes:

1. Extend config.
2. Add a route table with host/path prefix.
3. Implement longest-prefix match.
4. Apply rewrite before forwarding.
5. Test match and rewrite.
6. Document it.

Effort: low/medium.

### Phase 2: auto path tree from discovery

Goal:

```text
/<service-name>/* -> service-name
```

Changes:

1. `edge.auto_path_tree` config.
2. Generate dynamic routes from discovered services.
3. Remove routes when the service expires.
4. Sanitize service names.
5. Handle collisions.
6. Export effective routes from the admin API.

Effort: medium.

### Phase 3: route hints in service announcements

Goal: allow services to suggest routes.

Changes:

1. Extend the announcement schema.
2. Sign/validate the new fields.
3. Edge policy to accept/reject hints.
4. Compatibility with old announcements.
5. Admin diagnostics.

Effort: medium.

### Phase 4: advanced reverse-proxy features

Add gradually:

- route auth;
- ACLs;
- rate limiting;
- max body size;
- route timeout;
- header manipulation;
- WebSocket;
- TLS edge;
- health checking;
- observability.

Effort: medium/high, but incremental.

## Conclusion

Supporting the edge as an application reverse proxy is a very coherent direction for `tubo`.

The most useful and simple version would be:

```text
https://edge.example.com/<service-name>/...
```

with auto-discovery of services and optional prefix stripping.

This would turn the swarm into a single navigable HTTP endpoint and make `tubo` a:

```text
self-hosted P2P reverse proxy with libp2p service discovery
```

It is a more distinctive feature than simple TCP tunneling, because it builds on the architecture `tubo` already has.
