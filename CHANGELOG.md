# Changelog

**Historical prototype changelog:** Entries below describe preserved `prototype-v2-final` implementation cycle. Future A0–A4 direction is [`docs/implementation/ROADMAP.md`](docs/implementation/ROADMAP.md); do not read historical feature status as target architecture or compatibility promise.

## [Unreleased]

### Minimal exact-sharing readiness fixes (#76)

- Store schema-13 accepted-relationship backfill is version-gated to a
  one-time legacy upgrade: scoped redemptions are never promoted to accepted
  exact relationships on reopen.
- Revocation/accepted-relationship dependencies are mandatory and fail closed:
  the exact open authorizer cannot be constructed without revocation state;
  redemption and renewal reject missing revocation or persistence state.
- `revoke invite` accepts the full exact invite token so the owner can fence
  an unredeemed exact invite before redemption.
- `connect --token` performs all local conflict checks before consuming the
  one-time invite: a conflicting connect no longer burns the bootstrap
  credential.
- The tunnel receive path uses the owning run's lifecycle context
  (cancellable at shutdown) and host identity creation is atomic and
  concurrency-safe; a corrupt identity key fails closed instead of silently
  rotating the host identity.

### Corrected minimal-path verification (#76)

- Architecture checks now inspect actual Go dependency edges, including third-party dependencies. Missing guarded packages/files fail closed; regex guards scan production code without comments. Real-module negative tests prevent vacuous PASS results. Earlier A0 package-edge PASS evidence is superseded by the corrected checker.

### Removed (A0)

