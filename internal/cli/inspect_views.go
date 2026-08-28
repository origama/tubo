package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/store"
)

// inspectPipeView is the structured view of one local Pipe: the --json form
// and the source of the labeled human form. Fields are selected and ordered
// for what an operator needs: identity, target, authorization, status, and
// current transport path.
type inspectPipeView struct {
	Ref               string              `json:"ref"`
	ID                string              `json:"id"`
	Kind              string              `json:"kind"`
	Scope             string              `json:"scope,omitempty"`
	Ingress           domain.PipeIngress  `json:"ingress"`
	Target            string              `json:"target"`
	Visibility        domain.Visibility   `json:"visibility,omitempty"`
	Access            domain.AccessPolicy `json:"access,omitempty"`
	Status            string              `json:"status"`
	LogicalPath       []string            `json:"logical_path"`
	AuthorizationPath []any               `json:"authorization_path"`
	TransportSummary  map[string]any      `json:"transport_summary"`
	Sessions          []store.Session     `json:"sessions"`
	RecordAge         string              `json:"record_age,omitempty"`
	RecordFresh       bool                `json:"record_fresh"`
}

// inspectRemotePipeView is the structured view of a verified remote Pipe
// record (status "available").
type inspectRemotePipeView struct {
	Ref               string          `json:"ref"`
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Kind              string          `json:"kind"`
	Scope             domain.Scope    `json:"scope"`
	PeerID            string          `json:"peer_id"`
	Status            string          `json:"status"`
	LogicalPath       []string        `json:"logical_path"`
	AuthorizationPath []any           `json:"authorization_path"`
	TransportSummary  map[string]any  `json:"transport_summary"`
	Sessions          []store.Session `json:"sessions"`
	RecordAge         string          `json:"record_age,omitempty"`
	RecordFresh       bool            `json:"record_fresh"`
}

// renderLabeled prints aligned "Label: value" lines.
func renderLabeled(out io.Writer, pairs ...[2]string) error {
	width := 0
	for _, pair := range pairs {
		if len(pair[0]) > width {
			width = len(pair[0])
		}
	}
	for _, pair := range pairs {
		if _, err := fmt.Fprintf(out, "%-*s  %s\n", width, pair[0], pair[1]); err != nil {
			return err
		}
	}
	return nil
}

func summaryString(value map[string]any, key string) string {
	if value == nil {
		return ""
	}
	if s, ok := value[key].(string); ok {
		return s
	}
	return ""
}

func renderInspectPipe(out io.Writer, v inspectPipeView) error {
	scope := v.Scope
	if scope == "" {
		scope = "-"
	}
	path := summaryString(v.TransportSummary, "current")
	if path == "" {
		path = "unknown"
	}
	source := summaryString(v.TransportSummary, "source")
	pathLine := path
	if source != "" {
		pathLine += " (" + source + ")"
	}
	sessions := "-"
	if len(v.Sessions) > 0 {
		sessions = fmt.Sprintf("%d", len(v.Sessions))
	}
	record := "-"
	if v.RecordAge != "" {
		record = v.RecordAge
		if v.RecordFresh {
			record += " (fresh)"
		} else {
			record += " (stale)"
		}
	}
	fmt.Fprintln(out, "Pipe:")
	return renderLabeled(out,
		[2]string{"Ref", v.Ref},
		[2]string{"ID", v.ID},
		[2]string{"Status", v.Status},
		[2]string{"Ingress", string(v.Ingress.Kind)},
		[2]string{"Target", v.Target},
		[2]string{"Scope", scope},
		[2]string{"Access", string(v.Access)},
		[2]string{"Path", pathLine},
		[2]string{"Sessions", sessions},
		[2]string{"Record", record},
	)
}

func renderInspectRemotePipe(out io.Writer, v inspectRemotePipeView) error {
	path := summaryString(v.TransportSummary, "current")
	if path == "" {
		path = "unknown"
	}
	record := v.RecordAge
	if v.RecordFresh {
		record += " (fresh)"
	} else {
		record += " (stale)"
	}
	fmt.Fprintln(out, "Pipe:")
	return renderLabeled(out,
		[2]string{"Ref", v.Ref},
		[2]string{"ID", v.ID},
		[2]string{"Name", v.Name},
		[2]string{"Status", v.Status},
		[2]string{"Peer", v.PeerID},
		[2]string{"Scope", v.Scope.String()},
		[2]string{"Path", path},
		[2]string{"Sessions", "-"},
		[2]string{"Record", record},
	)
}

