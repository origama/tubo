package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/origama/tubo-rebuilt/internal/store"
)

// runtimeView is the JSON projection shared by ps and top. ps renders a
// compact lifecycle subset; top renders the full metrics table.
type runtimeView struct {
	PrimaryRef      string  `json:"primary_ref"`
	Kind            string  `json:"kind"`
	PID             int     `json:"pid"`
	Source          string  `json:"source"`
	Status          string  `json:"status"`
	PeerID          string  `json:"peer_id,omitempty"`
	Address         string  `json:"address,omitempty"`
	Sessions        int     `json:"sessions"`
	BytesIn         int64   `json:"bytes_in"`
	BytesOut        int64   `json:"bytes_out"`
	NetworkBytesIn  int64   `json:"network_bytes_in"`
	NetworkBytesOut int64   `json:"network_bytes_out"`
	RateIn          float64 `json:"network_rate_in"`
	RateOut         float64 `json:"network_rate_out"`
	ConnectedPeers  int     `json:"connected_peers"`
	OpenConnections int     `json:"open_connections"`
	Path            string  `json:"path,omitempty"`
}

func runtimeViews(ctx context.Context, state *store.Store) ([]runtimeView, error) {
	runs, err := state.Runs(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]runtimeView, 0, len(runs))
	for _, run := range runs {
		item := runtimeView{PrimaryRef: run.PrimaryRef, Kind: run.Kind, PID: run.PID, Source: run.Source, Status: run.Status, PeerID: run.PeerID, Address: run.Address}
		if run.Kind == "pipe" {
			sessions, err := state.Sessions(ctx, run.PrimaryRef)
			if err != nil {
				return nil, err
			}
			item.Sessions = len(sessions)
			item.Path, _ = sessionPath(sessions)
			if item.Path == "" && strings.Contains(run.Address, "/p2p-circuit") {
				item.Path = "relayed"
			}
			if item.Path == "" {
				item.Path = "unknown"
			}
			for _, session := range sessions {
				item.BytesIn += session.BytesIn
				item.BytesOut += session.BytesOut
			}
		}
		if stats, err := state.RuntimeStats(ctx, run.PrimaryRef); err == nil {
			item.NetworkBytesIn = stats.BytesIn
			item.NetworkBytesOut = stats.BytesOut
			item.RateIn = stats.RateIn
			item.RateOut = stats.RateOut
			item.ConnectedPeers = stats.ConnectedPeers
			item.OpenConnections = stats.OpenConnections
		}
		items = append(items, item)
	}
	return items, nil
}

// renderPS is the compact lifecycle view: what is running, its primary
// resource, status, PID/source, session count, and basic path/address. It
// deliberately omits byte rates, peer counts, and connection telemetry; those
// belong to top.
func renderPS(out io.Writer, items []runtimeView) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "REF\tKIND\tSTATUS\tPID\tSOURCE\tSESS\tPATH\tADDRESS"); err != nil {
		return err
	}
	for _, item := range items {
		address := truncate(item.Address, 56)
		path := item.Path
		if path == "" {
			path = "-"
		}
		source := item.Source
		if source == "" {
			source = "-"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%d\t%s\t%s\n", item.PrimaryRef, item.Kind, item.Status, item.PID, source, item.Sessions, path, address); err != nil {
			return err
		}
	}
	return w.Flush()
}

// renderTopTable is the live metrics view owned by top.
func renderTopTable(out io.Writer, items []runtimeView) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "REF\tKIND\tSTATUS\tPID\tSESS\tAPP RX\tAPP TX\tNET RX\tNET TX\tRX/s\tTX/s\tPEERS\tCONNS\tPATH\tADDRESS"); err != nil {
		return err
	}
	for _, item := range items {
		address := truncate(item.Address, 80)
		path := item.Path
		if path == "" {
			path = "-"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\n", item.PrimaryRef, item.Kind, item.Status, item.PID, item.Sessions, formatBytes(item.BytesIn), formatBytes(item.BytesOut), formatBytes(item.NetworkBytesIn), formatBytes(item.NetworkBytesOut), formatRate(item.RateIn), formatRate(item.RateOut), item.ConnectedPeers, item.OpenConnections, path, address); err != nil {
			return err
		}
	}
	return w.Flush()
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	if max <= 3 {
		return value[:max]
	}
	return value[:max-3] + "..."
}

func formatRate(value float64) string {
	if value <= 0 {
		return "0B/s"
	}
	return formatBytes(int64(value)) + "/s"
}

func formatBytes(value int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	n := float64(value)
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%dB", value)
	}
	return fmt.Sprintf("%.1f%s", n, units[i])
}

func (a App) ps(ctx context.Context, state *store.Store, f flags) error {
	items, err := runtimeViews(ctx, state)
	if err != nil {
		return err
	}
	if f.bools["json"] {
		return json.NewEncoder(a.Out).Encode(map[string]any{"items": items})
	}
	return renderPS(a.Out, items)
}

func (a App) top(ctx context.Context, state *store.Store, f flags) error {
	interval := time.Second
	if raw := f.values["interval"]; raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < 100*time.Millisecond {
			return fmt.Errorf("invalid_input: --interval must be a duration of at least 100ms")
		}
		interval = parsed
	}
	render := func(clear bool) error {
		items, err := runtimeViews(ctx, state)
		if err != nil {
			return err
		}
		if f.bools["json"] {
			return json.NewEncoder(a.Out).Encode(map[string]any{"observed_at": time.Now().UTC(), "items": items})
		}
		if clear {
			if _, err := fmt.Fprint(a.Out, "\033[H\033[2J"); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(a.Out, "Tubo runtime  %s  refresh %s  Ctrl-C exit\n\n", time.Now().Format(time.RFC3339), interval); err != nil {
			return err
		}
		return renderTopTable(a.Out, items)
	}
	if err := render(false); err != nil {
		return err
	}
	if f.bools["once"] || f.bools["json"] {
		return nil
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := render(true); err != nil {
				return err
			}
		}
	}
}
