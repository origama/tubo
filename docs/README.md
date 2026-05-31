# Docs

Canonical policy: tutta la documentazione tecnica vive in `docs/`.

Il sito pubblico con documentazione navigabile è su **[www.tubo.click/docs/](https://www.tubo.click/docs/)**.

## Project tracking

Project work, planning, implementation scope, and acceptance criteria are tracked in **GitHub Issues**.

Historical tracker items that still needed migration were captured in issue #180 before removing the old local tracker.

## Documentazione disponibile

### Guide operative

- [`cli.md`](./cli.md) — Riferimento completo CLI: tutti i comandi, flag, env vars, config YAML, flussi cluster/namespace
- [`OPERABILITY.md`](./OPERABILITY.md) — Runbook operativo: avvio relay/attach/connect, private swarm PSK, setup multi-host
- [`PROCESS_SUPERVISORS.md`](./PROCESS_SUPERVISORS.md) — Integrazione con systemd/launchd per processi long-running
- [`discovery-multi-host.md`](./discovery-multi-host.md) — Discovery V2 as-is + runbook pratico multi-host

### Protocollo e sicurezza

- [`PROTOCOL.md`](./PROTOCOL.md) — Wire protocol specification (binary framing, frame types, streaming, versioning)
- [`SECURITY.md`](./SECURITY.md) — Security policy corrente, limiti operativi, regole di trust
- [`security-model-0.7.md`](./security-model-0.7.md) — Security model v0.7: garanzie, non-goal, trust chain per ID-first discovery

### Versioning e release

- [`VERSIONING.md`](./VERSIONING.md) — Policy di versioning prodotto/protocollo e compatibilità tra ruoli
- [`RELEASING.md`](./RELEASING.md) — Checklist manuale per tag, changelog e GitHub Release

### Testing e infrastruttura

- [`LINODE_TERRAFORM_TESTBENCH.md`](./LINODE_TERRAFORM_TESTBENCH.md) — Terraform stack + smoke harness per bench distribuito Linode multi-region
- [`FAILURE_CAMPAIGN_TWO_HOST_2026-04-29.md`](./FAILURE_CAMPAIGN_TWO_HOST_2026-04-29.md) — Report failure campaign sul bench distribuito a 2 macchine
- [`COMPARISON-TUNNELING-PROJECTS.md`](./COMPARISON-TUNNELING-PROJECTS.md) — Confronto con ngrok, Tailscale, Cloudflare Tunnel, frp

### Storico

- [`obsoletes/README.md`](./obsoletes/README.md) — Design notes superseded dai documenti canonici correnti
