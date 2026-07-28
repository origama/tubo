# Operability

Status: target contract. Commands beyond `help` and `version` are not implemented in the bootstrap repository.

## Runtime topology

Recommended private three-node topology:

```text
public relay          authority + origin          consumer
transport only        scope + service + pipe      local pipe
     \                         |                      /
      \________________ private overlay ___________/
```

The relay is never authority. The origin service stays loopback-local; its published pipe obtains a relay reservation and advertises a reachable pipe endpoint.

## Expected workflow

Relay:

```bash
tubo relay --listen /ip4/0.0.0.0/tcp/4001 --public-addr /ip4/203.0.113.1/tcp/4001 -d
```

Authority/origin:

```bash
tubo join overlay --relay <relay-multiaddr> --swarm-key ./swarm.key
tubo scope create solar/default
tubo authority start solar --scope solar/default -d
tubo service create lmstudio --target http://127.0.0.1:1234
tubo pipe create lmstudio-team --ingress tubo --target service/lmstudio \
  --scope solar/default --visibility discoverable --access members
tubo start pipe/lmstudio-team -d
tubo scope invite solar/default --role member --json
```

Consumer:

```bash
tubo join overlay --relay <relay-multiaddr> --swarm-key ./swarm.key
tubo scope join --token <membership-invite>
tubo get pipes
tubo connect lmstudio --local 127.0.0.1:51234 -d
curl http://127.0.0.1:51234/
```

## Health

Each infrastructure or pipe run exposes a local-only control endpoint with:

```text
/healthz    readiness/liveness summary
/statusz    domain/runtime/auth status
/statsz     bounded counters
/p2pz       peer, relay, reachability diagnostics
/sessionsz  active session summaries, redacted by default
```

TCP local ingress must not consume an adjacent implicit port for health. Control endpoints use Unix sockets where supported or an explicitly allocated independent address.

## Path diagnostics

Operational output keeps separate:

- logical pipe chain;
- authorization state and next renewal;
- direct/relayed transport route per active session;
- historical failures versus current health.

A recovered session/pipe clears active degradation while retaining historical diagnostics.

## Supervisors

Foreground remains canonical. `-d` is lightweight local background execution. OS supervision is optional and generated explicitly; Tubo does not install privileged units by default.

## Failure handling

- relay unavailable: existing direct sessions continue; new NAT/NAT sessions may fail with `relay_unavailable`;
- authority unavailable: existing access remains TTL-bounded; issuance/refresh fails clearly;
- discovery unavailable: exact self-contained invite may still connect; collaborative name lookup reports discovery failure;
- service target unavailable: pipe remains running but rejects sessions with `target_unreachable` and reports degraded target health;
- state store unavailable: mutation and credential refresh fail closed.

## Logs and output

Technical logs include IDs and reason classes but never raw tokens, keys, service target credentials, or request bodies. Ordinary successful CLI output remains concise.
