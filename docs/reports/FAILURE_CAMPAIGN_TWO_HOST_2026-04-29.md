# Failure campaign — 2-host distributed bench

Date: 2026-04-29

Bench used:

- edge: `172.236.202.99`
- relay: `172.232.189.160`
- service + dummy origin co-hosted on relay host
- service loopback-bound to force relay-first traffic

## Scenarios tested

### 1) Relay disappears while edge↔service traffic exists

What I did:

- started from a healthy 2-host bench
- verified end-to-end `200`
- killed the relay process
- verified requests fail with `502`
- restarted the relay process
- waited for relay health, edge bootstrap reconnect, service reconnect, heartbeat re-publish

Observed:

- immediate failure while relay is down is expected
- after relay restart, control-plane signals looked healthy again:
  - edge reconnected to relay
  - service reconnected to relay
  - service kept publishing heartbeats
  - `/services` and `/routes` on edge still showed the service
- **but data plane stayed broken for minutes**
- edge kept returning `502`
- edge logs showed these patterns:
  - `dial backoff`
  - `relay_unavailable`
  - `error opening relay circuit: NO_RESERVATION (204)`
  - earlier in the recovery window also malformed-security errors such as:
    - `failed to negotiate security protocol: incoming message was too large`
    - `failed to negotiate security protocol: message did not have trailing newline`

Bug candidate:

- **relay restart can wedge relay-first traffic even after control-plane reconnect**
- likely stale relay reservation / stale circuit state / bad dial-backoff invalidation

Severity: high

---

### 2) Edge restarts after relay disruption

What I did:

- after the relay-restart failure above, restarted edge completely

Observed:

- edge HTTP/admin came back fine
- route was re-learned again after pubsub heartbeat
- but requests still failed with `502`
- edge continued to log:
  - `NO_RESERVATION (204)`
  - relay circuit dial backoff

Bug candidate:

- **edge restart alone does not clear the broken relay/circuit state**
- suggests the real bad state is not just in edge process memory, or the recovery handshake is incomplete

Severity: high

---

### 3) Service process disappears while relay stays up

What I did:

- from a healthy bench, killed the `service` process only
- checked edge admin and request behavior after 5s, 15s, 35s

Observed:

- after **5s**:
  - `/services` still reported `{"count":1}`
  - `/routes` still exposed `myapi`
  - requests returned `502`
- after **15s**:
  - still `count=1`
  - route still present
  - requests still `502`
- after **35s**:
  - `/services` became `{"count":0}`
  - `/routes` became `[]`
  - requests switched from `502` to `404 no route`

Interpretation:

- route/service removal is TTL-driven, not liveness-driven
- there is a stale-failure window of roughly 30s where the edge still advertises/routs to a dead service

Bug / design-gap candidate:

- **dead services remain routable for too long after process loss**
- probably acceptable as current TTL behavior, but poor UX / operability
- should likely move toward faster failure marking or separate health/liveness semantics

Severity: medium

---

### 4) Service disappears and then reappears

What I did:

- from the state above, restarted the same `service` process with same config/peer identity
- watched for route reappearance and real request recovery

Observed:

- service process started cleanly
- service connected to relay quickly
- service resumed heartbeat publication immediately
- edge re-learned the route
- **but usable traffic did not recover for ~2 minutes**
- only much later did the first inbound relayed stream appear on service and requests started working again

Bug candidate:

- **service restart recovery is far too slow even when relay is healthy**
- likely stale dial-backoff / stale reservation / delayed circuit re-establishment on edge or relay

Severity: high

---

### 5) Origin disappears while service process stays up

What I did:

- killed only the dummy origin process
- kept `service` running
- retried after restarting origin

Observed:

- edge returned fast `502`
- error body was explicit and useful:
  - `server error (code 502): upstream failed: ... connect: connection refused`
- after origin restart, traffic recovered

Conclusion:

- this behavior is mostly correct
- not a core bug; good baseline/control scenario

Severity: low / expected

---

### 6) Kill relay during a burst of already-established large requests

What I did:

- launched 8 parallel requests with `512 KiB` payloads
- killed relay ~1s after the burst started

Observed:

- all 8 requests completed `200`

Interpretation:

- once relayed streams are already established, the relay disappearing slightly later does not necessarily kill in-flight requests immediately
- this is a positive resilience signal
- this run did **not** prove behavior for earlier/more aggressive mid-flight relay loss timing

Severity: none; useful resilience datapoint

---

## Extra issue found in the test harness

While running the campaign, the helper scripts exposed a separate automation problem:

- remote `*.pid` files can become stale after ad-hoc restarts
- wrapper `bash -c ... nohup ... & echo $! > pid` processes can remain around
- later `kill $(cat pid)` may target the wrong/no-longer-live process
- this made repeated automated failure injection flaky

This is mostly a bench/harness bug, not necessarily a product/runtime bug.

The current bench scripts now clean up by both pidfile and command-line match, and they wait for listeners to drain before relaunching a restarted process so repeated restart injection stays deterministic.

Severity: low

---

## Bugs to schedule

### B1 — Relay restart wedges relay-first traffic

Symptoms:

- edge and service reconnect to relay
- discovery/routes look healthy
- requests still fail indefinitely with `dial backoff` / `NO_RESERVATION`

Good issue title:

- `fix(relay): recover relay-first traffic after relay process restart`

### B2 — Edge restart does not clear broken relay/circuit state

Symptoms:

- restarting edge after relay flap still leaves traffic broken

Good issue title:

- `fix(edge): clear stale relay circuit/backoff state after relay disruption`

### B3 — Service restart recovery is extremely slow

Symptoms:

- service reconnects and republishes quickly
- real traffic resumes only after ~2 minutes

Good issue title:

- `fix(service/edge): reduce post-restart recovery time for relayed service to seconds`

### B4 — Dead service remains advertised/routable for ~30s

Symptoms:

- service process gone
- edge still shows service/route for TTL window
- client sees repeated `502` instead of faster withdrawal/failover behavior

Good issue title:

- `improve(discovery): shorten stale-route window after service disappearance`

### B5 — Suspicious malformed-security handshake errors after relay/service restarts

Symptoms:

- `incoming message was too large`
- `message did not have trailing newline`
- `connection reset by peer`

Good issue title:

- `debug(p2p): investigate malformed security handshake errors after peer restarts`

### B6 — Bench pid management is unreliable after manual restart injection

Good issue title:

- `testbench: make distributed failure harness use robust pid/process management`

## Suggested priority

1. B1
2. B3
3. B2
4. B5
5. B4
6. B6
