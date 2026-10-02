package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRealGraphContainsRuntimeEdges(t *testing.T) {
	root := getModuleRoot()
	pkgs, err := listAllPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := ComputeRealGraph(root, pkgs)
	if err != nil {
		t.Fatal(err)
	}
	mod, err := getModulePath(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph[mod+"/internal/runtime"]) == 0 {
		t.Fatalf("empty runtime edges: computed %d packages from %d", len(graph), len(pkgs))
	}
}

func TestRealGraphDetectsInternalAndThirdPartyEdges(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                    "module example.test/app\n\ngo 1.24.0\n\nrequire example.test/external v0.0.0\nreplace example.test/external => ./external\n",
		"external/go.mod":           "module example.test/external\ngo 1.24.0\n",
		"external/lib.go":           "package external\n",
		"internal/core/core.go":     "package core\nimport _ \"example.test/external\"\n",
		"internal/engine/engine.go": "package engine\nimport _ \"example.test/app/internal/core\"\n",
	}
	for path, content := range files {
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	pkgs, err := listAllPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := ComputeRealGraph(root, pkgs)
	if err != nil {
		t.Fatal(err)
	}
	rules := []RuleConfig{
		{ID: "pure", Type: "stdlib-only", Targets: []string{"internal/core"}},
		{ID: "edge", Type: "forbidden-target", Targets: []string{"internal/engine"}, Forbid: []string{"example.test/external"}},
	}
	// Full import paths avoid any ambiguity between module and external names.
	rules[1].Forbid = []string{"example.test/external"}
	rules[0].Targets = []string{"example.test/app/internal/core"}
	rules[1].Targets = []string{"example.test/app/internal/engine"}
	violations := CheckGraph(graph, rules, nil, "")
	found := map[string]bool{}
	for _, v := range violations {
		found[v.RuleID] = true
	}
	if !found["pure"] || !found["edge"] {
		t.Fatalf("missing real violations: %s", ReportViolations(violations))
	}
}

func TestSemanticMissingSelectionFailsClosed(t *testing.T) {
	checks := []SemanticCheckConfig{{Rule: "test", PathGlob: "missing/*.go", ForbidPatterns: []string{"bad"}}}
	if len(CheckSemantic(t.TempDir(), checks)) == 0 {
		t.Fatal("missing guarded source silently passed")
	}
}

func TestSemanticRegexScansProductionCodeNotComments(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "guard.go")
	checks := []SemanticCheckConfig{{Rule: "test", PathGlob: "*.go", ForbidPatterns: []string{`map\[string\]any`}}}
	if err := os.WriteFile(path, []byte("package guard\n// map[string]any forbidden\nvar x map[string]any\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if len(CheckSemantic(root, checks)) == 0 {
		t.Fatal("regex guard did not detect production declaration")
	}
	if err := os.WriteFile(path, []byte("package guard\n// map[string]any forbidden\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := CheckSemantic(root, checks); len(got) != 0 {
		t.Fatalf("comment triggered guard: %v", got)
	}
}
