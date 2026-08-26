# State and persistence

**Prototype reference contract:** This document describes `prototype-v2-final` persistence. Future code follows [`HIGH_LEVEL_DESIGN.md`](HIGH_LEVEL_DESIGN.md): persistence is adapter behind narrow consumer-defined capabilities, while one embedded transactional SQLite database may remain implementation. Current schema/tables/migrations are not automatic A0–A4 compatibility constraints.

Implementation status: schema version 10 uses pure-Go SQLite in WAL mode. Current tables cover services, pipes, scopes, memberships, verified records, per-scope discovery freshness, authority snapshots, per-scope sync leases, runs, supervisor registrations, sessions, peer aliases, per-run network statistics, observed runtime peers, and migrations. Grant/revocation/redemption tables are added with corresponding features, not as inert placeholders.

## Separation

Tubo uses three storage classes.

### Static configuration

Human-editable YAML contains contexts and operational defaults only. Current implementation writes overlay settings atomically to `config.yaml`; scope/default expansion follows this model:

```yaml
version: 1
current_context: team
current_scope: solar/default
contexts:
  team:
    overlay:
      relays: []
      swarm_key_file: ~/.config/tubo/secrets/swarm.key
    scope:
      cluster: solar
      namespace: default
    authority:
      public_key: "..."
      peers: []
```

It does not contain service definitions, pipe definitions, runs, leases, discovery records, revocations, or redemption state.

Precedence:

```text
CLI flag > environment > context config > default
```

Explicit false/zero values must be representable; do not use merge logic that treats false as unset.

### Transactional state

An embedded database stores mutable metadata:

```text
services
pipes
pipe_bindings
runs
sessions
peer_aliases
runtime_stats
runtime_peers
supervisors
membership_grants
publish_grants
forward_grants
access_leases
refresh_leases
discovery_records
scope_freshness
authority_snapshots
scope_sync_leases
revocations
invite_redemptions
schema_migrations
```

SQLite in WAL mode using a pure-Go driver is the preferred first implementation, subject to a small spike for cross-compilation and lock behavior. Write-bearing Store transactions use database-native `BEGIN IMMEDIATE` arbitration, reserving SQLite's one writer before mutation-controlling reads while leaving WAL readers and the configured pool available. A different embedded store requires an ADR demonstrating equal transaction, crash, migration, query, and operational properties.

Transactions enforce:

- create-if-missing/reuse-if-compatible/conflict;
- one target per pipe;
- no fake success on commit failure;
- atomic invite redemption: insert-first unique-key arbitration on `(jti, pipe_id)` commits one winner; same-client retry is idempotent without changing its stored session, and another client is denied; persistence errors fail closed;
- atomic quota check plus grant insertion;
- monotonic revocation epochs;
- conditional lifecycle updates;
- complete pinned-authority 2.1 discovery snapshots atomically upsert verified records, prune expiry, remove absent records, and commit trusted freshness after writer reservation; no retry/backoff replays a snapshot transaction;
- partial, peer/cache, malformed, 2.0, rollback, same-generation-ambiguous, lease-lost, canceled, or persistence-failed refreshes preserve prior valid records;
- schema migration before runtime start, transactionally; cancellation observed after writer acquisition aborts before migration mutation.

#### Discovery freshness and snapshots

`scope_freshness` is consumer-local observation metadata, never authority. Each scope stores last attempt/success, source, completion, stable error class, bounded next retry, accepted authority generation/high-water, and canonical snapshot digest. A refresh before `next_retry` fails with stable `discovery_retry_after` without opening a fresh authority dial. `authority_snapshots` is separate authority-owned persistent metadata: authority-global generation plus authority mutation high-water, read atomically with authority records. Generic cache expiry cleanup touches only discovered rows; authority snapshot reads prune authority-owned expiry and advance metadata in same transaction.

