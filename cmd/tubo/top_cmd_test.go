package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	statspkg "github.com/origama/tubo/internal/runtime/stats"
)

func TestCollectTopReportComputesRatesFromDeltas(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	if err := os.MkdirAll(processStateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(processRunDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	cmdline, ok := processCommandLine(os.Getpid())
	if !ok || len(cmdline) == 0 {
		t.Fatal("expected current process cmdline")
	}
	var rx atomic.Int64
	var tx atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		snap := statspkg.Snapshot{
			CollectedAt:   time.Now().UTC(),
			Role:          "connect",
			Kind:          "http",
			Service:       "lms",
			ServiceID:     "12D3KooWServicePeer",
			Path:          "direct",
			Status:        "running",
			RxBytesTotal:  rx.Load(),
			TxBytesTotal:  tx.Load(),
			RequestsTotal: rx.Load() / 10,
		}
		_ = json.NewEncoder(w).Encode(snap)
	}))
	defer server.Close()
	state := detachedProcessState{
		ID:           "process/connect-lms-1234",
		Kind:         "process",
		ResourceKind: "pipe",
		Command:      "connect",
		Name:         "connect-lms-1234",
		PID:          os.Getpid(),
		PIDFile:      filepath.Join(processRunDir(), "connect-lms-1234.pid"),
		StateFile:    filepath.Join(processStateDir(), "connect-lms-1234.json"),
		Source:       "foreground",
		CommandLine:  cmdline,
		Service:      "lms",
		ServiceKind:  "http",
		Path:         "direct",
		StatusURL:    server.URL,
		StatsURL:     server.URL,
	}
	_ = os.WriteFile(state.PIDFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
	b, _ := json.Marshal(state)
	_ = os.WriteFile(state.StateFile, b, 0o600)
	prev := map[string]topSample{}
	report1, err := collectTopReport(context.Background(), false, prev)
	if err != nil {
		t.Fatal(err)
	}
	if len(report1.Items) != 1 || !report1.Items[0].StatsAvailable {
		t.Fatalf("unexpected first report: %#v", report1)
	}
	rx.Add(2048)
	tx.Add(1024)
	time.Sleep(20 * time.Millisecond)
	report2, err := collectTopReport(context.Background(), false, prev)
	if err != nil {
		t.Fatal(err)
	}
	if report2.Items[0].RxBytesPerSec <= 0 || report2.Items[0].TxBytesPerSec <= 0 {
		t.Fatalf("expected positive rates, got %#v", report2.Items[0])
	}
}

func TestTopCmdJSONIncludesStats(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	if err := os.MkdirAll(processStateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(processRunDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	cmdline, ok := processCommandLine(os.Getpid())
	if !ok || len(cmdline) == 0 {
		t.Fatal("expected current process cmdline")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(statspkg.Snapshot{CollectedAt: time.Now().UTC(), Role: "service", Kind: "http", Service: "piwebui", ServiceID: "12D3KooWServicePeer", Path: "relayed", Status: "running", RxBytesTotal: 512, TxBytesTotal: 256, Active: 2, RequestsTotal: 3})
	}))
	defer server.Close()
	state := detachedProcessState{ID: "process/attach-piwebui", Kind: "process", ResourceKind: "service", Command: "attach", Name: "attach-piwebui", PID: os.Getpid(), PIDFile: filepath.Join(processRunDir(), "attach-piwebui.pid"), StateFile: filepath.Join(processStateDir(), "attach-piwebui.json"), Source: "foreground", CommandLine: cmdline, Service: "piwebui", ServiceKind: "http", Path: "relayed", StatusURL: server.URL, StatsURL: server.URL}
	_ = os.WriteFile(state.PIDFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
	b, _ := json.Marshal(state)
	_ = os.WriteFile(state.StateFile, b, 0o600)
	out, err := capture(func() error { return topCmd([]string{"--json", "--interval", "0s"}) })
	if err != nil {
		t.Fatal(err)
	}
	var report topReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode top json: %v\n%s", err, out)
	}
	if len(report.Items) != 1 || !report.Items[0].StatsAvailable {
		t.Fatalf("unexpected top json: %#v", report)
	}
	if report.Items[0].Stats.RxBytesTotal != 512 || report.Items[0].Stats.Active != 2 {
		t.Fatalf("unexpected stats payload: %#v", report.Items[0])
	}
}
