# public_connect_auto_renew

Validates the #120 connect lease flow:

- ShareInvite redemption issues a client-key-bound ConnectAccessLease and ConnectRefreshLease;
- the bridge refreshes short-lived access leases before expiry;
- requests keep succeeding after the original access lease has expired.
