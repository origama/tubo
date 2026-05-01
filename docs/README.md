# Docs

Canonical policy: tutta la documentazione tecnica vive in `docs/`.

Available documentation:

- [`PROTOCOL.md`](./PROTOCOL.md) — Wire protocol specification (binary framing, messages, streaming)
- [`SECURITY.md`](./SECURITY.md) — Security policy, threat model, and design constraints
- [`discovery-multi-host.md`](./discovery-multi-host.md) — Discovery as-is + practical multi-host runbook (LM Studio laptop <-> Hermes Linode)
- [`OPERABILITY.md`](./OPERABILITY.md) — Avvio componenti e runbook pratico per tunnel p2p (anche sicuro/private swarm) tra 2+ servizi
- [`VERSIONING.md`](./VERSIONING.md) — Policy di versioning prodotto/protocollo e compatibilita' tra ruoli
- [`cli.md`](./cli.md) — CLI reference per `tubo`, YAML config e topology
- [`LINODE_TERRAFORM_TESTBENCH.md`](./LINODE_TERRAFORM_TESTBENCH.md) — Terraform stack + smoke harness per bench distribuito Linode multi-region
- [`FAILURE_CAMPAIGN_TWO_HOST_2026-04-29.md`](./FAILURE_CAMPAIGN_TWO_HOST_2026-04-29.md) — Report della failure campaign sul bench distribuito a 2 macchine

Planned documents (not yet written):
- `testing.md` — Test strategy and fixtures (*coming soon*)
