// Architecture dependency-edge checker.
// Runs go list -deps for target packages and checks each non-stdlib dep
// against the rules.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// DepGraph holds the computed dependency graph.
type DepGraph map[string][]string // package -> deps (non-stdlib only)

// Violation is a single rule violation found.
type Violation struct {
	RuleID      string
	Source      string
	Target      string
	Description string
	Path        string // transitive path summary
}

// ComputeRealGraph runs go list -deps for every internal package.
func ComputeRealGraph(moduleRoot string, allPackages []string) (DepGraph, error) {
	graph := make(DepGraph)
	for _, pkg := range allPackages {
		if !strings.HasPrefix(pkg, moduleRoot+"/internal/") && pkg != moduleRoot+"/internal" {
			continue
		}
		deps, err := listDeps(pkg, moduleRoot)
		if err != nil {
			return nil, fmt.Errorf("listing deps for %s: %w", pkg, err)
		}
		// Filter to non-stdlib deps (those in module)
		var filtered []string
		for _, d := range deps {
			if strings.HasPrefix(d, moduleRoot+"/") {
				filtered = append(filtered, d)
			}
		}
		graph[pkg] = filtered
	}
	return graph, nil
}

// listDeps runs go list -deps for a single package.
func listDeps(pkg, moduleRoot string) ([]string, error) {
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", pkg)
	cmd.Dir = moduleRoot
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list -deps %s: %w", pkg, err)
	}
	lines := strings.TrimSpace(stdout.String())
	if lines == "" {
		return []string{}, nil
	}
	return strings.Split(lines, "\n"), nil
}

// CheckGraph checks the computed dependency graph against all rules.
// modulePrefix is the module path (e.g., "github.com/origama/tubo-rebuilt");
// if non-empty, short rule paths like "internal/engine" are expanded to full paths.
func CheckGraph(graph DepGraph, rules []RuleConfig, zones ZonesConfig, modulePrefix string) []Violation {
	var violations []Violation

	// Build zone -> packages and package -> zone maps
	pkgToZone := buildPkgToZone(zones)

	// Build zone-level forbid sets
	forbiddenZonePackages := buildZonePackageSet(zones)

	// Build set of stdlib-only target packages
	stdlibOnlyTargets := make(map[string]bool)
	for _, rule := range rules {
		if rule.Type == "stdlib-only" {
			for _, t := range rule.Targets {
				stdlibOnlyTargets[resolvePkg(t, modulePrefix)] = true
			}
		}
	}

	for _, rule := range rules {
		for _, rawSource := range rule.Targets {
			source := resolvePkg(rawSource, modulePrefix)
			// Only check packages that exist in the graph
			deps, ok := graph[source]
			if !ok {
				// If source explicitly exists as a key, it was computed
				// If not, it may not be a real package; skip for synthetic tests
				continue
			}
			switch rule.Type {
			case "stdlib-only":
				// Check transitive deps are all non-module
				// Allow deps that are also stdlib-only targets (same pure-core zone)
				for _, dep := range deps {
					if stdlibOnlyTargets[dep] {
						continue
					}
					violations = append(violations, Violation{
						RuleID:      rule.ID,
						Source:      source,
						Target:      dep,
						Description: rule.Description,
						Path:        fmt.Sprintf("%s -> %s (transitive)", source, dep),
					})
				}
			case "forbidden-target":
				kind := rule.Kind
				if kind == "" {
					kind = "transitive" // default
				}
				forbidden := expandForbidden(rule.Forbid, forbiddenZonePackages, pkgToZone, modulePrefix)
				forbiddenMap := makeSet(forbidden)

				if kind == "direct" {
					violations = append(violations, checkDirect(source, deps, forbiddenMap, rule)...)
				} else {
					violations = append(violations, checkTransitive(source, deps, forbiddenMap, rule, graph)...)
				}
			}
		}
	}

	return violations
}

