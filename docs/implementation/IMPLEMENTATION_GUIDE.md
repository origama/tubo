# Implementation guide

This document describes dependency order, not implementation status. Create focused GitHub Issues before executing non-trivial slices.

## Walking skeleton

The first vertical slice should prove, on one machine:

```text
local TCP ingress -> local pipe -> local service target
```

It establishes store transactions, pipe planning, runtime readiness, sessions, half-close, stats, and lifecycle without libp2p.

The second slice replaces the middle edge with two in-process libp2p hosts:

```text
local ingress -> client pipe -> tunnel/2.0 -> origin pipe -> service
```

The third slice adds the real relay machine and authority.

## Recommended dependency order

1. domain and validation;
2. static config and transactional state spike;
3. pipe engine local adapters and sessions;
4. node kernel and host identity;
5. tunnel 2.0 bounded handshake and proof;
6. pipe owner identity and authority grants;
7. signed pipe records and local cache;
8. namespace-secret GossipSub plus query;
9. relay, reservations, NAT path selection;
10. CLI use cases and detached lifecycle;
11. three-machine private HTTP gate;
12. TCP/TLS, viewer, invite, revocation, recovery gates;
13. gateway and optional pipe re-export.

## Slice definition of done

A slice includes:

- issue acceptance criteria;
- phase advancement answer (five-question critical-path test: exact exit criteria advanced, critical-issue dependency, disable/remove/defer impossibility, required-primitive vs prototype-parity, deferral-wrong-or-insecure) and any deferred-debt record;
- domain/API changes;
- positive and negative tests;
- race/concurrency evidence where relevant;
- CLI/output updates if exposed;
- canonical docs and runbook updates;
- changelog/version decision;
- local verification result;
- applicable three-machine evidence.

## Reuse policy

Previous Tubo code may be reused only when:

1. its behavior matches a rebuilt invariant;
2. it has no dependency on old service-as-network-resource semantics;
3. it does not bring compatibility branches or legacy protocol IDs;
4. it can be covered by rewritten domain-level tests;
5. the resulting code is simpler than a fresh implementation.

Good candidates for behavioral reuse:

- bounded PSK loading;
- libp2p host setup techniques;
- relay reservation expiry logic;
- raw byte copying/half-close tests;
- test HTTP/TCP servers;
- process identity safeguards;
- atomic readiness rollback patterns.

Poor candidates:

- old CLI dispatch;
- role-wide config struct and merge;
- service/bridge app packages;
- materialized service catalog;
- delegated connect lease rollover;
- discovery V1/V2/V3 compatibility code;
- tunnel 1.1 framing.

## Early spikes requiring explicit decisions

- pure-Go SQLite driver cross-build/race/lock behavior;
- wrapping libp2p streams as `net.Conn` with correct deadlines/half-close;
- standard HTTP transport/reverse proxy behavior over one stream per connection;
- stable pipe ID versus rotating runtime PeerID;
- namespace record encryption envelope and rotation;
- relay opaque cache quotas and pagination;
- process control socket portability.
