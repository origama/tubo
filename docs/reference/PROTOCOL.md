# Protocol 2.1

**Prototype reference contract:** This document describes wire behavior at `prototype-v2-final`. [`HIGH_LEVEL_DESIGN.md`](HIGH_LEVEL_DESIGN.md) is normative for future architecture. No current protocol ID, encoding, Scope coupling, or state/wire compatibility is automatically required for A0–A4. Retain bounded-decoder and authorization lessons unless explicitly superseded.

Status: protocol 2.1 adds negotiated authority snapshot ordering for destructive `records.list` reconciliation. Protocol 2.0 implements authority-backed remote publication grants, record announcement/listing compatibility surface, and authenticated raw-duplex tunnels. Remaining sync operations, forwarding, and further negotiated extensions are pending. Authority 2.0 now supports bounded access/refresh lease exchange, revocation query, and one-time share invite issue/redemption.

Step 8A makes **no wire-format change**: the L4 use-case boundary (`internal/usecase`) and narrow persistence ports are wire-neutral; the exact-invite credential contract, tunnel handshake, and lease exchange are unchanged. Scope-less exact redemption serving and its wire path land with step 8B.

There is intentionally no negotiation or fallback to previous Tubo protocols.

## Protocol IDs

```text
/tubo/tunnel/2.0
/tubo/records/2.0
/tubo/authority/2.0
/tubo/authority/2.1
```

All decoders are length-bounded before allocation. Protocol errors reset only the affected stream.

### Session resume (removed from the A0 path)

