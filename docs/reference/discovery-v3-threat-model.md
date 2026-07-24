# Discovery V3 threat model and operational impact

This document is the canonical operator/developer note for secret-backed namespace discovery.

See also:
- [`SECURITY.md`](./SECURITY.md)
- [`security-model-0.7.md`](./security-model-0.7.md)
- [`cli.md`](./cli.md)
- [`../runbooks/OPERABILITY.md`](../runbooks/OPERABILITY.md)

## 1. Core model

Discovery V3 is **namespace-scoped discovery** backed by a real secret, not by public identifiers.

A peer can reach the overlay transport only if it can:
- join the same overlay/private swarm;
- dial a bootstrap/relay peer;
- open libp2p streams.

That is **not** enough to discover private namespace services.

Service discovery in a collaborative namespace additionally requires namespace discovery entry state for that exact cluster/namespace.

## 2. What namespace discovery entries do

A namespace discovery entry provides:
- the opaque Discovery V3 topic derivation input;
- the payload protection key derivation input;
- the key identifier used to distinguish current vs previous material.

The public envelope does **not** expose cleartext cluster id, namespace id, service name, addresses, or grant endpoint data.

## 3. What Discovery V3 is not

Discovery V3 does **not** provide:
- perfect hidden participation;
- relay anonymity;
- traffic-flow secrecy;
- immediate per-peer revocation after a key leak;
- global peer discovery through DHT/rendezvous/catalog.

Out of scope:
- public DHT discovery;
- rendezvous/global peer search;
- central authority-hosted catalog visibility.

## 4. Observable metadata that still remains

A peer able to observe PubSub traffic can still learn some metadata even without the namespace secret, for example:
- that an opaque topic exists;
- approximate timing/frequency of announcements;
- message sizes;
- participation spikes or silence windows.

Discovery V3 reduces cleartext service metadata exposure, but it does not make the control plane invisible.

## 5. Public bundle and public default namespace

The signed public bundle must **not** contain namespace discovery entries.

The public default namespace (`tubo-public` / `home/default`) remains:
- `discovery: disabled`
- `connect_policy: invite_only`

That scope is intentionally invite/token driven, not ambient-discovery driven.

## 6. Relay role

Relays remain:
- bootstrap peers;
- circuit relay v2 transport nodes;
- optional remote discovery-query cache/sync helpers;
- optional opaque forwarders for `AnnouncementV3` records they cannot themselves verify (see 6.1).

Relays are **not**:
- namespace discovery authorities;
- holders of namespace discovery entries by default;
- a substitute for cluster authority or membership state.

Running more relays improves transport reachability, not namespace authorization.

### 6.1 Opaque `AnnouncementV3` forwarding

A relay serves discovery for many overlays. It typically does not hold the
authority public key or the namespace discovery entries for every private
cluster whose members reach it.

To let members of a private cluster discover each other through a shared relay
without making the relay authority-aware, relays accept `announce_service_v3`
requests as **opaque** transport payloads when they lack a validation context
for that cluster:

- the relay does **not** verify the announcement signature;
- the relay does **not** decrypt or interpret the payload beyond routing hints
  (peer id, envelope size, TTL);
- the relay caps stored records globally at 256 records / 768 KiB and per
  observed publisher PeerID at 16 records / 128 KiB; each encoded announcement
  is limited to 32 KiB, refreshes of the same `(peer_id, key_id)` are accepted
  at most once per second, and stored TTL is capped at 15 minutes;
- the relay returns opaque records to consumers via `list_services` responses
  in a separate `opaque_announcements_v3` field, never mixed with validated
  services;
- complete encoded `list_services` responses, including JSON/base64 overhead
  and the encoder newline, stay within the 1 MiB client read limit. Validated
  services have priority; opaque records use deterministic peer-round-robin
  ordering. A bounded response that omits records sets `truncated: true`.

The **consumer** is the trust boundary. Before surfacing any opaque record as a
trusted service, the consumer re-verifies the announcement locally against:

- the cluster authority public key it trusts;
- the current (and optionally previous) namespace discovery context;
- the announcement's own signature envelope and payload constraints.

Records that fail local verification are silently dropped by the consumer and
never appear in `tubo get services`. Truncation does not change this trust
boundary and does not make an included opaque record trusted.

When the relay does have a validation context for the announcement's cluster,
the classic strict path is used and opaque forwarding never runs for that
announcement — validation always takes precedence.

Opaque forwarding does not weaken the security model:

- membership capability, publish lease, and service claim signatures are all
  still checked by the consumer;
- a tampering relay can drop or reorder records but cannot forge a service
  that passes local verification;
- the relay never becomes an implicit authority.

Opaque forwarding does not reintroduce the legacy namespace-scoped
`announce_service` (v2) accept path. Namespace-scoped v2 announcements are
still rejected at the relay.

## 7. Rotation model

Namespace discovery rotation uses a managed `current` / `previous` model:
- publishers emit using the **current** entry only;
- subscribers accept **current** and a non-expired **previous** entry during the grace window;
- expired previous state is ignored and can be cleaned up.

This is a compatibility window, not perfect immediate revocation.

If a previous entry was already distributed, peers that still have it may continue to discover publications that are still emitted or accepted during the grace period.

## 8. Compatibility statement

Discovery V3 intentionally replaces the earlier namespace-scoped Discovery V2 model.

For `v0.9.1`:
- Discovery V2 fallback is intentionally broken for discovery-enabled namespace runtime;
- operators should treat Discovery V3 as the required model for collaborative namespace discovery;
- mixed expectations such as “some nodes still publish/observe Discovery V2 in the same namespace runtime” are unsupported.

## 9. Operational consequences

Operators should understand these consequences:
- overlay membership alone does not grant service visibility;
- namespace invites (`share cluster/...` + `join cluster/...`) are now the normal way to distribute discovery state safely;
- secret files are local managed material and must stay on disk with restrictive permissions;
- `get secrets`, `describe secret/...`, and `rotate secret/...` are the intended local management surface;
- public default flows should keep using invite-based access instead of ambient listing.

## 10. Safe mental model

Use this model when reasoning about access:

```text
transport reachability != namespace discovery authorization != service connect authorization
```

More explicitly:
- overlay/swarm/relay access lets a peer try to participate in transport;
- namespace discovery entries let a peer observe collaborative service announcements for that namespace;
- membership capability / cluster invite / service grant state lets a peer connect, publish, or redeem invites according to policy.
