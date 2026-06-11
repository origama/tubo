# Member Publish Grants Runbook

This runbook covers the end-to-end steps required to allow **non-authority peers**
(members) to publish services in a private cluster/namespace using the Publish
Grant workflow.

## When to use this runbook

Use this runbook when **all** of the following are true:

- you have a private cluster (your own authority key, not the public `home/default`);
- at least one service must be published from a machine that does **not** hold
  the authority private key;
- you want the member to run `tubo attach` and get a valid `PublishLease` approved
  by the authority.

If the attaching node holds the authority private key, skip this runbook — it
mints publish leases locally without any grant protocol round-trip.

## Prerequisites

| What | Where |
|---|---|
| Cluster already created | authority node, via `tubo create cluster/<name>` |
| Namespace already created | authority node, via `tubo create namespace/<name>` |
| Member joined the cluster | member node, via `tubo join cluster/<name> --token <invite>` |
| Relay reachable by both nodes | public host, `tubo relay -d` |
| Both nodes configured with relay peers | `network.relay_peers` in config |

---

## Overview

```text
                        relay.example.com
                         (circuit relay)
                         /             \
            [authority node]        [member node]
            tubo grants serve       tubo attach myservice --port 8080
                   |                       |
                   |<--- Submit grant ------+
                   |---- TypePending ------>|  (saved locally)
              [approve]
                   |---- TypeApproved ----->|  (next attach / poll)
                                            |
                                      PublishLease saved
                                      tubo attach running
```

1. The authority starts `tubo grants serve` — this makes the Grant Service
   reachable and, when namespace discovery is enabled, publishes a discoverable
   `grant-service` system record so members can find it automatically.
2. The member runs `tubo attach` — it discovers the grant endpoint, submits a
   `PublishLease` request, and waits for approval.
3. The authority approves the request — the member's next `tubo attach` (or an
   explicit `tubo grants request --poll`) retrieves the signed lease and starts
   publishing.

---

## Step 1 — Start the Grant Service on the authority node

Run this on the **authority node** (the machine that holds `authority_private_key_file`):

```bash
tubo grants serve --cluster <cluster-name> --namespace <namespace-name>
```

To run it in the background:

```bash
tubo grants serve --cluster <cluster-name> --namespace <namespace-name> -d
tubo logs process/grants-serve-<cluster-name>-<namespace-name>
```

Expected output:

```
grant service listening peer=12D3KooW... protocol=/tubo/grants/1.0
relay addr: /dns4/relay.example.com/tcp/4001/p2p/<relay-peer>/p2p-circuit/p2p/12D3KooW...
grant service discovery announced peer=<relay-peer> service=service-...
```

**What this does:**

- opens the `/tubo/grants/1.0` protocol listener;
- maintains a relay reservation so members behind NAT can reach it;
- when the namespace has `discovery: enabled`, publishes a `grant-service`
  system record via gossipsub and announces it to relay discovery caches — this
  means `tubo attach` on members can find the endpoint automatically via
  `tubo get services --system`.

### Verify discoverability from the member

Once `grants serve` is running, verify from the member:

```bash
tubo get services --system --cluster <cluster-name> --namespace <namespace-name> \
  --timeout 10s --json
```

Expected: a `grant-service` record with `kind: grant-service`, matching
`cluster_id` and `namespace_id`, and at least one relay-circuit peer in
`grant_service.peers`.

If the record is missing, check:

