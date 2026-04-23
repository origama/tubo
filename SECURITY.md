# Security

## Threat model

This system exposes services over a p2p fabric. The main risks are:
- unauthorized peer registration
- tunnel or service hijacking
- replay of control frames
- abuse of relay infrastructure
- accidental exposure of local services
- tenant cross-talk
- DoS through stream or connection flooding

## Security properties

- no inbound ports on clients or services
- peers authenticate before registration
- services are authorized per tenant and route
- requests are logged with correlation IDs
- relay usage is rate limited
- local target access is deny-by-default

## Authentication

MVP recommendation:
- bearer tokens for service enrollment and client access
- peer identity binding in the control plane
- short-lived leases for registrations

## Authorization

Authorize by:
- tenant ID
- service name
- hostname
- path prefix
- method allowlist
- peer ID

## Replay protection

- nonce or request IDs for control messages
- short TTLs for enrollment and leases
- reject stale heartbeats and reused session IDs

## Secret management

- do not hardcode secrets
- load from environment or secret files in dev
- support external secret stores in later phases

## Operational guidance

- rotate credentials regularly
- log registration, revocation, and denied access events
- alert on relay spikes and failed heartbeats
- keep local target allowlists strict