- Session-resume surface removed from the A0 path (issue #39): `/tubo/tunnel-resume/2.1`, the `session-resume-v1` capability, resume/control stream roles, continuous ACK/retention/replay window, planned-switch barrier, abrupt carrier recovery with detached grace, `ResumeOffer`, and the memory-only `resume_secret`. A0 sessions are raw full-duplex authorized `/tubo/tunnel/2.0`; carrier failure terminates the session and a new authorized connection is required.
- Supersedes prototype resume work previously carried in PRs #38/#29/#33 (closed unmerged; retained only as evidence branches `issue-34-session-resume-data-plane` and `issue-28-neutral-carrier`). Session-resume/carrier-replacement correctness remains deferred post-A0 resilient-Session debt (#32).

### Added (A0)

- `revoke invite <jti>` family resolution (PR #74 final regression): the CLI
  now resolves each invite JTI deterministically before revoking. An invite
  redeemed through the exact redemption path (kind
  `'exact'` in the schema-14 `invite_redemptions` discriminator) revokes as
  `exact.invite` under its resolved exact Pipe ID — including obsolete exact
  invites whose accepted relationship was later replaced by a newer invite.
  Every other invite (legacy scoped share.invite, or unknown) is revoked
  through the pre-existing scoped path: `share.invite` in the current Scope.
  A persistence error during resolution fails closed and never falls back to
  the scoped family. E2E hardened: both obsolete-invite and current-invite
  revoke commands must succeed (no `|| true` masking), with application
  reachability as the primary evidence.

- Review fixes (PR #74 blockers): the exact downstream open path now consumes
  the injected neutral `openauth.AccessAcquirer` contract instead of importing
  L4 (`internal/runtime/exact_access.go` is the sole composition adapter;
  arch rules R19-R22 guard the execution files). The authoritative accepted
  relationship is exactly one deterministic `accepted_exact_relationships`
  row per `(pipe_id, recipient_public)` (backing invite JTI + accepted epoch),
  committed atomically with one-time redemption
  (`RedeemInviteAndAccept`). Pipe-access epochs now flow: invite minted at
  current epoch, redemption requires equality, the accepted relationship and
  every lease carry the accepted epoch, renewal compares
  `current == accepted`, and `revoke pipe-access` advances the epoch so a
  fresh share+accept re-authorizes after revocation (no permanent kill
  switch); `revoke invite` uses the `exact.invite` subject type.

- Durable accepted remote-Pipe binding + post-acceptance renewal (Journey A
  durable lifecycle). The one-time exact invite is bootstrap only: acceptance
  persists a durable, recipient-bound `RemotePipeBinding` (exact PipeID,
  owner-bound Exact Authority identity, verified owner binding, verified
  locator, accepted recipient reference; no local ingress, no AccessLease)
  and caches the initial short-lived AccessLease ephemerally. Ongoing Pipe
  opens reacquire fresh short-lived AccessLeases through
  `AcquireAcceptedPipeAccess` (unexpired cached lease, else reachability +
  `/tubo/exact-renewal/1` renewal with recipient possession proof under a
  fresh server challenge + verified fresh lease). The invite is never
  required again after acceptance (invite expiry never destroys the accepted
  relationship). Schema 12 splits persistence: `remote_pipe_bindings` +
  `exact_lease_cache` replace the old `exact_connections` row (dropped;
  unreconstructable); `invite_redemptions` gains an index on
  `(pipe_id, client_public)` as the authoritative accepted relationship for
  renewal. `tubo accept --token` redeems once and persists the binding
  (no Pipe/ingress/runtime); `tubo connect --remote <pipe-id>` reuses the
  binding without an invite; `revoke pipe-access` works on scope-less exact
  Pipes. `TUBO_EXACT_LEASE_TTL` (bounded to `MaxTTL`) allows operators/tests
  to shorten the short-lived lease. Renewal fails closed on missing accepted
  state, revocation, stale epoch, wrong subject/pipe, invalid proofs, and
  authority errors; existing lease/session expiry fencing unchanged.

- Relay-reservation maintenance moved to the L1 Reachability boundary (reachability reliability): new `internal/reachability.RelayReservation` owns initial relay connection and reservation acquisition, periodic Circuit Relay v2 reservation renewal, reconnect, reservation reacquisition after reconnect, failure surfacing, and recovery — encoding that relay connection liveness is not proof of reservation validity (a still-connected relay whose reservation has lapsed is renewed). Renewal is scheduled from the relay-provided reservation expiration minus a conservative safety margin (never an assumed relay TTL), so a shorter returned TTL moves renewal earlier and a minimum-delay floor prevents a pathological near-instant expiration from driving a tight loop. Both scoped and scope-less Journey A published Pipes consume the same L1 `Acquire`/`Serve` lifecycle; the runtime (`publishedPipeLifecycle`) only maps maintenance outcomes onto its existing degraded/ready Run status and holds no reservation timing or validity policy (arch guard R16). The scope-less Journey A path previously reacquired its reservation only on disconnect, so a connected-but-stale reservation could mint an exact invite with a circuit locator the relay refused (`NO_RESERVATION`); it now proactively renews. Initial readiness still requires a successful reservation (fail closed).

- A0 final distributed proof + acceptance (Step 10B, issue #68): full clean distributed Journey-A zero-config exact-share proof executed on exact merged main `c031185c1cd45bf4e58fdbad16f64df2fd76999b` (PR #70 merge). Machines A=oripi, B=macb, R=relay.tubo.click (public, no PSK, pinned PeerID `12D3KooWM2hGwzrJYGyjUwtzrzLZuPWAmjiG8JeNM5YYJNPeHLME`): real HTTP small/sequential/7 MiB hash-verified traffic through REAL relay traversal (path=relayed both endpoints, byte symmetry 7341674/880, relay-side sockets under both source IPs), tampered-invite negative (`authorization_denied`, no Session), zero-Scope/discovery/membership, Journey-D stable PipeID across clean restart. Prerequisites: Step 10A production trust material (PR #66, merge `6dd60e1c7113da726b6c39567b72da454a919e3a`; embedded signed bundle, single-use root `tubo-bootstrap-root-2026-01`, expires 2027-02-23) and relay upgrade (PR #67, merge `277e4366de4feb58e473308b0967d9bea9164dab`; public no-PSK relay, same PeerID preserved, no re-signing). A0 exit criteria 1-12 marked COMPLETE in `docs/testing/ARCHITECTURE_ACCEPTANCE.md` and `docs/testing/ACCEPTANCE_MATRIX.md`; sanitized evidence summary `docs/testing/A0_STEP10B_EVIDENCE.md` (evidence root `/tmp/a0-10b-final/evidence`).
- Exact Pipe Authority remote-open authorization (issue #49 / step 6B2): completes the receiving-boundary open-authorization seam with canonical Scope-independent exact-Pipe Access Lease + possession proof validation. The concrete `ExactOpenAuthorizer` (composition-root adapter in `internal/runtime`) validates 16 fail-closed cases (missing/malformed/non-canonical lease, wrong authority key, subject, pipe audience, expired lease, wrong-purpose/missing/wrong-key proof, revoked lease, stale epoch, revocation/epoch read error, invite-as-lease, prototype-token-as-lease, Scope-membership-alone) and returns only generic "authorization denied". The prototype Scope-token adapter is quarantined (`newScopeTokenOpenAuthorizer` panics). The store adapter (`store.ExactAuthorityRevocationState`) provides revocation/epoch/redemption persistence without `*store.Store` in `internal/exactauthority`. Redemption (`RedeemExactInvite`) implements the mandatory 12-step ordering with Loader-only capability, zero persistent mutation before atomic redemption, and one-winner atomic contract. `localkey.parseCanonicalPrivate` gains P2 hardening to reject Ed25519 private keys with inconsistent seed/public-suffix. `internal/accesslease` adds domain-separated possession proof helpers (`SignProof`/`VerifyProof`, `RandomChallenge`, `NewSessionID`) with purpose domains `tubo-open-v2` and `tubo-exact-redeem-v2`.
- Neutral receiving-boundary OpenAuthorizer seam (issue #48): receiving L2 serve consumes an injected neutral `internal/openauth` contract and opens no local target before a successful result (independent PipeID/Subject/non-zero-future ExpiresAt binding); prototype Scope-token decode/verify/revocation/epoch/proof validation preserved fail-closed behind transitional `scopeTokenOpenAuthorizer`, which issue #49 replaces with the canonical Scope-independent exact-Pipe Access Lease + Exact Pipe Authority.
- Neutral Carrier seam (issue #28): Session byte flow, cancellation, owner bound, and inbound exact-owner dispatch now run through a neutral `node.Carrier` contract (raw full-duplex bytes, `Close`/`Reset`/`CloseWrite` half-close, `SetDeadline`) realized at the host/L1 edge, instead of libp2p concrete host/stream/peer and `*store.Store`. The production tunnel data plane (responder `tunnelServer.serve` and initiator `openLocalConnection`) is re-grounded through an owner-bound `Session` + active `Carrier`; the direct `copyDuplex` tunnel path is deleted. Wire frames, lease-validation-before-target-open ordering, `authorizationTimer` lease-expiry close, `store.Session` telemetry, full-duplex and FIN ordering, and quarantine negative tests are preserved. `tests/verify-architecture.sh` now also enforces that the Session boundary imports no libp2p host/network/peer/protocol or store and that the host adapter has no runtime coupling (A0 exit criterion 4).

### Added (A0)

- L1 Reachability boundary (issue #44): new `internal/reachability` seam turns verified bootstrap/relay provider identity (a signed bootstrap bundle or test-injected `peer.AddrInfo`) into an acquirable dial target and a raw full-duplex `node.Carrier`. The API is transport-only by construction — it carries no policy, lease, scope, or authorization input and returns no authorization output. Initial path selection is direct-first with relay/circuit fallback, never mid-session migration. Zero-config resolution consults the embedded/preinstalled signed bootstrap bundle; with no production trust material installed it fails closed deterministically (`ErrNoBootstrapBundle`) — no overlay fallback, no hardcoded relay address, no temporary provider. Carrier acquisition in `openOverlayHost` and `runPublishedPipe` now routes through this seam (`relayOrBootstrap`); user-configured overlay flows are unchanged.
- Bounded signed public-bootstrap bundle contract (issue #44): pure `internal/reachability/bundle` verifier with strict fail-closed checks — fixed canonical big-endian binary encoding bounded (16 KiB) before allocation; supported-version and downgrade checks; dedicated bootstrap-role signing root (no TOFU, a public token cannot introduce a root, and an authority-role root is rejected); exact no-grace validity window (not-yet-valid/expired); Ed25519 signature; malformed/trailing/duplicate/oversized/identity-address-mismatch rejection. Deterministic test-only Ed25519 roots; no production key material ships.
- `internal/reachability` tests drive direct-then-fallback initial selection and a signed-bundle → Provider → `node.Carrier` → existing Session pump path (full-duplex + exact half-close) over in-process libp2p hosts, without external network.
- #43 finding 1 remediation: `sessionPump` now distinguishes clean both-FIN completion (graceful `Carrier.Close`, preserving mux FIN ordering) from error/forced termination (`Carrier.Reset`, fail-closed) at the Session/Carrier boundary; the continuity mid-cancel test now exercises the abrupt-reset fail-closed path.
- Architecture guard extended: `internal/reachability` may not import runtime/authority/discovery/store/CLI; the pure bundle subpackage may not import the node adapter or any runtime/authority/store/discovery/CLI package.

### Added (A0)

- Pipe Engine (L2 connectivity composition, issue #57 / step 7): new `internal/engine` package implements the canonical `Ingress -> Pipe -> Target (Service | Pipe)` traversal model. Engine resolves logical paths via `domain.ResolveLogicalPath` (reusing L0 cycle/hop enforcement), then opens each hop sequentially through narrow injected capabilities. TraversalState provides immutable-copy Advance with loop detection and hop-limit enforcement. Engine.Forward handles both Service targets (via injected dialer) and Pipe targets (via injected DownstreamOpener for per-hop authorization). Engine imports no `internal/store`, `internal/discovery`, `internal/authority`, `internal/cli`, or L3 concrete packages.
- Serve path supports Pipe target forwarding: `tunnelServer` gained `engine` and `downstreamOpener` fields; authorization is separated from target opening via `authorizeOpenOnly`; Pipe targets advance traversal state and open the downstream hop through `Engine.Forward`.
- Architecture guard extended: Step 7 engine verified to import no store/discovery/authority/Scope/CLI packages.

### Added (A0)

- L4 Journey-A use-case boundary + narrow persistence ports (issue #59 / step 8A): new `internal/usecase` package defines the consumer-owned `ServiceRepository` and `PipeRepository` ports and composes `Attach`, `ShareExact`, `ConnectExact`, and `StartPipe` as pure Go functions over injected capabilities. Attach creates a durable Scope-less Service+owned-Pipe definition with stable self-deriving PipeID; ShareExact proves ownership, provisions the owner-local dedicated Exact Authority key once, and mints a bounded one-time canonical ExactInvite; ConnectExact decodes/verifies the invite, redeems through injected reachability for the canonical short-lived Access Lease, verifies the lease binds recipient+exact-Pipe+bound-issuer, and creates a durable local ingress Pipe (never started in 8A); StartPipe delegates runtime launch to an injected starter. Use cases import no `store`, `runtime`, `discovery`, `authority`, `CLI`, `node`, `engine`, or concrete reachability; no `*store.Store` appears in any contract; no generic `Repository[T]`/UnitOfWork/Storage framework. `internal/store` gains `ServiceRepoAdapter`/`PipeRepoAdapter` (composition-root adapters satisfying the consumer-owned contracts without importing usecase). Durable/runtime separation asserted: PipeSpec/ServiceSpec structurally carry no PID/Carrier/Session/address/lease fields, identity survives repository restart, and transient Run/Session writes never mutate definition rows. Zero CLI, serving-path, or runtime behavior change in 8A. Architecture guard extended: usecase imports no forbidden concrete packages and exposes no *store.Store or generic persistence framework.

### Added

Resume-related entries immediately below are historical prototype records superseded by the quarantine above; they are not current A0 features.

- Session resume protocol `/tubo/tunnel-resume/2.1` with control and resume stream roles, authenticated via HMAC-SHA256 proof over memory-only resume secret.
- `sessionPump`: bounded, epoch-fenced, receiver-committed byte transport replacing uncoordinated `copyDuplex` for resume-capable sessions.
- Continuous ACK sender (every 64 KiB or 25 ms) with retained replay window (1 MiB per direction, 64 MiB aggregate).
- Planned switch barrier (relay ↔ direct): SwitchPrepare, Barrier, BarrierAck, SwitchAbort messages.
- Abrupt carrier recovery: epoch + 1 relay resume candidate, replay unacked only, detached grace timer (10 s).
- Resume handshake: ResumeHello, ResumeChallenge, ResumeCommit, ResumeResult, ResumeActivate, ResumeActivated.
- `current_transport_path` field on sessions and `path_events` table tracking transport transitions with epoch and committed offsets.
- DCUtR hole punching (`EnableHolePunching` with 5s `DirectDialTimeout`), AutoNATv2 auto reachability, and QUIC-v1 listen in libp2p host.
- `session-resume-v1` capability negotiation in tunnel handshake.
- `ResumeOffer` in `OpenResult` carrying resume secret, control protocol, and buffer limits.
- `tubo watch pipes [--scope …] [--interval …] [--json]`: deterministic, membership-authorized multi-scope pipe changes from verified local state with explicit `added`, `updated`, `expired`, `removed`, `degraded`, and `recovered` event types; JSON is NDJSON and slow stdout consumers backpressure polling without an event queue.
- Schema 8 transactional revocations, atomic one-time share-invite redemptions, 10-minute access leases, 48-hour online-checked refresh leases, and pipe-only invite endpoints. Refresh updates later sessions only; established tunnels remain bounded by initial access lease until a future tunnel resume/switch protocol.
- `tubo share pipe/<name> --json`, `tubo revoke invite|session|pipe-access|pipe-publish <id>`, and `connect --token <invite>` foundation.
- Greenfield repository contract for Tubo's service/pipe architecture.
- Initial domain invariants and deterministic local verification gate.
- Isolated three-machine E2E harness contract for `tuborelay`, `oripi`, and `macb`.
- Pure-Go SQLite schema 3 state store in WAL mode with atomic YAML overlay/current-scope config, local peer aliases, per-run libp2p telemetry/observed peers, and separate `0600` secrets.
- Private-swarm libp2p relay, renewable reservations/records, and relayed stream transport.
- Ed25519-signed membership invites, remote publisher grants/announcements, access leases, pipe records, and proof-of-possession tunnel handshake.
- Service/pipe creation, scope workflows, verified record discovery, detached connect runtime, path-aware deduplicated pipe inventory/inspection, local alias plus observed-peer inventory, relay-aware bandwidth/rate/connection telemetry in `ps`/`top`, logs, config/doctor diagnostics, restart/removal/stale cleanup, process registry, identity-safe stop, and explicit macOS `launchd` supervision for authority/pipes.
- Isolated local relay/publisher/member/viewer HTTP and WebSocket integration gate.
- Verified TLS 1.2+ upstream dialing for local HTTPS service targets and CLI verbosity controls that preserve JSON stdout.
- Negotiated `/tubo/authority/2.1` bounded `records.list` snapshots with persistent authority-global generation/high-water metadata, atomic reconciliation, rollback/same-generation ambiguity rejection, and no deletion fallback to authority 2.0.
- Schema 7 fenced per-scope periodic discovery-sync leases for membership-backed published/local pipe runtimes, with bounded jitter/deadline, retry suppression, guarded final writes, and redacted scope sync status. Local authority remains source-of-truth and does not self-sync.
- Isolated `periodic-sync` three-machine harness: split authority/publisher homes, same-home contender election, exact crash expiry takeover, relay recovery, cached-only convergence, and secret-artifact rejection. Three-machine campaign passed: `20260730T092006Z-83493` (`generated/e2e-three-machine/20260730T092006Z-83493/artifacts/report.json`).

### Changed
- `get pipes` is cache-first: complete pinned-authority snapshots are fresh for strictly less than five minutes, `--refresh` forces bounded refresh past retry cooldown, and `--cached-only` is socket-free. Scope-local typed freshness/error JSON and human freshness columns retain verified cached/local inventory on refresh failure.
- This codebase has no compatibility relationship with the previous Tubo implementation.
- State schema 4 adds run source and supervisor registration metadata; schema 3 upgrades in place, and older binaries reject schema 4.
- State schema 6 adds authority snapshot provenance and canonical snapshot digest. Schema-5 locally derived freshness ordering is reset transactionally without erasing valid cached records; older binaries reject schema 6. Authority snapshot protocol adds negotiated 2.1 while tunnel and existing authority operations remain 2.0.
- State schema 7 adds local fenced sync-lease persistence only; protocol remains unchanged.
- Protocol 2.0 first encoding is bounded length-prefixed JSON; raw full-duplex bytes begin after `OpenOK`.

### Fixed
- Authority-owned publication and expiry now advance persistent snapshot metadata atomically; reconciliation prunes only discovered cache rows and refresh retry deadlines prevent repeated authority dials.
- Configured relay connections now clear stale libp2p dial backoff and retry every five seconds after network interruption; authority/publisher reservations and records recover without process restart.
- Local pipe status now degrades on relay/session setup failure, `doctor` detects degraded/disconnected runtimes, and `get pipes` retains verified cached inventory during discovery outages.

### Compatibility
- Product version: `0.1.0-dev`
- Protocol version: `2.1` for negotiated authority snapshots; tunnel and existing authority operations remain `2.0`
- State schema version: `10`
- Protocol version: `2.1` for tunnel resume; tunnel open remains `2.0`
- Mixed-version behavior: schema-8/9 binaries reject schema-10 state; session resume requires both peers to advertise `session-resume-v1`; protocol 1.x is rejected
- Compatibility with previous Tubo versions: intentionally unsupported
- Rollout: back up state, deploy schema-10 binary, restart. Resume sessions work only when both peers are upgraded
- Rollback: stop schema-10 binaries and restore pre-schema-10 backup; no downgrade migration exists
- Validation: `make verify-local`; no three-machine campaign run
- Operator action: use isolated state and do not point this binary at an existing Tubo home
