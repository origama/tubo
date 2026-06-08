# Comparison with other tunneling tools

Tubo is a self-hostable libp2p tunnel for trusted peers and named private services. It is designed for HTTP APIs, raw TCP/TLS endpoints, local AI tools, and agent-facing services across NAT without depending on a managed SaaS control plane.

This page is not a scorecard. The tools below solve different problems; Tubo is strongest when service identity matters more than device identity.

## Tubo positioning

Tubo is for private named services, not just private networks.

It gives you:

- signed discovery for named services;
- invite- or membership-based access;
- private swarm / PSK options;
- self-hosted relays;
- a local join/share/connect workflow.

No mandatory managed SaaS is required. If you self-host the relays, you keep the control plane in your own environment. Like any relay-based system, transport metadata such as source IPs and timing can still be visible to relay operators.

## Comparison table

| Tool | Primary model | Where it shines | How Tubo differs |
|---|---|---|---|
| Tubo | Private service tunnel + signed service discovery | Trusted-peer access to named HTTP/TCP services, local AI tools, and agent-facing endpoints | Service-first, cluster/namespace-scoped, self-hostable, and built around discovery + access control |
| ngrok | Managed public ingress | Fast public URLs, webhooks, demos, and polished SaaS UX | Tubo is private-first and does not require a vendor SaaS control plane |
| Tailscale | Device mesh VPN | Private networking between devices, ACLs, subnet routing, SSH | Tubo publishes named services instead of placing whole devices into a VPN-style mesh |
| Cloudflare Tunnel | Managed edge tunnel | Public hostname routing, CDN/WAF, and Access integration | Tubo is self-hosted and peer-oriented rather than edge-vendor-oriented |
| frp | General-purpose reverse proxy | Mature TCP/UDP/HTTP/HTTPS tunneling and operational knobs | Tubo adds signed discovery and service identity; frp is broader and already offers P2P modes |

## When Tubo is a good fit

Use Tubo when:

- you want to share a named service, not an entire machine;
- you want trusted-peer access to local HTTP or TCP/TLS endpoints;
- service identity, discovery, and revocation matter;
- you want a self-hostable alternative without mandatory SaaS;
- you want a small tool for labs, edge nodes, local AI services, and agent-to-agent experiments.

## When another tool may be better

- ngrok: when you need a public URL immediately.
- Tailscale: when you want a full device mesh VPN.
- Cloudflare Tunnel: when you want Cloudflare edge features and Access.
- frp: when you need broad TCP/UDP tunneling today.
- WireGuard: when you want a minimal VPN and manage peers directly.

## Honest trade-offs

Tubo is still focused on HTTP and raw TCP/TLS service publishing. It is not trying to replace every tunnel or every VPN.

Current gaps include:

- generic UDP;
- advanced edge routing;
- built-in WAF/CDN;
- mature dashboard and traffic policy layers;
- the full breadth of long-lived general-purpose reverse-proxy ecosystems.

## Bottom line

If your problem is “I want to publish a private, named service to trusted peers across NAT,” Tubo is a good fit.

If your problem is “I need a public ingress product” or “I want a device mesh VPN,” another tool may fit better.
