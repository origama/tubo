# A0 Step 10B — Distributed Journey-A zero-config exact-share proof (evidence summary)

Status: **COMPLETE**. This document is the sanitized summary of the real distributed proof recorded under evidence root `/tmp/a0-10b-final/evidence` (manifest.txt lists all 37 files with sha256; content hashes verified). It is the acceptance source of truth for A0 exit criterion #12 (ROADMAP) and the criteria 1-12 reconciliation table in [`ARCHITECTURE_ACCEPTANCE.md`](ARCHITECTURE_ACCEPTANCE.md) and [`ACCEPTANCE_MATRIX.md`](ACCEPTANCE_MATRIX.md).

- Exact merged main: `c031185c1cd45bf4e58fdbad16f64df2fd76999b` (PR #70 merge; binary module build `v0.0.0-20260828112205-c031185c1cd4`).
- Machines: A (owner) = oripi, B (recipient) = macb, R = relay.tubo.click (public, no PSK, pinned PeerID `12D3KooWM2hGwzrJYGyjUwtzrzLZuPWAmjiG8JeNM5YYJNPeHLME`). Relay was NOT modified for this proof.
- This document contains no invite values, leases, keys, tokens, seeds, or private key material. The invite JTI and value are redacted; public PeerIDs are shown in full because they are public routing identity.
- Independent verification: every evidence file's sha256 matches manifest.txt except the manifest's own (stale self-entry); actual file count is 37 (summary.md's "38" is a typo).

## Phase-by-phase results (issue #68 procedure)

| Step | Result |
|------|--------|
| 1 fresh owner home | PASS (oripi `/tmp/a0-10b-final/owner`) |
| 2 localhost HTTP fixture, deterministic small + 7 MiB (sha256 `67560aff…51e17`) | PASS |
| 3 `attach http://127.0.0.1:39101 --name a0-final -d` | PASS ready |
| 4 ready runtime verified | PASS (status ready, PID 1246901) |
| 5 `share pipe/a0-final` (canonical invite) | PASS exit 0, invite len 1604, mode-0600 |
| 6 canonical locator proof | PASS: single relay `/p2p`, single `/p2p-circuit`, final `/p2p` == owner peer; ready/live gate |
| 7 fresh recipient home | PASS (macb) |
| 8 `connect --token <invite> --local 39102 -d` | PASS (pid 76227) |
| 9 readiness probe (deterministic, no sleeps) | PASS listener ready on probe 1 |
| 10 real small HTTP 200, body `tubo-a0-final` | PASS |
| 11 ≥3 sequential requests all correct | PASS (3x HTTP 200, 13 B, fixture log matches) |
| 12 large 7 MiB transfer exact size + sha256 | PASS 7340032 B, sha256 `67560aff…51e17` |
| 13 post-large small request still works | PASS |
| 14 REAL relay traversal A→relay→B | PASS (path relayed both ends; byte-count symmetry owner-out == recipient-in 7341674; relay-side sockets both IPs) |
| 15 final state scopes/membership/discovery/PipeRecord = 0 | PASS (`get scopes` `{}` both; `get pipes` local-only) |
| 16 DefaultResolver embedded bundle only | PASS (config.yaml absent both; bundle tests; relay via `ResolveDefault`) |
| 17 tampered invite negative | PASS rejected `authorization_denied`; no Session; no fixture hit |
| 18 valid path after negative | PASS (fresh home, new invite, small OK) |
| 19 Journey-D restart | PASS same PipeID + node identity, new PID (1246901 → 1249418) |
| 20 component negatives | PASS 20 named exact-head tests, 0 failures (invalid/missing lease, wrong proof, expiry, revocation, no-revocation-state, tampered binding, no-private-key bundle) |
| 21 no-shortcut audit | PASS (no-shortcut-audit.txt) |

## Canonical owner redemption locator facts (step 6)

From owner runtime state (oripi, fresh home) plus share wiring (`owner_locator.go`, `node.CircuitAddress`, `usecase.ShareExact`):

- Owner runtime peer_id: `12D3KooWBhp1ontddoTosKP9qKQaRgKNngTGuvY9oHi27YzT5JLJ`
- status ready, source detached, PID alive, connected_peers 1 (relay), sessions 0 (before recipient connect)
- address: `/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWM2hGwzrJYGyjUwtzrzLZuPWAmjiG8JeNM5YYJNPeHLME/p2p-circuit/p2p/12D3KooWBhp1ontddoTosKP9qKQaRgKNngTGuvY9oHi27YzT5JLJ`
- Canonical-form check PASS: exactly one relay `/p2p` == pinned bundle relay PeerID, exactly one `/p2p-circuit`, final `/p2p` == owner runtime PeerID.
- Share gate: `LocateOwner` requires the ready/live owner Pipe runtime and equality of `run.Address` with `CircuitAddress(relay, ownerPeer)`; any failure fails closed `runtime_unavailable` before invite output (no invite, no authority provisioning).
- Invite carries only the redemption locator binding; no local Service target, Scope, discovery, or application metadata inside the invite binding (`accesslease.NewExactInvite` with `RedemptionPeers=locator` only).
- Invite (redacted): single canonical mint used for the entire proof; 10-minute exact TTL window; persisted only at `/tmp/a0-10b-final/invite-owner.txt` mode-0600; JTI/value redacted from all evidence.
- Recipient-side redemption dials exactly this address (`ExactConnectionState.Locator` == redemption address from invite; verified on recipient).

## REAL relay traversal evidence (step 14)

Topology: A = oripi `172.236.202.99` (public), B = macb (private `10.139.19.129` → public `2.43.160.235`), R = relay.tubo.click `172.232.189.160` (public, no PSK). Recipient dials ONLY the canonical circuit locator from the invite:

```
/dns4/relay.tubo.click/tcp/4001/p2p/<relay-peer>/p2p-circuit/p2p/<owner-peer>
```

Endpoint path/session evidence (tunnel live, after traffic):

| Endpoint | path | sessions | bytes_in | bytes_out | peer_id |
|---|---|---|---|---|---|
| B (recipient) | relayed | 11 | 7341674 | 880 | `12D3KooWLZ3zV6WVEWXbVk3vEaRD8a2sugtQQg8MD7fua7gWt9Ba` |
| A (owner) | relayed | 11 | 880 | 7341674 | `12D3KooWBhp1ontddoTosKP9qKQaRgKNngTGuvY9oHi27YzT5JLJ` |

- Byte symmetry across the relay: B bytes_in == A bytes_out == **7341674** (7 MiB transfer + request overhead); B bytes_out == A bytes_in == **880** (small requests). Both runtimes connected_peers=2, open_connections=3 (each peer: TCP to relay + circuit/stream).
- Relay-side observation (ssh tuborelay, `ss -tn` state established sport=:4001) while session live:
  - `172.232.189.160:4001` established from `172.236.202.99:34727` (oripi/owner)
  - `2.43.160.235:62731`, `:59351`, `:52230` (macb/recipient) (+ `:56136` long-lived legacy attach, unrelated)
  - `ss -tin` per socket shows bidirectional data on both (bytes_sent/bytes_received nonzero), i.e. the relay physically carries A↔B circuit-v2 traffic.
- Conclusion: real A→relay.tubo.click→B fallback; not merely "both peers can reach the relay" — application traffic (11 HTTP sessions, 7341674-byte large transfer intact, exact sha256) traversed the relay, visible as `relayed` path on both endpoints and as parallel relay-side sockets under both source IPs carrying both directions.

## Negatives

- Tampered invite (byte-flipped token): `tubo: authorization_denied: invalid exact invite: accesslease: invalid encoding` — no Session, no fixture hit (negative/tampered-invite.log, post-negative-validity.txt).
- Post-negative validity: fresh home, NEW canonical invite, connect exit 0, listener ready, small request body `tubo-a0-final` exact → valid path still succeeds after the negative.
- Component negatives (exact-head, named tests, 0 failures):
  - accesslease: `TestLeaseWrongSubject`, `TestLeaseWrongPipeAudience`, `TestLeaseTamperedSignature`, `TestLeaseExpired`, `TestInviteNonBoundSigner`, `TestInviteTamperedBinding`
  - runtime: `TestExactOpenAuthorizerFailsClosedForInvalidLease` (8 subcases: missing/malformed/non-canonical/wrong key/wrong subject/wrong pipe audience/expired), `TestExactOpenAuthorizerRejectsInvalidProof`, `TestExactOpenAuthorizerChecksRevocation`, `TestExactOpenAuthorizerNoRevocationState`, `TestRedemptionAtomicity`, `TestAuthorizeOpenDenialNeverDialsTarget` (7 subcases: denial/wrong pipe id/wrong subject/zero expiry/expired expiry/no injected authorizer), `TestAuthorizeOpenZeroExpiryNeverOk`
  - cli: `TestZeroConfigShareEmbedsCanonicalRunningOwnerLocator`, `TestShareRuntimeUnavailableEmitsNoInvite` (12 subcases)
  - reachability: `TestProviderDirectThenFallbackInitialSelection`, `TestResolverProductionBundleResolves`
  - bundle: `Test4SignatureVerifies`, `Test5AddressPeerIDMatches`, `Test12NoPrivateKeyEmbedded`

## Zero-Scope / no-discovery evidence (steps 15-16)

- config.yaml ABSENT on A and B (`tubo config validate`/`print`: "no such file or directory") → zero-config: relay resolved ONLY from embedded signed production bundle (`reachability.ResolveDefault` via relay-or-bootstrap fallback when Overlay lookup returns `ErrNotFound`); NO manual provider/relay flags, env overrides, swarm key, old config, hardcoded provider.
- Post-run state: `get scopes --json` → `{"items":[]}` on both; `get pipes` → single local row with `"scopes":[]` (no PipeRecord / discovery record; source=local, no network row); membership 0; no discovery publication/query, no publish grant, no authority interaction beyond exact redemption.
- Tamper home: created NO pipe/runtime/session (rejected before durable state).
- Absence of Scope is NOT absence of authorization: exact-invite redemption IS the authorization on this path.

## Binary integrity (step 4)

- Source: exact merged main `c031185c…`; `CGO_ENABLED=0 go build -trimpath`; go 1.24.6; module build `v0.0.0-20260828112205-c031185c1cd4`.
- Owner (A, oripi, GOOS=linux GOARCH=amd64): sha256 `2170e5a7319f7a3eede789bbe651cdfa42fddcc099f31a127a643d68966a8662`; remote sha256sum verified on oripi after scp.
- Recipient (B, macb, GOOS=darwin GOARCH=arm64): sha256 `0f387d7450957ca075a1435397e275a0a3550a6a8f1df0adb0ffad8140d148ad`.
- Relay machine: NOT rebuilt, NOT redeployed, untouched.

## Journey-D bounded re-confirmation (step 19)

- BEFORE: PipeID `pip_vtxitat5nt3mbparbkdds7ewa62ltqo7`; node identity `12D3KooWBhp1ontddoTosKP9qKQaRgKNngTGuvY9oHi27YzT5JLJ`; PID 1246901, detached, ready; durable spec: target service/a0-final, ingress tubo, visibility unlisted, access invite_only.
- AFTER clean stop + supported restart (`tubo stop pipe/a0-final` then `tubo start pipe/a0-final -d`, READY poll=1): PipeID UNCHANGED, node identity UNCHANGED (owner node key persisted under home/secrets/nodes), PID CHANGED 1249418 (transient runtime), status ready, durable definitions unchanged, session accounting records persisted (13 completed sessions).
- BOUNDED: no resume/migration/refresh/recovery claims. Pipe identity + durable spec + node identity stable; runtime process transient (PID/source).

## No-shortcut audit summary (step 21)

All 12 checks attested ABSENT: hardcoded relay outside bundle; unsigned fallback; TOFU; test provider in normal CLI; old overlay config; Scope/discovery/membership prerequisite; user-configured authority; invite-as-tunnel-authorization; prototype Token on canonical path; target open before authorizer success; debug bypasses; fabricated relay/ownership in evidence. Also: no tracked repo file edits during the run (git status clean at exact SHA), no secrets/invites/keys in evidence, invite file mode-0600 only, outsider homes fresh.

## Environment finding (resolved, TEST_ENVIRONMENT — not a product issue)

oripi initially failed relay dial (`failed to negotiate security protocol: EOF/connection reset by peer`) while macb succeeded. Raw probe: RST on ANY data bytes from oripi to relay:4001 while SSH(22) and multistream(4001) probes intermittently worked. Root cause: legacy prototype daemons on oripi (authority solar, echo-team, connect-…, running since Jul 30 with old PSK-era config) continuously retried private-network handshakes to the (now no-PSK) relay, saturating the per-source connection budget at :4001 for oripi's public IP. After stopping those pre-existing daemons (their config/state files untouched) and a cooldown, oripi handshakes succeeded (4/4 probes). Narrow fix + one retry; no product change.

## Direct-path limitation (explicit)

This A0 implementation advertises only relay-circuit addresses (direct dialing/hole punching reserved for future L1 Reachability work); both hosts are NAT'd. Relay circuit is the deterministic (and only) path in this topology. **No direct-path claim is made for the distributed proof.** Deterministic direct-first initial path selection is evidenced as component-only by `TestProviderDirectThenFallbackInitialSelection` (reachability). This is an environmental/implementation limitation, not a shortcut: no provider injection, no fake path selection, reversible topology manipulation neither required nor attempted.

## Relay facts (live)

- PeerID `12D3KooWM2hGwzrJYGyjUwtzrzLZuPWAmjiG8JeNM5YYJNPeHLME`, tcp/4001, no PSK (systemd unit "Tubo relay (A0, no swarm PSK)", command `tubo relay --listen /ip4/0.0.0.0/tcp/4001 --public-addr /ip4/172.232.189.160/tcp/4001`, no `--swarm-key`).
- DNS `relay.tubo.click` → `172.232.189.160`; multiaddrs `/dns4/relay.tubo.click/tcp/4001` and `/ip4/172.232.189.160/tcp/4001` in the embedded bundle.
- The relay is a reachability/connectivity forwarder only; it holds no authority semantics (see relay/security-observation.txt).

## Local gates on exact head

`make verify-local` PASS on exact merged main (archcheck, `go test ./...`, `go test -race ./...`, build, CLI contract, Journey-A private-http integration incl. relay/attach/share canonical locator/connect/negative tampered invite, hygiene) — verify-local.log.