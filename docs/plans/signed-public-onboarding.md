# Signed public onboarding and CLI UX simplification

## Status

Planning PR for the next implementation pass.

This document defines the work required to:

1. remove the legacy role-based CLI UX;
2. keep only the intent-based user commands;
3. add signed public network bundles;
4. make `tubo attach` and `tubo connect` auto-join the default public Tubo network on first run.

## Target demo flow

Machine A:

```bash
curl -fsSL https://tubo.click/install.sh | sh
tubo attach dummysvc --port 8080
```

Machine B:

```bash
curl -fsSL https://tubo.click/install.sh | sh
tubo connect dummysvc
```

Expected first-run behavior:

```text
No Tubo network configured.
Fetching default network bundle: tubo-public
Signature verified: tubo-root-2026
Joined network: tubo-public
```

After the implicit join, `attach` publishes the service and `connect` searches the service using the existing libp2p discovery and tunnel runtime.

## CLI direction

Tubo should expose only intent-based commands to users:

```text
attach    publish a local HTTP endpoint into a Tubo network
connect   open a local listener to a remote service
gateway   run an HTTP gateway to the Tubo network
relay     run a relay/bootstrap node
join      configure this machine for a Tubo network
get       list or retrieve resources
```

The old role-based public UX is removed without backward compatibility:

```bash
tubo edge run
tubo service run
tubo bridge run
tubo relay run
```

The internal packages may keep their current names:

```text
internal/app/service
internal/app/edge
internal/app/relay
internal/app/bridge
```

Those are implementation details and should no longer leak into the CLI model.

## Existing implementation notes

The current repository already has the right runtime pieces:

- `attach` maps to the `service` runtime.
- `gateway` maps to the `edge` runtime.
- `join` writes `~/.config/tubo/config.yaml` and installs `~/.config/tubo/swarm.key`.
- `service` already supports PSK, static autorelay, hole punching, discovery publishing, and relay reservations.
- `edge` already supports discovery cache, direct stream attempts, relayed fallback, and connection path reporting.

Therefore this work should mostly change CLI dispatch and config onboarding, not the libp2p runtime.

## Key model

There are three different keys and they must stay conceptually separate.

### 1. Bundle signing key

Purpose: sign network bundles published on `tubo.click`.

- private key: held by the project maintainer or CI secret;
- public key: embedded in the `tubo` binary;
- algorithm: Ed25519;
- key id: `tubo-root-2026`.

Embedded trusted public key:

```text
ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEMmu4uNA2C/KKW1VX/1Cr/PSasaa8bvi9ExjBhNqltQ bettersafethansorry@tubo.click
```

The private signing key must never be committed.

### 2. Swarm key / PSK

Purpose: define the libp2p private network used by a Tubo network.

For `tubo-public`, this key is distributed inside the signed bundle. It is not a peer identity key and should not be called a private key in UX copy.

### 3. Peer identity key

Purpose: identify a local libp2p node.

This is generated locally by the client runtime. It is never downloaded from `tubo.click` and never included in a network bundle.

## Signing key lifecycle

The bundle signing keypair is generated before the first release that supports public onboarding.

Recommended lifecycle:

1. Generate an Ed25519 signing keypair once.
2. Commit only the public key into the client trust store.
3. Build and release the `tubo` binary with the embedded public key.
4. Generate and sign `tubo-public.bundle` with the private signing key.
5. Publish the bundle to `https://tubo.click/.well-known/tubo/networks/tubo-public.bundle`.
6. At runtime, `tubo` downloads the bundle and verifies it with the embedded public key.

For the MVP, signing can be manual from a maintainer machine. CI-based signing can be added later with the private key stored as a secret.

## Bundle format

Use an envelope where the signature is computed over the raw payload bytes.

Envelope:

```json
{
  "kind": "tubo.network.bundle",
  "version": 1,
  "payload_encoding": "base64url",
  "payload": "eyJuYW1lIjoidH...",
  "signature": {
    "alg": "ed25519",
    "key_id": "tubo-root-2026",
    "value": "base64url-signature"
  }
}
```

Decoded payload:

