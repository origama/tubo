package cli

import (
	"fmt"
	"io"
	"strings"
)

// helpTop prints the top-level help. It is derived from the command spec tree
// so the described surface cannot drift from the implemented one. No Store,
// network, or side effect is touched.
func (a App) helpTop() {
	out := a.Out
	fmt.Fprintln(out, "tubo — private connectivity through composable pipes")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage: tubo <command> [args]")
	fmt.Fprintln(out)
	printTopGroup(out, "Workflows:", []string{"accept", "attach", "connect", "share"})
	printTopGroup(out, "Resource operations:", []string{"get", "inspect", "start", "stop", "restart", "rm", "logs", "ps", "top", "watch", "doctor"})
	printTopGroup(out, "Resources:", []string{"service", "pipe", "scope", "peers", "config", "supervise"})
	printTopGroup(out, "Infrastructure:", []string{"join", "relay", "authority", "revoke", "keygen", "version"})
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Global flags:")
	fmt.Fprintln(out, "  -q, --quiet       suppress progress/hint output on stderr")
	fmt.Fprintln(out, "  -v, -vv, -vvv     increase verbosity")
	fmt.Fprintln(out, "  --log-level LEVEL error|warn|info|debug|trace")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "stdout carries the primary result; stderr carries progress,")
	fmt.Fprintln(out, "warnings, and hints. --json emits exactly one JSON value on stdout")
	fmt.Fprintln(out, "(watch --json emits NDJSON).")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Run 'tubo <command> --help' for command details.")
}

// printTopGroup renders one help section using each command's summary from
// the spec tree, so the top-level help cannot drift from the real surface.
func printTopGroup(out io.Writer, heading string, names []string) {
	fmt.Fprintln(out, heading)
	for _, name := range names {
		spec := specFor(name)
		if spec == nil {
			continue
		}
		fmt.Fprintf(out, "  %-10s %s\n", name, spec.summary)
	}
	fmt.Fprintln(out)
}

func printGroup(out io.Writer, rows [][]string) {
	for _, row := range rows {
		fmt.Fprintf(out, "  %-10s %s\n", row[0], row[1])
	}
}

// printHelp renders help for one command, group, or leaf, derived entirely
// from its commandSpec.
func (a App) printHelp(spec *commandSpec) {
	out := a.Out
	fmt.Fprintf(out, "%s — %s\n", spec.id(), spec.summary)
	if spec.usage != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Usage:")
		fmt.Fprintf(out, "  %s\n", spec.usage)
	}
	if len(spec.flags) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Flags:")
		longest := 0
		for _, fs := range spec.flags {
			form := "--" + fs.name
			if fs.value {
				form += " <value>"
			}
			if fs.short != "" {
				form += " (or -" + fs.short + ")"
			}
			if len(form) > longest {
				longest = len(form)
			}
		}
		for _, fs := range spec.flags {
			form := "--" + fs.name
			if fs.value {
				form += " <value>"
			}
			marker := ""
			if fs.required {
				marker = " (required)"
			}
			note := fs.help
			if fs.obsolete != "" {
				note = "obsolete; " + fs.obsolete
			}
			if fs.short != "" {
				note = strings.TrimSpace(note + " (also -" + fs.short + ")")
			}
			fmt.Fprintf(out, "  %-*s %s%s\n", longest, form, note, marker)
		}
	}
	if len(spec.args) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Arguments:")
		for _, arg := range spec.args {
			fmt.Fprintf(out, "  %s\n", arg)
		}
	}
	if spec.isGroup() {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Subcommands:")
		for _, child := range groupSpecs(spec) {
			fmt.Fprintf(out, "  %-20s %s\n", strings.Join(child.path[len(spec.path):], " "), child.summary)
		}
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Run 'tubo <command> <subcommand> --help' for details.")
	}
	if spec.detail != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, spec.detail)
	}
}

// validateSpecTree returns an error when the command spec tree is internally
// inconsistent: duplicate paths, leaves missing from leafPaths, or leafPaths
// entries without a spec. It is exercised by a test.
func validateSpecTree() error {
	seen := map[string]bool{}
	var leaves []string
	for _, spec := range commands {
		if seen[spec.id()] {
			return fmt.Errorf("duplicate command spec %q", spec.id())
		}
		seen[spec.id()] = true
		if !spec.isGroup() {
			leaves = append(leaves, spec.id())
		}
	}
	want := map[string]bool{}
	for _, path := range leafPaths {
		want[path] = true
	}
	for _, path := range leaves {
		if !want[path] {
			return fmt.Errorf("spec leaf %q missing from leafPaths", path)
		}
	}
	got := map[string]bool{}
	for _, path := range leafPaths {
		got[path] = true
	}
	for _, path := range leaves {
		got[path] = false
	}
	for path, present := range got {
		if present {
			return fmt.Errorf("leafPaths entry %q has no command spec", path)
		}
	}
	return nil
}
