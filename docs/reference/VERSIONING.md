# Versioning policy

This project versions `tubo` at two levels:

1. **Product version** — one version for the whole `tubo` binary
2. **Protocol version** — one shared wire-compatibility version for all roles

## 1. Product version (`tubo`)

`tubo` uses **Semantic Versioning**:

- `MAJOR`: breaking product change or intentionally broken cross-version compatibility
- `MINOR`: backward-compatible feature or capability
- `PATCH`: backward-compatible bugfix, reliability fix, docs/testbench/release-note update tied to a release

Example:

- `tubo v0.3.2`

All runtime roles in the binary share the same product version:

- `tubo relay`
- `tubo gateway`
- `tubo attach`
- `tubo connect`
- `tubo grants serve`

We do **not** version roles independently.

## 2. Protocol version (`major.minor`)

The protocol is versioned separately as:

- `protocol <major>.<minor>`

Example:

- `protocol 1.1`

This protocol version defines compatibility between different `tubo` nodes.

### Protocol rules

- `protocol major` change = breaking wire/protocol change
- `protocol minor` change = backward-compatible protocol extension
- new protocol features must be optional or negotiated when possible
- current `1.1` examples include hello/capability negotiation and optional `raw-tcp-v1`; nodes that do not share that capability must still interoperate on the common HTTP behavior

## 3. Compatibility guarantees

Compatibility is defined by the **protocol version**, not only by the product version.

### Guaranteed

- all nodes with the same `protocol major` must be able to connect
- `PATCH` product releases must remain protocol-compatible
- `MINOR` product releases should remain protocol-compatible unless explicitly documented otherwise
- if one side supports newer optional protocol features, it must fall back to the common supported behavior when that fallback is intentionally supported

### Not guaranteed

- compatibility across different `protocol major` versions
- use of a new feature that requires a capability not supported by the remote node

### Expected operator outcome

`v0.9.0` note:
- Discovery V3 for discovery-enabled namespaces intentionally replaces the earlier Discovery V2 namespace runtime;
- there is no Discovery V2 fallback for collaborative namespace runtime in `v0.9.0`;
- operators should upgrade relay/service/client binaries together when enabling secret-backed collaborative discovery.


Mixed deployments like these should keep working:

- edge `tubo v2.1.4` + relay `tubo v2.0.0`
- service `tubo v0.4.3` + relay `tubo v0.5.1`

...as long as the nodes still share the same compatible `protocol major` and do not require unsupported optional capabilities.

## 4. Release artifacts

Option A is the project default:

- `VERSION` file in repo for the current product version
- `CHANGELOG.md` in repo for release notes
- Git tag per release, formatted as `vX.Y.Z`
- GitHub Release created from the tag

## 5. What changes require which bump?

### Product `PATCH`

Use for:

- bugfixes
- reliability/stability fixes
- testbench, perf baseline, or packaging fixes that belong to a release
- internal refactors with no intended compatibility break

Normally these do **not** change protocol version.

### Product `MINOR`

Use for:

- new backward-compatible CLI/runtime features
- new backward-compatible protocol capabilities
- new optional behavior that older nodes can safely ignore or negotiate away

These may increase `protocol minor`.

### Product `MAJOR`

Use for:

- intentional breaking changes
- incompatible config/runtime semantics that materially change upgrade expectations
- incompatible wire/protocol changes

These usually increase `protocol major`.

## 6. Release notes requirements

Every release must add a `CHANGELOG.md` entry with at least:

- `Added`
- `Changed`
- `Fixed`
- `Compatibility`

The `Compatibility` section must state:

- product version
- protocol version
- whether protocol compatibility changed
- whether any operator action is required

## 7. Implementation requirements

The binary should expose its build metadata, including:

- product version
- protocol version
- commit SHA
- build date

Recommended CLI surface:

- `tubo version`

## 8. Agent and maintainer rule

When closing an issue or preparing a release, explicitly decide:

1. does this change bump product `PATCH`, `MINOR`, or `MAJOR`?
2. does this change leave protocol version unchanged, bump `minor`, or bump `major`?
3. what compatibility note must appear in `CHANGELOG.md`?
