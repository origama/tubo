package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	statspkg "github.com/origama/tubo/internal/runtime/stats"
)

type topRow struct {
	ProcessView    processView       `json:"process"`
	StatsAvailable bool              `json:"stats_available"`
	StatsError     string            `json:"stats_error,omitempty"`
	Stats          statspkg.Snapshot `json:"stats,omitempty"`
	RxBytesPerSec  float64           `json:"rx_bytes_per_sec,omitempty"`
	TxBytesPerSec  float64           `json:"tx_bytes_per_sec,omitempty"`
	RequestsPerSec float64           `json:"requests_per_sec,omitempty"`
}

type topReport struct {
	GeneratedAt time.Time `json:"generated_at"`
	Count       int       `json:"count"`
	Items       []topRow  `json:"items"`
}

type topSample struct {
	At   time.Time
	Snap statspkg.Snapshot
}

func topCmd(args []string) error {
	fs := flag.NewFlagSet("top", flag.ContinueOnError)
	all := fs.Bool("all", false, "")
	jsonOut := fs.Bool("json", false, "")
	interval := fs.Duration("interval", 2*time.Second, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	prev := map[string]topSample{}
	ctx, stop := signalContext()
	defer stop()
	for {
		report, err := collectTopReport(ctx, *all, prev)
		if err != nil {
			return err
		}
		if *jsonOut {
			return printJSON(report)
		}
		printTopReport(report)
		if *interval <= 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*interval):
		}
	}
}

func collectTopReport(ctx context.Context, includeAll bool, prev map[string]topSample) (topReport, error) {
	items, err := listProcessViews(includeAll)
	if err != nil {
		return topReport{}, err
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	now := time.Now().UTC()
	report := topReport{GeneratedAt: now, Count: len(items), Items: make([]topRow, 0, len(items))}
	for _, item := range items {
		row := topRow{ProcessView: item}
		if strings.TrimSpace(item.StatsURL) != "" {
			snap, err := fetchTopStats(ctx, item.StatsURL)
			if err != nil {
				row.StatsError = err.Error()
			} else {
				row.StatsAvailable = true
				row.Stats = snap
				if prevSnap, ok := prev[item.ID]; ok && snap.CollectedAt.After(prevSnap.At) {
					dt := snap.CollectedAt.Sub(prevSnap.At).Seconds()
					if dt > 0 {
						row.RxBytesPerSec = float64(snap.RxBytesTotal-prevSnap.Snap.RxBytesTotal) / dt
						row.TxBytesPerSec = float64(snap.TxBytesTotal-prevSnap.Snap.TxBytesTotal) / dt
						row.RequestsPerSec = float64(snap.RequestsTotal-prevSnap.Snap.RequestsTotal) / dt
					}
				}
				prev[item.ID] = topSample{At: snap.CollectedAt, Snap: snap}
			}
		} else {
			row.StatsError = "stats endpoint unavailable"
		}
		report.Items = append(report.Items, row)
	}
	return report, nil
}

func fetchTopStats(ctx context.Context, rawURL string) (statspkg.Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return statspkg.Snapshot{}, err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return statspkg.Snapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statspkg.Snapshot{}, fmt.Errorf("stats endpoint returned %s", resp.Status)
	}
	var snap statspkg.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return statspkg.Snapshot{}, err
	}
	if snap.CollectedAt.IsZero() {
		snap.CollectedAt = time.Now().UTC()
	}
	return snap, nil
}

func printTopReport(report topReport) {
	fmt.Printf("Running Tubo top (%s)\n\n", report.GeneratedAt.Format(time.RFC3339))
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tROLE\tKIND\tPATH\tRX/s\tTX/s\tRX TOTAL\tTX TOTAL\tACTIVE\tDONE\tERR\tREQ/s\tSTATUS")
	for _, item := range report.Items {
		status := item.ProcessView.Status
		if item.Stats.Status != "" {
			status = item.Stats.Status
		}
		path := item.ProcessView.Path
		if path == "" {
			path = item.Stats.Path
		}
		kind := item.ProcessView.ServiceKind
		if kind == "" {
			kind = item.Stats.Kind
		}
		rxTotal := humanizeBytes(item.Stats.RxBytesTotal)
		txTotal := humanizeBytes(item.Stats.TxBytesTotal)
		if item.StatsAvailable {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				item.ProcessView.Name,
				defaultTopValue(item.ProcessView.Command),
				defaultTopValue(kind),
				defaultTopValue(path),
				humanizeBytesPerSecond(item.RxBytesPerSec),
				humanizeBytesPerSecond(item.TxBytesPerSec),
				defaultTopValue(rxTotal),
				defaultTopValue(txTotal),
				defaultTopValue(fmt.Sprintf("%d", item.Stats.Active)),
				defaultTopValue(fmt.Sprintf("%d", item.Stats.Completed)),
				defaultTopValue(fmt.Sprintf("%d", item.Stats.Errors)),
				humanizeRate(item.RequestsPerSec),
				defaultTopValue(status),
			)
			continue
		}
		if item.StatsError != "" {
			status = defaultTopValue(status) + " (stats unavailable)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t-\t-\t-\t-\t-\t-\t%s\n", item.ProcessView.Name, defaultTopValue(item.ProcessView.Command), defaultTopValue(kind), defaultTopValue(path), defaultTopValue(status))
	}
	_ = w.Flush()
}

func defaultTopValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "-"
	}
	return v
}

func humanizeBytes(n int64) string {
	if n < 0 {
		return "-"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	value := float64(n)
	idx := 0
	for value >= 1024 && idx < len(units)-1 {
		value /= 1024
		idx++
	}
	if idx == 0 {
		return fmt.Sprintf("%.0f%s", value, units[idx])
	}
	return fmt.Sprintf("%.1f%s", value, units[idx])
}

func humanizeBytesPerSecond(rate float64) string {
	if rate <= 0 {
		return "-"
	}
	units := []string{"B/s", "KiB/s", "MiB/s", "GiB/s", "TiB/s"}
	value := rate
	idx := 0
	for value >= 1024 && idx < len(units)-1 {
		value /= 1024
		idx++
	}
	if idx == 0 {
		return fmt.Sprintf("%.0f%s", value, units[idx])
	}
	return fmt.Sprintf("%.1f%s", value, units[idx])
}

func humanizeRate(rate float64) string {
	if rate <= 0 {
		return "-"
	}
	if rate < 10 {
		return fmt.Sprintf("%.1f/s", rate)
	}
	return fmt.Sprintf("%.0f/s", rate)
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
