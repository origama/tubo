// Architecture harness self-tests.
// All tests use synthetic graph input — never mutate the working tree.
package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func moduleRoot() string {
	return getModuleRoot()
}

// testRules loads rules from the real rules.yaml.
func testRules(t *testing.T) *RulesFile {
	t.Helper()
	rf, err := LoadRules(filepath.Join(moduleRoot(), "tests/archcheck/rules.yaml"))
	if err != nil {
		t.Fatalf("loading rules: %v", err)
	}
	return rf
}

// buildGraph creates a synthetic DepGraph from pairs.
func buildGraph(pairs [][2]string) DepGraph {
	graph := make(DepGraph)
	for _, p := range pairs {
		src, dst := p[0], p[1]
		graph[src] = append(graph[src], dst)
	}
	// Ensure leaf packages have empty dep lists
	seen := make(map[string]bool)
	for _, p := range pairs {
		seen[p[0]] = true
		seen[p[1]] = true
	}
	for p := range seen {
		if _, ok := graph[p]; !ok {
			graph[p] = nil
		}
	}
	return graph
}

// short package names matching rules.yaml format
var (
	pDomain      = "internal/domain"
	pAccessLease = "internal/accesslease"
	pLocalKey    = "internal/localkey"
	pExactAuth   = "internal/exactauthority"
	pNode        = "internal/node"
	pReach       = "internal/reachability"
	pBundle      = "internal/reachability/bundle"
	pEngine      = "internal/engine"
	pOpenAuth    = "internal/openauth"
	pUseCase     = "internal/usecase"
	pRuntime     = "internal/runtime"
	pStore       = "internal/store"
	pCLI         = "internal/cli"
	pDiscovery   = "internal/discovery"
	pAuthority   = "internal/authority"
	pProtocol    = "internal/protocol"
)

func Test1_AllowedDirectImport(t *testing.T) {
	rf := testRules(t)
	graph := buildGraph([][2]string{
		{pEngine, pDomain},
	})
	violations := CheckGraph(graph, rf.Rules, rf.Zones, "")
	if len(violations) > 0 {
		t.Errorf("expected 0 violations for engine->domain, got %d: %s", len(violations), ReportViolations(violations))
	}
}

