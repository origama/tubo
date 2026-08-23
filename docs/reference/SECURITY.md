# Security model

**Status:** Future security architecture is governed by [`HIGH_LEVEL_DESIGN.md`](HIGH_LEVEL_DESIGN.md), especially universal Access Lease enforcement and Scope-independent connectivity. Detailed token, Scope, protocol, and state behavior below documents preserved prototype reference implementation; retain its threat lessons without treating format/topology as compatibility contract.

## Future core statement

```text
Every remote Pipe open requires a short-lived, cryptographically verifiable Access Lease.
Reachability != discovery != authorization.
Public infrastructure provides reachability, never ambient authorization.
```

Exact Pipe Authority and Scope Authority converge on same exact-Pipe lease. L2 receiving boundary invokes consumer-side/neutral open-authorization contract and fails closed on unsuccessful result; it opens no local target. L3 owns concrete signature/lease validation, issuer trust, revocation, stale-epoch, policy, and refresh implementation. L2 must not import concrete L3 authority/revocation implementations.

## Prototype reference statement

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

Relays without namespace context may store opaque records under fixed capacity and response limits. Consumers remain verification boundary. Relay, cache, query, peer, and authority-2.0 responses never authorize local record deletion; only a complete pinned authority-2.1 snapshot with trusted ordering metadata can do so. Periodic local sync leases only elect one scheduler; they never grant authority. Fenced final writes require an unexpired opaque lease token checked after SQLite writer reservation, so delayed/expired owners cannot overwrite a successor. Cache inventory and scope-sync status expose only holder refs, lease expiry, and stable freshness/error fields; they never expose lease tokens, process identity, membership tokens, keys, service targets, addresses, or private routes. Local authorities do not self-sync or mint membership tokens.

## Public/default behavior

Public sharing is unlisted and invite-only by default. A self-contained invite may carry the exact pipe endpoint and authority route, but not the local service target. One-time means one successful lease/session redemption.

## Revocation

- access leases are short-lived;
- refresh leases are longer and checked by the authority;
- invite JTI, session ID, pipe-access epoch, and pipe-publish state are revocable;
- already-issued offline access remains valid until lease expiry unless pipe checks its local authoritative revocation state before OpenOK; gossip propagation is future work.
- access leases expire after 10 minutes; refresh leases expire after 48 hours and require authority online check. Refresh affects later sessions only. Live tunnel renewal requires future tunnel resume/switch protocol.

Do not claim immediate revocation without such a check.

### Session resume security

Session resume uses a memory-only `resume_secret` (32 random bytes) negotiated during `OpenOK`. It is never persisted, logged, or included in any output. The resume handshake uses HMAC-SHA256 proof bound to:

- domain label (`tubo-resume-v1`)
- `pipe_id`
- `session_id`
- epoch
- client and server peer IDs
- client and server nonces (each 32 random bytes)
- canonical counters and transcript digest

Proofs are verified on every message. A mismatched proof, nonce, or epoch closes the candidate stream. The candidate stream must reach the same authenticated peer; claimed direct/relay labels are verified locally from the libp2p connection state, never trusted from peer-supplied fields.

One candidate carrier per session is enforced. The resume secret never leaves the session's memory.

## Private overlay

A swarm PSK provides transport isolation but is a shared secret, not identity or authorization. Compromise requires rotation. Application grants remain mandatory.

## Parser and resource safety

- bound all outer and nested fields before allocation;
- cap records per publisher and globally on shared relay caches;
- rate-limit same-key refresh and authority requests;
- verify every bounded record before one complete pinned-authority 2.1 snapshot transaction; reserve SQLite writer ownership before mutation-controlling reads, sample record/lease expiry afterward, require nonzero authority-global generation, reject generation/high-water rollback and same-generation/different-digest snapshots, and retain valid cache on malformed, mixed-scope, bad-signature, expired, partial, peer/cache, lease-lost, canceled, or 2.0 responses;
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
