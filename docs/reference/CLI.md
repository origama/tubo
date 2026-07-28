# CLI contract

## Output

```text
stdout = primary command result
stderr = progress, warning, hint, diagnostics
```

`--json` prints exactly one parseable JSON value to stdout. Detached children and implicit authorization/discovery work must not contaminate it.

## Ergonomic commands

```bash
tubo attach http://127.0.0.1:1234 --name lmstudio -d
tubo attach tcp://127.0.0.1:8443 --name tlsdemo -d
tubo connect lmstudio --local 127.0.0.1:51234 -d
tubo connect --token <share-invite> --local 127.0.0.1:51234 -d
```

Semantics:

```text
attach  = create/reuse service + create/reuse/start published pipe
connect = resolve exact pipe_id + create/reuse/start local pipe
```

Both use `create-if-missing, reuse-if-compatible, fail-if-conflicting`.

## Explicit resources

```bash
tubo service create lmstudio --target http://127.0.0.1:1234
tubo get services                   # local services only
tubo describe service/lmstudio
tubo rm service/lmstudio

tubo pipe create lmstudio-team \
  --ingress tubo \
  --target service/lmstudio \
  --scope solar/default \
  --visibility discoverable \
  --access members

tubo pipe create lmstudio-local \
  --ingress local://127.0.0.1:51234 \
  --target pipe/pip_...

tubo get pipes                      # local definitions + verified network records
tubo describe pipe/lmstudio-team
tubo inspect pipe/lmstudio-team --json
tubo start|restart|stop|rm pipe/lmstudio-team
```

A service may not be started. Only a pipe has a connectivity runtime.

## Overlay and scope

```bash
tubo join overlay --relay <multiaddr> --swarm-key <file> [--force-reachability private|public]
tubo scope create solar/default
tubo authority start solar --scope solar/default -d
tubo scope invite solar/default --role member --expires 24h --json
tubo scope invite solar/default --role viewer --expires 24h --json
tubo scope join --token <membership-invite>
tubo scope use solar/default
tubo scope rotate-discovery-key solar/default --grace 24h
```

Roles:

- member: discover and connect; publish only if policy grants it;
- viewer: discover/list but cannot connect;
- publisher: may request/receive pipe publication authority;
- forwarder: may re-export a pipe when explicitly granted.

## Sharing and revocation

```bash
tubo share pipe/lmstudio-team --expires 1h --one-time --json
tubo revoke invite <jti>
tubo revoke session <session-id>
tubo revoke pipe-access <pipe-id>
tubo revoke pipe-publish <pipe-id>
```

Invites target exact `pipe_id`; display name is only a hint. One-time means one successful session/lease redemption, not one HTTP request.

## Runtime views

```bash
tubo ps
tubo top
tubo logs pipe/lmstudio-team
tubo inspect pipe/lmstudio-team --json
tubo inspect session/ses_... --json
```

`ps` displays primary resources and capabilities, never `attach` or `connect` as kinds.

Suggested compact columns:

```text
NAME  KIND  INGRESS  TARGET  CAPS  STATUS  AUTH  SESSIONS  PATH
```

A pipe's path is an aggregate. Per-session direct/relay data belongs in session inspection.

## Discovery

```bash
tubo get pipes
tubo get pipes --live
tubo get pipe/<pipe-id>
tubo watch pipes
```

`get services` is local-only. Remote connectivity is represented by verified pipes.

## Infrastructure

```bash
tubo relay --listen /ip4/0.0.0.0/tcp/4001 --public-addr <addr> -d
tubo gateway --listen :8443 -d
```

Relay and authority remain primary infrastructure resources in `ps`; they are not pipes.

## Common JSON requirements

Mutation output includes:

```json
{"ref":"pipe/lmstudio-team","id":"pip_...","status":"running"}
```

Pipe inspection distinguishes:

```json
{
  "logical_path": [],
  "authorization_path": [],
  "transport_summary": {},
  "sessions": []
}
```

Errors have stable classes, for example:

```text
invalid_input
not_found
conflict
authorization_denied
authority_unreachable
discovery_unreachable
pipe_unreachable
relay_unavailable
persistence_failed
readiness_failed
```
