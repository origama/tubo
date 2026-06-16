# Service / pipe / process lifecycle model (#200)

This is the design slice for the persistent lifecycle model behind #200.
It clarifies what is stable resource identity vs. what is runtime-only state.

## Reference code paths

- `internal/config/config.go` — current persistent service fields (`service_id`, `service_seed`, `service_owner_key_file`, `service_claim_file`, `service_publish_lease_file`, `grant_service_peer`, `grant_request_id`).
- `internal/workspace/service.go` + `internal/workspace/paths.go` — current service identity materialization and file locations.
- `cmd/tubo/local_service_claims.go` — attach-side publish authorization submit/poll/renew flow.
- `cmd/tubo/grants_cmd.go` — manual grant request/approval flow writes the same persistent service metadata.
- `internal/processes/processes.go` + `cmd/tubo/connect_detach.go` + `cmd/tubo/main.go` — detached runtime process state.
- `internal/app/service/app.go` — service runtime rechecks publish authorization from its files and handler.
- `internal/app/bridge/app.go` — bridge runtime keeps connect-leases and selected route data as runtime state.

## A. Resource model

### `service/<name>`
Persistent service definition. Owns stable identity and publication-related local artifacts.

Fields:
- name
- service_id
- service_kind
- target
- scope: cluster/namespace
- service_seed
- service_owner_key_file
- service_claim_file
- service_publish_lease_file
- grant_service_peer
- grant_request_id, when a publish-grant request is pending
- any already-existing last-known publish authorization metadata

Suggested storage: the cluster/namespace service entry in local config, e.g. `clusters/<cluster>/namespaces/<namespace>/services/<name>`.

### `pipe/<name>`
Persistent local consumer definition. Owns local listener intent and the pinned remote service identity after first resolution.

Fields:
- name
- service_ref
- pinned_service_id after first successful resolution
- scope: cluster/namespace
- local address
- service_kind
- connect mode: discovery/share-token/direct where applicable

Suggested storage: a peer resource tree beside services, e.g. `clusters/<cluster>/namespaces/<namespace>/pipes/<name>`.

### `process/<name>`
Runtime-only process/PID/log/status state. Owns observable runtime details, not stable identity.

Fields:
- PID
- log file
- state file
- runtime status
- selected peer id
- selected addr
- selected path
- last errors
- retry/backoff timestamps
- liveness status

Current storage: `~/.local/share/tubo/processes`, `~/.local/share/tubo/logs`, `~/.local/share/tubo/run`.

## B. Ownership rules

- `service_seed` must be stable for a service definition.
- `service_id` and `service_seed` are not runtime-only fields.
- `grant_request_id` belongs to the persistent service definition while the grant request is pending.
- process state may cache selected/runtime values, but must not become the source of truth for service identity.
- stale process cleanup must not delete service identity.
- stopping a process must not remove persistent service/pipe definitions.

## C. Attach semantics

- `tubo attach ...` = define service if missing + start service.
- If the definition exists and is compatible, reuse it.
- If the definition exists and conflicts, fail closed with a clear error.
- Attach renewal should reuse the persistent `grant_request_id` instead of submitting duplicate equivalent requests.
- If a pending request expires, attach may clear it and submit a new one.
- If a pending request is approved, attach must consume it, write the publish lease, clear the pending request id, and resume publication.

## D. Connect semantics

- `tubo connect ...` = define pipe if missing + start pipe.
- After first successful resolution, pipe should pin the resolved `service_id`.
- Runtime-selected peer/path is process state, not pipe definition.
- Rebind behavior is out of scope here and belongs to #208.

## E. Relationship to open issues

- #255 should implement publish-grant renewal idempotence using this service definition model.
- #256 is CLI/operator UX for grouped duplicate pending requests.
- #257 is diagnostic wording for degraded connect states.
- #202 is stale process recovery and must respect service/pipe definitions.
- #205/#206 should wait until this model is stable.
- #207/#208/#209/#211 are later resilience/policy layers.

## F. Non-goals

- no full lifecycle command implementation;
- no policy changes;
- no auto-approve changes;
- no PublishEpoch decision;
- no duplicate publisher decision;
- no migration of existing configs unless unavoidable.

## Current gaps

- `grant_request_id` is already stored in the service definition, but approval handling still does not consistently clear it after the lease is consumed.
- Detached `connect` currently persists only runtime process state; there is not yet a first-class persistent `pipe/<name>` config resource.
- The running service process does not watch config changes; it relies on the publish-authorization handler and lease files, so any future request-id clearing must stay file-backed and deterministic.
- `grant_service_peer` may be seeded or refreshed from discovery at runtime, but the persisted service definition remains the source of truth for durable identity and pending-request tracking; #249 can still rediscover and update a stale peer field.

## Why this slice helps #255

#255 can build on this model by making publish-grant renewal idempotent for one persistent service definition: one stable `service_id`, one stable `service_seed`, one pending `grant_request_id` at a time, and runtime process state that never owns identity.
