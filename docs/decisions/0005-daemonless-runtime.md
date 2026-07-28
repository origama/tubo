# ADR 0005: daemonless by default

Status: accepted

## Decision

Foreground is canonical; `-d` is an optional owned child process; OS supervisors are optional. No mandatory central daemon is introduced.

All execution paths use one runtime plan/executor and register a primary resource. Detached success is reported only after readiness and committed process identity.

## Consequences

- local development remains simple;
- process metadata may become stale and needs ownership safeguards;
- external supervisor integration is explicit rather than implicit;
- failure rollback must kill and wait for owned children.