func Test2_ForbiddenDirectImport(t *testing.T) {
	rf := testRules(t)
	graph := buildGraph([][2]string{
		{pEngine, pStore},
	})
	violations := CheckGraph(graph, rf.Rules, rf.Zones, "")
	found := false
	for _, v := range violations {
		if v.RuleID == "R6" && v.Source == pEngine && v.Target == pStore {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected R6 violation for engine->store, got %d violations: %s", len(violations), ReportViolations(violations))
	}
}

func Test3_TransitiveClosurePass(t *testing.T) {
	rf := testRules(t)
	graph := buildGraph([][2]string{
		{pAccessLease, pDomain},
	})
	violations := CheckGraph(graph, rf.Rules, rf.Zones, "")
	for _, v := range violations {
		if v.RuleID == "R1" && v.Source == pAccessLease {
			t.Errorf("unexpected R1 violation for accesslease->domain: %s", v.Path)
		}
	}
}

func Test4_TransitiveClosureFailure(t *testing.T) {
	rf := testRules(t)
	graph := buildGraph([][2]string{
		{pDomain, "github.com/some/thirdparty/pkg"},
	})
	violations := CheckGraph(graph, rf.Rules, rf.Zones, "")
	found := false
	for _, v := range violations {
		if v.RuleID == "R1" && v.Source == pDomain {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected R1 violation for domain->thirdparty, got %d violations: %s", len(violations), ReportViolations(violations))
	}
}

func Test5_ErrorNaming(t *testing.T) {
	rf := testRules(t)
	graph := buildGraph([][2]string{
		{pEngine, pStore},
	})
	violations := CheckGraph(graph, rf.Rules, rf.Zones, "")
	if len(violations) == 0 {
		t.Fatal("expected violation, got none")
	}
	v := violations[0]
	if v.RuleID == "" {
		t.Error("violation missing RuleID")
	}
	if v.Source == "" {
		t.Error("violation missing Source")
	}
	if v.Target == "" {
		t.Error("violation missing Target")
	}
	if v.Description == "" {
		t.Error("violation missing Description")
	}
}

func Test6_MalformedRuleFailsClosed(t *testing.T) {
	_, err := LoadRules("/nonexistent/rules.yaml")
	if err == nil {
		t.Error("expected error for nonexistent file, got nil")
	}
}

func Test7_UnknownProtectedPackage(t *testing.T) {
	rf := testRules(t)
	unknown := "internal/unknown"
	graph := buildGraph([][2]string{
		{unknown, pStore},
	})
	violations := CheckGraph(graph, rf.Rules, rf.Zones, "")
	_ = violations
}

func Test8_RealGraphPasses(t *testing.T) {
	violations, err := RealRun()
	if err != nil {
		t.Fatalf("RealRun failed: %v", err)
	}
	report := ReportViolations(violations)
	if report != "" {
		t.Errorf("real architecture violations found:\n%s", report)
	}
}

func Test9_SyntheticEngineTransitiveStore(t *testing.T) {
	rf := testRules(t)
	graph := buildGraph([][2]string{
		{pEngine, pRuntime},
		{pRuntime, pStore},
	})
	violations := CheckGraph(graph, rf.Rules, rf.Zones, "")
	foundStore := false
	for _, v := range violations {
		if v.RuleID == "R6" && v.Source == pEngine && v.Target == pStore {
			foundStore = true
			break
		}
	}
	if !foundStore {
		t.Errorf("expected R6 violation for engine->...->store, got %d violations: %s", len(violations), ReportViolations(violations))
	}
}

func Test10_SyntheticUsecaseRuntime(t *testing.T) {
	rf := testRules(t)
	graph := buildGraph([][2]string{
		{pUseCase, pRuntime},
	})
	violations := CheckGraph(graph, rf.Rules, rf.Zones, "")
	found := false
	for _, v := range violations {
		if v.RuleID == "R8" && v.Source == pUseCase && v.Target == pRuntime {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected R8 violation for usecase->runtime, got %d violations: %s", len(violations), ReportViolations(violations))
	}
}

func Test11_PurePackageGainingThirdPartyDep(t *testing.T) {
	rf := testRules(t)
	graph := buildGraph([][2]string{
		{pOpenAuth, "github.com/thirdparty/lib"},
	})
	violations := CheckGraph(graph, rf.Rules, rf.Zones, "")
	found := false
	for _, v := range violations {
		if v.RuleID == "R7" && v.Source == pOpenAuth {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected R7 violation for openauth->thirdparty, got %d violations: %s", len(violations), ReportViolations(violations))
	}
}

func Test12_SelfNonTrigger(t *testing.T) {
	rf := testRules(t)
	graph := buildGraph([][2]string{
		{pEngine, pEngine},
	})
	violations := CheckGraph(graph, rf.Rules, rf.Zones, "")
	for _, v := range violations {
		if v.RuleID == "R6" && v.Source == pEngine && v.Target == pEngine {
			t.Errorf("self-import should not trigger violation: %s", v.Path)
		}
	}
}

func TestCheckFileGuards(t *testing.T) {
	rf := testRules(t)
	violations := CheckFileGuards(moduleRoot(), rf.FileGuards)
	for _, v := range violations {
		if strings.HasPrefix(v.Target, "cannot read") {
			t.Errorf("file guard failed to read file %s: %s", v.Source, v.Target)
		}
	}
}

func TestCheckSemantic(t *testing.T) {
	rf := testRules(t)
	violations := CheckSemantic(moduleRoot(), rf.SemanticChecks)
	for _, v := range violations {
		if strings.Contains(v.Source, "_test.go") {
			continue
		}
		t.Errorf("semantic violation: %s", v.Path)
	}
}

func TestZonesExpandForbidden(t *testing.T) {
	zones := ZonesConfig{
		"outer-adapter-composition": {pRuntime, pStore, pCLI, pDiscovery, pAuthority, pProtocol},
	}
	pkgToZone := buildPkgToZone(zones)
	zonePkgs := buildZonePackageSet(zones)

	result := expandForbidden([]string{"outer-adapter-composition"}, zonePkgs, pkgToZone, "")
	expected := []string{pRuntime, pStore, pCLI, pDiscovery, pAuthority, pProtocol}
	if len(result) != len(expected) {
		t.Errorf("expected %d expanded forbidden packages, got %d: %v", len(expected), len(result), result)
	}
	for _, e := range expected {
		found := false
		for _, r := range result {
			if r == e {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %s in expanded forbidden list, got %v", e, result)
		}
	}
}

func TestEmptyGraph(t *testing.T) {
	rf := testRules(t)
	graph := buildGraph(nil)
	violations := CheckGraph(graph, rf.Rules, rf.Zones, "")
	if len(violations) != 0 {
		t.Errorf("expected 0 violations from empty graph, got %d", len(violations))
	}
}

func TestRuleValidation(t *testing.T) {
	tests := []struct {
		rf      RulesFile
		wantErr bool
	}{
		{RulesFile{Rules: []RuleConfig{{ID: "X", Type: "stdlib-only", Targets: []string{"pkg"}}}}, false},
		{RulesFile{Rules: []RuleConfig{{ID: "X", Type: "unknown-type", Targets: []string{"pkg"}}}}, true},
		{RulesFile{Rules: []RuleConfig{{ID: "X", Type: "forbidden-target", Targets: []string{"pkg"}}}}, true},
		{RulesFile{Rules: []RuleConfig{{ID: "X", Type: "forbidden-target", Targets: []string{"pkg"}, Forbid: []string{"other"}}}}, false},
		{RulesFile{Rules: []RuleConfig{{ID: "X", Type: "stdlib-only"}, {ID: "X", Type: "forbidden-target", Targets: []string{"pkg"}, Forbid: []string{"other"}}}}, true},
	}
	for _, tt := range tests {
		err := tt.rf.validate()
		if tt.wantErr && err == nil {
			t.Error("expected validation error, got nil")
		}
		if !tt.wantErr && err != nil {
			t.Errorf("unexpected validation error: %v", err)
		}
	}
}
