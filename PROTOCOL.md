# Protocol

## Versioning

- `major.minor` protocol versioning
- major changes are breaking
- minor changes add optional fields or features

## Entities

- `client peer`
- `service peer`
- `control plane`
- `relay`

## Core messages

### RegisterService
Sent by the service agent to register a service peer and its capabilities.

Fields:
- protocol version
- peer ID
- tenant ID
- service name
- local target
- supported features
- auth material

### ServiceAck
Returned by control plane.

Fields:
- service ID
- lease ID
- lease TTL
- allowed hostnames
- heartbeat interval

### ResolveService
Sent by the client bridge or native client to discover the target service peer.

Fields:
- service name or hostname
- tenant ID
- path or route hint
- requested features

### OpenRequest
Opens a request stream.

Fields:
- request ID
- correlation ID
- method
- authority
- path
- query
- headers
- body mode
- content length

### ResponseStart
Carries status code and response headers.

### BodyChunk
Carries request or response body data.

### Error
Carries machine-readable error codes.

## Streaming rules

- one HTTP request maps to one libp2p application stream
- streaming responses must flush incrementally
- body chunks should be bounded and ordered
- requests may be rejected if the peer or route is unauthorized

## Timeout rules

- registration lease timeout
- heartbeat timeout
- connect timeout
- request header timeout
- idle streaming timeout

## Error codes

- `UNAUTHORIZED`
- `FORBIDDEN`
- `NOT_FOUND`
- `SERVICE_UNAVAILABLE`
- `CONNECT_TIMEOUT`
- `UPSTREAM_TIMEOUT`
- `RELAY_FAILURE`
- `PROTOCOL_MISMATCH`
- `RATE_LIMITED`

## Compatibility

Unknown fields must be ignored when possible.
Feature negotiation should be explicit.

## MVP framing recommendation

Use a compact binary framing format with length-prefixing. Keep the first version simple and debuggable.
