# Changelog

All notable changes to this project will be documented in this file.

This project follows the versioning policy in `docs/reference/VERSIONING.md`.

## [Unreleased]

### Changed
- Removed runtime support for legacy `/p2p-tunnel/1.0` negotiation and stream handlers; Tubo now uses `/p2p-tunnel/1.1` only.
- `grants serve` now publishes a discoverable system `grant-service` record when namespace discovery is enabled, and `tubo get services --system` shows system resources without exposing them in the default listing.
- Documentation and testbench references now describe the current protocol path without promising a legacy fallback.

### Compatibility
- Product version: pending next release
- Protocol version: 1.1
- Protocol compatibility change: legacy `/p2p-tunnel/1.0` peers are no longer supported
- Operator action required: upgrade clients/services to 1.1-capable binaries

## [v0.10.0] - 2026-06-08

### Added
- Detached `connect` now accepts connect-side verbosity controls (`-v`/`-vv`/`-vvv`, `--log-level`) before or after the subcommand, and forwards them into detached child logs.
- `tubo connect -vvv` now logs each direct/relayed candidate attempt with path, address, and outcome.
- `tubo connect` now emits explicit notices when the live tunnel path upgrades to direct or downgrades to relayed.

### Changed
- Detached raw TCP `connect` now performs one bounded inline self-heal attempt when pre-stream setup fails (for example stale path before stream open/handshake), while still failing fast once application bytes have already started flowing.
- Detached `connect` logs now include concise start and service-resolution summaries, and process visibility now exposes degraded runtime state plus remaining lease lifetime.
- `tubo ps` now distinguishes `service` and `pipe` rows and shows `SERVICE KIND` alongside `SERVICE ID`/`SCOPE` for local runtimes when known.
- `describe process/...` and `inspect process/... --json` now expose service/pipe runtime binding details such as service kind, peer id, selected address, and selected path when available.

### Fixed
- `describe process/attach...` now shows whether the service-scoped grant endpoint is enabled, plus the effective connect policy and grant protocol when available.
- `namespace_members` connect sessions no longer surface a misleading fresh-token hint while membership-based rollover is still available; invite-only refresh failures keep the existing fresh-token/invite wording.
- Connect lease renewal now prefers member rollover when possible and only surfaces fresh-token/invite guidance on invite-only paths.
- Refresh results that are too short-lived for a rollover-capable namespace member now skip the alarmist token/invite hint and roll over through membership instead.
- Detached raw TCP `connect` no longer always requires a manual restart to recover from some stale direct-path failures before a new stream starts.
- `connect` now re-resolves pinned `service_id` metadata on stream/setup self-heal and can rebind to the newly verified peer/address instead of staying stuck on the original endpoint.
- `connect --token` no longer treats the service peer address from `service_endpoint` as a fallback grant endpoint; it now requires either a local authority key for minting or an explicit `grant_service` path and fails clearly when neither exists.
- `stop` now accepts degraded live processes as stoppable, `rm --stale` now treats degraded live processes as non-stale, and raw TCP detached `connect` no longer publishes a bogus HTTP health URL on the local tunnel port, including `--token` flows that learn `service_kind` from the invite payload.
- `rm --stale` now collapses legacy/new aliases for the same stale connect runtime, so repeated cleanup is idempotent.
- `tubo ps` / `describe process/...` no longer misleadingly treat an expired short-lived access lease as the primary tunnel TTL when a longer-lived refresh lease still governs recoverability.

### Compatibility
- Product version: v0.10.0
- Protocol version: 1.1
- Protocol compatibility change: none
- Operator action required: none

## [v0.9.1] - 2026-06-04

Patch release for private collaborative namespaces where `join cluster/... --token ...` previously installed enough state for Discovery V3 listing but not enough reusable signed proof for `connect_policy: namespace_members` lease authorization.

### Added
- Cluster invites now embed a separate signed `cluster-membership-grant` token that carries reusable membership proof without bundling namespace discovery secret material.
- `join cluster/... --token ...` now installs that reusable membership grant into a local `0600` token file and persists only the safe `membership_grant.invite_token_file` reference plus metadata in `config.yaml`.
- End-to-end coverage now explicitly exercises the member invite flow through join, by-name discovery, and successful by-name connect in a Discovery V3 collaborative namespace.

