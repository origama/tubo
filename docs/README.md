# Tubo documentation

## Product and architecture — normative future direction

1. [`reference/HIGH_LEVEL_DESIGN.md`](reference/HIGH_LEVEL_DESIGN.md) — Tubo from first principles; L0–L5, authorization, infrastructure, discovery
2. [`reference/ARCHITECTURE.md`](reference/ARCHITECTURE.md) — implementation-boundary rules for A0 onward
3. [`reference/ONTOLOGY.md`](reference/ONTOLOGY.md) — canonical terms; prototype notes where applicable
4. [`reference/INVARIANTS.md`](reference/INVARIANTS.md) — binding invariants; prototype notes where applicable
5. [`reference/SECURITY.md`](reference/SECURITY.md) — security model; prototype threat/behavior reference where marked

## Implementation

- [`implementation/ROADMAP.md`](implementation/ROADMAP.md) — canonical A0–A4 roadmap and phase gates
- [`implementation/PROTOTYPE_BASELINE.md`](implementation/PROTOTYPE_BASELINE.md) — frozen reference baseline and reusable learned knowledge

## Contracts — current prototype/reference unless superseded by HLD

- [`reference/CLI.md`](reference/CLI.md)
- [`reference/PROTOCOL.md`](reference/PROTOCOL.md)
- [`reference/STATE.md`](reference/STATE.md)
- [`reference/VERSIONING.md`](reference/VERSIONING.md)

## Testing and operations

- [`testing/ARCHITECTURE_ACCEPTANCE.md`](testing/ARCHITECTURE_ACCEPTANCE.md) — A0–A4 architecture journeys
- [`testing/ACCEPTANCE_MATRIX.md`](testing/ACCEPTANCE_MATRIX.md) — prototype behavioral/security evidence
- [`runbooks/THREE_MACHINE_E2E.md`](runbooks/THREE_MACHINE_E2E.md) — prototype distributed runbook and isolation rules
- [`runbooks/OPERABILITY.md`](runbooks/OPERABILITY.md)

## Historical decisions and mapping

- [`decisions/`](decisions/) — historical decisions; reassess when they conflict with HLD
- [`testing/LEGACY_BEHAVIOR_MAPPING.md`](testing/LEGACY_BEHAVIOR_MAPPING.md)
- [`testing/LEGACY_FEATURE_GAP_ANALYSIS.md`](testing/LEGACY_FEATURE_GAP_ANALYSIS.md)

Implementation status lives in GitHub Issues. HLD and roadmap define future direction; prototype documents preserve evidence without silently becoming future architecture.