func renderInspectService(out io.Writer, value domain.ServiceSpec) error {
	fmt.Fprintln(out, "Service:")
	return renderLabeled(out,
		[2]string{"Ref", value.Ref.String()},
		[2]string{"Kind", string(value.Kind)},
		[2]string{"Target", value.Target},
	)
}

func renderInspectRun(out io.Writer, run store.Run) error {
	source := run.Source
	if source == "" {
		source = "-"
	}
	fmt.Fprintln(out, "Runtime:")
	return renderLabeled(out,
		[2]string{"Ref", run.PrimaryRef},
		[2]string{"Status", run.Status},
		[2]string{"PID", fmt.Sprintf("%d", run.PID)},
		[2]string{"Source", source},
		[2]string{"Peer", fmtOrDash(run.PeerID)},
		[2]string{"Address", fmtOrDash(run.Address)},
	)
}

func fmtOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func renderInspectScope(out io.Writer, value map[string]any) error {
	role, _ := value["role"].(string)
	authority, _ := value["authority"].(bool)
	roleLine := "-"
	if role != "" {
		roleLine = role
	} else if authority {
		roleLine = "authority"
	}
	current := "no"
	if currentValue, ok := value["current"].(bool); ok && currentValue {
		current = "yes"
	}
	sync := value["sync"].(map[string]any)
	syncLines := []string{}
	if owner, ok := sync["owner_ref"].(string); ok && owner != "" {
		syncLines = append(syncLines, "sync owner: "+owner)
	}
	if last, ok := sync["last_success"].(string); ok && last != "" {
		syncLines = append(syncLines, "last sync:  "+last)
	}
	if errClass, ok := sync["error_class"].(string); ok && errClass != "" {
		syncLines = append(syncLines, "sync error: "+errClass)
	}
	renderLabeled(out,
		[2]string{"Ref", fmt.Sprintf("%v", value["ref"])},
		[2]string{"Role", roleLine},
		[2]string{"Current", current},
		[2]string{"Authority", value["authority_public"].(string)},
	)
	for _, line := range syncLines {
		fmt.Fprintln(out, line)
	}
	return nil
}

func renderInspectOverlay(out io.Writer, value map[string]any) error {
	relays := "-"
	if list, ok := value["relays"].([]string); ok && len(list) > 0 {
		relays = strings.Join(list, ", ")
	} else if list, ok := value["relays"].([]any); ok {
		parts := make([]string, 0, len(list))
		for _, item := range list {
			parts = append(parts, fmt.Sprintf("%v", item))
		}
		relays = strings.Join(parts, ", ")
	}
	return renderLabeled(out,
		[2]string{"Ref", fmt.Sprintf("%v", value["ref"])},
		[2]string{"Relay", relays},
		[2]string{"Reachability", fmt.Sprintf("%v", value["force_reachability"])},
		[2]string{"Current scope", fmt.Sprintf("%v", value["current_scope"])},
		[2]string{"Swarm key", fmt.Sprintf("%v", value["swarm_key"])},
	)
}

func renderInspectSession(out io.Writer, value store.Session) error {
	path := value.CurrentTransportPath
	if path == "" {
		path = value.InitialTransportPath
	}
	return renderLabeled(out,
		[2]string{"Ref", "session/" + value.ID},
		[2]string{"ID", value.ID},
		[2]string{"Pipe", value.PipeRef},
		[2]string{"Path", fmtOrDash(path)},
		[2]string{"Started", value.StartedAt.Local().Format("2006-01-02 15:04:05")},
		[2]string{"Bytes in", fmt.Sprintf("%d", value.BytesIn)},
		[2]string{"Bytes out", fmt.Sprintf("%d", value.BytesOut)},
	)
}

func renderInspectPeer(out io.Writer, value store.PeerAlias) error {
	note := value.Note
	if note == "" {
		note = "-"
	}
	updated := "-"
	if !value.UpdatedAt.IsZero() {
		updated = value.UpdatedAt.Local().Format("2006-01-02 15:04:05")
	}
	return renderLabeled(out,
		[2]string{"Ref", "peer/" + value.PeerID},
		[2]string{"Peer ID", value.PeerID},
		[2]string{"Name", value.Name},
		[2]string{"Note", note},
		[2]string{"Updated", updated},
	)
}