```json
{
  "name": "tubo-public",
  "id": "tubo-public-v1",
  "visibility": "public",
  "description": "Default public Tubo network",
  "relays": [
    "/dnsaddr/relay.tubo.click"
  ],
  "swarm_key": {
    "type": "libp2p-pnet",
    "encoding": "text",
    "value": "/key/swarm/psk/1.0.0/\n/base16/\n..."
  },
  "network": {
    "autorelay": true,
    "hole_punching": true,
    "force_reachability": "private"
  },
  "validity": {
    "not_before": "2026-05-01T00:00:00Z",
    "not_after": "2027-05-01T00:00:00Z"
  }
}
```

For the MVP, the swarm key should use the existing libp2p pnet text format because the current runtime already supports reading that file.

## Local config layout

Do not introduce multi-network config in this PR.

Keep the current MVP layout:

```text
~/.config/tubo/config.yaml
~/.config/tubo/swarm.key
```

A successful public bundle install should write a config equivalent to:

```yaml
network:
  private_key_file: /home/user/.config/tubo/swarm.key
  bootstrap_peers:
    - /dnsaddr/relay.tubo.click
  relay_peers:
    - /dnsaddr/relay.tubo.click
  autorelay: true
  hole_punching: true
  force_reachability: private
```

Multi-network layout can be designed later for private networks:

```text
~/.config/tubo/networks/*.yaml
~/.config/tubo/config.yaml with defaultNetwork
```

## New packages

### `internal/trust`

Suggested files:

```text
internal/trust/bundle_keys.go
```

Responsibilities:

- hold embedded bundle-signing public keys;
- expose default public network metadata.

Suggested content:

```go
package trust

const DefaultPublicNetworkName = "tubo-public"
const DefaultPublicNetworkBundleURL = "https://tubo.click/.well-known/tubo/networks/tubo-public.bundle"

var BundleSigningKeys = map[string]string{
    "tubo-root-2026": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEMmu4uNA2C/KKW1VX/1Cr/PSasaa8bvi9ExjBhNqltQ bettersafethansorry@tubo.click",
}
```

### `internal/networkbundle`

Suggested files:

```text
internal/networkbundle/bundle.go
internal/networkbundle/fetch.go
internal/networkbundle/verify.go
internal/networkbundle/install.go
```

Responsibilities:

- parse bundle envelopes;
- decode base64url payloads;
- parse SSH Ed25519 public keys;
- verify Ed25519 signatures;
- validate time windows;
- validate relay multiaddrs;
- validate swarm key format;
- install config and swarm key into the existing local layout.

Suggested API:

```go
type InstallOptions struct {
    BundleURL string
    ConfigDir string
    Force bool
}

type InstallResult struct {
    NetworkName string
    NetworkID string
    ConfigPath string
    SwarmKeyPath string
    RelayPeers []string
    BootstrapPeers []string
    KeyID string
}

func Fetch(ctx context.Context, url string) ([]byte, error)
func Parse(data []byte) (*Bundle, error)
func Verify(bundle *Bundle, trustedKeys map[string]string) ([]byte, string, error)
func DecodePayload(payloadBytes []byte) (*NetworkPayload, error)
func Install(payload *NetworkPayload, opts InstallOptions) (*InstallResult, error)
```

## CLI implementation plan

### 1. Replace legacy dispatch

Remove public dispatch for:

```bash
tubo edge run
tubo service run
tubo bridge run
tubo relay run
```

Prefer explicit command functions:

```go
func attachCmd(args []string) error
func connectCmd(args []string) error
func gatewayCmd(args []string) error
func relayCmd(args []string) error
func joinCmd(args []string) error
```

`runRole` may remain as an internal helper during the first refactor, but it should not be reachable through public role commands.

### 2. Keep manual join but add bundle join

Current manual join remains valid:

```bash
tubo join --relay <multiaddr> --swarm-key ./swarm.key
```

New public join modes:

```bash
tubo join
tubo join tubo-public
tubo join --bundle-url https://example.com/network.bundle
```

Behavior:

```text
fetch bundle
verify signature
validate payload
write config.yaml
write swarm.key
print joined network summary
```

### 3. Replace implicit init with implicit public join

Current `maybeImplicitInit` creates a local swarm. That should no longer happen for `attach` and `connect`.

Add:

