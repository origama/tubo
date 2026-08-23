# Protocol 2.1

**Prototype reference contract:** This document describes wire behavior at `prototype-v2-final`. [`HIGH_LEVEL_DESIGN.md`](HIGH_LEVEL_DESIGN.md) is normative for future architecture. No current protocol ID, encoding, Scope coupling, or state/wire compatibility is automatically required for A0–A4. Retain bounded-decoder and authorization lessons unless explicitly superseded.

Status: protocol 2.1 adds negotiated authority snapshot ordering for destructive `records.list` reconciliation. Protocol 2.0 implements authority-backed remote publication grants, record announcement/listing compatibility surface, and authenticated raw-duplex tunnels. Remaining sync operations, forwarding, and further negotiated extensions are pending. Authority 2.0 now supports bounded access/refresh lease exchange, revocation query, and one-time share invite issue/redemption.

There is intentionally no negotiation or fallback to previous Tubo protocols.

## Protocol IDs

```text
/tubo/tunnel/2.0
/tubo/tunnel-resume/2.1
/tubo/records/2.0
/tubo/authority/2.0
/tubo/authority/2.1
```

All decoders are length-bounded before allocation. Protocol errors reset only the affected stream.

### Tunnel resume protocol 2.1

`/tubo/tunnel-resume/2.1` serves two stream roles declared in the first framed message:

- `role=control`: continuous framed stream for ACKs, barriers, and switch coordination.
- `role=resume`: authenticated handshake that becomes raw carrier after `ResumeActivated`.

Both stream types are independent libp2p streams under the same protocol ID. The control stream must not become a single point of failure: the resume handshake carries full authenticated state so recovery works even if the control stream is unavailable.

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

No absolute lifetime applies after `OpenOK`. Relay circuit byte/duration truncation is disabled; relay reservation and concurrent-circuit bounds remain enforced. Current session policy closes tunnel and target connection when initial access lease expires. Refresh exchange obtains access+refresh leases for subsequent/new session setup only; it never extends a live tunnel. Live in-session renewal needs future tunnel resume/switch protocol.

### Retry rule

Setup may retry another direct/relay candidate before `OpenOK` and before application bytes are accepted. A local pipe re-resolves its exact `pipe_id` from its pinned authority scope every five minutes and after one classified transient pre-`OpenOK` dial/stream/handshake failure. Recovery is bounded by discovery retry cooldown and one setup retry; a newer verified record atomically replaces only later connection attempts. Terminal authorization/protocol responses never trigger re-resolution. Once bytes flow, Tubo does not replay them. Recovery creates a new application connection/session unless session resume is negotiated.

### Session resume

When both peers advertise `session-resume-v1` capability and `OpenOK` includes a `ResumeOffer`, the session is resume-capable. A separate control stream carries continuous ACKs, barrier coordination, and switch commands. The resume handshake authenticates via HMAC-SHA256 proof over a memory-only `resume_secret`.

#### Resume handshake messages

All resume messages are bounded to 4 KiB, use the same length-prefixed JSON encoding as the initial handshake, and are limited by `MaxResumeFrame`.

```text
client                              server
  |--- ResumeHello -------------------->|
  |    version, pipe_id, session_id,    |
  |    next_epoch, nonce, proof         |
  |<-- ResumeChallenge -----------------|  
  |    session_id, next_epoch, nonce,   |
  |    proof                            |
  |--- ResumeCommit ------------------->|
  |    session_id, next_epoch,          |
  |    client_nonce, server_nonce,      |
  |    c2s_old_sent, s2c_delivered,     |
  |    c2s_fin_seen, s2c_fin_delivered, |
  |    proof                            |
  |<-- ResumeResult --------------------|
  |    ok, session_id, epoch,           |
  |    c2s_delivered, s2c_old_sent,     |
  |    c2s_resume_at, s2c_resume_at,    |
  |    c2s_fin_delivered, s2c_fin_seen, |
  |    proof                            |
  |--- ResumeActivate ------------------>|
  |    session_id, epoch,               |
  |    transcript_digest, proof         |
  |<-- ResumeActivated -----------------|
  |=== raw full-duplex bytes ===========|
```

After `ResumeActivated`, framing ends and the stream becomes the raw carrier.

#### Control messages

| Message | Purpose |
|---|---|
| `Ack` | Continuous receiver-committed offset |
| `SwitchPrepare` | Announce intent to switch epoch |
| `Barrier` | Quiesce one direction at cut offset |
| `BarrierAck` | Confirm barrier completion |
| `SwitchAbort` | Cancel planned switch |
| `SwitchSuggested` | Server hint to initiate switch |

#### Limits

| Element | Maximum |
|---|---|
| resume/control frame | 4 KiB |
| unacknowledged data per direction | 1 MiB |
| aggregate session memory | 64 MiB |
| ACK interval | 64 KiB or 25 ms |
| candidate handshake | 10 s |
| detached grace | 10 s |
| nonce/resume secret | 32 bytes |
| epoch/offsets | uint64 |

#### Planned switch sequence

1. `ACTIVE(epoch, relay)`: old carrier continues.
2. `PROBING`: open and authenticate direct candidate. No application pause.
3. `QUIESCING`: stop old carrier writers at exact cut offsets. Source readers continue into bounded buffers.
4. Receiver drains old carrier through cut, commits bytes locally, sends `BarrierAck`.
5. Both directions freeze old generation. Late bytes cannot reach application.
6. Exchange `ResumeCommit/Result`.
7. Sender resumes at receiver's exact committed offset. Only retained bytes retransmitted.
8. Exchange activation. Install candidate as sole active carrier.
9. Emit path event only after both directions activate.
10. Close/reset old carrier without propagating application FIN.

#### Abrupt recovery

On unexpected carrier failure:

1. Fence failed carrier generation immediately.
2. Do not close target or local application socket.
3. Preserve retained buffers and committed receiver offsets.
4. Client opens relay resume candidate with epoch + 1.
5. Resume handshake exchanges receiver-committed offsets.
6. Replay only bytes at or above those offsets.
7. If relay resume fails, offset outside window, grace expires, or commit ambiguous: close session and both endpoints.

Single coordinator (client) prevents split-brain. Server may send `SwitchSuggested`, but client chooses epoch and candidate.

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
