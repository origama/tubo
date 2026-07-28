# ADR 0002: pipe identity and one scope authority

Status: accepted

## Decision

`pipe_id` is cryptographically bound to a pipe-owner key and is the secure identity for records, publish grants, invites, leases, and proofs. Runtime PeerID is a replaceable hosting binding signed by the owner.

One logical authority per scope issues all membership, publication, forwarding, share, access, and refresh artifacts. Pipe runtimes do not issue delegated connect leases.

## Consequences

- duplicate display names are safe but ambiguous;
- origin restart/rebind does not change pipe identity;
- refresh has one issuer and one route;
- multi-active independent authorities are unsupported;
- authority availability is required for new issuance, while existing access remains TTL-bounded.
