# ADR 0004: transactional state separate from configuration

Status: accepted, implementation choice pending spike

## Decision

Human-editable YAML stores contexts/defaults only. Mutable resources, grants, records, runs, revocations, and redemption state use one embedded transactional database. Secrets remain separate restricted files/keyring entries.

Pure-Go SQLite in WAL mode is preferred, subject to cross-platform verification.

## Consequences

- no YAML read-modify-write database behavior;
- concurrent operations use real transactions;
- schema migrations are explicit;
- tests can isolate all state with `TUBO_HOME`;
- database failure prevents successful mutation output.
