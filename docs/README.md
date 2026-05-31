# Docs

Canonical policy: tutta la documentazione tecnica vive in `docs/`, separata per categoria.

Il sito pubblico con documentazione navigabile è su **[www.tubo.click/docs/](https://www.tubo.click/docs/)**.

Project work, planning, implementation scope, and acceptance criteria are tracked in **GitHub Issues**.

Historical tracker items that still needed migration were captured in issue #180 before removing the old local tracker.

## Layout canonico

- `reference/` — riferimenti tecnici canonici
- `runbooks/` — guide operative e runbook
- `reports/` — report e campagne a tempo
- `comparisons/` — note comparative
- `archive/obsoletes/` — materiale storico o superato
- `docs/` — HTML pubblicato/compatibilità URL
- root assets: `index.html`, `CNAME`, `install.sh`, `.well-known/`, `.nojekyll`

## Documentazione canonica

### Reference

- [`reference/PROTOCOL.md`](./reference/PROTOCOL.md) — Wire protocol specification (binary framing, messages, streaming)
- [`reference/SECURITY.md`](./reference/SECURITY.md) — Current security policy, current limits, and links to the next security model
- [`reference/security-model-0.7.md`](./reference/security-model-0.7.md) — Canonical 0.7.0.b0 security/trust model, guarantees, and non-goals for ID-first discovery and scoped trust
- [`reference/VERSIONING.md`](./reference/VERSIONING.md) — Policy di versioning prodotto/protocollo e compatibilita' tra ruoli
- [`reference/cli.md`](./reference/cli.md) — CLI reference per `tubo`, YAML config e flussi locali/join

### Runbooks

- [`runbooks/OPERABILITY.md`](./runbooks/OPERABILITY.md) — Avvio componenti e runbook pratico per tunnel p2p (anche sicuro/private swarm) tra 2+ servizi
- [`runbooks/PROCESS_SUPERVISORS.md`](./runbooks/PROCESS_SUPERVISORS.md) — Strategia consigliata per systemd/launchd senza introdurre un demone centrale
- [`runbooks/discovery-multi-host.md`](./runbooks/discovery-multi-host.md) — Discovery as-is + practical multi-host runbook (LM Studio laptop <-> Hermes Linode)
- [`runbooks/LINODE_TERRAFORM_TESTBENCH.md`](./runbooks/LINODE_TERRAFORM_TESTBENCH.md) — Terraform stack + smoke harness per bench distribuito Linode multi-region
- [`runbooks/RELEASING.md`](./runbooks/RELEASING.md) — Checklist manuale per tag, changelog e GitHub Release

### Reports

- [`reports/FAILURE_CAMPAIGN_TWO_HOST_2026-04-29.md`](./reports/FAILURE_CAMPAIGN_TWO_HOST_2026-04-29.md) — Report della failure campaign sul bench distribuito a 2 macchine

### Comparisons

- [`comparisons/COMPARISON-TUNNELING-PROJECTS.md`](./comparisons/COMPARISON-TUNNELING-PROJECTS.md)

### Archive

- [`archive/obsoletes/README.md`](./archive/obsoletes/README.md) — Historical design notes superseded by the current canonical documents

## Compatibility notes

The legacy top-level markdown paths were removed; new work should link the canonical paths above.

## Planned documents

- `testing.md` — Test strategy and fixtures (*coming soon*)
