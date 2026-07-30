# Documentation

Technical contracts for Tubo Rebuilt live here.

## Canonical reference

- [`reference/ARCHITECTURE.md`](reference/ARCHITECTURE.md) — components and dependency direction
- [`reference/ONTOLOGY.md`](reference/ONTOLOGY.md) — service, pipe, run, session, record
- [`reference/INVARIANTS.md`](reference/INVARIANTS.md) — binding product and security invariants
- [`reference/CLI.md`](reference/CLI.md) — target command and output contract
- [`reference/PROTOCOL.md`](reference/PROTOCOL.md) — protocol 2.1 authority snapshot extension and 2.0 data/control plane
- [`reference/SECURITY.md`](reference/SECURITY.md) — trust boundaries and threat model
- [`reference/STATE.md`](reference/STATE.md) — static config, transactional state, secrets
- [`reference/VERSIONING.md`](reference/VERSIONING.md) — product, protocol, and state versions

## Operations and testing

- [`runbooks/OPERABILITY.md`](runbooks/OPERABILITY.md)
- [`runbooks/THREE_MACHINE_E2E.md`](runbooks/THREE_MACHINE_E2E.md)
- [`testing/ACCEPTANCE_MATRIX.md`](testing/ACCEPTANCE_MATRIX.md)
- [`testing/LEGACY_BEHAVIOR_MAPPING.md`](testing/LEGACY_BEHAVIOR_MAPPING.md)
- [`testing/LEGACY_FEATURE_GAP_ANALYSIS.md`](testing/LEGACY_FEATURE_GAP_ANALYSIS.md) — legacy capability gaps, v2 compatibility, and effort classification

## Decisions

- [`decisions/0001-service-pipe-ontology.md`](decisions/0001-service-pipe-ontology.md)
- [`decisions/0002-pipe-identity-and-authority.md`](decisions/0002-pipe-identity-and-authority.md)
- [`decisions/0003-single-duplex-tunnel.md`](decisions/0003-single-duplex-tunnel.md)
- [`decisions/0004-transactional-state.md`](decisions/0004-transactional-state.md)
- [`decisions/0005-daemonless-runtime.md`](decisions/0005-daemonless-runtime.md)
- [`decisions/0006-three-machine-gate.md`](decisions/0006-three-machine-gate.md)

Implementation status belongs in GitHub Issues once the repository has a remote. Documentation describes contracts and does not act as a local task tracker.
