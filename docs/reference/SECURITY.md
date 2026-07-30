# Security model

## Core statement

```text
Transport reachability != discovery visibility != connect authorization
```

Possessing an overlay PSK or reaching a relay does not grant namespace visibility or pipe access.

## Trust roots

A context pins:

- overlay/bundle trust when applicable;
- cluster authority public key;
- namespace discovery key material for collaborative discovery.

A token may refer to an already trusted public authority. It may introduce a private authority only through an explicit scope-join/TOFU operation. A public token cannot define a new public trust root.

## Pipe identity

`pipe_id` is derived from or cryptographically bound to the pipe-owner public key. The owner signs the current record that binds the stable pipe to a runtime PeerID and addresses.

Display names are non-unique and never authorize. Invites and leases resolve exact `pipe_id`.

Local services are not identities on the network. Their targets are private and must not appear in records or remote diagnostics.

## Authority

One logical authority per scope issues membership, publication, forwarding, share, access, and refresh grants. Multi-active independent issuer stores are unsupported.

Connect issuance is authority-owned rather than delegated to pipe runtimes. This avoids mixed issuer classes and ambiguous refresh routing.

## Hop authorization

Every published pipe validates an access lease and proof of possession before opening its target. A forwarding pipe must separately hold `pipe.forward` and access authority for its next pipe.

By default, the next pipe sees the forwarding pipe as caller. Claims about the original user are descriptive unless a future signed delegation protocol validates them.

## Discovery privacy

Collaborative discovery uses a random namespace secret for topic derivation and payload encryption. Publishers emit only on the current key; consumers may accept a non-expired previous key during rotation grace.

Relays may observe timing, size, peer connections, and opaque topic activity. Discovery does not provide anonymity or traffic-flow secrecy.

Relays without namespace context may store opaque records under fixed capacity and response limits. Consumers remain verification boundary. Relay, cache, query, peer, and authority-2.0 responses never authorize local record deletion; only a complete pinned authority-2.1 snapshot with trusted ordering metadata can do so. Periodic local sync leases only elect one scheduler; they never grant authority. Fenced final writes require an unexpired opaque lease token, so delayed/expired owners cannot overwrite a successor. Cache inventory and scope-sync status expose only holder refs, lease expiry, and stable freshness/error fields; they never expose lease tokens, process identity, membership tokens, keys, service targets, addresses, or private routes. Local authorities do not self-sync or mint membership tokens.

## Public/default behavior

Public sharing is unlisted and invite-only by default. A self-contained invite may carry the exact pipe endpoint and authority route, but not the local service target. One-time means one successful lease/session redemption.

## Revocation

- access leases are short-lived;
- refresh leases are longer and checked by the authority;
- invite JTI, session ID, pipe-access epoch, and pipe-publish state are revocable;
- already-issued offline access remains valid until lease expiry unless pipe checks its local authoritative revocation state before OpenOK; gossip propagation is future work.
- access leases expire after 10 minutes; refresh leases expire after 48 hours and require authority online check. Refresh affects later sessions only. Live tunnel renewal requires future tunnel resume/switch protocol.

Do not claim immediate revocation without such a check.

## Private overlay

A swarm PSK provides transport isolation but is a shared secret, not identity or authorization. Compromise requires rotation. Application grants remain mandatory.

## Parser and resource safety

- bound all outer and nested fields before allocation;
- cap records per publisher and globally on shared relay caches;
- rate-limit same-key refresh and authority requests;
- verify every bounded record before one complete pinned-authority 2.1 snapshot transaction; require nonzero authority-global generation, reject generation/high-water rollback and same-generation/different-digest snapshots, and retain valid cache on malformed, mixed-scope, bad-signature, expired, partial, peer/cache, or 2.0 responses;
- enforce setup deadlines before target dial;
- do not dial a service target before authorization succeeds;
- fuzz tunnel, record, authority, token, and state decoders;
- reject malformed trailing payload and unknown security-critical operations.

## Secret handling

Never print or commit:

```text
swarm PSKs
node private keys or deterministic seeds
pipe-owner private keys
cluster authority private keys
namespace discovery secrets
membership/share/access/refresh tokens
state databases or revocation stores
```

Ordinary inspect/config output prints references and fingerprints only.

The literal PSK header `/key/swarm/psk/1.0.0/` may appear in implementation/tests only when followed by generated test material outside Git.