Only a bounded successful `/tubo/authority/2.1` `records.list` response from pinned scope authority is a complete deletion-capable snapshot. Reconciliation samples expiry and rechecks a lease fence only after its immediate writer reservation, so a waiting stale owner cannot commit. CLI cache-first inventory treats this observation as fresh for strictly less than five minutes; this consumer-side TTL never grants authority or changes record verification. Every record is parsed and verified against pinned authority, scope, owner-derived `pipe_id`, signature, publication grant, expiry, and sequence before one reconciliation transaction begins. Client requires nonzero authority generation, rejects generation/high-water rollback and same-generation changed digest/high-water, then may remove absent discovered-cache records only; local and authority rows are retained. Per-pipe sequence is replay protection only, never scope snapshot ordering. Cache, relay, peer, malformed, partial, and 2.0 responses may only preserve/add verified records and never authorize deletion. Expired records are pruned and never resolve.

#### Periodic sync lease

`scope_sync_leases` has one row per scope: non-secret holder ref/process identity, opaque fencing token, acquisition/renewal times, and expiry. Acquire, renew, and release compare scope plus token and holder identity atomically. An expired lease can transfer after committed expiry; stale PID/token holders cannot renew, release, reconcile, or record refresh failure for a successor. Lease commit precedes a scheduler attempt; storage errors grant no in-memory ownership. Token and process identity never appear in status output.

Membership-backed published and local-pipe runtimes contend locally every bounded interval with bounded jitter. Owner renews before expiry and gives each shared authority-2.1 fetch a 20-second outer deadline; renewal loss/cancellation stops later attempts. Persisted `next_retry` remains final dial suppression. Final reconcile and failure freshness writes recheck current unexpired lease fencing in same transaction. Local authorities do not self-sync or mint membership tokens: authority records remain local source-of-truth.

## Secrets

Private material stays outside the database where practical:

```text
~/.config/tubo/secrets/node.key
~/.config/tubo/secrets/swarm.key
~/.config/tubo/secrets/authorities/<cluster>.key
~/.config/tubo/secrets/pipes/<pipe-id>.key
~/.config/tubo/secrets/exact-authorities/<pipe-id>.key
~/.config/tubo/secrets/namespaces/<scope>/<key-id>.secret
```

Directories use `0700`; files use `0600`. Pipe owner keys and Exact Authority keys share one strict file format: canonical unpadded raw-base64url Ed25519 private key bytes, decoded length exactly 64 bytes, encoded length exactly 86 bytes, with no whitespace, padding, trimming, envelope metadata, or SQLite row. Database references secrets by path/fingerprint, never by bytes in ordinary output.

## Home override

`TUBO_HOME` is the canonical all-in-one override for tests and isolated runs. When set:

```text
$TUBO_HOME/config.yaml
$TUBO_HOME/state.db
$TUBO_HOME/secrets/
$TUBO_HOME/logs/
$TUBO_HOME/run/
```

Without it, the OS user config directory is used. The three-machine harness always sets `TUBO_HOME` to run-specific directories. `config validate` checks the static overlay and secret-file permissions; `config print` masks secret paths.

## Runtime registry

A run row contains:

```text
primary_ref
pid and process identity
source: foreground|detached|systemd|launchd
readiness/control endpoint
log reference
capabilities
aggregate status
authorization status
started/updated timestamps
```

Session rows contain dynamic route, current transport path, and application counters. `path_events` tracks session transport path transitions with epoch, from/to path, reason, committed byte offsets, and timestamp. `runtime_stats` stores per-run libp2p byte/rate/connection snapshots. `runtime_peers` is a replace-on-sample view keyed by run and observed PeerID; rows cascade when a run is deleted and are cleared on orderly stop.

## Supervisor registration

`supervisors` maps a primary ref to an operator-selected manager, exact unit label, and unit path. It is separate from runs so an installed unit may exist while its resource is stopped. A supervisor record is scheduling metadata, never authority or resource identity. Deleting a stale run never deletes a supervisor registration. `launchd` install commits the registry only after exact-label bootstrap; bootout/uninstall waits for matching process identity before registry removal.