### Changed
- `connect` now loads reusable membership proof from either the legacy inline `membership_grant.invite_token` field or the new file-backed `membership_grant.invite_token_file` path.
- Service-scoped grant endpoints now accept the membership-only token kind for `namespace_members` connect-lease authorization while still enforcing signed authority scope and `connect` permission.
- Canonical CLI docs now explain that cluster-invite join installs both discovery secret material and a separate safe reusable membership-grant token file.

### Fixed
- Fixed the `v0.9.0` regression where a node that joined a private cluster via cluster invite could discover services in a discovery-enabled namespace but could not obtain a connect lease for `connect_policy: namespace_members` without a separate service share invite.
- Fixed the config/install gap so join no longer requires persisting the full cluster invite token to make collaborative by-name connect work.

### Compatibility
- Product version: v0.9.1
- Protocol version: 1.1
- Protocol compatibility change: none
- Operator action required: update service/publisher and client binaries together if you use cluster-invite join plus `namespace_members` connect, because the attached service’s embedded grant endpoint must understand the new `cluster-membership-grant` token kind

## [v0.9.0] - 2026-06-03

Secret-backed namespace discovery release with Discovery V3 runtime, namespace invite install flows, current/previous secret rotation, metadata-only secret management CLI, and aligned end-to-end/documentation coverage.

### Added
- Secret-backed Discovery V3 namespace topics and encrypted payload helpers for collaborative discovery scopes.
- Namespace discovery entry installation through cluster invite share/join flows.
- Metadata-only secret management commands: `tubo get secrets`, `tubo describe secret/namespace-discovery/...`, and `tubo rotate secret/namespace-discovery/... --grace ...`.
- End-to-end and integration coverage for secret-backed namespace discovery, including Alice/Bob invite join + discover, mismatched secret state, rotation grace behavior, and expired-previous handling.
- Canonical Discovery V3 threat-model documentation and updated operational guidance for relay/public-bundle boundaries.

### Changed
- Discovery-enabled collaborative namespace runtime is now Discovery V3-only and requires a valid namespace discovery secret entry for ambient discovery.
- `share cluster/...` now carries the current namespace discovery entry, while `join cluster/... --token ...` installs the current discovery secret locally with metadata-only config state.
- Secret rotation now follows the managed `current` / `previous` model, and local secret views repair expired previous state by clearing stale metadata and removing the old local file when safe.
- Workflow compose/static docs/examples now use real Discovery V3 secret state and current `attach http://... --name ...` CLI examples.

### Fixed
- Fixed service-side Discovery V3 local cache population and authority-key wiring so local query/cache behavior remains deterministic after publish.
- Fixed join/install flows so cluster invite joins remain metadata-only in config output and do not persist full invite secrets.
- Fixed smoke/integration workflow configs so collaborative discovery scenarios include valid namespace discovery secret files and permissions.

### Compatibility
- Product version: v0.9.0
- Protocol version: 1.1
- Protocol compatibility change: none for the data-plane stream protocol; collaborative ambient discovery now intentionally requires Discovery V3 and no longer falls back to Discovery V2 in discovery-enabled namespace runtime
- Operator action required: update relay/edge/service/client binaries together if you use collaborative namespace discovery, cluster invites, or namespace discovery secret rotation

## [v0.8.0] - 2026-06-01

Raw TCP / TLS passthrough release with end-to-end `service_kind` propagation, invite-aware TCP connect flow, and discovery metadata fixes.

### Added
- Raw TCP tunnel transport over libp2p with explicit `TunnelRequest` / `TunnelReady` framing and `raw-tcp-v1` capability negotiation.
- End-to-end `service_kind=http|tcp` propagation through config, discovery, catalog, `connect`, and share-invite flows.
- Integration coverage for TCP echo, concurrent sessions, large payloads, and HTTPS passthrough over the new raw TCP tunnel.

### Changed
- `attach tcp://...` now publishes services as `service_kind=tcp`, and `connect` exposes TCP services as local `tcp://host:port` listeners instead of forcing the HTTP bridge path.
- Service share invites, delegated share-mint requests, and authority-side grant approval now preserve `service_kind`, so `connect --token` can reconstruct the correct local listener mode even from self-contained invites.
- Canonical docs, runbooks, and test docs now describe the mixed HTTP/TCP runtime model and the current release/versioning semantics.

