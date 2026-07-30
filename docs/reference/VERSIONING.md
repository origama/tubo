# Versioning

Tubo Rebuilt versions three surfaces independently.

## Product version

Semantic Versioning for the whole binary:

```text
MAJOR.MINOR.PATCH
```

During greenfield development the product remains pre-1.0. All runtime capabilities ship in one binary.

## Protocol version

Shared wire version:

```text
major.minor
```

Initial protocol: `2.0`; current negotiated authority snapshot extension: `2.1`.

- major: incompatible tunnel, record, authority, or trust semantics;
- minor: explicitly negotiated additive capability;
- patch product fixes do not alter protocol semantics.

There is no compatibility with prior Tubo protocol 1.x and no fallback handler may be added without an explicit product decision.

## State schema version

The transactional store uses an integer schema version independent of product and protocol. Every migration documents:

- forward transformation;
- rollback/backup behavior;
- failure behavior;
- whether concurrent old binaries are rejected;
- operator action.

## Current migration decision: schema 6 authority snapshot provenance

- product bump: unchanged (`0.1.0-dev`);
- protocol bump: authority extension `2.0` to `2.1`; `/tubo/authority/2.1` adds bounded strict `records.list` request/response metadata while tunnel and other authority operations remain `2.0`;
- state bump: schema 5 to 6, adding persistent `authority_snapshots` and snapshot digest; migration clears schema-5 locally derived freshness ordering/completion but retains cached records;
- mixed-version behavior: new consumers require authority 2.1 for deletion-capable reconciliation and fail closed without fallback. A 2.0 authority may retain its explicit handler surface but cannot drive new consumer deletion. Schema-5 binaries reject schema-6 state;
- rollout: stop affected local runtime, back up state, deploy schema-6 authority/origin first, verify authority 2.1 availability, then deploy consumers;
- rollback: stop schema-6 binaries and restore pre-schema-6 backup. Do not point a rolled-back consumer at schema-6 state; no downgrade migration exists;
- evidence: focused unit/in-process and race coverage. No three-machine 2.1 campaign run for this change.

## Current migration decision: schema 7 fenced periodic sync lease

- product bump: unchanged (`0.1.0-dev`);
- protocol bump: unchanged; lease state is local and periodic fetch still uses `/tubo/authority/2.1`;
- state bump: schema 6 to 7, adding `scope_sync_leases` with opaque token fencing and expiry;
- mixed-version behavior: schema-6 binaries reject schema-7 state. Schema-7 periodic ownership is local; no authority/wire compatibility fallback exists or is needed;
- rollout: stop affected local runtimes, back up state, deploy schema-7 binary, then restart membership-backed pipe runtimes. Local authority needs no membership token or self-sync;
- rollback: stop schema-7 binaries and restore pre-schema-7 backup. No downgrade migration exists;
- evidence: focused unit/in-process and race coverage plus checked-in isolated `periodic-sync` three-machine harness. No three-machine periodic-sync campaign has run for this change; only a retained exact run ID and artifact `report.json` with `result: pass` upgrades this evidence.

## Release notes

Every release records:

```text
Added
Changed
Fixed
Compatibility
```

Compatibility includes product version, protocol version, state schema version, mixed-node expectations, and operator action.

## Required decisions

Before merging a wire/state change, state explicitly:

1. product bump;
2. protocol bump or unchanged;
3. state schema bump or unchanged;
4. mixed-version behavior;
5. rollout and rollback instructions;
6. three-machine scenarios run.

## Current migration decision: schema 8 revocation and invite redemption

- product bump: unchanged (`0.1.0-dev`);
- protocol bump: unchanged (`2.1` snapshot extension; authority 2.0 adds documented operations);
- state bump: schema 7 to 8, adding transactional revocations, atomic invite redemption, and persisted client lease cache;
- mixed-version behavior: schema-7 binaries reject schema-8 state; older authority handlers do not support refresh/invite/revocation operations;
- rollout: back up state, deploy authority first, then clients/origin pipes;
- rollback: stop schema-8 binaries and restore pre-schema-8 backup; no downgrade migration;
- evidence: local unit/in-process and race coverage; no three-machine campaign run.

## Current migration decision: schema 10 session resume and path events

- product bump: unchanged (`0.1.0-dev`);
- protocol bump: tunnel adds `/tubo/tunnel-resume/2.1` for resume/control streams; existing `/tubo/tunnel/2.0` unchanged;
- state bump: schema 8 to 10, adding `sessions.current_transport_path` and `path_events` table;
- mixed-version behavior: schema 8/9 binaries reject schema 10 state; session resume requires both peers to advertise `session-resume-v1`;
- rollout: stop affected local runtimes, back up state, deploy schema 10 binary, then restart. Resume sessions only work when both peers are upgraded;
- rollback: stop schema 10 binaries and restore pre-schema 10 backup; no downgrade migration exists;
- evidence: local unit/in-process and race coverage; no three-machine campaign run.
