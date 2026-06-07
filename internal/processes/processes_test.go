package processes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cfgpkg "github.com/origama/tubo/internal/config"
)

type stubSystem struct {
	running   map[int]bool
	cmdlines  map[int][]string
	terminate func(int) error
	kill      func(int) error
}

func (s *stubSystem) PIDRunning(pid int) bool {
	return s.running[pid]
}

func (s *stubSystem) CommandLine(pid int) ([]string, bool) {
	if s.cmdlines == nil {
		return nil, false
	}
	cmdline, ok := s.cmdlines[pid]
	if !ok {
		return nil, false
	}
	return append([]string(nil), cmdline...), true
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

func TestRegisterCurrentProcess(t *testing.T) {
	root := t.TempDir()
	state := State{
		ID:          "process/relay-default",
		Kind:        "process",
		Command:     "relay",
		Name:        "relay-default",
		LogFile:     filepath.Join(root, "logs", "relay-default.log"),
		StateFile:   filepath.Join(StateDir(root), "relay-default.json"),
		PIDFile:     filepath.Join(RunDir(root), "relay-default.pid"),
		CommandLine: []string{"/bin/tubo", "relay"},
	}
	system := &stubSystem{running: map[int]bool{os.Getpid(): true}, cmdlines: map[int][]string{os.Getpid(): []string{"/bin/tubo", "relay"}}}
	registered, cleanup, err := RegisterCurrentProcess(root, state, system)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup == nil {
		t.Fatal("expected cleanup")
	}
	defer func() { _ = cleanup() }()
	if registered.PID != os.Getpid() {
		t.Fatalf("registered pid = %d", registered.PID)
	}
	if registered.Source == "" {
		t.Fatal("expected source")
	}
	if _, err := os.Stat(registered.StateFile); err != nil {
		t.Fatalf("state file missing: %v", err)
	}
}

func TestStatusDetailsUsesCommandLineValidation(t *testing.T) {
	root := t.TempDir()
	state := State{
		ID:          "process/relay-default",
		Kind:        "process",
		Command:     "relay",
		Name:        "relay-default",
		PID:         os.Getpid(),
		PIDFile:     filepath.Join(RunDir(root), "relay-default.pid"),
		StateFile:   filepath.Join(StateDir(root), "relay-default.json"),
		LogFile:     filepath.Join(LogDir(root), "relay-default.log"),
		CommandLine: []string{"/bin/tubo", "relay"},
	}
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(state.PIDFile, []byte(fmt.Sprintf("%d\n", state.PID)), 0o600)
	_ = os.WriteFile(state.StateFile, mustJSON(t, state), 0o600)
	okSystem := &stubSystem{running: map[int]bool{state.PID: true}, cmdlines: map[int][]string{state.PID: []string{"/bin/tubo", "relay"}}}
	status, confidence := StatusDetails(state, okSystem)
	if status != "running" || confidence != "pid+cmdline" {
		t.Fatalf("expected running with cmdline confidence, got %s/%s", status, confidence)
	}
	badSystem := &stubSystem{running: map[int]bool{state.PID: true}, cmdlines: map[int][]string{state.PID: []string{"/bin/tubo", "attach"}}}
	status, confidence = StatusDetails(state, badSystem)
	if status != "stale" || confidence != "cmdline-mismatch" {
		t.Fatalf("expected stale/cmdline-mismatch, got %s/%s", status, confidence)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestListViewsAndLoadState(t *testing.T) {
	root := t.TempDir()
	system := &stubSystem{running: map[int]bool{1234: true}, cmdlines: map[int][]string{1234: []string{"/bin/tubo", "relay"}}}
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

func TestStatusDetailsUsesHealthEndpointForDegradedProcess(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("degraded"))
	}))
	defer server.Close()
	state := State{
		ID:          "process/connect-lms-1234",
		Kind:        "process",
		Command:     "connect",
		Name:        "connect-lms-1234",
		PID:         os.Getpid(),
		PIDFile:     filepath.Join(RunDir(root), "connect-lms-1234.pid"),
		StateFile:   filepath.Join(StateDir(root), "connect-lms-1234.json"),
		LogFile:     filepath.Join(LogDir(root), "connect-lms-1234.log"),
		CommandLine: []string{"/bin/tubo", "connect", "lms", "--local", "127.0.0.1:1234"},
		StatusURL:   server.URL,
	}
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(state.PIDFile, []byte(fmt.Sprintf("%d\n", state.PID)), 0o600)
	system := &stubSystem{running: map[int]bool{state.PID: true}, cmdlines: map[int][]string{state.PID: state.CommandLine}}
	status, confidence := StatusDetails(state, system)
	if status != "degraded" || confidence != "pid+cmdline+healthz-degraded" {
		t.Fatalf("expected degraded/pid+cmdline+healthz-degraded, got %s/%s", status, confidence)
	}
}

func TestReadLogTailAndRemoveStale(t *testing.T) {
	root := t.TempDir()
	system := &stubSystem{running: map[int]bool{}, cmdlines: map[int][]string{}}
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
	if err := os.WriteFile(state.StateFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
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
	removed, err = RemoveStale(root, system)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("second stale removal = %d, want 0", removed)
	}
}