### Fixed
- Fixed config merge precedence so a higher-precedence `tcp://...` target correctly re-infers `service.kind=tcp` instead of staying pinned to stale/default `http` metadata.
- Fixed discovery publication/query/cache paths so relay-served service listings preserve `service_kind` and announced capabilities instead of degrading them to `http` and empty capability sets.

### Compatibility
- Product version: v0.8.0
- Protocol version: 1.1
- Protocol compatibility change: none; this release adds optional `raw-tcp-v1` behavior under protocol 1.1 and falls back to the existing HTTP path when the capability is absent
- Operator action required: update relay/service/client binaries if you want raw TCP/TLS passthrough plus correct `service_kind` and capability propagation in discovery and invite flows

## [v0.7.0] - 2026-05-27

Invite-only public-default, collaboration connect flow, one-time share hardening, delegated share minting, detached connect management, and CLI output/logging cleanup release.

### Added
- Collaboration namespace direct-connect flow: Discovery V2 connect metadata, attached-service service-scoped grant endpoints, delegated connect leases, membership-based connect authorization, member/viewer invite roles, and Docker E2E coverage for discover-by-name plus cross-scope token flows.
- Revocation and redemption hardening for service sharing: issuer-side invite/session/service-access/publish revocation primitives, persistent one-time share redemption stores, grant-endpoint abuse controls, and new public-default/one-time invite regression scenarios.
- Delegated `share service/...` minting through the cluster grant service when the local authority key is absent but a valid publish lease with `share.mint` exists.
- Detached `tubo connect -d/--detach` process support with local process-state/log management and `tubo ps` visibility.
- Shared CLI logging/output abstraction with verbosity controls (`--quiet`, `-v`, `-vv`, `-vvv`, `--log-level ...`) plus regression coverage for clean stdout and JSON safety.

### Changed
- Public default (`tubo-public` / `home/default`) is now explicitly invite-only and unlisted: ambient discovery is disabled, `attach` runs unlisted, `connect --token` is the happy path, and share invites must carry self-contained remote-dialable service endpoint metadata.
- `connect --token` now uses self-contained invite endpoint/grant metadata instead of depending on discovery in invite-only public scopes, while collaboration namespaces use discovery-driven grant-service metadata for `connect <service>`.
- `attach` and `share service/...` now treat expired local publish authorization artifacts as stale renewable state for the same `service_id` rather than forcing a new service name/identity.
- CLI output now follows a clearer contract: stdout for stable command results, stderr for progress/hints/warnings, diagnostics hidden by default unless verbosity/log-level is enabled.

### Fixed
- Removed the share-token path that silently fell back to embedded legacy connect grants after redemption failure, closing the fresh-client one-time invite reuse bypass.
- Hardened share/grant metadata so relay-aware, remote-dialable peers/endpoints are preferred and local-only or peer-mismatched endpoint metadata is rejected.
- Fixed `share service/...` so expired/missing publish leases can renew/re-request authorization for the existing local service identity before minting a fresh invite.
- Fixed noisy internal grant refresh output so `share service/...` no longer prints internal final-looking grant result blocks onto stdout during authorization refresh.

### Compatibility
- Product version: v0.7.0
- Protocol version: 1.1
- Protocol compatibility change: none; this is a backward-compatible feature and hardening release on top of protocol 1.1
- Operator action required: update public relay/grant-service/client binaries if you want invite-only public-default behavior, collaboration namespace connect-by-name flow, one-time share enforcement, delegated share minting, detached connect management, and the cleaned-up CLI output/logging model

## [v0.6.0] - 2026-05-17

Scoped discovery/grants and public quick-share release.

### Added
- Mandatory authority-signed `ServiceClaim` validation for Discovery V2 service publication, plus stable attach-side scoped service identities and claim persistence.
- Authority-side publish-grant workflow: `/tubo/grants/1.0`, persistent grant request store, `tubo grants serve`, and authority CLI for `pending`, `describe`, `approve`, `deny`, and `history`.
- Client-side publish-grant request flow integrated into `tubo attach`, including saved request metadata, polling, approved-claim persistence, and service-share token output.
- Signed public bundle metadata for `home/default`, including cluster authority key and grant-service relay metadata used by clean-config public attach/connect.
- Deterministic end-to-end harness under `tests/e2e/` covering the default-cluster/default-namespace quick-share flow.

