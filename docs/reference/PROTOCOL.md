# Protocol 2.1

Status: protocol 2.1 adds negotiated authority snapshot ordering for destructive `records.list` reconciliation. Protocol 2.0 implements authority-backed remote publication grants, record announcement/listing compatibility surface, and authenticated raw-duplex tunnels. Remaining sync operations, forwarding, access refresh, revocation, and further negotiated extensions are pending.

There is intentionally no negotiation or fallback to previous Tubo protocols.

## Protocol IDs

```text
/tubo/tunnel/2.0
/tubo/records/2.0
/tubo/authority/2.0
/tubo/authority/2.1
```

All decoders are length-bounded before allocation. Protocol errors reset only the affected stream.

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

No absolute lifetime applies after `OpenOK`. Relay circuit byte/duration truncation is disabled; relay reservation and concurrent-circuit bounds remain enforced. Current session policy closes tunnel and target connection when access lease expires; refresh/lease exchange before expiry is pending.

### Retry rule

Setup may retry another direct/relay candidate before `OpenOK` and before application bytes are accepted. Once bytes flow, Tubo does not replay them. Recovery creates a new application connection/session.

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
