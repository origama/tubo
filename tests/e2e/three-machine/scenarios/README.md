# Three-machine scenario contracts

`private-http.sh` validates private member/viewer HTTP relay path.

`periodic-sync.sh` validates schema-7 periodic authority sync with isolated state:

```text
tuborelay: isolated relay
goripi:     authority home + separate publisher-member home
macb:       one member home, two live local pipe contenders
```

Authority home creates `solar/default` but never joins membership. Publisher home joins with publisher membership, owns origin publication, and may contend. Macb creates `pipe/sync-a` and `pipe/sync-b` in same SQLite state; one local per-scope lease owner exists. This is local scheduling ownership, not global network authority ownership.

Run only after preflight and operator confirmation:

```bash
make e2e-three-machine-preflight
TUBO_E2E_CONFIRM=three-machine \
  make e2e-three-machine E2E_SCENARIO=periodic-sync
```

Scenario verifies distinct ready contenders; election `last_success` strictly later than pre-contender baseline within 90s (60s cadence + 6s jitter + 20s attempt deadline + polling); exact PID/root/resource-validated `SIGKILL` without CLI release; no successor before stored lease expiry; successor `last_success` strictly later than pre-crash baseline within same 90s bound using local scope status; relay loss/restart with stable PeerID; verified cache survival; and post-recovery cached-only convergence to a new exact pipe ID. It permits configured polling bounds: takeover 65s, outage 120s, recovery 180s.

Artifacts are under `generated/e2e-three-machine/<run-id>/artifacts/`. `report.json`, phase JSONL, redacted scope projections, cached listings, relay status, and safe process resource/status projections are retained. Every `*-ps-*.json` retains only `primary_ref` and `status`; PID, peer ID, address, and process identity data stay in private cleanup manifests outside artifacts. Invites, swarm keys, database/home copies, service targets, and fencing tokens are rejected. Harness availability is not campaign evidence: record a successful run ID and retained `report.json` before claiming 3M evidence.

All scenarios use run-specific binaries, homes, ports, PIDs, and roots. `KEEP_WORK=1` preserves exact owner-only run roots for debugging after artifact redaction; default cleanup removes them. No scenario may use installed Tubo paths, system services, `pkill`, or broad deletion.
