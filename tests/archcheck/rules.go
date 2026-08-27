// Architecture dependency-edge rule definitions and YAML loading.
package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ZonesConfig maps zone names to package import paths.
type ZonesConfig map[string][]string

// RuleConfig is a single dependency rule.
type RuleConfig struct {
	ID          string   `yaml:"id"`
	Description string   `yaml:"description"`
	Type        string   `yaml:"type"` // stdlib-only, forbidden-target
	Targets     []string `yaml:"targets"`
	Forbid      []string `yaml:"forbid,omitempty"`
	Kind        string   `yaml:"kind,omitempty"` // direct, transitive (default: transitive)
}

// FileGuardConfig is a file-level import guard.
type FileGuardConfig struct {
	Rule             string   `yaml:"rule"`
	Description      string   `yaml:"description"`
	Path             string   `yaml:"path"`
	ForbiddenImports []string `yaml:"forbidden_imports"`
}

// SemanticCheckConfig is a source-level pattern scan.
type SemanticCheckConfig struct {
	Rule           string   `yaml:"rule"`
	Description    string   `yaml:"description"`
	PathGlob       string   `yaml:"path_glob"`
	ForbidPatterns []string `yaml:"forbid_patterns"`
}

// RulesFile is the top-level YAML structure.
type RulesFile struct {
	Zones          ZonesConfig           `yaml:"zones"`
	Rules          []RuleConfig          `yaml:"rules"`
	FileGuards     []FileGuardConfig     `yaml:"file_guards"`
	SemanticChecks []SemanticCheckConfig `yaml:"semantic_checks"`
}

// LoadRules reads and parses the rules YAML file.
func LoadRules(path string) (*RulesFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading rules file %s: %w", path, err)
	}
	rf := &RulesFile{}
	if err := yaml.Unmarshal(data, rf); err != nil {
		return nil, fmt.Errorf("parsing rules file %s: %w", path, err)
	}
	if err := rf.validate(); err != nil {
		return nil, fmt.Errorf("invalid rules: %w", err)
	}
	return rf, nil
}

func (rf *RulesFile) validate() error {
	validTypes := map[string]bool{
		"stdlib-only":      true,
		"forbidden-target": true,
	}
	validKinds := map[string]bool{
		"direct":     true,
		"transitive": true,
	}
	seenIDs := map[string]bool{}
	for _, r := range rf.Rules {
		if !validTypes[r.Type] {
			return fmt.Errorf("rule %s: unknown type %q", r.ID, r.Type)
		}
		if len(r.Targets) == 0 {
			return fmt.Errorf("rule %s: no targets", r.ID)
		}
		if r.Type == "forbidden-target" && len(r.Forbid) == 0 {
			return fmt.Errorf("rule %s: forbidden-target with no forbid entries", r.ID)
		}
		if r.Kind != "" && !validKinds[r.Kind] {
			return fmt.Errorf("rule %s: unknown kind %q", r.ID, r.Kind)
		}
		if seenIDs[r.ID] {
			return fmt.Errorf("duplicate rule ID %q", r.ID)
		}
		seenIDs[r.ID] = true
	}
	seenFG := map[string]bool{}
	for _, fg := range rf.FileGuards {
		if fg.Rule == "" {
			return fmt.Errorf("file guard with no rule ID")
		}
		if fg.Path == "" {
			return fmt.Errorf("file guard %s: no path", fg.Rule)
		}
		if len(fg.ForbiddenImports) == 0 {
			return fmt.Errorf("file guard %s: no forbidden imports", fg.Rule)
		}
		if seenFG[fg.Rule] {
			return fmt.Errorf("duplicate file guard rule ID %q", fg.Rule)
		}
		seenFG[fg.Rule] = true
	}
	if len(seenFG) == 0 && len(rf.FileGuards) > 0 {
		return fmt.Errorf("duplicate file guard rule IDs")
	}
	seenSC := map[string]bool{}
	for _, sc := range rf.SemanticChecks {
		if sc.Rule == "" {
			return fmt.Errorf("semantic check with no rule ID")
		}
		if sc.PathGlob == "" {
			return fmt.Errorf("semantic check %s: no path_glob", sc.Rule)
		}
		if len(sc.ForbidPatterns) == 0 {
			return fmt.Errorf("semantic check %s: no forbid_patterns", sc.Rule)
		}
		if seenSC[sc.Rule] {
			return fmt.Errorf("duplicate semantic check rule ID %q", sc.Rule)
		}
		seenSC[sc.Rule] = true
	}
	_ = seenIDs
	return nil
}
