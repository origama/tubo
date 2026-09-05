# Three-machine durable exact-Pipe renewal — operand report (2026-09-05)

**Scope:** operator deployment test of the durable remote-Pipe binding on the
fixed three-machine topology (`macb`, `oripi`, `tuborelay`). Proves the
Journey-A exact connection continues to work after the initial
short-lived AccessLease expires: the accepting side reacquires fresh
short-lived AccessLeases through the accepted binding (post-acceptance
renewal), so connectivity does **not** die after the first ten minutes.

Normative reference for the mechanism: `docs/reference/PROTOCOL.md`
(`/tubo/exact-renewal/1`), `docs/reference/STATE.md` (schema 13/14:
`accepted_exact_relationships`, `invite_redemptions.kind`,
`remote_pipe_bindings`, `exact_lease_cache`), `docs/implementation/ROADMAP.md`.

## Version under test

```text
repo:      origama/tubo-rebuilt
branch:    a0-durable-remote-pipe-binding  (PR #74)
commit:    4d4a38e  fix(revoke): restore scoped share.invite revocation ...
binary:    tubo 0.1.0-dev (protocol 2.1)  — schema 14
lease TTL: default MaxTTL = 10 minutes (TUBO_EXACT_LEASE_TTL unset)
```

## Environment cleanup (old versions removed)

- `tuborelay` (Linux x86_64): stopped and disabled `tubo-relay` and
  `tubo-grants` systemd units; killed stale `tubo` processes; removed
  `/usr/local/bin/tubo` and old state (`/root/.config/tubo`,
  `/root/.local/share/tubo`); installed the current Linux build at
  `/usr/local/bin/tubo`.
- `oripi` (Linux x86_64): killed stale `tubo`; removed old binary and old
  state; installed the current Linux build (plus the test HTTP fixture
  binary at `/usr/local/bin/tubo-httpfixture`).
- `macb` (Darwin arm64): killed stale `tubo`; removed `~/.local/bin/tubo`
  and old state dirs; installed the current Darwin build at
  `~/.local/bin/tubo`.

## Topology and run state

```text
tuborelay   172.232.189.160  public Circuit Relay v2  tcp/4001
oripi       172.236.202.99   owner/publisher + Exact Pipe Authority (TUBO_HOME=/root/tubo3m/origin)
macb        local            recipient/client (TUBO_HOME=/tmp/tubo3m/recv)
```

Journey A exact only: no Scope, no membership, no discovery, no bridge/grants.

```text
relay peer: 12D3KooWFatgBKa6MT3MBSZEBtuRfYoCqBPkqcPrRxkJQkMzxQVG
remote pipe: pip_azrh6kcaqkbrlgtbtetyj3y7df42kr7u   (source=invite binding)
recipient pipe: pipe/west  local ingress 127.0.0.1:19999
```

## Procedure

1. generate a fresh swarm key; deploy to all three hosts;
2. start the relay on `tuborelay` (listen 0.0.0.0:4001,
   public 172.232.189.160:4001);
3. `oripi`: join overlay; run HTTP fixture on 127.0.0.1:18787
   (`instance=tubo3m-origin`); `attach http://127.0.0.1:18787 --name origin -d`;
   `share pipe/origin` → one-time invite;
4. `macb`: join overlay; `accept --token <invite>` (durable binding persisted,
   initial 10-minute AccessLease); `connect --remote <pipe-id> --local
   127.0.0.1:19999 --name west -d` (NO further invite);
5. verify traffic, then let the real 10-minute lease expire and keep probing.

## Results

### Baseline (t0)

```text
13:58:13Z accept --token        → durable binding, initial lease expires 14:08:13Z
13:58:17Z connect --remote      → pipe/west ready, local listener 127.0.0.1:19999
13:58:56Z probe baseline        → OK  (instance=tubo3m-origin, body echoed)
```

### C1 — after the FIRST lease expiry (~14:09–14:10Z)

```
initial lease expiry 14:08:13Z
probe1=C2 ~14:09:50Z  OK
probe2              OK
probe3              OK
exact_lease_cache at 14:11:56Z: expires 14:19:53Z   (fresh 10-min lease, issued ~14:09:53Z)
```