func TestRemoveStaleCollapsesLegacyConnectAliases(t *testing.T) {
	root := t.TempDir()
	system := &stubSystem{running: map[int]bool{}, cmdlines: map[int][]string{}}
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(LogDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	current := State{ID: "process/connect-lms-1234", Kind: "process", Command: "connect", Service: "lms", Name: "connect-lms-1234", PID: 999999, PIDFile: filepath.Join(RunDir(root), "connect-lms-1234.pid"), StateFile: filepath.Join(StateDir(root), "connect-lms-1234.json"), LogFile: filepath.Join(LogDir(root), "connect-lms-1234.log"), Local: "127.0.0.1:1234"}
	legacy := State{ID: "process/connect-lms-tcp-127-0-0-1-1234", Kind: "process", Command: "connect", Service: "lms", Name: "connect-lms-tcp-127-0-0-1-1234", PID: 999998, PIDFile: filepath.Join(RunDir(root), "connect-lms-tcp-127-0-0-1-1234.pid"), StateFile: filepath.Join(StateDir(root), "connect-lms-tcp-127-0-0-1-1234.json"), LogFile: filepath.Join(LogDir(root), "connect-lms-tcp-127-0-0-1-1234.log"), Local: "tcp://127.0.0.1:1234"}
	for _, st := range []State{current, legacy} {
		b, _ := json.Marshal(st)
		if err := os.WriteFile(st.StateFile, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := RemoveStale(root, system)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d", removed)
	}
	if _, err := os.Stat(current.StateFile); !os.IsNotExist(err) {
		t.Fatalf("expected current state removed, stat err=%v", err)
	}
	if _, err := os.Stat(legacy.StateFile); !os.IsNotExist(err) {
		t.Fatalf("expected legacy state removed, stat err=%v", err)
	}
	removed, err = RemoveStale(root, system)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("second stale removal = %d, want 0", removed)
	}
}

func TestRemoveStaleDoesNotRemoveDegradedProcess(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	system := &stubSystem{running: map[int]bool{4321: true}, cmdlines: map[int][]string{4321: {"/bin/tubo", "relay"}}}
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(LogDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	state := State{ID: "process/relay-default", Kind: "process", Command: "relay", Name: "relay-default", PID: 4321, PIDFile: filepath.Join(RunDir(root), "relay-default.pid"), StateFile: filepath.Join(StateDir(root), "relay-default.json"), LogFile: filepath.Join(LogDir(root), "relay-default.log"), StatusURL: server.URL}
	_ = os.WriteFile(state.PIDFile, []byte(fmt.Sprintf("%d\n", state.PID)), 0o600)
	_ = os.WriteFile(state.LogFile, []byte("degraded\n"), 0o600)
	b, _ := json.Marshal(state)
	_ = os.WriteFile(state.StateFile, b, 0o600)
	removed, err := RemoveStale(root, system)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d", removed)
	}
	for _, path := range []string{state.StateFile, state.PIDFile, state.LogFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to remain, stat err=%v", path, err)
		}
	}
}

func TestRemoveStaleKeepsDegradedConnectAliases(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	system := &stubSystem{running: map[int]bool{9876: true}, cmdlines: map[int][]string{9876: {"/bin/tubo", "connect", "lms", "--local", "127.0.0.1:1234"}}}
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(LogDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	current := State{ID: "process/connect-lms-1234", Kind: "process", Command: "connect", Service: "lms", Name: "connect-lms-1234", PID: 9876, PIDFile: filepath.Join(RunDir(root), "connect-lms-1234.pid"), StateFile: filepath.Join(StateDir(root), "connect-lms-1234.json"), LogFile: filepath.Join(LogDir(root), "connect-lms-1234.log"), Local: "127.0.0.1:1234", StatusURL: server.URL}
	legacy := State{ID: "process/connect-lms-tcp-127-0-0-1-1234", Kind: "process", Command: "connect", Service: "lms", Name: "connect-lms-tcp-127-0-0-1-1234", PID: 9876, PIDFile: filepath.Join(RunDir(root), "connect-lms-tcp-127-0-0-1-1234.pid"), StateFile: filepath.Join(StateDir(root), "connect-lms-tcp-127-0-0-1-1234.json"), LogFile: filepath.Join(LogDir(root), "connect-lms-tcp-127-0-0-1-1234.log"), Local: "tcp://127.0.0.1:1234", StatusURL: server.URL}
	for _, st := range []State{current, legacy} {
		_ = os.WriteFile(st.PIDFile, []byte(fmt.Sprintf("%d\n", st.PID)), 0o600)
		_ = os.WriteFile(st.LogFile, []byte("degraded\n"), 0o600)
		b, _ := json.Marshal(st)
		if err := os.WriteFile(st.StateFile, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := RemoveStale(root, system)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d", removed)
	}
	for _, path := range []string{current.StateFile, legacy.StateFile, current.PIDFile, legacy.PIDFile, current.LogFile, legacy.LogFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to remain, stat err=%v", path, err)
		}
	}
}

func TestStopAllowsDegradedProcess(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	system := &stubSystem{running: map[int]bool{4321: true}, cmdlines: map[int][]string{4321: {"/bin/tubo", "relay"}}}
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	state := State{ID: "process/relay-default", Kind: "process", Command: "relay", Name: "relay-default", PID: 4321, PIDFile: filepath.Join(RunDir(root), "relay-default.pid"), StateFile: filepath.Join(StateDir(root), "relay-default.json"), LogFile: filepath.Join(LogDir(root), "relay-default.log"), StatusURL: server.URL}
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
		t.Fatal("expected degraded process to stop")
	}
}

func TestStop(t *testing.T) {
	root := t.TempDir()
	system := &stubSystem{running: map[int]bool{4321: true}, cmdlines: map[int][]string{4321: {"/bin/tubo", "relay"}}}
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
