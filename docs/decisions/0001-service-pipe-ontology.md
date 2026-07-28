# ADR 0001: local services and network pipes

Status: accepted

## Decision

A service models a local terminal and is never advertised. A pipe is a persistent directed resource with one ingress and one service/pipe target. Only pipes have network identity, publication, access policy, runs, and sessions.

`attach` and `connect` remain UX presets that create/start pipe plans; they are not runtime roles.

## Consequences

- one pipe engine replaces service and bridge runtimes;
- multiple independently governed pipes may expose one service;
- discovery and grants target `pipe_id`;
- local service targets remain private;
- remote users discover connectivity offers rather than local process definitions.
