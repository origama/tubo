# Security model 0.7.0.b0

Status: target design and implementation contract for the `0.7.0.b0` branch.

Parent issues:

- `#112` — layered security model with ID-first discovery and scoped trust leases
- `#113` — security guarantees, trust roots, and non-goals

This document turns the security assumptions from those issues into explicit guarantees, non-goals, and implementation rules for coding agents.

It supersedes older design notes that described alternative architecture directions. Historical proposal documents moved under `docs/archive/obsoletes/` remain useful for context, but this file is the canonical reference for the 0.7.0.b0 security model.

## 1. Core invariants

The following rules are the foundation of the 0.7.0.b0 design:

```text
Tubo authorizes service_id.
Tubo resolves service_id.
Tubo displays display_name.
```

Meaning:

- `service_id` is the secure identity used by authorization and discovery resolution.
- `display_name` is a human-readable, non-unique label.
- aliases are optional unique resources and are not part of the minimal public flow.

For 0.7.0.b0:

- duplicate `display_name` values are allowed;
- `connect --token ...` must bind to exactly one `service_id`;
- convenience lookup by name must not change the exactness of token-based connect.

## 2. Trust roots and authority introduction

### 2.1 Level 1 public trust root

For the public Level 1 flow (`public` overlay + `home/default`):

- the public issuer trust root must be pinned in the public Tubo bundle or another local trusted source;
- a `ShareInvite` must not make an arbitrary public issuer trusted just because the token says so;
- public trust is established by local bundle verification, not by token self-assertion.

Equivalent rule:

```text
A public token may refer to the public authority.
A public token may not define the public authority.
```

### 2.2 Private authority introduction

For private scopes (Level 2 / Level 3), an invite or scoped token may introduce a private authority as invitation / TOFU trust material.

That must be represented as:

```text
joining a private trust domain
```

not as:

```text
automatically extending public trust semantics
```

## 3. Security levels

## 3.1 Level 1 — public overlay + `home/default`

Purpose: easiest public sharing path. The shared public overlay is convenience transport only: it is not anonymity, hidden participation, or transport isolation by itself.

```text
overlay:   public
cluster:   home
namespace: default
relays:    public
issuer:    public issuer for home/default
join:      implicit
UX:        tubo attach ... / tubo connect --token ...
```

### Guarantees

Level 1 guarantees:

- `service_id`-based authorization;
- issuer-signed publish and connect material;
- proof-of-possession-bound connect access after invite redemption;
- `connect --token` redemption into a short-lived `ConnectAccessLease` plus bounded `ConnectRefreshLease` when the ShareInvite carries grant-service metadata;
- clean-machine `attach` / `connect --token` bootstrap when the public bundle is trusted locally.

### Non-goals

Level 1 does **not** guarantee:

- strong metadata privacy;
- anonymity from relays;
- hidden participation;
- traffic-pattern privacy;
- transport isolation from the shared public relay/operator path;
- immediate revocation without fresh online/cached state.

### Revocation model

Level 1 revocation is bounded by short TTLs unless online or cache-refreshed revocation state is explicitly enabled. The issuer-side revocation store supports revoked invite JTIs, revoked connect sessions, service-access epochs, and publish revocation records.

## 3.2 Level 2 — public overlay + private cluster/namespace

Purpose: public transport, private logical trust domain.

```text
overlay:   public
cluster:   private
namespace: private
relays:    public
issuer:    private scoped authority
join:      explicit or token/bundle-driven
```

### Guarantees

Level 2 guarantees:

- public relays are transport only, never authority;
- private namespace service metadata is hidden from non-members when protected by real namespace discovery keys;
- private scoped issuer controls membership, publish policy, connect policy, and revocation for that scope.

### Required mechanism

Level 2 metadata privacy requires a real secret:

```text
namespace_discovery_key
```

distributed through membership / join material.

This key must **not** be derived only from public or guessable identifiers such as `cluster_id` and `namespace_id`.

### Non-goals

