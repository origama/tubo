# State and persistence

## Separation

Tubo uses three storage classes.

### Static configuration

Human-editable YAML contains contexts and operational defaults only:

```yaml
version: 1
current_context: team
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
membership_grants
publish_grants
forward_grants
access_leases
refresh_leases
discovery_records
revocations
invite_redemptions
schema_migrations
```

SQLite in WAL mode using a pure-Go driver is the preferred first implementation, subject to a small spike for cross-compilation and lock behavior. A different embedded store requires an ADR demonstrating equal transaction, crash, migration, query, and operational properties.

Transactions enforce:

- create-if-missing/reuse-if-compatible/conflict;
- one target per pipe;
- no fake success on commit failure;
- atomic invite redemption;
- atomic quota check plus grant insertion;
- monotonic revocation epochs;
- conditional lifecycle updates;
- schema migration before runtime start.

### Secrets

Private material stays outside the database where practical:

```text
~/.config/tubo/secrets/node.key
~/.config/tubo/secrets/swarm.key
~/.config/tubo/secrets/authorities/<cluster>.key
~/.config/tubo/secrets/pipes/<pipe-id>.key
~/.config/tubo/secrets/namespaces/<scope>/<key-id>.secret
```

Directories use `0700`; files use `0600`. Database references secrets by path/fingerprint, never by bytes in ordinary output.

## Home override

`TUBO_HOME` is the canonical all-in-one override for tests and isolated runs. When set:

```text
$TUBO_HOME/config.yaml
$TUBO_HOME/state.db
$TUBO_HOME/secrets/
$TUBO_HOME/logs/
$TUBO_HOME/run/
```

Without it, XDG config/data locations may be used. The three-machine harness always sets `TUBO_HOME` to run-specific directories.

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

Session rows contain dynamic route and counters. They are not pipe definitions.

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
- This repository has no migration requirement from previous Tubo formats.
- Import/export is an explicit product feature, not compatibility fallback.