### Changed
- Public attach/connect UX is now zero-config on the default public bundle: `tubo attach http://... --name ...` bootstraps the public overlay, obtains a publish grant, and prints a copyable `tubo connect --token ...` command.
- `tubo connect --token ...` now imports discovery context from the service-share token so clean clients can connect without manual `join cluster/home` setup.
- Cluster invite/grant-requester metadata, membership scopes, and grant-service addressing were narrowed and hardened for the public flow.
- The public grant service is now expected to be relay-aware/non-discoverable, with clients dialing it through configured relay/circuit metadata instead of Discovery V2.

### Fixed
- Relayed grant protocol streams now open correctly with limited relay connections, fixing live public `attach` grant requests over `/p2p-circuit`.
- Public bundle trust/key metadata and live grant-service relay address were aligned so GitHub Pages onboarding assets match the deployed public infrastructure.
- Fresh-config Bob connect is now covered end-to-end, including discovery-context import and signed public swarm-key bootstrap.

### Compatibility
- Product version: v0.6.0
- Protocol version: 1.1
- Protocol compatibility change: none; this is a backward-compatible feature release on top of protocol 1.1
- Operator action required: update public relay/grant-service/client binaries and public bundle assets to get scoped publish grants, ServiceClaim-enforced discovery, and zero-config public quick-share UX

## [v0.5.1] - 2026-05-07

CI/smoke hotfix for the signed public onboarding release.

### Fixed
- CLI UX smoke coverage now matches current connect behavior: when a service advertises only loopback direct addresses, `connect` correctly selects the relayed path instead of expecting direct.

### Compatibility
- Product version: v0.5.1
- Protocol version: 1.1
- Protocol compatibility change: none
- Operator action required: none beyond updating from v0.5.0 if you want the CI/smoke metadata hotfix

## [v0.5.0] - 2026-05-06

Signed public onboarding and connect UX release.

### Added
- Signed public network bundle verification and trust store for onboarding into the default Tubo public network.
- `tubo join` bundle mode plus published public assets under `docs/.well-known/tubo/` and a GitHub Pages installer.
- Implicit public join for `attach`, `connect`, `gateway`, `relay`, and discovery commands (`get`, `describe`, `inspect`, `watch`) when no local config exists.
- WebSocket upgrade tunneling for `connect`/`attach` so browser apps using `/ws` work through the tunnel.
- Readable `tubo help` and command-specific help for the current intent-based UX.

### Changed
- `attach` now generates a unique runtime PeerID seed by default and listens on `/ip4/0.0.0.0/tcp/0` to allow direct dial and hole punching when the network permits it.
- `connect` enables AutoRelay/hole punching when relay metadata is available and reports when an initial relayed path may later upgrade direct.
- Discovery defaults now use a longer timeout so live observation covers at least one default service heartbeat.
- Docker Compose smoke commands now use the current `relay`, `gateway`, and `attach` command surface.
- Website copy in `docs/index.html` now reflects signed onboarding, local HTTP/WebSocket listeners, and current install/attach/connect flow.

### Fixed
- Public relay/service/client now join the same signed swarm key from zero when using the default bundle.
- `get services` on a fresh machine now auto-joins instead of failing before discovery.
- Multiple `attach` processes no longer share the old demo PeerID by default.
- `connect` no longer treats loopback/unspecified direct addresses as usable remote direct candidates.
- The installer is POSIX `sh` compatible and no longer fails under dash with `set: Illegal option -o pipefail`.

### Compatibility
- Product version: v0.5.0
- Protocol version: 1.1
- Protocol compatibility change: none; this release adds runtime/CLI behavior on top of protocol 1.1
- Operator action required: update binaries on public relay/service/client hosts to get signed onboarding, unique attach PeerIDs, WebSocket tunneling, and improved connect path handling

## [v0.4.0] - 2026-05-05

Repository/module rename release that aligns the project identity around `tubo`, updates operational paths and image names, and validates the renamed tree on the real 3-node Linode bench.

### Added
- No new runtime protocol features; this release is focused on project/repository identity alignment and operability validation.

### Changed
- GitHub repository moved from `origama/p2p-api-tunnel` to `origama/tubo`.
- Go module path changed from `p2p-api-tunnel` to `github.com/origama/tubo`.
- Internal imports, release ldflags, and source references now use the new module path.
- Operational/docs references were updated from `p2p-api-tunnel` to `tubo`.
- Remote runtime paths now use `/opt/tubo` and `/var/run/tubo`.
- Local compose image names now use `tubo` / `tubo-dummy-api-server`.
- Local checkout path is now `/root/tubo`.

