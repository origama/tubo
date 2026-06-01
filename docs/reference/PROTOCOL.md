# Wire Protocol Specification — Binary Framing

**Current Version:** 1.1 | **Current Protocol ID:** `/p2p-tunnel/1.1`

Backward-compatible legacy stream protocol still accepted:
- `1.0` via `/p2p-tunnel/1.0`

## Overview

The wire protocol uses binary framing with varint length prefixes for efficient HTTP-over-libp2p tunneling. Replaces the legacy JSON-based protocol to fix critical issues: multi-value header truncation, no streaming support, and excessive overhead.

## Frame Format

```
┌───────────┬───────┬───────────┐
│  Length   │ Type  │  Payload  │
│ (varint)  │ (1B)  │ (N bytes) │
└───────────┴───────┴───────────┘
```

- **Length**: Unsigned varint encoding the byte count of `Type + Payload`
- **Type**: Single byte identifying the frame type
- **Payload**: Type-specific binary data

## Frame Types

| Byte | Name | Description |
|------|------|-------------|
| 0x00 | Hello | Protocol version, role, and capabilities |
| 0x01 | RequestHeader | HTTP request metadata |
| 0x02 | ResponseHeader | HTTP response metadata |
| 0x03 | BodyChunk | Request/response body data (streaming) |
| 0x04 | Error | Error notification |
| 0x06 | TunnelRequest | Request to switch the stream into raw TCP mode |
| 0x07 | TunnelReady | Positive ack before entering raw TCP mode |

## Hello Payload (protocol 1.1+)

```
┌───────────────┬───────────────┬───────────┬─────────────────┐
│ ProtocolMajor │ ProtocolMinor │ Role      │ Capabilities    │
│   (uint16)    │   (uint16)    │ (string)  │ (string list)   │
└───────────────┴───────────────┴───────────┴─────────────────┘
```

- `ProtocolMajor`: breaking compatibility line
- `ProtocolMinor`: backward-compatible extension level
- `Role`: caller identity (`edge`, `bridge`, `service`, ...)
- `Capabilities`: optional features supported by the sender (`hello-v1`, `connect-proof-v1`, `raw-tcp-v1`, ...)

Rules:
- if protocol major differs, the stream must be rejected
- if protocol minor differs but major matches, peers must use the shared behavior subset
- optional features should be capability-gated

## RequestHeader Payload

```
┌───────────┬───────────┬───────────┬──────────────┐
│ Method    │ Path      │ Query     │ Headers      │
│ (string)  │ (string)  │ (string)  │ (header_list)│
└───────────┴───────────┴───────────┴──────────────┘

┌───────────────────┐
│ ContentLengthHint │
│     (varint)      │
└───────────────────┘
```

- **Method**: varint-length-prefixed string (`GET`, `POST`, etc.)
- **Path**: varint-length-prefixed string (`/api/v1/users`)
- **Query**: varint-length-prefixed string (query parameters)
- **Headers**: varint count N, followed by N header entries:
  - Each entry: varint key_count → varint key_length → key_bytes → varint value_count → varint value_length → value_bytes
- **ContentLengthHint**: varint (-1 = unknown/streaming, ≥0 = exact byte count)

## ResponseHeader Payload

```
┌───────────┬───────────┬──────────────┐
│ Status    │ StatusText│ Headers      │
│ (varint)  │ (string)  │(header_list) │
└───────────┴───────────┴──────────────┘
```

- **StatusCode**: varint (HTTP status code: 200, 404, 500...)
- **StatusText**: varint-length-prefixed string (`OK`, `Not Found`...)
- **Headers**: same format as RequestHeader headers

## BodyChunk Payload

```
┌───────────┬──────────────┐
│ IsFinal   │ Data         │
│ (1 byte)  │ (raw bytes)  │
└───────────┴──────────────┘
```

- **IsFinal**: `0x01` = last chunk, `0x00` = more chunks follow
- **Data**: raw HTTP body bytes (no encoding overhead)

## Error Payload

```
┌───────────┬───────────┐
│ Code      │ Message   │
│ (varint)  │ (string)  │
└───────────┴───────────┘
```

- **Code**: varint error code
- **Message**: varint-length-prefixed string description

## Streaming Protocol Flow

```
Client                          Server
  |                               |
  |--- Hello (1.1+) ──────────>  |  (major.minor, role, capabilities)
  |<-- Hello (1.1+) ───────────  |  (major.minor, role, capabilities)
  |--- RequestHeader ─────────>  |  (method, path, headers)
  |--- BodyChunk(isFinal=0) ──>  |  (streaming body part 1)
  |--- BodyChunk(isFinal=1) ──>  |  (streaming body part N)
  |                               |
  |<-- ResponseHeader ───────────|  (status, headers)
  |<-- BodyChunk(isFinal=0) ─────|  (streaming response part 1)
  |<-- BodyChunk(isFinal=1) ─────|  (streaming response part N)
```

## Raw TCP mode

For `service_kind=tcp`, peers still negotiate Hello/auth/connect-proof first, then switch the stream to raw bytes:

```text
Client                          Server
  |                               |
  |--- Hello / ConnectProof --->  |
  |<-- Hello -------------------  |
  |--- TunnelRequest(kind=tcp) >  |
  |<-- TunnelReady(kind=tcp) ---  |
  |=== raw TCP bytes ==========>  |
  |<== raw TCP bytes ==========   |
```

After `TunnelReady`, no further Tubo frames are exchanged on that stream.

## Legacy JSON Protocol (Deprecated)

The legacy JSON protocol (`RequestMessage`/`ResponseMessage`) had critical issues:
- **Multi-value headers**: `map[string]string` truncated duplicate header values
- **No streaming**: entire body buffered in memory before sending
- **Overhead**: JSON encoding adds ~30% overhead vs binary framing
- **Encoding bugs**: UTF-8 bodies could corrupt during JSON serialization

Migrate to the new binary protocol for all new tunnels.

## Go Implementation

Package: `github.com/origama/tubo/internal/protocol`

Key types and functions:
- `RequestHeader`, `ResponseHeader`, `BodyChunk`, `ErrorFrame` — frame structs
- `EncodeFrame(w, payload)` / `DecodeFrame(r)` — raw frame encoding
- `StreamWriter` / `StreamReader` — high-level streaming API
- `WriteRequestHeader()`, `ReadRequestHeader()` — typed convenience methods

## Varint Encoding

Uses [IETF RFC 704](https://www.rfc-editor.org/rfc/rfc704.html) unsigned varint encoding (same as Protocol Buffers). Implemented via `github.com/multiformats/go-varint`.

Max encoded size: 9 bytes for values up to 2^63-1.