## Detached startup

1. reserve a run identity transactionally;
2. spawn the child;
3. wait for the explicit readiness contract;
4. commit PID/process identity and ready state;
5. print success.

Any failure after spawn kills and waits for the owned child, removes/resolves the reservation, retains diagnostic logs, and returns failure.

## Backups and migrations

- State schema has its own monotonically increasing version.
- Migrations run transactionally and retain a backup before destructive transformations.
- Schema 6 adds `authority_snapshots` and `scope_freshness.snapshot_digest`; schema 5 freshness generation/high-water was locally derived, so migration transactionally clears only that untrusted ordering metadata and completion state while retaining valid cached records.
- Schema 7 adds `scope_sync_leases` without changing cached records or authority snapshot metadata.
- Schema 8 adds transactional `revocations`, one-time `invite_redemptions`, and encrypted-state lease cache rows. Older binaries reject schema 8. No downgrade migration exists; restore pre-upgrade backup before rollback.
- Schema 9/10 adds `current_transport_path` to sessions and a `path_events` table tracking session transport path transitions with epoch, committed byte offsets, and reason. Older binaries reject schema 10. No downgrade migration exists.
- This repository has no migration requirement from previous Tubo formats.
- Import/export is an explicit product feature, not compatibility fallback.

## Owner-bound Exact Authority key state (step 6B1b + 6B2)

Step 6B1b adds filesystem secret state only, not SQLite. Dedicated Exact Authority keys live under `secrets/exact-authorities/<pipe-id>.key`; existing Pipe owner key callers are re-grounded on the same `internal/localkey` primitive. `Load` checks directory and key modes, final symlink status, canonical encoding, exact length, and bounded size without creating entries. `Provision` is trusted-local only and publishes by temp write, file fsync, final no-clobber link, temp removal, directory fsync, then success; concurrent provisioners serialize and losers load the durable committed key. Failed write/fsync/dir-fsync reports failure and no store/table mutation occurs.

Step 6B2 adds SQLite-backed storage for exact authority revocation/epoch state and atomic invite redemption, accessed through narrow consumer-defined interfaces (`exactauthority.RevocationState` and `exactauthority.RedemptionStore`). The adapter `store.ExactAuthorityRevocationState` (in `internal/store`) translates between these interfaces and the existing store methods (`IsRevoked`, `RevocationEpoch`, `RedeemInvite`). The adapter lives in the store package so `internal/exactauthority` never imports `*store.Store`.

`.RedeemInvite` implements the one-winner atomic redemption contract: the first call with a given invite JTI succeeds, subsequent calls with different subjects fail, and same-subject retries are idempotent. Redemption persists the winning client public key, session ID, and pipe ID atomically.

## L4 consumer-owned persistence ports (step 8A)

Step 8A (issue #59) adds no schema change and no new tables: durable Service/Pipe definition access routes through narrow consumer-owned ports (`usecase.ServiceRepository`, `usecase.PipeRepository`) satisfied by composition-root adapters `store.ServiceRepoAdapter`/`store.PipeRepoAdapter` over the existing `services` and `pipes` tables (same `Store.PutService`/`Service`/`DeleteService` and `PutPipe`/`Pipe`/`DeletePipe` methods; the Pipe port stores the owner-key path and drops the publish-grant argument, which remains a store-level scoped-publishing concern). Durable definitions and transient state stay separate: `runs`/`sessions`/`path_events` tables are never touched by the ports, transient Run/Session writes never mutate definition rows, and Pipe identity (Ref, PipeID, Ingress, Target) survives repository restart. Owner/local keys continue to live under `secrets/pipes/<name>.key` (owner identity) and `secrets/exact-authorities/<pipe-id>.key` (dedicated Exact Authority, provisioned once by ShareExact via `exactauthority.LocalOwnerWorkflow`).
