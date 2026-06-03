# Security notes

For the canonical 0.7.0 security/trust model, see [`security-model-0.7.md`](./security-model-0.7.md).
For the Discovery V3 namespace-discovery model, see [`discovery-v3-threat-model.md`](./discovery-v3-threat-model.md).
This file is the short operational summary.

## Current posture

- public bundle trust is established by a locally pinned bundle signing key;
- `service_id` is the secure identity for Discovery V3 service publication and exact service resolution;
- service publication requires a valid authority-signed `PublishLease`;
- connect authorization uses `ConnectAccessLease` / `ConnectRefreshLease` and service-side connect proofs;
- optional private swarm PSK and PeerID allowlist gating are supported;
- authority-side publish grants use `/tubo/grants/1.0`.

## Trust boundaries

- public relays are transport/bootstrap only, not authority or namespace-discovery holders by default;
- the bundle decides which trust roots are accepted locally;
- one active grant authority per public scope is the safe operating model;
- share-invite tokens are credentials and one-time bootstrap material.

## Limits

- metadata privacy is limited in the public model;
- Discovery V3 improves namespace metadata protection, but observable PubSub timing/size metadata still remains;
- Discovery V2 fallback is intentionally broken for discovery-enabled namespace runtime in `v0.9.0`;
- revocation is TTL/cache/fresh-state bounded unless issuer state is consulted;
- short-lived invite/redemption state should be treated as sensitive.

## Canonical model

- [`discovery-v3-threat-model.md`](./discovery-v3-threat-model.md)
- [`security-model-0.7.md`](./security-model-0.7.md)
