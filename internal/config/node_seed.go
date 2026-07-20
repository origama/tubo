package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// GenerateNodeSeed returns a 32-byte hex-encoded high-entropy seed suitable
// for deterministic libp2p identity derivation via p2p.PeerIDFromSeed.
func GenerateNodeSeed() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate node seed: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// EnsureNodeSeed makes sure the config has a stable node.seed. If the seed is
// already set, it is left untouched. Otherwise a fresh high-entropy seed is
// generated, written back into the returned Config, and persisted to the
// provided config path (when non-empty). This gives peer-bound membership
// capabilities a stable requester identity across restarts of processes such
// as `tubo connect service/...`, which otherwise pick an ephemeral peer id
// per candidate retry and get denied by namespace_members policy.
//
// Returned bool indicates whether a new seed was generated.
func EnsureNodeSeed(configPath string, cfg Config) (Config, bool, error) {
	if strings.TrimSpace(cfg.Node.Seed) != "" {
		return cfg, false, nil
	}
	seed, err := GenerateNodeSeed()
	if err != nil {
		return cfg, false, err
	}
	cfg.Node.Seed = seed
	if strings.TrimSpace(configPath) == "" {
		return cfg, true, nil
	}
	if err := WriteFile(configPath, cfg, true); err != nil {
		return cfg, false, fmt.Errorf("persist generated node.seed to %s: %w", configPath, err)
	}
	return cfg, true, nil
}