### Fixed
- Rename fallout in Linode runtime scripts was corrected so remote pid/state paths no longer expand to invalid `github.com/origama/tubo` filesystem paths.
- The renamed tree was validated successfully on the real 3-node Linode setup after fixing those path regressions.

### Compatibility
- Product version: v0.4.0
- Protocol version: 1.1
- Protocol compatibility change: none
- Operator action required: if you consume the source tree directly, update Git remotes, Go module imports, local checkout path expectations, Docker image references, and remote runtime paths from `p2p-api-tunnel` to `tubo`

## [v0.3.0] - 2026-05-03

Remote discovery query release focused on making service discovery and connect resolution more reliable when clients arrive after the initial pubsub announcement window.

### Added
- New libp2p application protocol `/tubo/discovery/query/1.0` for querying remote discovery cache state.
- New package `internal/discovery/query` with request/response structs, JSON stream handlers, client helpers, and discovery DTO mapping.
- Discovery query serving on gateway/edge, relay, and attach/service roles.
- Relay-side discovery cache usable for remote query responses.
- Role-specific tests covering gateway, relay, and attach query serving.

### Changed
- CLI discovery resolution now prefers: local edge admin cache -> remote discovery query -> live pubsub observer fallback.
- Single-service lookups (`get service/...`, `describe service/...`, `inspect service/...`, `connect`) now use targeted remote `get_service` queries instead of depending only on list+filter or live observer timing.
- Human-readable CLI output now reports when discovery data came from remote query, and JSON output includes remote-query metadata.
- CLI UX smoke now validates the remote-query path before connect/service inspection.

### Fixed
- `get services` and related resource-oriented commands no longer depend solely on catching a fresh heartbeat during the observer timeout window when a bootstrap/relay peer already has the service in cache.
- `connect <service>` can now resolve services through remote cache query before falling back to live observation, reducing false negatives after late joins.
- Same-swarm service inspection is more reliable when no local edge cache exists but a relay already knows the service.

### Compatibility
- Product version: v0.3.0
- Protocol version: 1.1
- Protocol compatibility change: none; `/tubo/discovery/query/1.0` is an additive optional application protocol
- Operator action required: none; this is a backward-compatible feature release on top of protocol `1.1`

## [v0.2.0] - 2026-05-03

CLI UX v2 milestone release with the intent-based/resource-oriented command set, daemonless local process management, docs-driven happy-path smoke coverage, and direct-first service connection behavior when relay fallback is also available.

### Added
- Intent-based CLI surface for `relay`, `attach`, `gateway`, `join`, and `connect`.
- Resource-oriented swarm discovery commands: `get services`, `get service/<name>`, `describe service/<name>`, `inspect service/<name> --json`, and `watch services`.
- Local detached process management commands: `ps`, `get processes`, `logs`, `stop`, `rm --stale`, `describe process/...`, and `inspect process/... --json`.
- Docs-driven CLI happy-path smoke harness `tests/smoke-cli-ux.sh` plus dedicated CI coverage.
- Direct-vs-relayed address classification in service discovery output and inspect JSON.
- GitHub release workflow for publishing platform binaries and checksums to GitHub Releases.

### Changed
- `connect` now prefers direct service addresses when available and keeps relay addresses as explicit fallback.
- `describe service/...` now shows dial policy plus separate direct and relayed address sections.
- Default CLI workflow is now centered on `relay -> join -> attach -> get services -> connect`, while legacy role commands remain available as advanced compatibility commands.
- Local detached process state/log management now follows stable `process/...` resource IDs.
- Implicit local init for `attach`, `gateway`, and `relay` is now available outside CI, with `--no-init` and CI-safe fail-fast behavior.

### Fixed
- Same-machine relay/bootstrap setups no longer misleadingly prefer relayed service paths when a usable direct path is advertised.
- `connect` output now clearly reports whether direct was selected, unavailable, or being kept behind relay fallback.
- CLI UX smoke portability and CI reliability improved for local observer/cache discovery paths.
- Release asset publishing workflow issues discovered in live validation were fixed, and published releases can now be re-run successfully.

