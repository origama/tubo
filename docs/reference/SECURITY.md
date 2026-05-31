# Security notes

For the canonical 0.7.0 security/trust model, see [`security-model-0.7.md`](./security-model-0.7.md).
This file is the short operational summary.

## Current posture

- public bundle trust is established by a locally pinned bundle signing key;
- `service_id` is the secure identity for Discovery V2 and service publication;
- service publication requires a valid authority-signed `PublishLease`;
- connect authorization uses `ConnectAccessLease` / `ConnectRefreshLease` and service-side connect proofs;
- optional private swarm PSK and PeerID allowlist gating are supported;
- authority-side publish grants use `/tubo/grants/1.0`.

## Trust boundaries

- public relays are transport/bootstrap only, not authority;
- the bundle decides which trust roots are accepted locally;
- one active grant authority per public scope is the safe operating model;
- share-invite tokens are credentials and one-time bootstrap material.

## Limits

- metadata privacy is limited in the public model;
- Discovery V2 payload confidentiality is scope separation, not private-namespace secrecy;
- revocation is TTL/cache/fresh-state bounded unless issuer state is consulted;
- short-lived invite/redemption state should be treated as sensitive.

## Canonical model

- [`security-model-0.7.md`](./security-model-0.7.md)
