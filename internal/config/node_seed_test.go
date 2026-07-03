package config

import (
	"path/filepath"
	"testing"
)

func TestEnsureNodeSeedGeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Seed the file with empty config first.
	if err := WriteFile(path, Config{}, true); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.Node.Seed != "" {
		t.Fatalf("expected empty seed, got %q", cfg.Node.Seed)
	}
	out, generated, err := EnsureNodeSeed(path, cfg)
	if err != nil {
		t.Fatalf("EnsureNodeSeed: %v", err)
	}
	if !generated {
		t.Fatal("expected generated=true")
	}
	if out.Node.Seed == "" {
		t.Fatal("returned config has empty seed")
	}
	reloaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile after: %v", err)
	}
	if reloaded.Node.Seed != out.Node.Seed {
		t.Fatalf("persisted seed %q != returned %q", reloaded.Node.Seed, out.Node.Seed)
	}
}

func TestEnsureNodeSeedIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := WriteFile(path, Config{}, true); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	first, gen, err := EnsureNodeSeed(path, Config{})
	if err != nil || !gen || first.Node.Seed == "" {
		t.Fatalf("first ensure: gen=%v err=%v seed=%q", gen, err, first.Node.Seed)
	}
	second, gen2, err := EnsureNodeSeed(path, first)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if gen2 {
		t.Fatal("expected generated=false on second call")
	}
	if second.Node.Seed != first.Node.Seed {
		t.Fatalf("seed changed: %q vs %q", second.Node.Seed, first.Node.Seed)
	}
}

func TestEnsureNodeSeedWithoutConfigPathUpdatesValueOnly(t *testing.T) {
	cfg := Config{}
	out, generated, err := EnsureNodeSeed("", cfg)
	if err != nil {
		t.Fatalf("EnsureNodeSeed: %v", err)
	}
	if !generated {
		t.Fatal("expected generated=true")
	}
	if out.Node.Seed == "" {
		t.Fatal("expected returned config to contain generated seed")
	}
	if cfg.Node.Seed != "" {
		t.Fatalf("input config unexpectedly mutated: %q", cfg.Node.Seed)
	}
}

func TestGenerateNodeSeedHighEntropy(t *testing.T) {
	seed1, err := GenerateNodeSeed()
	if err != nil {
		t.Fatalf("GenerateNodeSeed: %v", err)
	}
	seed2, err := GenerateNodeSeed()
	if err != nil {
		t.Fatalf("GenerateNodeSeed 2: %v", err)
	}
	if seed1 == "" || seed2 == "" {
		t.Fatal("empty seed generated")
	}
	if seed1 == seed2 {
		t.Fatalf("duplicate seeds: %q", seed1)
	}
	if len(seed1) != 64 {
		t.Fatalf("expected 64-char hex, got %d", len(seed1))
	}
}