func buildPkgToZone(zones ZonesConfig) map[string]string {
	pkgToZone := make(map[string]string)
	for zoneName, pkgs := range zones {
		for _, pkg := range pkgs {
			pkgToZone[pkg] = zoneName
		}
	}
	return pkgToZone
}

func buildZonePackageSet(zones ZonesConfig) map[string][]string {
	result := make(map[string][]string)
	for zoneName, pkgs := range zones {
		result[zoneName] = pkgs
	}
	return result
}

func expandForbidden(forbid []string, zonePkgs map[string][]string, pkgToZone map[string]string, modulePrefix string) []string {
	var result []string
	for _, f := range forbid {
		if pkgs, ok := zonePkgs[f]; ok {
			for _, p := range pkgs {
				result = append(result, resolvePkg(p, modulePrefix))
			}
		} else if _, ok := pkgToZone[f]; ok {
			// It's a package name
			result = append(result, resolvePkg(f, modulePrefix))
		} else {
			// Might be a package outside zones or a zone name we don't know
			result = append(result, resolvePkg(f, modulePrefix))
		}
	}
	return result
}

func resolvePkg(pkg, modulePrefix string) string {
	if modulePrefix != "" && !strings.HasPrefix(pkg, modulePrefix) {
		return modulePrefix + "/" + pkg
	}
	return pkg
}

func checkDirect(source string, deps []string, forbidden map[string]bool, rule RuleConfig) []Violation {
	var violations []Violation
	for _, dep := range deps {
		if forbidden[dep] {
			violations = append(violations, Violation{
				RuleID:      rule.ID,
				Source:      source,
				Target:      dep,
				Description: rule.Description,
				Path:        fmt.Sprintf("%s -> %s (direct)", source, dep),
			})
		}
	}
	return violations
}

func checkTransitive(source string, deps []string, forbidden map[string]bool, rule RuleConfig, graph DepGraph) []Violation {
	var violations []Violation
	visited := make(map[string]bool)
	var walk func(pkg string, path []string)
	walk = func(pkg string, path []string) {
		if visited[pkg] {
			return
		}
		visited[pkg] = true
		if forbidden[pkg] && pkg != source {
			violations = append(violations, Violation{
				RuleID:      rule.ID,
				Source:      source,
				Target:      pkg,
				Description: rule.Description,
				Path:        fmt.Sprintf("%s -> ... -> %s", source, pkg),
			})
			return
		}
		subdeps := graph[pkg]
		for _, sub := range subdeps {
			newPath := append(path, sub)
			walk(sub, newPath)
		}
	}
	walk(source, []string{source})
	return violations
}

// MakeSet creates a set from a string slice.
func makeSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}

// CheckFileGuards runs file-level import checks.
func CheckFileGuards(moduleRoot string, guards []FileGuardConfig) []Violation {
	var violations []Violation
	for _, g := range guards {
		fullPath := filepath.Join(moduleRoot, g.Path)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			violations = append(violations, Violation{
				RuleID:      g.Rule,
				Description: g.Description,
				Source:      g.Path,
				Target:      "",
				Path:        fmt.Sprintf("cannot read file: %v", err),
			})
			continue
		}
		content := string(data)
		for _, forbid := range g.ForbiddenImports {
			// Look for the import line
			importLine := `"` + forbid + `"`
			if strings.Contains(content, importLine) {
				violations = append(violations, Violation{
					RuleID:      g.Rule,
					Source:      g.Path,
					Target:      forbid,
					Description: g.Description,
					Path:        fmt.Sprintf("%s imports %s (direct file import)", g.Path, forbid),
				})
			}
		}
	}
	return violations
}

