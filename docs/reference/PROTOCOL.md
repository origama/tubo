# Protocol 2.0

Status: design contract; implementation pending.

There is intentionally no negotiation or fallback to previous Tubo protocols.

## Protocol IDs

```text
/tubo/tunnel/2.0
/tubo/records/2.0
/tubo/authority/2.0
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

After `OpenOK`, framing ends. HTTP uses ordinary HTTP bytes; WebSocket is an ordinary upgrade; TCP/TLS is opaque. Half-close propagates in both directions.

### Handshake messages

A binary length-delimited envelope carries:

```text
ClientHello: protocol major/minor, client public key, capabilities
ServerHello: protocol major/minor, pipe_id, challenge, capabilities
OpenPipe: pipe_id, challenge proof, access lease, visited pipe IDs, hop limit
OpenOK: session_id, selected protocol, optional redacted hop receipt
OpenError: stable error code, safe message, retry class
```

The exact encoding must be selected in a separate issue. JSON is acceptable for the first bounded prototype; production encoding must remain deterministic for signatures and fuzzable.

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

No absolute lifetime applies after `OpenOK`. Access lease expiry closes or prevents renewal according to documented session policy.

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

Responses are bounded and indicate truncation. Pagination/cursors are required before claiming complete enumeration.

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
10. cache insertion.

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

Initial maximum encoded request/response size is 64 KiB. Unknown fields are rejected in security-sensitive requests until additive compatibility rules are explicitly defined.

## Version behavior

A protocol major mismatch is rejected. Minor additions require explicit capability negotiation and bounded optional fields. See `VERSIONING.md`.
