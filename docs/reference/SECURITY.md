# Security model

**Status:** Future security architecture is governed by [`HIGH_LEVEL_DESIGN.md`](HIGH_LEVEL_DESIGN.md), especially universal Access Lease enforcement and Scope-independent connectivity. Detailed token, Scope, protocol, and state behavior below documents preserved prototype reference implementation; retain its threat lessons without treating format/topology as compatibility contract.

## Future core statement

```text
Every remote Pipe open requires a short-lived, cryptographically verifiable Access Lease.
Reachability != discovery != authorization.
Public infrastructure provides reachability, never ambient authorization.
```

### Durable accepted remote-Pipe binding (schema 12, Journey A durable)

The one-time invite is bootstrap only. Acceptance establishes a durable
recipient-bound remote-Pipe relationship (the accepted binding), and every
later Pipe open reacquires a fresh short-lived Access Lease through it:

```text
Invite != AccessLease
AcceptedBinding != AccessLease
AcceptedBinding != local Pipe
AcceptedBinding does not define an ingress
```

- The accepted binding is cryptographically bound to the exact remote
  PipeID, the accepted recipient identity, and the correct owner-bound Exact
  Authority. Renewal (post-acceptance reissuance) requires recipient
  possession proof by that same identity; there is no reusable unbound
  bearer refresh token as the root of permanent access.
- The actual Pipe open always uses a short-lived AccessLease at the L2
  receiving boundary (unchanged). A persisted accepted binding is not itself
  permission to open.
- Invite expiry never destroys the accepted relationship: the invite is the
  evidence used to establish the relationship once, not its lifetime clock
  (intentionally different from the old share-invite-derived refresh-lease
  behavior).
- Every renewal fails closed on unavailable revocation state, invalid
  signatures, subject mismatch, Pipe mismatch, missing authoritative
  accepted state, or a stale authorization epoch. The authoritative accepted
  relationship is exactly one current row per `(pipe_id, recipient_public)`
  carrying the backing invite JTI and the accepted epoch; resolving it is
  deterministic. The invite epochs, accepted relationship epoch, and every
  AccessLease epoch are equal at acceptance; a pipe-access revocation
  advances the epoch, previous accepted relationships become stale (renewal
  denied), and a fresh share+accept at the new epoch re-authorizes — never a
  permanent kill switch. Revoking an obsolete invite's JTI never affects a
  newer current relationship; revoking the current one denies renewal. Live
  sessions keep the existing short-lease/session expiry fencing, and the L2
  receiving boundary keeps the final stale-epoch denial.

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

### Bootstrap trust root (issue #45, Step 10A)

A dedicated Ed25519 bootstrap root (`tubo-bootstrap-root-2026-01`) is used exclusively for L1 Reachability bootstrap bundle signing. This root has role `tubo.bootstrap.v1` and is NEVER used for Exact/Scope Authority, node identity, relay identity, or membership signing. The private key was generated outside the repository, used to sign the production bundle, and permanently deleted from the build environment. Only the public key (32 bytes) is embedded in the binary.

### Identity stability policy

