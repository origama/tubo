# Security Policy & Current Constraints

This document is the canonical high-level security reference for the repository.

For the target 0.7.0.b0 security contract and trust model, see:

- [`security-model-0.7.md`](./security-model-0.7.md)

That file is the normative design reference for the upcoming ID-first model under issues `#112` and `#113`.

## 1. Current security posture (as of v0.6.x)

Tubo currently relies on a layered but still evolving security model.

Implemented today:

- signed network bundle verification for the public bootstrap flow;
- local trust root pinning for the public bundle signing key;
- namespace-scoped Discovery V2 validation keyed by `service_id`, with service public key matching and mandatory authority-signed `PublishLease` for service publication;
- membership capability checks for namespace-scoped discovery/listing;
- connect-proof validation on the service side for tunneled traffic;
- optional private swarm PSK support across runtime roles;
- optional PeerID allowlist connection gating;
- authority-side publish grants via `/tubo/grants/1.0`.

## 2. Current operational rules

### 2.1 Public trust root

For the public `tubo-public` flow, trust in the public bootstrap path comes from a locally pinned bundle signing key and a verified public bundle.

Equivalent rule:

```text
Local trust roots decide whether the public bundle is accepted.
Public tokens do not create trust roots by themselves.
```

### 2.2 Public relays are transport, not authority

Relays provide transport/bootstrap/reachability.

They are not, by themselves:

- a publish authority;
- a membership authority;
- a service identity authority.

### 2.3 Single grant authority per public scope

With the current v0.6.x publish-grant model, public operation is safest when one scope has one active authority/grant service.

For example, `tubo-public` should currently use:

```text
one active Grant Service for home/default
```

Multiple public relays are fine. Multiple independent grant services for the same scope are not currently safe because they can approve conflicting state.

## 3. Current limitations and non-goals

### 3.1 Metadata privacy is limited in the current public model

Level 1 public operation is designed for ease of sharing, not strong metadata privacy.

### 3.2 Discovery V2 payload confidentiality is not yet a private-namespace-grade guarantee

Current Discovery V2 payloads are encrypted, but the key is derived from `cluster_id` and `namespace_id`.

That means current Discovery V2 payload protection should be treated as:

- scope separation / obscuring of payloads;
- not a strong private-namespace confidentiality boundary.

A future Level 2 private-namespace design must use a real secret `namespace_discovery_key`, not only public or guessable IDs.

### 3.3 Revocation is TTL-bounded unless fresh state is consulted

Current and near-term revocation semantics are bounded by:

- token / claim TTLs;
- cache freshness;
- renewal state.

Do not assume immediate revocation unless the specific mechanism explicitly says it depends on fresh online or refreshed cached state.

### 3.4 Current share-invite tokens are sensitive credentials

Share-invite tokens are connect-scoped and time-bounded, but they are still sensitive bootstrap material and must be handled like credentials.

## 4. 0.7.0.b0 direction

The next security model iteration moves toward:

- `service_id` as the secure identity;
- `display_name` as a non-unique human label;
- scoped issuer-signed publish and connect leases;
- bounded renewal loops for attach/connect;
- explicit guarantees and non-goals for public/private trust levels.

Canonical reference:

- [`security-model-0.7.md`](./security-model-0.7.md)