```go
func ensureJoinedPublicNetwork(command string, noInit bool) error
```

Behavior:

```text
if config.yaml exists:
  return nil

if --no-init:
  return helpful error

if CI=true:
  return helpful error

fetch and install tubo-public bundle
```

Use for:

```text
attach
connect
gateway
```

Do not use for:

```text
relay
```

### 4. Add demo shorthand for attach

Add:

```bash
tubo attach dummysvc --port 8080
```

Equivalent to:

```bash
tubo attach http://127.0.0.1:8080 --name dummysvc
```

Rules:

- if the first positional argument looks like an HTTP URL, treat it as the target;
- if the first positional argument is not a URL and `--port` is set, treat it as the service name;
- if shorthand name and `--name` conflict, return an error;
- default host is `127.0.0.1`.

### 5. Add network catalog inspection

Add:

```bash
tubo get networks
tubo get network tubo-public
```

`get networks` reads:

```text
https://tubo.click/.well-known/tubo/networks.json
```

`get network tubo-public` fetches and verifies the bundle but does not install it.

This can be implemented in this PR or a direct follow-up. It is not required for the first demo path.

## Documentation changes

Update:

```text
README.md
docs/cli.md
TASKS.md
```

Remove:

- advanced role command examples;
- old UX mapping table;
- quickstart sections that require manual relay address and swarm key copy for the public demo path.

New primary quickstart:

```bash
# Machine A
curl -fsSL https://tubo.click/install.sh | sh
tubo attach dummysvc --port 8080
```

```bash
# Machine B
curl -fsSL https://tubo.click/install.sh | sh
tubo connect dummysvc
```

Manual join can remain documented under advanced/private development usage.

## Test plan

Remove or rewrite tests that assert legacy role command compatibility.

Add tests:

```text
TestJoinDefaultPublicNetworkFromSignedBundle
TestJoinNamedPublicNetworkFromSignedBundle
TestJoinRejectsInvalidBundleSignature
TestJoinRejectsUnknownBundleKey
TestJoinRejectsExpiredBundle
TestJoinRejectsMalformedRelay
TestJoinRejectsMalformedSwarmKey
TestAttachAutoJoinsDefaultPublicNetwork
TestConnectAutoJoinsDefaultPublicNetwork
TestAttachNamePortShorthand
TestLegacyRoleCommandsAreRejected
TestManualJoinStillWorks
```

Acceptance cases:

```text
No local config + attach = public bundle installed + service starts
No local config + connect = public bundle installed + discovery starts
Invalid bundle signature = no config written
Unknown key_id = no config written
Expired bundle = no config written
Existing config = no bundle fetch
CI=true = no implicit join
--no-init = no implicit join
```

## Security acceptance criteria

```text
bundle signature is verified before writing any config
unknown key_id is rejected
invalid signature is rejected
expired bundle is rejected
malformed swarm key is rejected
malformed relay multiaddr is rejected
peer identity is not downloaded or bundled
signing private key is never committed
swarm key is described as swarm key / PSK, not peer private key
```

## Runtime acceptance criteria

The implementation should not require major changes to:

```text
internal/app/service
internal/app/edge
internal/app/relay
internal/p2p
internal/discovery
```

The existing runtime behavior should continue to power the new UX.

## Recommended implementation order

1. Add `internal/trust` with embedded public signing key.
2. Add `internal/networkbundle` parser, verifier, and installer.
3. Add tests for bundle verification and install.
4. Extend `tubo join` with bundle mode.
5. Add implicit public join for `attach`.
6. Add implicit public join for `connect`.
7. Remove public legacy role command dispatch.
8. Add `tubo attach <name> --port <port>` shorthand.
9. Update docs and usage.
10. Add optional `tubo get networks` and `tubo get network <name>`.
11. Run unit, integration, and smoke tests.

## Open decisions

- Whether `gateway` should auto-join `tubo-public` in the same way as `attach` and `connect`.
- Whether `relay` should keep any implicit local init behavior or require explicit flags/config.
- Whether `tubo get networks` belongs in the first implementation PR or should follow immediately after.
- Whether bundle creation/signing should be implemented as `tubo bundle ...` commands or as scripts under `scripts/`.
