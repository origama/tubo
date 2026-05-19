# public_revoke_session

Validates issuer-side connect session revocation: a revoked `ConnectRefreshLease` session can no longer mint new access leases. Existing access leases remain TTL-bounded unless online service validation is enabled.
