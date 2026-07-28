# ADR 0003: one authenticated raw-duplex tunnel

Status: accepted

## Decision

Protocol 2.0 uses a bounded authenticated handshake and then raw full-duplex bytes for HTTP, WebSocket, TCP, and TLS passthrough. Tubo does not frame HTTP headers or bodies after open.

## Consequences

- standard HTTP stacks handle chunking and upgrades;
- TCP and HTTP share transfer/recovery mechanics;
- half-close and streaming are core contracts;
- retry is allowed only before application bytes flow;
- protocol 1.x compatibility is intentionally absent.
