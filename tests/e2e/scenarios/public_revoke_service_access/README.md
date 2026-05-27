# public_revoke_service_access

Validates issuer-side service-access epoch revocation: incrementing the service access epoch rejects old ShareInvites and old ConnectRefreshLeases when the grant service has fresh revocation state.
