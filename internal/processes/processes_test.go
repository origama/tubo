package processes

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cfgpkg "github.com/origama/tubo/internal/config"
)

type stubSystem struct {
	running   map[int]bool
	terminate func(int) error
	kill      func(int) error
}

func (s *stubSystem) PIDRunning(pid int) bool {
	return s.running[pid]
}

func (s *stubSystem) TerminatePID(pid int) error {
	if s.terminate != nil {
		return s.terminate(pid)
	}
	s.running[pid] = false
	return nil
}

func (s *stubSystem) KillPID(pid int) error {
	if s.kill != nil {
		return s.kill(pid)
	}
	s.running[pid] = false
	return nil
}

func TestBuildSpec(t *testing.T) {
	root := t.TempDir()
	spec, err := BuildSpec("attach", cfgpkg.Config{Service: cfgpkg.Service{Name: "lmstudio", Target: "http://127.0.0.1:1234"}, HealthListen: "127.0.0.1:8091"}, []string{"http://127.0.0.1:1234", "--name", "lmstudio"}, root)
	if err != nil {
		t.Fatal(err)
	}
	if spec.State.ID != "process/attach-lmstudio" {
		t.Fatalf("id = %q", spec.State.ID)
	}
	if !strings.Contains(spec.State.LogFile, filepath.Join("logs", "attach-lmstudio.log")) {
		t.Fatalf("unexpected log path: %q", spec.State.LogFile)
	}
	if spec.HealthURL != "http://127.0.0.1:8091/healthz" {
		t.Fatalf("health url = %q", spec.HealthURL)
	}
}

func TestListViewsAndLoadState(t *testing.T) {
	root := t.TempDir()
	system := &stubSystem{running: map[int]bool{1234: true}}
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	running := State{ID: "process/attach-myapi", Kind: "process", Command: "attach", Name: "attach-myapi", PID: 1234, PIDFile: filepath.Join(RunDir(root), "attach-myapi.pid"), StateFile: filepath.Join(StateDir(root), "attach-myapi.json"), LogFile: filepath.Join(LogDir(root), "attach-myapi.log")}
	stale := State{ID: "process/relay-default", Kind: "process", Command: "relay", Name: "relay-default", PID: 999999, PIDFile: filepath.Join(RunDir(root), "relay-default.pid"), StateFile: filepath.Join(StateDir(root), "relay-default.json"), LogFile: filepath.Join(LogDir(root), "relay-default.log")}
	_ = os.WriteFile(running.PIDFile, []byte(fmt.Sprintf("%d\n", running.PID)), 0o600)
	for _, st := range []State{running, stale} {
		b, _ := json.Marshal(st)
		if err := os.WriteFile(st.StateFile, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	items, err := ListViews(root, false, system)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != running.ID {
		t.Fatalf("unexpected running items: %#v", items)
	}
	items, err = ListViews(root, true, system)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items with --all, got %#v", items)
	}
	if _, status, err := LoadState(root, "attach-myapi", system); err != nil || status != "running" {
		t.Fatalf("LoadState running err=%v status=%q", err, status)
	}
}

func TestReadLogTailAndRemoveStale(t *testing.T) {
	root := t.TempDir()
	system := &stubSystem{running: map[int]bool{}}
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(LogDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	state := State{ID: "process/attach-myapi", Kind: "process", Command: "attach", Name: "attach-myapi", PID: 999999, PIDFile: filepath.Join(RunDir(root), "attach-myapi.pid"), StateFile: filepath.Join(StateDir(root), "attach-myapi.json"), LogFile: filepath.Join(LogDir(root), "attach-myapi.log")}
	_ = os.WriteFile(state.LogFile, []byte("line1\nline2\nline3\n"), 0o600)
	b, _ := json.Marshal(state)
	_ = os.WriteFile(state.StateFile, b, 0o600)
	lines, err := ReadLogTail(state.LogFile, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "line2" || lines[1] != "line3" {
		t.Fatalf("unexpected log tail: %#v", lines)
	}
	removed, err := RemoveStale(root, system)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d", removed)
	}
	if _, err := os.Stat(state.StateFile); !os.IsNotExist(err) {
		t.Fatalf("expected state file removed, stat err=%v", err)
	}
}

func TestStop(t *testing.T) {
	root := t.TempDir()
	system := &stubSystem{running: map[int]bool{4321: true}}
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	state := State{ID: "process/relay-default", Kind: "process", Command: "relay", Name: "relay-default", PID: 4321, PIDFile: filepath.Join(RunDir(root), "relay-default.pid"), StateFile: filepath.Join(StateDir(root), "relay-default.json"), LogFile: filepath.Join(LogDir(root), "relay-default.log")}
	_ = os.WriteFile(state.PIDFile, []byte(fmt.Sprintf("%d\n", state.PID)), 0o600)
	b, _ := json.Marshal(state)
	_ = os.WriteFile(state.StateFile, b, 0o600)
	stopped, err := Stop(root, state.ID, system, false)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.ID != state.ID {
		t.Fatalf("stopped.ID = %q", stopped.ID)
	}
	if system.PIDRunning(state.PID) {
		t.Fatal("expected process to stop")
	}
}