`/tubo/tunnel-resume/2.1` with the `session-resume-v1` capability and its resume/control stream roles, continuous ACK, retention/replay window, planned-switch barrier, abrupt carrier recovery, `ResumeOffer`, memory-only `resume_secret`, and the resume handshake message set were **prototype machinery removed from the A0 data plane** by the fail-closed quarantine (issue #39). No production code advertises, offers, selects, accepts, or registers a handler for them; an unsolicited resume stream has no handler and is reset by libp2p. The A0 path keeps only the authorized `/tubo/tunnel/2.0` raw-duplex tunnel. Session-resume/carrier-replacement correctness remains deferred post-A0 resilient-Session debt (#32).

## Tunnel data plane

One authenticated handshake is used for HTTP, WebSocket, raw TCP, and TLS passthrough.

```text
client                              pipe runtime
  |--- ClientHello -------------------->|
  |<-- ServerHello ---------------------|
  |--- OpenPipe ----------------------->|
  |    pipe_id, session nonce, proof    |
  |<-- OpenOK / OpenError --------------|
  |=== raw full-duplex bytes ===========|
```

After `OpenOK`, framing ends. HTTP uses ordinary HTTP bytes; WebSocket is an ordinary upgrade; TCP/TLS is opaque. For a local `https://` service target, the origin pipe establishes and verifies TLS 1.2+ using the target hostname before carrying those HTTP bytes. Half-close propagates in both directions.

### Handshake messages

A binary length-delimited envelope carries:

```text
ClientHello: protocol major/minor, client public key, capabilities
ServerHello: protocol major/minor, pipe_id, challenge, capabilities
OpenPipe: pipe_id, challenge proof, access lease, visited pipe IDs, hop limit
OpenOK: session_id, selected protocol, optional redacted hop receipt
OpenError: stable error code, safe message, retry class
```

Initial encoding uses a four-byte big-endian length followed by JSON. Outer length is rejected above 64 KiB before allocation. Security-sensitive decoders reject unknown fields and trailing JSON. Signed token and record payloads use fixed Go structs with deterministic field order. Any replacement encoding is a protocol change governed by `VERSIONING.md`.

### Initial limits

| Element | Maximum |
|---|---:|
| outer handshake message | 64 KiB |
| string field | 8 KiB |
| token/envelope field | 48 KiB |
| capabilities | 64 |
| logical pipe hops | 8 |
| handshake duration | 10 s |
| clock skew accepted for proof | 30 s |

No absolute lifetime applies after `OpenOK`. Relay circuit byte/duration truncation is disabled; relay reservation and concurrent-circuit bounds remain enforced. Current session policy closes tunnel and target connection when initial access lease expires. Refresh exchange obtains access+refresh leases for subsequent/new session setup only; it never extends a live tunnel.

### Retry rule

Setup may retry another direct/relay candidate before `OpenOK` and before application bytes are accepted. A local pipe re-resolves its exact `pipe_id` from its pinned authority scope every five minutes and after one classified transient pre-`OpenOK` dial/stream/handshake failure. Recovery is bounded by discovery retry cooldown and one setup retry; a newer verified record atomically replaces only later connection attempts. Terminal authorization/protocol responses never trigger re-resolution. Once bytes flow, Tubo does not replay them. Recovery creates a new application connection/session. There is no session resume on the A0 path: carrier failure terminates the session, and a new connection is required.

### Session resume

Removed from the A0 path (issue #39). See the note under Protocol IDs. The A0 recovery model is: carrier failure terminates the session, and a new authorized connection/session is created. No `ResumeOffer`, control stream, retention/replay window, epoch fencing, proof, or detached grace exists on the A0 data plane.

## Pipe chaining

A forwarding pipe opens a new authorized session to its target pipe. It appends its pipe ID to `visited_pipe_ids`; a repeated ID or exhausted hop limit is rejected.

The downstream pipe authorizes the forwarding pipe as immediate caller. End-user identity is not propagated as authority unless a future protocol adds an explicit attenuated delegation chain.

## Record protocol

`/tubo/records/2.0` exchanges the same signed/encrypted `PipeRecord` used by GossipSub.

Operations:

```text
AnnounceRecord
ListRecords
GetRecord
SyncSummary
SyncRecords
```

`/tubo/authority/2.0` retains `records.list` only for explicit compatibility surface; its response has no trusted ordering metadata and cannot delete cache entries. `/tubo/authority/2.1` successful `records.list` response is one complete bounded snapshot of discoverable records for its pinned scope. Authority rejects an over-count or over-byte response as `incomplete authoritative snapshot`; receivers retain prior verified cache and must not delete from it. Future pagination/cursors require an explicit protocol-version decision before any paged response can claim complete enumeration.

A record includes:

```text
version
pipe_id
owner public key
runtime peer_id
scope IDs
opaque/discoverable display metadata
protocol/capabilities
reachable addresses
access-policy hint
publish grant
issued_at/expires_at
nonce/sequence
owner signature
optional namespace encryption envelope
```

It never contains the local service target or local service reference.

Consumer verification order:

1. outer size/schema;
2. namespace decrypt/topic binding when applicable;
3. scope and authority trust root;
4. `pipe_id` derivation from owner key;
5. owner signature;
6. publish grant signature/audience/permissions;
7. runtime peer/address binding;
8. time/TTL;
9. replay/sequence;
10. atomic complete-snapshot reconciliation and cache insertion.

Before reconciliation, receiver bounds every response and verifies every record. Absent records are removed only from a complete pinned-authority `/tubo/authority/2.1` snapshot with nonzero authority-issued generation. Receiver persists accepted generation, authority mutation high-water, and canonical snapshot digest only after successful commit. It rejects lower generation/high-water and same-generation changed digest or high-water responses. Relay, cache, peer, malformed, partial, and `/tubo/authority/2.0` responses never authorize deletion.

## Authority protocol

`/tubo/authority/2.0` supports typed requests for:

```text
membership.issue
pipe_publish.request/approve/deny
pipe_forward.request/approve/deny
share_invite.issue/redeem
connect_access.issue
connect_refresh.exchange
revocation.query
```

There is one issuer key class per scope. Pipe runtimes do not issue delegated connect leases.

Initial maximum encoded request/response size is 64 KiB. `records.list` returns no partial success: oversized snapshots fail rather than silently truncate. Unknown fields are rejected in security-sensitive requests until additive compatibility rules are explicitly defined.

### Authority snapshot extension 2.1

`/tubo/authority/2.1` serves only `records.list`; it has separate strict request/response structs:

```text
request:  token, client_public, nonce, proof
response: ok, error, generation, high_water, records
```

Request proof binds literal operation `records.list`. Authority reads records plus metadata in one transaction. `generation` is nonzero, authority-global, persistent across authority restart, and sole deletion ordering value. `high_water` is nonzero authority mutation watermark, increments with authority snapshot mutation, and is diagnostic/consistency metadata only; it is not derived from nor compared with per-pipe record sequence. Client does not fall back to 2.0 when 2.1 unavailable and retains cache while reporting refresh failure.

`/tubo/authority/2.0` remains handler-compatible for explicit legacy operations. Its `records.list` response has no ordering metadata and new consumers must fail closed for reconciliation.

## Version behavior

A protocol major mismatch is rejected. Authority 2.1 snapshot consumers require that exact protocol ID; unavailable 2.1 is failure, not 2.0 fallback. Minor additions require explicit capability negotiation and bounded optional fields. See `VERSIONING.md`.

## Exact-Pipe Access Lease, Owner Binding, and Exact Invite (contract foundation, step 6B1a)

Issue #52 / step 6B1a defines, as pure value contracts only, the canonical exact-Pipe credentials that step 6B2 will later bind into the receiving remote-open path. These are NOT yet served on any protocol ID; neither `/tubo/authority/2.0` nor `/tubo/authority/2.1` emits or accepts them, and this document describes contract bytes, not a served operation.

- `AccessLease`: `version=1`, `type=pipe.open.access`, fields `issuer`, `subject`, `audience_pipe_id`, `jti`, `issued_at`, `not_before`, `expires_at`, `access_epoch`, `signature`. Signature is Ed25519 over the canonical JSON (signature omitted) prefixed by the NUL-delimited domain `tubo://access-lease/v1`. No Scope, endpoint, route, target, discovery, namespace, or arbitrary metadata.
- `OwnerBinding`: owner-signed, `version=1`, `type=exact.owner.binding`, fields `owner`, `authority_key` (exactly one dedicated Exact Authority key), `pipe_id`, `redemption_peers` (bounded neutral address string), `redemption_protocol`, `epoch`, `signature`. NUL-delimited domain `tubo://exact-owner-binding/v1`.
- `ExactInvite`: recipient-unbound, `version=1`, `type=pipe.open.invite`, fields `issuer` (the dedicated Exact Authority key), `audience_pipe_id`, `binding` (bounded base64url OwnerBinding reference), `jti`, `issued_at`, `not_before`, `expires_at`, `access_epoch`, `one_time`, `signature`. NUL-delimited domain `tubo://exact-invite/v1`. The invite composes the owner-signed binding so redemption proves the issuer is the exact-Pipe owner's bound Authority.

Signing bytes: each signature is Ed25519 over the canonical JSON body WITH the
`signature` field omitted entirely (an explicit signature-less wire body, never
a `"signature":""` placeholder) prefixed by the NUL-delimited signing domain.

Decoding is strict and bounded (≤48 KiB total payload; each string member
≤4096 bytes; comma-joined redemption peers ≤8 entries / 1024 bytes; redemption
protocol ≤128 bytes; pipe id ≤256): unknown keys, duplicate keys, trailing
payload, nested objects/arrays, and every over-long string member are rejected
DURING the strict scan before typed unmarshal and before any byte-level work.
The `signature` member is length-bounded at scan; its base64 validity and
cryptographic value are enforced only by each credential's Verify path.

All base64url text in these credentials — public keys, signatures, outer
credential bodies, and embedded binding references — must be
canonical unpadded URL-safe base64url. Each decoded value is required to
re-encode byte-for-byte to its original text (RawURLEncoding re-encode
equality); since Go's encoder can skip newlines and tolerances vary, CR/LF/CRLF,
padding, whitespace, wrong decoded lengths, alternate spellings, and malformed
text are rejected explicitly before any cryptographic verification or typed
unmarshal. Nothing is trimmed or canonicalized on input.

Step 6B1b changes no served protocol ID and adds no redemption operation. It only makes dedicated Exact Authority key material loadable/provisionable locally and able to produce/verify the existing owner-bound `OwnerBinding`.

## Exact-Pipe Access Lease remote-open authorization (step 6B2)

Step 6B2 binds the canonical AccessLease into the receiving remote-open path on `/tubo/tunnel/2.0` (the same protocol ID as the A0 authorized tunnel transport from issue #48). The receiving serve path (`internal/runtime/serve.go`) performs bounded frame decode, then delegates to the injected `openauth.OpenAuthorizer` seam. The concrete `ExactOpenAuthorizer` (in `internal/runtime` as composition-root adapter) validates:

1. **AccessLease decode**: canonical bounded flat-JSON decode, strict base64url, type/version/domain check.
2. **Issuer trust**: issuer must match the local owner-bound Exact Authority public key.
3. **Lease verify**: Ed25519 signature over NUL-delimited domain `tubo://access-lease/v1`, subject, pipe audience, time window.
4. **Possession proof**: a fresh server challenge is generated and sent in `ServerHello`. The client must include a `ChallengeProof` in `OpenPipe` that is a domain-separated Ed25519 signature over `"tubo-open-v2"\x00<challenge>\x00<pipe_id>`. The proof is bound to the exact Pipe and challenge, preventing replay across pipes or sessions.
5. **Revocation/epoch check**: the lease JTI is checked against the revocation store; the authorization epoch is validated against the current epoch for the pipe.

All 16 fail-closed cases return only the generic error message "authorization denied". The ordering is bounded decode -> AuthorizeOpen -> defensive result binding (PipeID/Subject/non-zero future expiry) -> target dial -> OpenOK -> Session.

Redemption is not yet served on a dedicated protocol ID; it is a local owner-side operation that produces the AccessLease for the recipient. The mandatory 12-step redemption ordering (RedeemExactInvite) is: canonical invite decode, OwnerBinding verify, existing Loader.Load (no Provision), invite signature verify, time/one_time/epoch checks, recipient public key validation, fresh server challenge generation, recipient possession proof verify (domain-separated `"tubo-exact-redeem-v2"\x00<jti>\x00<pipe_id>\x00<challenge>`), atomic redemption mutation, and AccessLease minting. Steps 4-9 produce zero state mutations.
