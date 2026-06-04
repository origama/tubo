# Docs

Canonical policy: all technical documentation lives in `docs/`, separated by category.

Project work, planning, implementation scope, and acceptance criteria are tracked in **GitHub Issues**.

## Canonical layout

- `reference/` — technical references
- `runbooks/` — operational guides
- `reports/` — time-bound reports
- `comparisons/` — comparison notes
- `archive/obsoletes/` — historical or superseded material
- root assets — `index.html`, `CNAME`, `install.sh`, `.well-known/`, `.nojekyll`

## Canonical docs

### Reference

- [`reference/cli.md`](./reference/cli.md)
- [`reference/PROTOCOL.md`](./reference/PROTOCOL.md)
- [`reference/SECURITY.md`](./reference/SECURITY.md)
- [`reference/discovery-v3-threat-model.md`](./reference/discovery-v3-threat-model.md)
- [`reference/security-model-0.7.md`](./reference/security-model-0.7.md)
- [`reference/VERSIONING.md`](./reference/VERSIONING.md)

### Runbooks

- [`runbooks/OPERABILITY.md`](./runbooks/OPERABILITY.md)
- [`runbooks/PROCESS_SUPERVISORS.md`](./runbooks/PROCESS_SUPERVISORS.md)
- [`runbooks/discovery-multi-host.md`](./runbooks/discovery-multi-host.md)
- [`runbooks/LINODE_TERRAFORM_TESTBENCH.md`](./runbooks/LINODE_TERRAFORM_TESTBENCH.md)
- [`runbooks/RELEASING.md`](./runbooks/RELEASING.md)

### Reports

- [`reports/FAILURE_CAMPAIGN_TWO_HOST_2026-04-29.md`](./reports/FAILURE_CAMPAIGN_TWO_HOST_2026-04-29.md)
- [`reports/TCPRAW-PERF-ISSUE-198-INITIAL.md`](./reports/TCPRAW-PERF-ISSUE-198-INITIAL.md)

### Comparisons

- [`comparisons/COMPARISON-TUNNELING-PROJECTS.md`](./comparisons/COMPARISON-TUNNELING-PROJECTS.md)

### Archive

- [`archive/obsoletes/README.md`](./archive/obsoletes/README.md)

## Notes

- Current user-facing transport scope: HTTP reverse proxying plus raw TCP/TLS passthrough (`service_kind=http|tcp`).
- `gateway` remains HTTP ingress; `attach`/`connect` may now publish/expose either HTTP or raw TCP depending on the service kind.
- Keep new links pointed at the canonical paths above.
- Historical tracker items were migrated into GitHub issues; do not reintroduce a local tracker.
- Lightweight repo hygiene check: `make verify-repo-hygiene` (or `./tests/verify-repo-hygiene.sh`).
