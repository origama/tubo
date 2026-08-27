// Package bundle defines and verifies the bounded, signed public-bootstrap
// bundle contract (A0 issue #44).
//
// This file embeds the production bootstrap trust material: the pinned
// bootstrap root public key and the signed bundle for relay.tubo.click.
// The private key for tubo-bootstrap-root-2026-01 was generated outside this
// repository, used to sign the bundle, and then permanently deleted from the
// build environment. Only the public key and signed bundle bytes are embedded.
//
// Bundle content:
//   - Version: 1, Kind: bootstrap
//   - Root key ID: tubo-bootstrap-root-2026-01
//   - Relay PeerID: 12D3KooWM2hGwzrJYGyjUwtzrzLZuPWAmjiG8JeNM5YYJNPeHLME
//   - Addresses: /dns4/relay.tubo.click/tcp/4001, /ip4/172.232.189.160/tcp/4001
//   - Validity: 2026-08-27T00:00:00Z to 2027-02-23T00:00:00Z (180 days)
//   - Signature: Ed25519 / tubo-bootstrap-root-2026-01
//
// Governance: issue #45, Step 10A.
//
// Trust policy for root tubo-bootstrap-root-2026-01:
//   - SINGLE-USE signing root: the private signing key was generated outside
//     the repository, used to sign this bundle, and permanently deleted.
//   - The current bundle CANNOT be renewed or reissued under this root.
//   - Preserving the relay identity (PeerID, addresses) avoids re-signing
//     for Step 10B; if provider identity/address material must change, a NEW
//     root+bundle release is required under the current A0 model.
//   - Runtime trust-root and bundle rotation remains A2 work.
//   - The embedded bundle expires 2027-02-23.
//
// The bundle references relay.tubo.click which currently runs prototype code
// with a private swarm PSK. The relay software must be upgraded to
// A0-compatible code (plain libp2p without swarm key) before the bundle can
// be used in production. The prototype does NOT "reject the A0 bundle": the
// bundle is locally verifiable; the connectivity failure is the prototype
// private-network/swarm-PSK handshake incompatibility.
package bundle

import (
	"embed"
)

//go:embed production_bundle.bin
var productionBundleFS embed.FS

// MustProductionBundle returns the embedded production bootstrap bundle
// bytes. It panics if the embedded file is missing (a build-time error).
func MustProductionBundle() []byte {
	data, err := productionBundleFS.ReadFile("production_bundle.bin")
	if err != nil {
		panic("production_bundle.bin: " + err.Error())
	}
	return data
}

// ProductionRoots returns the pinned bootstrap trust roots. Each root has
// a dedicated RoleBootstrap role and a public key. A bundle can reference a
// root by KeyID but can never introduce a new root (no TOFU).
func ProductionRoots() []Root {
	return append([]Root(nil), productionRoots...)
}

// productionRoots is the set of pinned bootstrap trust roots.
var productionRoots = []Root{
	{
		KeyID: "tubo-bootstrap-root-2026-01",
		Role:  RoleBootstrap,
		// Generated outside repository, signed bundle, private key deleted.
		Public: []byte{
			0x2a, 0x4c, 0x58, 0x8b, 0x7c, 0x73, 0x46, 0x46,
			0xc7, 0xc3, 0x11, 0x68, 0x85, 0x3e, 0x2a, 0xbb,
			0x33, 0x3b, 0xaa, 0x12, 0x4d, 0x1c, 0x93, 0x9b,
			0x10, 0xf2, 0xf1, 0xee, 0x8d, 0xb1, 0xd2, 0x2a,
		},
	},
}