### Compatibility
- Product version: v0.2.0
- Protocol version: 1.1
- Protocol compatibility change: none
- Operator action required: none; this is a backward-compatible CLI/runtime feature release on top of protocol `1.1`

## [v0.1.3] - 2026-05-02

Service-restart recovery release focused on reducing relayed traffic disruption during service restarts, especially on the real 3-host Linode bench.

### Added
- Edge regression coverage for coordinated relay recovery, retry-time discovery re-resolution, guarded last-known entry use, and recovery-end route handling.

### Changed
- Edge now coordinates relay recovery per service as a bounded single-flight flow instead of letting each failing request independently thrash the retry path.
- Stream-open retries now re-resolve discovery state on each attempt and only rely on last-known service entries inside the active recovery window.
- Requests arriving during active relay recovery now briefly wait for the in-flight recovery/announcement refresh instead of failing immediately while the service is still republishing its relay path.

### Fixed
- Relayed traffic during service restart now recovers cleanly on the validated 3-host Linode restart stress, with zero request failures on the final release-candidate rerun.
- Route expiry/removal no longer races a successful coordinated recovery completion.

### Compatibility
- Product version: v0.1.3
- Protocol version: 1.1
- Protocol compatibility change: none
- Operator action required: none; this is a backward-compatible recovery and latency improvement on top of the existing protocol `1.1` + legacy fallback behavior

## [v0.1.2] - 2026-05-01

Relay-first recovery release focused on making traffic recover cleanly after relay restarts in both NAT compose tests and the real Linode multi-host bench.

### Added
- NAT integration regression test: `TestRelayNATTrafficRecoversAfterRelayRestart`.
- Service regression tests for synthesized relay-circuit announcements and tracked reservation readiness.
- Edge regression tests for relay-recovery gating behavior.

### Changed
- Service now clears tracked reservation state when the relay peer disconnects and forces a fresh reservation on reconnect.
- Service republishes usable relay-circuit announcement addresses immediately after reservation refresh, even when host-reported `/p2p-circuit` addresses lag.
- Edge now seeds its peerstore from discovery announcements and prefers explicitly announced relayed addresses when the service is operating on a relay-first path.
- Edge no longer treats stale direct addresses as first-class recovery candidates when the service is clearly advertising a relay-only recovery path.

### Fixed
- Relay-first traffic now recovers after relay process restart instead of remaining wedged in repeated `502`, `dial backoff`, and `NO_RESERVATION` failures.
- Relay restart no longer causes the service announcement to drift into stale reservation state after the relay disconnects and reconnects.

### Compatibility
- Product version: v0.1.2
- Protocol version: 1.1
- Protocol compatibility change: none
- Operator action required: none; this is a backward-compatible reliability fix on top of the existing `1.1` + legacy fallback behavior

## [v0.1.1] - 2026-05-01

First clean versioned release with explicit product/protocol versioning, protocol negotiation, and real mixed-version compatibility evidence on the Linode multi-host bench.

### Added
- Release/versioning policy in `docs/reference/VERSIONING.md`.
- `tubo version` command with product version, protocol version, commit, and build date output.
- Root `VERSION` file, `CHANGELOG.md`, and manual release checklist in `docs/RELEASING.md`.
- Protocol 1.1 hello handshake carrying `protocol major.minor`, role, and capabilities.
- Edge/service protocol debug endpoints exposing negotiated protocol state.
- Real mixed-version Linode compatibility smoke harness: `tests/smoke-terraform-linode-mixed-version.sh`.

### Changed
- Stream negotiation now prefers `/p2p-tunnel/1.1` and falls back to legacy `/p2p-tunnel/1.0`.
- Service accepts both `/p2p-tunnel/1.1` and legacy `/p2p-tunnel/1.0` stream protocol IDs during rollout.
- Linode testbench docs now include the mixed-version compatibility workflow.

### Fixed
- Mixed-version same-major stream setup now has an explicit fast-fail path for incompatible protocol-major peers.
- Selected stream protocol is now visible in runtime logs even on the legacy fallback path.

### Compatibility
- Product version: v0.1.1
- Protocol version: 1.1
- Protocol compatibility change: backward-compatible addition; legacy `/p2p-tunnel/1.0` remains accepted
- Operator action required: none for same-major upgrades; old/new nodes can mix during rollout through the legacy fallback path
