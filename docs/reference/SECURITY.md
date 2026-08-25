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
- access leases expire after 10 minutes; refresh leases expire after 48 hours and require authority online check. Refresh affects later sessions only. There is no live in-session renewal on the A0 path: on carrier/authorization failure the session terminates and a new authorized connection is required.

Do not claim immediate revocation without such a check.

### Session resume security

Removed from the A0 path (issue #39). The session-resume machinery (`/tubo/tunnel-resume/2.1`, `session-resume-v1`, `ResumeOffer`, memory-only `resume_secret`) was a prototype surface with documented defects and a live attack surface; the fail-closed quarantine removes it from the A0 data plane. No production code generates, persists, logs, or negotiates a resume secret or proof on the A0 path; an unsolicited resume stream has no handler and is reset. Session-resume/carrier-replacement correctness is deferred post-A0 debt (#32).

## Private overlay

A swarm PSK provides transport isolation but is a shared secret, not identity or authorization. Compromise requires rotation. Application grants remain mandatory.

## Parser and resource safety

- bound all outer and nested fields before allocation;
- cap records per publisher and globally on shared relay caches;
- rate-limit same-key refresh and authority requests;
- verify every bounded record before one complete pinned-authority 2.1 snapshot transaction; reserve SQLite writer ownership before mutation-controlling reads, sample record/lease expiry afterward, require nonzero authority-global generation, reject generation/high-water rollback and same-generation/different-digest snapshots, and retain valid cache on malformed, mixed-scope, bad-signature, expired, partial, peer/cache, lease-lost, canceled, or 2.0 responses;
- enforce setup deadlines before target dial;
- do not dial a service target before authorization succeeds (issue #48: receiving L2 serve invokes the injected neutral OpenAuthorizer and opens the local target only after a result whose PipeID/Subject/non-zero future ExpiresAt independent binding succeeds; any denial, mismatched result, or zero/expired expiry yields zero target dial/session/bytes);
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

## Owner-bound Exact Authority (step 6B1)

Issue #52 / step 6B1 establishes the pure owner-bound contract and owner-local key lifecycle; it does not yet wire the receiving remote-open path. Followed fail-closed invariants:

- Every remote Pipe open still requires a short-lived, cryptographically verifiable Access Lease (owner rule). `PipeID` is self-deriving (`sha256(ownerPublic)`), so `VerifyPipeOwner` proves the exact-Pipe owner.
- The `OwnerBinding` is owner-signed and binds exactly one dedicated Exact Authority key (plus bounded neutral redemption peers/protocol and an authorization epoch) to one exact Pipe. `VerifyOwnerAuthority` requires the binding owner to be the self-derived pipe owner and the authority key to match; anything else fails closed. This removes the PR #51 flaw where a remote/ambient key could be invented/trusted without owner binding.
- The `ExactInvite` composes the owner-signed binding and is recipient-unbound: invite possession is never an Access Lease and never authorization on its own.
- Distinct NUL-delimited signing domains (`tubo://access-lease/v1`, `tubo://exact-owner-binding/v1`, `tubo://exact-invite/v1`, `tubo://exact-owner-key-proof/v1`) prevent signed bytes from being re-used or mistaken across credential kinds.
- Strict bounded decode (≤48 KiB limit, flat object, allow-listed keys, no duplicate/nested/trailing bytes) runs before typed unmarshal and before any cryptographic verification, so unknown, duplicate, and oversized fields fail closed with no unbounded allocation.
- The deterministic key lifecycle creates the owner proof under the owner's identity secret; read-only `Load` fails closed on missing/corrupt/mismatched material and never creates a filesystem entry. `exactauthority` depends only on the pure `accesslease` contract (no runtime/reachability/discovery/store/authority/libp2p/CLI import).
