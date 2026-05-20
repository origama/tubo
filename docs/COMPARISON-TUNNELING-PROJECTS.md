# Confronto con progetti simili a tubo

Questo documento riassume il confronto tra `tubo` e altri progetti di tunneling, reverse proxy, NAT traversal e localhost exposure.

## Posizionamento di tubo

`tubo` oggi è meglio descritto come:

```text
reverse proxy HTTP service-oriented su overlay libp2p self-hosted
```

Non è ancora un sostituto completo di ngrok/frp/chisel/rathole, perché oggi non offre TCP/UDP generico. Ha però una caratteristica distintiva: discovery p2p, relay libp2p, private swarm e routing per service name.

## Tabella comparativa

| Progetto | Modello | Protocolli | Self-host | NAT traversal | P2P/relay | Sicurezza | Differenza rispetto a tubo |
|---|---|---:|---:|---:|---:|---|---|
| tubo | Overlay libp2p + discovery service-oriented | HTTP oggi; HTTPS upstream base | Sì | Sì, via libp2p relay/autorelay | Sì, libp2p | PSK/private swarm | Più p2p/service-discovery; meno general-purpose |
| frp | Client/server reverse proxy | TCP, UDP, HTTP, HTTPS | Sì | Sì | P2P mode disponibile | Token/TLS | Molto più maturo su TCP/UDP |
| ngrok | SaaS tunnel/gateway | HTTP/S, TCP, TLS | No, SaaS | Sì | No | Auth, policies, OAuth, TLS | Product molto completo, non self-host puro |
| Cloudflare Tunnel | Cloud edge + daemon outbound | HTTP/S, TCP/private network in vari scenari | Parziale | Sì | No | Zero Trust | Forte ma vendor-dependent |
| Tailscale Funnel/Serve | WireGuard mesh + public ingress | HTTP/S e TCP in vari casi | Parziale/SaaS control-plane | Sì | DERP relay | ACL tailnet/identity | Più VPN/mesh che reverse proxy custom |
| inlets | Client/server tunnel cloud-native | HTTP, TCP | Sì/pro | Sì | No | TLS/token | Forte per Kubernetes/LB |
| chisel | TCP/UDP tunnel su HTTP/WebSocket, secured via SSH | TCP, UDP, SOCKS | Sì | Sì | No | SSH-based | Ottimo per pivoting e port forwarding |
| wstunnel | Tunnel su WebSocket/HTTP2 | TCP, UDP | Sì | Sì | No | TLS/opzioni varie | Forte per bypass proxy/firewall |
| rathole | Reverse proxy NAT traversal in Rust | TCP, UDP | Sì | Sì | No | Token/TLS/noise a seconda config | Alternativa performante a frp/ngrok |
| bore | TCP tunnel minimale in Rust | TCP | Sì | Sì | No | Basic/limitata | Semplicissimo, ma molto meno feature |
| Holesail | P2P reverse proxy | HTTP/TCP-oriented | Sì/servizio | Sì | Sì | Chiavi P2P | Concettualmente vicino per P2P |
| NRelay/fxTunnel/OutRay | Alternative ngrok self-hosted | HTTP/TCP/UDP variabile | Sì | Sì | Di solito relay server | Variabile | Più “ngrok clone”, maturità da verificare |

## Fonti principali trovate

- frp: https://github.com/fatedier/frp
- inlets: https://inlets.dev/
- chisel: https://github.com/jpillora/chisel
- wstunnel: https://github.com/erebe/wstunnel
- rathole: https://github.com/rathole-org/rathole
- bore: https://github.com/ekzhang/bore
- Cloudflare Tunnel: https://developers.cloudflare.com/tunnel/
- Tailscale Serve/Funnel: https://tailscale.com/docs/features/tailscale-serve e https://tailscale.com/docs/features/tailscale-funnel
- ngrok: https://ngrok.com/docs/guides/share-localhost/tunnels
- Holesail: https://holesail.io/