// CheckSemantic runs pattern-based source code checks.
func CheckSemantic(moduleRoot string, checks []SemanticCheckConfig) []Violation {
	var violations []Violation
	for _, sc := range checks {
		glob := filepath.Join(moduleRoot, sc.PathGlob)
		matches, err := filepath.Glob(glob)
		if err != nil {
			violations = append(violations, Violation{
				RuleID:      sc.Rule,
				Description: sc.Description,
				Source:      sc.PathGlob,
				Target:      "",
				Path:        fmt.Sprintf("invalid glob: %v", err),
			})
			continue
		}
		for _, match := range matches {
			data, err := os.ReadFile(match)
			if err != nil {
				continue
			}
			content := string(data)
			for _, pattern := range sc.ForbidPatterns {
				if strings.Contains(content, pattern) {
					violations = append(violations, Violation{
						RuleID:      sc.Rule,
						Source:      match,
						Target:      pattern,
						Description: sc.Description,
						Path:        fmt.Sprintf("%s contains forbidden pattern %q", match, pattern),
					})
				}
			}
		}
	}
	return violations
}

// ReportViolations formats and prints violations.
func ReportViolations(violations []Violation) string {
	if len(violations) == 0 {
		return ""
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].RuleID != violations[j].RuleID {
			return violations[i].RuleID < violations[j].RuleID
		}
		return violations[i].Source < violations[j].Source
	})
	var b strings.Builder
	for _, v := range violations {
		b.WriteString(fmt.Sprintf("[%s] %s\n", v.RuleID, v.Description))
		b.WriteString(fmt.Sprintf("  Source: %s\n", v.Source))
		if v.Target != "" {
			b.WriteString(fmt.Sprintf("  Target: %s\n", v.Target))
		}
		b.WriteString(fmt.Sprintf("  Path: %s\n", v.Path))
		b.WriteString("\n")
	}
	return b.String()
}

// getModuleRoot returns the git repo root (where go.mod lives).
func getModuleRoot() string {
	// Use PROJECT_ROOT env var
	if root := os.Getenv("PROJECT_ROOT"); root != "" {
		return root
	}
	// Walk up from cwd to find go.mod
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// getModulePath reads the module path from go.mod.
func getModulePath(moduleRoot string) (string, error) {
	data, err := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("reading go.mod: %w", err)
	}
	// Parse first line: "module <path>"
	first := string(data)
	if idx := strings.Index(first, "\n"); idx >= 0 {
		first = first[:idx]
	}
	parts := strings.Fields(first)
	if len(parts) < 2 || parts[0] != "module" {
		return "", fmt.Errorf("cannot parse module path from go.mod")
	}
	return parts[1], nil
}

// RealRun runs the checker on the actual repository.
func RealRun() ([]Violation, error) {
	moduleRoot := getModuleRoot()
	modulePath, err := getModulePath(moduleRoot)
	if err != nil {
		return nil, err
	}

	// Load rules
	rf, err := LoadRules(filepath.Join(moduleRoot, "tests/archcheck/rules.yaml"))
	if err != nil {
		return nil, fmt.Errorf("loading rules: %w", err)
	}

	// Get all internal packages
	allPkgs, err := listAllPackages(moduleRoot)
	if err != nil {
		return nil, err
	}

	// Compute graph
	graph, err := ComputeRealGraph(moduleRoot, allPkgs)
	if err != nil {
		return nil, err
	}

	// Add empty dep entries for packages that had no result (may not be keyed)
	for _, pkg := range allPkgs {
		if _, ok := graph[pkg]; !ok {
			graph[pkg] = nil
		}
	}

	// Check rules
	violations := CheckGraph(graph, rf.Rules, rf.Zones, modulePath)

	// Check file guards
	violations = append(violations, CheckFileGuards(moduleRoot, rf.FileGuards)...)

	// Check semantic
	violations = append(violations, CheckSemantic(moduleRoot, rf.SemanticChecks)...)

	_ = modulePath
	return violations, nil
}

// listAllPackages runs go list ./internal/... to find all internal packages.
func listAllPackages(moduleRoot string) ([]string, error) {
	cmd := exec.Command("go", "list", "./internal/...")
	cmd.Dir = moduleRoot
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list ./internal/...: %w", err)
	}
	lines := strings.TrimSpace(stdout.String())
	if lines == "" {
		return nil, nil
	}
	return strings.Split(lines, "\n"), nil
}
