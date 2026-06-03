# Tests / E2E deterministic runner

This harness runs Docker-based scenarios, one at a time, with separate containers for actors and persistent per-actor state under `generated/e2e/<scenario>-<run-id>/`.

Available scenarios:

- `001-default-cluster-default-namespace`
- `collaboration_namespace_flows`
- `secret_backed_namespace_discovery`
- `public_duplicate_display_names`
- `public_one_time_share_invite`
- `public_stolen_access_token_rejected`

Usage:

```bash
tests/e2e/run.sh 001-default-cluster-default-namespace
tests/e2e/run.sh all
tests/e2e/run.sh clean
```

Available Make targets:

```bash
make e2e-default
make e2e
make e2e-clean
```

The runner:

- builds `tubo` and `dummy-api-server` from the current checkout;
- builds a small local Docker image with the newly built binaries;
- creates an isolated Docker network per scenario;
- starts actors in separate containers (`admin`, `alice`, `bob`);
- preserves logs and artifacts in the scenario workdir;
- removes network and containers after execution, unless `KEEP_WORK=1`.

The first scenario validates the basic happy path:

- relay container `admin`;
- Alice publishes an `e2e-echo` service and generates the `tubo share service/...` token;
- Bob starts from a clean config, performs implicit public join, and connects directly with `tubo connect --token`, without `tubo join cluster/home`.

The `collaboration_namespace_flows` scenario covers the collaboration branch: a `member` invite that can discover and connect by name, a `viewer` invite that can list but cannot open a connect lease, and a share invite that continues to work cross-scope even without namespace membership.

The `secret_backed_namespace_discovery` scenario covers the user-facing Discovery V3 happy path: a clean namespace member joins through a cluster invite and discovers an attached service by name. The mismatched-state and rotation grace/expiry cases are covered in the integration suite.

The `public_*` scenarios cover the security/discovery gates from `0.7.0.b0`: duplicate display names accepted only as distinct records by `service_id`, invalid leases rejected, stolen/expired/replayed connect proofs rejected, `ConnectAccessLease` auto-renewal, one-time invites even after attach restart and fresh-client retry, and issuer-side revocations for invite/session/service-access.