Relay/bootstrap nodes have stable PeerIDs derived from their persistent Ed25519 key files. The relay at `relay.tubo.click` (PeerID `12D3KooWM2hGwzrJYGyjUwtzrzLZuPWAmjiG8JeNM5YYJNPeHLME`) runs A0-compatible relay code with NO swarm PSK (public no-PSK relay, PR #67 merge `277e4366de4feb58e473308b0967d9bea9164dab`). The upgrade preserved the key file, so the PeerID remained stable and the embedded signed bundle stayed valid — no re-signing was required for the Step 10B distributed proof ([`A0_STEP10B_EVIDENCE.md`](../testing/A0_STEP10B_EVIDENCE.md)). A future relay key-file change WOULD change the PeerID and require a new bundle.

The production bundle references the current relay PeerID above. If the relay PeerID/addresses must change, a NEW bundle under a NEW bootstrap root will be required (the `tubo-bootstrap-root-2026-01` private signing key was SINGLE-USE and permanently destroyed after signing). Runtime trust-root and bundle rotation remains A2 work. The current embedded bundle expires 2027-02-23.

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

Public sharing is unlisted and invite-only by default. A0 exact share embeds one canonical owner-runtime circuit locator in the owner-signed binding: `<relay transport>/p2p/<relay>/p2p-circuit/p2p/<owner runtime>`. Share derives it from a current ready, live owner Pipe runtime plus configured or verified-default relay facts and checks it against current runtime state; relay-only, empty, stale, malformed, or mismatched locators fail closed before invite minting. This locator reaches the owner's exact-redemption handler only. It is not an Access Lease, grants no Pipe authorization, and contains no local Service target, Scope, discovery, or application metadata. One-time means one successful lease/session redemption.

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

## Owner-bound Exact Authority (step 6B1a)

Step 6B1a establishes the pure owner-bound value contract; it wires no
remote-open and introduces no durable key/secret lifecycle (that is step 6B1b,
the mandatory predecessor to 6B2's receiving path). Followed fail-closed
invariants:

- Every remote Pipe open still requires a short-lived, cryptographically
  verifiable Access Lease (owner rule). `PipeID` is self-deriving
  (`sha256(ownerPublic)`), so `VerifyPipeOwner` proves the exact-Pipe owner.
- The `OwnerBinding` is owner-signed and binds exactly one dedicated Exact
  Authority key (plus bounded neutral redemption peers/protocol and an access
  epoch) to one exact Pipe. `VerifyOwnerAuthority` requires the binding owner
  to be the self-derived pipe owner and the authority key to match; anything
  else fails closed. This disallows the old flaw where a remote/ambient key
  could be invented/trusted without owner binding.
- The `ExactInvite` composes the owner-signed binding and is recipient-unbound:
  invite possession is never an Access Lease and never authorization on its
  own.
- Distinct NUL-delimited signing domains (`tubo://access-lease/v1`,
  `tubo://exact-owner-binding/v1`, `tubo://exact-invite/v1`) prevent signed
  bytes from being re-used or mistaken across credential kinds.
- Strict bounded decode (≤ 48 KiB limit, flat object, allow-listed keys, no
  duplicate/nested/trailing bytes) runs before typed unmarshal and before any
  cryptographic verification, and every base64url input (public keys,
  signatures, outer credential bodies, embedded bindings) must be
  canonical unpadded URL-safe base64url verified by re-encode equality.
- 6B1a writes, reads, or implies no durable secret or filesystem key lifecycle;
  no key file, path, envelope, or lifecycle exists here. It depends only on the
  standard library and remains a pure value contract.

## Owner-local private-key lifecycle (step 6B1b)

Step 6B1b introduces one local filesystem threat model and one shared key-file primitive for Pipe owner keys and dedicated Exact Authority keys. In scope: network attackers, untrusted relays/bootstrap/discovery/caches/query peers, other local OS users, unsafe group/other file modes, final-component symlinks, partial/corrupt/oversized/noncanonical key files, concurrent first provision races, crash before durable publication, and accidental remote-request-driven provisioning. Out of scope: a malicious process already running as the same OS UID; such a process can usually read, replace, race, or delete owner-readable files and would require future OS keychain/sandbox/hardware-key integration.

Security contract: `Load` performs zero filesystem mutation and fails closed on missing or unsafe state. Private-key files are canonical unpadded raw-base64url Ed25519 private keys of the expected decoded length, read through a bounded path before allocation, with no trimming or alternate spellings accepted. Provisioning is available only to trusted local owner workflow code; it serializes local contenders, writes a temp file, fsyncs file data, publishes without clobber, fsyncs the containing directory, and reports no success before durable publication. Losers in concurrent first provision only return after loading the committed winner. `internal/exactauthority.Loader` is read-only and cannot create keys; `LocalOwnerWorkflow` is the separate provisioning capability that requires the owner private key and cannot bind a foreign Pipe.

## Exact Pipe Authority remote-open authorization (step 6B2)

Step 6B2 introduces the remote-open authorization boundary for the receiving serve path. The concrete `ExactOpenAuthorizer` (composition-root adapter in `internal/runtime`) validates a canonical AccessLease + possession proof against the owner-bound Exact Authority key.

**Threat model:** Network attackers with the invite, discovery record, relay route, Pipe ID, endpoint, or prototype Scope token; untrusted relay/bootstrap/cache/query peers; replay of captured AccessLease or proof across different pipes, sessions, or challenges; revocation/stale-epoch read failures; issuer mismatch; expired or non-canonical lease; possession proof signed for wrong purpose, missing, or signed by wrong key.

**Security contract:** All 16 fail-closed cases return only the generic error "authorization denied" — no internal trust, revocation, epoch, or issuer detail leaks to the caller. The ordering is bounded decode -> AuthorizeOpen -> defensive result binding -> target dial -> OpenOK -> Session. The authorization timer terminates Session+Carrier+target on lease expiry. Prototype Scope-token remote-open authorization is quarantined (`newScopeTokenOpenAuthorizer` panics); the prototype Scope-token is UNREACHABLE as remote-open authorization.

Redemption (`RedeemExactInvite`) performs zero persistent mutations before the atomic redemption step (step 10 of 12). Steps 4-9 (key loading, invite verification, time checks, epoch checks, possession proof verification) produce no state changes. `Loader` is the only key-access capability available to the redemption path; `LocalOwnerWorkflow` (which can provision keys) is never passed to redemption. The one-winner atomic redemption contract is enforced by the store's `RedeemInvite` method.

**Possession proof domains:** Open proof uses domain `"tubo-open-v2"`; redeem proof uses `"tubo-exact-redeem-v2"`. Each proof is signed over a NUL-delimited message: `<purpose>\x00<parts...>`. A proof created for one purpose cannot be reused for another. The signature is Ed25519 over the plaintext message, returned as canonical base64url. Verification rejects non-canonical, malformed, or wrong-length signatures.

**P2 hardening:** `localkey.parseCanonicalPrivate` verifies that the public suffix (last 32 bytes) of a loaded 64-byte Ed25519 private key matches the canonical key derived from its 32-byte seed. This prevents corrupt or adversarial key files with an arbitrary public suffix from silently defeating identity and signature verification guarantees.
