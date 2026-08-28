package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
)

// renderServiceTable prints the `get services` table: name, kind, target.
func renderServiceTable(out io.Writer, rows []map[string]any) error {
	sort.Slice(rows, func(i, j int) bool {
		a, _ := rows[i]["name"].(string)
		b, _ := rows[j]["name"].(string)
		return a < b
	})
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tKIND\tTARGET"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", row["name"], row["kind"], row["target"]); err != nil {
			return err
		}
	}
	return w.Flush()
}

// renderScopeTable prints the `get scopes` table: scope, role, current.
func renderScopeTable(out io.Writer, rows []map[string]any) error {
	sort.Slice(rows, func(i, j int) bool {
		a, _ := rows[i]["name"].(string)
		b, _ := rows[j]["name"].(string)
		return a < b
	})
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "SCOPE\tROLE\tCURRENT"); err != nil {
		return err
	}
	for _, row := range rows {
		role := "-"
		if authority, ok := row["authority"].(bool); ok && authority {
			role = "authority"
		}
		if roleValue, ok := row["role"].(string); ok && roleValue != "" {
			role = roleValue
		}
		current := "no"
		if value, ok := row["current"].(bool); ok && value {
			current = "yes"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", row["name"], role, current); err != nil {
			return err
		}
	}
	return w.Flush()
}

// renderOverlayTable prints the `get overlays` table (one row).
func renderOverlayTable(out io.Writer, row map[string]any) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	relays := joinRelays(row["relays"])
	if _, err := fmt.Fprintln(w, "REF\tRELAY\tREACHABILITY\tCURRENT SCOPE"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", row["ref"], relays, row["force_reachability"], row["current_scope"]); err != nil {
		return err
	}
	return w.Flush()
}

func joinRelays(value any) string {
	switch list := value.(type) {
	case []string:
		if len(list) == 0 {
			return "-"
		}
		return strings.Join(list, ",")
	case []any:
		parts := make([]string, 0, len(list))
		for _, item := range list {
			parts = append(parts, fmt.Sprintf("%v", item))
		}
		if len(parts) == 0 {
			return "-"
		}
		return strings.Join(parts, ",")
	}
	return "-"
}