- Is `discovery: enabled` in the namespace config on the authority?
- Did `grants serve` log `"grant service discovery announced"`?
- Did `grants serve` log `"grant service discovery publication disabled"` or an
  error about missing discovery runtime? If so, see
  [Troubleshooting](#troubleshooting) below.

---

## Step 2 — Run `attach` from the member node

On the **member node**, run attach as normal:

```bash
tubo attach <service-name> --port <port>
```

On first run (no existing `PublishLease`), `attach` will:

1. discover the grant service peer via `tubo get services --system`;
2. connect to it through the relay circuit;
3. submit a `PublishLease` grant request;
4. receive `TypePending` and save the `grant_request_id` locally;
5. exit with:

```
tubo: publish grant request "gr_..." is pending; publication requires an approved publish lease
```

This is expected — the request is queued on the authority and must be approved
before the member can publish.

---

## Step 3 — Approve the grant request on the authority node

On the **authority node**, list pending requests:

```bash
tubo grants pending
```

Default output is now a compact action-oriented list that groups repeated attempts by requester/service identity and shows the local alias when available. Use `--wide` for the full technical table:

```bash
tubo grants pending --wide
```

History uses compact sections too; `tubo grants history --all` shows older expired groups and `--wide` shows the raw table.

Inspect the request if needed:

```bash
tubo grants describe gr_a0f4b0d61b77d0f1
```

`describe` now prints a readable review card with requester, service, verification hints, and approve/deny suggestions.

Approve with a TTL:

```bash
tubo grants approve gr_a0f4b0d61b77d0f1 --ttl 168h   # 7 days
```

To deny:

```bash
tubo grants deny gr_a0f4b0d61b77d0f1
```

---

## Step 4 — Re-run `attach` on the member node

After approval, run `attach` again on the member:

```bash
tubo attach <service-name> --port <port>
```

This time:

1. `attach` polls the grant service with the saved `grant_request_id`;
2. receives `TypeApproved` with the signed `PublishLease`;
3. saves the lease locally;
4. starts publishing the service to the swarm.

Expected output:

```
publish authorization refreshed for service "<service-name>"
attached service "<service-name>"
service id: service-...
scope: public/<cluster-name>/<namespace-name>
```

From this point forward the saved `PublishLease` is reused on every `attach`
until it expires (after the TTL approved in step 3).

---

## Lease renewal

When the `PublishLease` expires, `attach` automatically submits a new request to
the grant service and the flow repeats from step 2. The saved `grant_request_id`
is reused for polling if the request is already pending.

To renew manually before expiry:

```bash
tubo grants request service/<service-name> \
  --cluster <cluster-name> \
  --namespace <namespace-name>
```

---

## Service name constraints

Service names sent to the Grant Service must match:

```
^[a-z0-9][a-z0-9@._-]{0,62}$
```

Valid examples: `myapi`, `piwebui@oripi`, `svc-1.prod`, `backend@node2`.  
Invalid examples: `MyAPI` (uppercase), `my service` (space), `_internal` (leading
underscore).

Names containing `@` (host-qualified names like `piwebui@oripi`) are valid and
recommended when multiple nodes publish the same logical service under different
hostnames.

---

## Troubleshooting

### `attach` fails with `missing grant service peer`

The member could not discover or reach the Grant Service. Debug steps:

```bash
# 1. Check that the grant-service record is visible from the member
tubo get services --system \
  --cluster <cluster-name> --namespace <namespace-name> \
  --timeout 10s --json

# 2. If the record is missing, check grants serve logs on the authority
#    Look for:
#      "grant service discovery announced peer=..."   ← OK
#      "grant service discovery publication disabled" ← discovery: disabled on namespace
#      "grant service discovery publication requires a valid discovery runtime" ← missing discovery secret

# 3. If the record is present but attach still fails, try an explicit peer:
tubo grants request service/<service-name> \
  --cluster <cluster-name> \
  --namespace <namespace-name> \
  --peer '<relay-circuit-multiaddr-of-grant-service>'
```

### `grants serve` logs `"grant service discovery publication disabled"`

The namespace has `discovery: disabled`. The Grant Service is reachable but not
discoverable via `get services --system`. Members must have the grant service peer
pre-configured in their cluster membership grant metadata or config.

Options:

1. Enable discovery on the namespace (`discovery: enabled`) — recommended for
   collaborative clusters.
2. Distribute the grant service peer address to members explicitly (embed it in
   the cluster invite via `tubo share cluster/<name>`).

### `grants serve` fails with `"grant service discovery publication requires a valid discovery runtime"`

The namespace is discovery-enabled but is missing the `discovery_secret_current`
entry. Fix:

```bash
# On the authority node, check the namespace has a discovery secret
tubo describe namespace/<namespace-name>

# If missing, rotate to generate one
tubo rotate secret namespace/<namespace-name>
```

### Member gets `invalid service name "..."`

The service name contains characters not allowed by the grant protocol. Rename
the service to match `^[a-z0-9][a-z0-9@._-]{0,62}$`.

### Grant request never appears in `tubo grants pending`

The Submit never completed. Common causes:

- relay reservation dropped between the client connecting and the Submit
  completing — the client retries automatically on the next `attach` run once it
  can hold a stable relay reservation;
- wrong relay/bootstrap peer in member config — verify `network.relay_peers`
  points to the same relay the authority uses;
- the grant service peer address in the discovered record uses an unreachable
  direct address instead of a relay circuit — verify the authority's
  `network.relay_peers` are set so `grants serve` obtains a relay reservation
  before publishing the discovery record.

### `attach` fails with `publish grant request "gr_..." is pending`

Normal: the request was submitted successfully but not yet approved. Run
`tubo grants pending` on the authority and approve it (step 3), then re-run
`attach` on the member.

---

## Security notes

- The authority never signs a `PublishLease` without explicit `tubo grants approve`.
  Use `--public-auto-approve` **only** on fully public clusters (`home/default`)
  where any peer may publish without review.
- Approved `PublishLease` TTLs are bounded by `grants serve --claim-ttl` (default
  24 h). Approve with a TTL that matches your rotation policy.
- The grant service peer address exposed in discovery only allows reaching the
  grants protocol listener; it does not grant network access to the authority
  node beyond `/tubo/grants/1.0` streams.
- Revoke a member's publish right at any time:

```bash
tubo revoke publish <service-id> --cluster <cluster-name> --namespace <namespace-name>
```

The revocation takes effect on the authority's grant server immediately; the
member's existing running `attach` process will fail to renew its lease on the
next heartbeat cycle.