Level 2 does **not** guarantee:

- anonymity from public relays;
- hidden participation;
- traffic-pattern privacy;
- invisibility of all control-plane metadata from the transport provider.

## 3.3 Level 3 — private overlay + private cluster/namespace

Purpose: strongest isolation.

```text
overlay:   private
cluster:   private
namespace: private
relays:    private
issuer:    private authority
join:      private bundle / swarm key / trusted bootstrap
```

### Guarantees

Level 3 requires:

- private overlay participation material (private bundle, swarm key, or trusted bootstrap equivalent);
- private relays;
- the same lease model for audit, revocation, and defense-in-depth.

### Non-goals

Level 3 still does not imply perfect secrecy or perfect unlinkability by itself; transport and operational hardening still matter.

## 4. Cross-cutting decisions for 0.7.0.b0

### 4.1 Identity model

- `service_id` is the secure resolution and authorization identity.
- `display_name` is a non-unique human label.
- aliases are optional unique resources with explicit claim / release / recovery semantics.

### 4.2 Issuer consistency model

For 0.7.0.b0, the issuer consistency model is:

```text
single active issuer per scope
```

where scope means the trust domain formed by overlay/trust root + cluster + namespace.

Non-goals for 0.7.0.b0:

- multi-active issuer for one scope;
- issuer consensus;
- quorum signing;
- HA issuance across independent writers.

If multiple processes serve the same scope in future, they must still behave as one logical issuer with strongly consistent state. That is out of scope for the first landing.

### 4.3 PoP granularity

For 0.7.0.b0, proof-of-possession is applied at:

```text
bridge / libp2p stream establishment
or bridge-session establishment
```

not to every proxied HTTP request unless the proxy protocol is explicitly extended.

Equivalent rule:

```text
Per-stream or per-session PoP is in scope.
Per-HTTP-request PoP is out of scope unless protocol changes say otherwise.
```

The PoP proof binds the client key, scope, `service_id`, access-lease hash, nonce/JTI, and issued-at timestamp. In `0.7.0.b0` replay protection is a local service-side cache; concurrent publisher instances for the same service need shared replay state before cross-instance replay protection can be claimed.

### 4.4 Revocation timing guarantees

All revocation semantics must be documented in one of two categories:

#### Offline validation

```text
revocation bounded by TTL
```

#### Online or cache-refreshed validation

```text
revocation bounded by cache freshness + TTL
```

0.7.0.b0 must not claim immediate revocation without explicitly requiring fresh state. When a grant service has fresh revocation state, it rejects revoked ShareInvite redemption, revoked session refresh, stale service-access epochs, and publish requests for publish-revoked services. Already-issued access leases remain TTL-bounded unless the service validator is extended with online/cache revocation checks.

## 5. 0.7.0.b0 implementation boundary

This document defines the security contract for the first implementation wave under `#112`, but it does not require every possible future feature to land at once.

The first secure core should prioritize:

1. stable service identity + `service_id`;
2. ID-first discovery semantics;
3. publish authorization bound to `service_id`;
4. exact token-based connect by `service_id`;
5. bounded connect renewal;
6. single active issuer per scope;
7. deterministic E2E coverage for duplicate display names and renewal behavior.

The following are intentionally non-core for the first landing unless trivial:

- verified aliases;
- Level 2 encrypted namespace discovery implementation details;
- Level 3 hardening beyond the shared lease substrate;
- multi-active issuer / HA issuance;
- immediate revocation promises;
- per-request HTTP PoP.

## 6. Developer checklist

Security-sensitive implementation work under `#112` should be checked against this file.

Before merging a subissue, confirm that it does **not** violate the following:

- public tokens do not introduce arbitrary public trust roots;
- Level 2 privacy does not rely only on public IDs as encryption input;
- a component does not silently assume multi-active issuer safety for one scope;
- token-based connect still resolves one exact `service_id`;
- revocation promises match the actual freshness model;
- PoP enforcement claims match the actual stream/session granularity.