## Dove tubo è forte

`tubo` è interessante quando l'obiettivo non è solo esporre una porta, ma costruire una rete applicativa privata.

Punti forti:

- service discovery integrata;
- annunci nello swarm;
- routing per service name;
- relay libp2p;
- private swarm con PSK;
- local resource + join/share workflow;
- runtime unico `tubo`;
- deploy self-hosted;
- modello adatto a edge pubblico + servizi privati.

Questo lo rende più simile a un piccolo service mesh p2p che a un semplice tunnel TCP.

## Dove tubo è più debole

Rispetto a strumenti maturi come frp, chisel, rathole, ngrok e Cloudflare Tunnel, oggi mancano:

- TCP generico;
- UDP;
- TLS diretto sull'edge;
- HTTPS upstream con CA custom/mTLS configurabile;
- TLS passthrough;
- SNI routing;
- auth HTTP lato edge;
- ACL mature;
- dashboard/UI;
- traffic policies;
- rate limiting;
- osservabilità completa;
- gestione certificati automatica;
- route/rewrite avanzati da reverse proxy.

## Differenza architetturale principale

La differenza più importante è:

```text
frp/chisel/rathole/bore: porte e stream TCP/UDP

tubo: HTTP request/response + service discovery libp2p
```

Quindi `tubo` oggi è molto buono per:

```text
HTTP client -> tubo edge -> libp2p -> tubo service -> HTTP target
```

Ma non ancora per:

```text
Postgres TCP
SSH TCP
Redis TCP
TLS passthrough
DNS UDP
QUIC UDP
```

## Confronto HTTPS

| Progetto | HTTPS pubblico | HTTPS upstream | TLS passthrough |
|---|---:|---:|---:|
| tubo | Via reverse proxy esterno; non ancora diretto | Probabilmente sì con cert validi | No |
| frp | Sì | Sì | Sì/SNI patterns |
| ngrok | Sì | Sì | Sì, endpoint TLS |
| Cloudflare Tunnel | Sì | Sì | Limitato/secondo modalità |
| inlets | Sì | Sì | Via TCP mode |
| chisel/wstunnel | Trasporta TCP/TLS raw | Sì se TCP | Sì |
| rathole | TCP/TLS raw | Sì | Sì |
| bore | TCP raw | Sì | Sì |

## Confronto TCP/UDP

| Progetto | TCP | UDP | Note |
|---|---:|---:|---|
| tubo | No | No | Da implementare |
| frp | Sì | Sì | Molto completo |
| chisel | Sì | Sì | HTTP/WebSocket/SSH transport |
| wstunnel | Sì | Sì | WebSocket/HTTP2 transport |
| rathole | Sì | Sì | Performance-focused |
| bore | Sì | No | Minimalista |
| ngrok | Sì | No/limitato | Principalmente HTTP/S/TCP/TLS |
| Cloudflare Tunnel | Sì in certi scenari | Limitato/non generico | Forte su HTTP/private network |
| Tailscale Funnel/Serve | HTTP/S e TCP in vari casi | Non focus | Mesh/VPN style |

## Come rendere tubo più competitivo

Le feature con maggiore impatto sarebbero:

1. HTTPS upstream robusto.
2. HTTPS edge diretto.
3. TCP tunnel generico.
4. TLS passthrough/SNI routing.
5. Edge reverse proxy con path routing e rewrite.
6. Auth/ACL lato edge.
7. UDP semplice/best-effort.

## Valutazione finale

`tubo` non è ancora un sostituto completo di frp/chisel/rathole/ngrok.

Però ha un'identità diversa e interessante:

```text
self-hosted p2p service tunnel
con discovery, relay libp2p, private swarm e bootstrap/join locale
```

La feature che lo renderebbe davvero comparabile ai migliori tunnel general-purpose è il TCP generico.

La feature che lo renderebbe più distintivo, invece, è l'edge come reverse proxy applicativo dinamico basato sui service annunciati nello swarm.