The 14:08:13Z lease was already expired; the receiving fall‑closed cache
(`exact_lease_cache` returns `ErrNotFound` once expired) forced a
post-acceptance renewal. Each `probe=N OK` opens a brand-new tunnel using a
lease the L2 authorizer verified as unexpired — impossible without a fresh
lease.

### C2 — after the SECOND lease expiry (~14:19–14:20Z)

```text
renewed lease expiry 14:19:53Z
probe1=OK  (14:19:17Z, cached lease still valid)
probe2=OK  (14:19:37Z, cached lease still valid)
probe3=OK  (14:19:57Z, cached lease expired → renewal)
14:20:06Z info exact lease renewed pipe=pip_azrh6kcaqkbrlgtbtetyj3y7df42kr7u jti=jti_5JOJ3… expires=2026-09-05T14:30:06Z
exact_lease_cache at 14:21:16Z: expires 14:30:06Z
```

A second, explicit renewal exchange is observed in the runtime log with a
fresh lease at the exact moment the second lease expired. The lease-cache
fencing delta is the DB-level proof:

```text
initial lease  → 14:08:13Z
renewal #1     → 14:19:53Z   (issued ~14:09:53Z)
renewal #2     → 14:30:06Z   (issued 14:20:06Z)
```

### Owner / authority side (oripi)

```text
schema                           14
invite_redemptions               1 row     (one-time bootstrap only — no re-invite)
accepted_exact_relationships    1 row     (pipe, backing invite, epoch 0)
revocations                      0
```

### Continuous connectivity

Probes succeeded every time they were attempted from t0 (13:58) through t0+22
minutes (14:20), across two full lease expirations:

```text
07/07 probes OK (baseline, 3 at C1, 3 at C2)
0 probe failures
```

## Interpretation

- `Invite != AccessLease`, `AcceptedBinding != AccessLease`,
  `AcceptedBinding != local Pipe`, `AcceptedBinding does not define an
  ingress` — all hold by construction and are exercised here.
- The one-time invite was consumed exactly once (`invite_redemptions` = 1).
- Connectivity after the initial ten minutes depends on the durable binding
  and the `/tubo/exact-renewal/1` reissuance path, which re-issued fresh
  10-minute leases twice (14:09:53Z, 14:20:06Z), each deterministically
  replacing the expired lease in the ephemeral cache with a fresh one.
- The remote open in every probe still carried a valid short-lived
  AccessLease to the L2 receiving boundary (unchanged security invariant) —
  renewal never relaxed the lease lifetime.

## Evidence artifacts (private, not committed)

```text
/tmp/tubo3m/wait-renew.out        probe-by-probe transcript
/tmp/tubo3m/recv/logs/pipe-west.log   renewal/session log (truncated in citations above)
/tmp/tubo3m/recv/state.db             recipient state (schema, binding, lease cache)
/root/tubo3m/origin/state.db          owner state (redemptions, relationships)
/tmp/tubo3m/swarm.key                 private swarm key (never committed)
```

Raw invite value, swarm key bytes, and full lease payloads are intentionally
not reproduced here.

## Repeating the scenario

In-repo deterministic equivalent (isolated, shortened TTL):
`make integration` → `tests/integration/journey-a-durable.sh`
(also covers consumer restart, owner restart, revoke ->
re-authorize at a new epoch, and multi-invite determinism).

Full 3-machine op command reference (see
`docs/runbooks/THREE_MACHINE_E2E.md` “Durable exact-renewal campaign”):
install the current binary on all three hosts, run the relay, attach+share on
the owner, `accept --token` then `connect --remote <pipe-id>` on the client,
and let two lease boundaries elapse while probing the local listener.

## Cleanup (this operator run; the new binary remains installed)

```text
macb:      TUBO_HOME=/tmp/tubo3m/recv  <tubo> stop pipe/west
oripi:     pkill -f tubo-httpfixture; TUBO_HOME=/root/tubo3m/origin <tubo> stop pipe/origin
tuborelay: kill <relay-pid>; rm -rf /root/tubo3m
```

(Restoring the previous product would reinstall its binaries and re-enable
its systemd units — deliberately out of scope for this campaign.)