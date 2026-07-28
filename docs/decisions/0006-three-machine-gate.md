# ADR 0006: real three-machine behavior is a release gate

Status: accepted

## Decision

`tuborelay`, `oripi`, and `macb` form the canonical real-network gate:

- `tuborelay`: stable public transport relay only;
- `oripi`: private-scope authority, local service, published origin pipe;
- `macb`: scope member and local consumer pipe.

The harness deploys run-specific binaries and state under `/tmp` and `generated/`; it never touches the installed Tubo binary, systemd units, or existing configuration.

## Consequences

- NAT/relay behavior is not inferred from Docker tests;
- tests require explicit acknowledgement and infrastructure reporting;
- release evidence includes HTTP, TCP/TLS, authorization, discovery, lifecycle, and path semantics;
- old three-machine commands are rewritten against pipe resources rather than preserved for compatibility.
