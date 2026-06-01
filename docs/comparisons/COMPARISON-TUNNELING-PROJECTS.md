# Comparison with projects similar to tubo

This document summarizes the comparison between `tubo` and other tunneling, reverse proxy, NAT traversal, and localhost exposure projects.

## tubo positioning

`tubo` is best described today as:

```text
self-hosted libp2p overlay reverse proxy with service-oriented discovery
```

It is not yet a full replacement for ngrok/frp/chisel/rathole, because it does not provide generic TCP/UDP support yet. It does have a distinctive trait: P2P discovery, libp2p relay, private swarm, and service-name routing.

## Comparison table

| Project | Model | Protocols | Self-host | NAT traversal | P2P/relay | Security | Difference vs tubo |
|---|---|---:|---|---|---|---|---|
| tubo | libp2p overlay + service-oriented discovery | HTTP today; HTTPS upstream basics | Yes | Yes, via libp2p relay/autorelay | Yes, libp2p | PSK/private swarm | More P2P/service-discovery oriented; less general-purpose |
| frp | Client/server reverse proxy | TCP, UDP, HTTP, HTTPS | Yes | Yes | P2P mode available | Token/TLS | Much more mature for TCP/UDP |
| ngrok | SaaS tunnel/gateway | HTTP/S, TCP, TLS | No, SaaS | Yes | No | Auth, policies, OAuth, TLS | Very complete product, not pure self-host |
| Cloudflare Tunnel | Cloud edge + outbound daemon | HTTP/S, TCP/private network in some scenarios | Partial | Yes | No | Zero Trust | Strong, but vendor-dependent |
| Tailscale Funnel/Serve | WireGuard mesh + public ingress | HTTP/S and TCP in some cases | Partial/SaaS control-plane | Yes | DERP relay | Tailnet ACL/identity | More VPN/mesh than custom reverse proxy |
| inlets | Client/server cloud-native tunnel | HTTP, TCP | Yes/pro | Yes | No | TLS/token | Strong for Kubernetes/LB |
| chisel | TCP/UDP tunnel over HTTP/WebSocket, secured via SSH | TCP, UDP, SOCKS | Yes | Yes | No | SSH-based | Excellent for pivoting and port forwarding |
| wstunnel | Tunnel over WebSocket/HTTP2 | TCP, UDP | Yes | Yes | No | TLS/various options | Strong for proxy/firewall bypass |
| rathole | Reverse proxy NAT traversal in Rust | TCP, UDP | Yes | Yes | No | Token/TLS/noise depending on config | Faster alternative to frp/ngrok |
| bore | Minimal TCP tunnel in Rust | TCP | Yes | Yes | No | Basic/limited | Very simple, but far fewer features |
| Holesail | P2P reverse proxy | HTTP/TCP-oriented | Yes/service | Yes | Yes | P2P keys | Conceptually close to P2P |
| NRelay/fxTunnel/OutRay | ngrok-like self-hosted alternatives | HTTP/TCP/UDP varies | Yes | Yes | Usually relay-server based | Varies | More “ngrok clone”; maturity to verify |

## Where tubo is strong

`tubo` becomes interesting when the goal is not just exposing a port, but building a private application network.

Strengths:

- service discovery built in;
- swarm announcements;
- service-name routing;
- libp2p relay;
- private swarm with PSK;
- local resource + join/share workflow;
- single `tubo` runtime;
- self-hosted deployment;
- a model suited for public edge + private services.

This makes it more similar to a small P2P service mesh than a simple TCP tunnel.

## Where tubo is weaker

Compared with mature tools such as frp, chisel, rathole, ngrok, and Cloudflare Tunnel, the current gaps are:

- generic TCP;
- UDP;
- direct TLS on the edge;
- HTTPS upstream with custom CA/mTLS;
- TLS passthrough;
- SNI routing;
- HTTP auth on the edge;
- mature ACLs;
- dashboard/UI;
- traffic policies;
- rate limiting;
- full observability;
- automatic certificate management;
- advanced route/rewrite reverse-proxy features.

## Main architectural difference

The most important difference is:

```text
frp/chisel/rathole/bore: TCP/UDP ports and streams

tubo: HTTP request/response + libp2p service discovery
```

So `tubo` is already very good for:

```text
HTTP client -> tubo edge -> libp2p -> tubo service -> HTTP target
```

But not yet for:

```text
Postgres TCP
SSH TCP
Redis TCP
TLS passthrough
DNS UDP
QUIC UDP
```

## HTTPS comparison

| Project | Public HTTPS | Upstream HTTPS | TLS passthrough |
|---|---:|---:|---|
| tubo | Via external reverse proxy; not direct yet | Probably yes with valid certs | No |
| frp | Yes | Yes | Yes / SNI patterns |
| ngrok | Yes | Yes | Yes, TLS endpoints |
| Cloudflare Tunnel | Yes | Yes | Limited / mode-dependent |
| inlets | Yes | Yes | Via TCP mode |
| chisel/wstunnel | Raw TCP/TLS transport | Yes if TCP | Yes |
| rathole | Raw TCP/TLS transport | Yes | Yes |
| bore | Raw TCP | Yes | Yes |

## TCP/UDP comparison

| Project | TCP | UDP | Notes |
|---|---:|---:|---|
| tubo | No | No | To be implemented |
| frp | Yes | Yes | Very complete |
| chisel | Yes | Yes | HTTP/WebSocket/SSH transport |
| wstunnel | Yes | Yes | WebSocket/HTTP2 transport |
| rathole | Yes | Yes | Performance-focused |
| bore | Yes | No | Minimalist |
| ngrok | Yes | No/limited | Mainly HTTP/S/TCP/TLS |
| Cloudflare Tunnel | Yes in some scenarios | Limited/not generic | Strong on HTTP/private network |
| Tailscale Funnel/Serve | HTTP/S and TCP in some cases | Not the focus | Mesh/VPN style |

## How to make tubo more competitive

The highest-impact features would be:

1. Robust upstream HTTPS.
2. Direct HTTPS edge support.
3. Generic TCP tunneling.
4. TLS passthrough / SNI routing.
5. Reverse proxy edge with path routing and rewrite.
6. Edge auth/ACL.
7. Simple/best-effort UDP.

## Final assessment

`tubo` is not yet a full replacement for frp/chisel/rathole/ngrok.

But it has a different and interesting identity:

```text
self-hosted P2P service tunnel
with discovery, libp2p relay, private swarm, and local bootstrap/join workflow
```

The feature that would make it truly comparable to the best general-purpose tunnels is generic TCP.

The feature that would make it more distinctive, though, is the edge as a dynamic application reverse proxy based on the services advertised in the swarm.
