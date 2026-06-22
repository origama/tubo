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
	"time"

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
	if spec.State.PrimaryRef != "service/lmstudio" || spec.State.PrimaryKind != "service" || spec.State.PrimaryName != "lmstudio" || spec.State.Purpose != "service-runtime" {
		t.Fatalf("unexpected primary metadata: %#v", spec.State)
	}
	if got := strings.Join(spec.State.Capabilities, ","); got != "publish,discovery.cache,discovery.query,discovery.sync" {
		t.Fatalf("capabilities = %q", got)
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
	if items[0].Name != running.Name || items[1].Name != stale.Name {
		t.Fatalf("expected deterministic sort by name, got %#v", items)
	}
	if _, status, err := LoadState(root, "attach-myapi", system); err != nil || status != "running" {
		t.Fatalf("LoadState running err=%v status=%q", err, status)
	}
}

func TestListViewsAndLoadStateIgnoreTrailingGarbage(t *testing.T) {
	root := t.TempDir()
	system := &stubSystem{running: map[int]bool{26656: true}, cmdlines: map[int][]string{26656: []string{"/Users/gvirzi/local_repos/origama/tubo/tubo", "connect", "piwebui", "--local", "127.0.0.1:38080"}}}
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	state := State{ID: "process/connect-piwebui-38080", Kind: "process", Command: "connect", ResourceKind: "pipe", Name: "connect-piwebui-38080", PID: 26656, PIDFile: filepath.Join(RunDir(root), "connect-piwebui-38080.pid"), StateFile: filepath.Join(StateDir(root), "connect-piwebui-38080.json"), LogFile: filepath.Join(LogDir(root), "connect-piwebui-38080.log")}
	if err := os.WriteFile(state.PIDFile, []byte(fmt.Sprintf("%d\n", state.PID)), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, []byte("\nconnect runtime status update failed: invalid character 't' after top-level value\n")...)
	if err := os.WriteFile(state.StateFile, b, 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := ListViews(root, true, system)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %#v", items)
	}
	if items[0].Name != state.Name || items[0].ID != state.ID {
		t.Fatalf("unexpected recovered view: %#v", items[0])
	}
	if status := items[0].Status; status != "running" {
		t.Fatalf("unexpected recovered status: %q", status)
	}
	loaded, status, err := LoadState(root, "connect-piwebui-38080", system)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != state.Name || status != "running" {
		t.Fatalf("unexpected loaded state/status: %#v %q", loaded, status)
	}
	if err := UpdateState(state.StateFile, func(st *State) {
		st.RuntimeStatus = "degraded"
		st.DegradedReason = "test repair"
	}); err != nil {
		t.Fatal(err)
	}
	repaired, err := readStateIfExists(state.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.RuntimeStatus != "degraded" || repaired.DegradedReason != "test repair" {
		t.Fatalf("unexpected repaired state: %#v", repaired)
	}
}

func TestLoadStateBackfillsCapabilitiesFromCommand(t *testing.T) {
	root := t.TempDir()
	system := &stubSystem{running: map[int]bool{1234: true}}
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	state := State{ID: "process/connect-lms-1234", Kind: "process", Command: "connect", Name: "connect-lms-1234", PID: 1234, PIDFile: filepath.Join(RunDir(root), "connect-lms-1234.pid"), StateFile: filepath.Join(StateDir(root), "connect-lms-1234.json"), LogFile: filepath.Join(LogDir(root), "connect-lms-1234.log")}
	if err := os.WriteFile(state.PIDFile, []byte(fmt.Sprintf("%d\n", state.PID)), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.StateFile, b, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, status, err := LoadState(root, "connect-lms-1234", system)
	if err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Fatalf("unexpected status: %q", status)
	}
	if got := strings.Join(loaded.Capabilities, ","); got != "proxy,client" {
		t.Fatalf("unexpected loaded capabilities: %q", got)
	}
	items, err := ListViews(root, true, system)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %#v", items)
	}
	if got := strings.Join(items[0].Capabilities, ","); got != "proxy,client" {
		t.Fatalf("unexpected listed capabilities: %q", got)
	}
}

func TestListViewsFallsBackWhenSnapshotLacksIdentity(t *testing.T) {
	root := t.TempDir()
	system := &stubSystem{running: map[int]bool{26656: true}, cmdlines: map[int][]string{26656: []string{"/Users/gvirzi/local_repos/origama/tubo/tubo", "connect", "piwebui", "--local", "127.0.0.1:38080"}}}
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(StateDir(root), "connect-piwebui-38080.json")
	pidPath := filepath.Join(RunDir(root), "connect-piwebui-38080.pid")
	if err := os.WriteFile(pidPath, []byte("26656\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("{\n  \"unexpected\": \"value\"\n}\ntrailing garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := ListViews(root, true, system)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %#v", items)
	}
	if items[0].Name != "connect-piwebui-38080" || items[0].ID != "process/connect-piwebui-38080" {
		t.Fatalf("expected filename fallback, got %#v", items[0])
	}
	if items[0].Status != "running" {
		t.Fatalf("expected running status from fallback, got %#v", items[0])
	}
	if _, status, err := LoadState(root, "connect-piwebui-38080", system); err != nil || status != "running" {
		t.Fatalf("LoadState fallback err=%v status=%q", err, status)
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

func TestStartDetachedReusesCompatibleStaleAttachState(t *testing.T) {
	root := t.TempDir()
	spec := DetachedSpec{State: State{ID: "process/attach-myapi", Kind: "process", ResourceKind: "service", Command: "attach", Name: "attach-myapi", Service: "myapi", ServiceKind: "http", Cluster: "home", Namespace: "default", Target: "http://127.0.0.1:1234", LogFile: filepath.Join(LogDir(root), "attach-myapi.log"), StateFile: filepath.Join(StateDir(root), "attach-myapi.json"), PIDFile: filepath.Join(RunDir(root), "attach-myapi.pid")}, ChildArgs: []string{"-c", "sleep 2"}}
	stale := spec.State
	stale.PID = 999999
	stale.CommandLine = []string{"/bin/sh", "-c", "sleep 2"}
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale.PIDFile, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale.StateFile, mustJSON(t, stale), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := StartDetached(spec, "/bin/sh", []string{"PATH=/usr/bin:/bin"}, &stubSystem{running: map[int]bool{}}, nil, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if state.PID == 0 {
		t.Fatal("expected new pid")
	}
	if p, err := os.FindProcess(state.PID); err == nil {
		_ = p.Kill()
	}
}

func TestStartDetachedReusesCompatibleStaleConnectState(t *testing.T) {
	root := t.TempDir()
	spec := DetachedSpec{State: State{ID: "process/connect-myapi-1234", Kind: "process", ResourceKind: "pipe", Command: "connect", Name: "connect-myapi-1234", Service: "myapi", ServiceKind: "tcp", ServiceID: "service-123", Cluster: "home", Namespace: "default", Local: "127.0.0.1:1234", Target: "myapi", LogFile: filepath.Join(LogDir(root), "connect-myapi-1234.log"), StateFile: filepath.Join(StateDir(root), "connect-myapi-1234.json"), PIDFile: filepath.Join(RunDir(root), "connect-myapi-1234.pid")}, ChildArgs: []string{"-c", "sleep 2"}}
	stale := spec.State
	stale.PID = 999998
	stale.CommandLine = []string{"/bin/sh", "-c", "sleep 2"}
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale.PIDFile, []byte("999998\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale.StateFile, mustJSON(t, stale), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := StartDetached(spec, "/bin/sh", []string{"PATH=/usr/bin:/bin"}, &stubSystem{running: map[int]bool{}}, nil, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if state.PID == 0 {
		t.Fatal("expected new pid")
	}
	if p, err := os.FindProcess(state.PID); err == nil {
		_ = p.Kill()
	}
}

func TestStartDetachedRejectsLiveCompatibleState(t *testing.T) {
	root := t.TempDir()
	spec := DetachedSpec{State: State{ID: "process/connect-myapi-1234", Kind: "process", ResourceKind: "pipe", Command: "connect", Name: "connect-myapi-1234", Service: "myapi", ServiceKind: "tcp", ServiceID: "service-123", Cluster: "home", Namespace: "default", Local: "127.0.0.1:1234", Target: "myapi", LogFile: filepath.Join(LogDir(root), "connect-myapi-1234.log"), StateFile: filepath.Join(StateDir(root), "connect-myapi-1234.json"), PIDFile: filepath.Join(RunDir(root), "connect-myapi-1234.pid")}, ChildArgs: []string{"-c", "sleep 2"}}
	live := spec.State
	live.PID = 4321
	live.CommandLine = []string{"/bin/sh", "-c", "sleep 2"}
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live.PIDFile, []byte("4321\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live.StateFile, mustJSON(t, live), 0o600); err != nil {
		t.Fatal(err)
	}
	system := &stubSystem{running: map[int]bool{4321: true}, cmdlines: map[int][]string{4321: {"/bin/sh", "-c", "sleep 2"}}}
	_, err := StartDetached(spec, "/bin/sh", []string{"PATH=/usr/bin:/bin"}, system, nil, 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected already-running error, got %v", err)
	}
}

func TestStartDetachedRejectsStaleIncompatibleState(t *testing.T) {
	root := t.TempDir()
	spec := DetachedSpec{State: State{ID: "process/attach-myapi", Kind: "process", ResourceKind: "service", Command: "attach", Name: "attach-myapi", Service: "myapi", ServiceKind: "http", Cluster: "home", Namespace: "default", Target: "http://127.0.0.1:1234", LogFile: filepath.Join(LogDir(root), "attach-myapi.log"), StateFile: filepath.Join(StateDir(root), "attach-myapi.json"), PIDFile: filepath.Join(RunDir(root), "attach-myapi.pid")}, ChildArgs: []string{"-c", "sleep 2"}}
	stale := spec.State
	stale.Target = "http://127.0.0.1:9999"
	stale.PID = 777777
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale.PIDFile, []byte("777777\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale.StateFile, mustJSON(t, stale), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := StartDetached(spec, "/bin/sh", []string{"PATH=/usr/bin:/bin"}, &stubSystem{running: map[int]bool{}}, nil, 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "conflict") || !strings.Contains(err.Error(), "target mismatch") {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

func TestStartDetachedRejectsZeroPidStateWhenPidFileIsLive(t *testing.T) {
	root := t.TempDir()
	spec := DetachedSpec{State: State{ID: "process/connect-myapi-1234", Kind: "process", ResourceKind: "pipe", Command: "connect", Name: "connect-myapi-1234", Service: "myapi", ServiceKind: "tcp", ServiceID: "service-123", Cluster: "home", Namespace: "default", Local: "127.0.0.1:1234", Target: "myapi", LogFile: filepath.Join(LogDir(root), "connect-myapi-1234.log"), StateFile: filepath.Join(StateDir(root), "connect-myapi-1234.json"), PIDFile: filepath.Join(RunDir(root), "connect-myapi-1234.pid")}, ChildArgs: []string{"-c", "sleep 2"}}
	stale := spec.State
	stale.PID = 0
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale.PIDFile, []byte("4321\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale.StateFile, mustJSON(t, stale), 0o600); err != nil {
		t.Fatal(err)
	}
	system := &stubSystem{running: map[int]bool{4321: true}, cmdlines: map[int][]string{4321: {"/bin/sh", "-c", "sleep 2"}}}
	_, err := StartDetached(spec, "/bin/sh", []string{"PATH=/usr/bin:/bin"}, system, nil, 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected already-running error, got %v", err)
	}
}

func TestReadLogTailReturnsLastNLines(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tail.log")
	if err := os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err := ReadLogTail(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "line2" || lines[1] != "line3" {
		t.Fatalf("unexpected log tail: %#v", lines)
	}
}

func TestReadLogTailHandlesEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err := ReadLogTail(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 0 {
		t.Fatalf("expected empty tail, got %#v", lines)
	}
}

func TestReadLogTailHandlesNoTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-trailing-newline.log")
	if err := os.WriteFile(path, []byte("line1\nline2\nlast line"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err := ReadLogTail(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "line2" || lines[1] != "last line" {
		t.Fatalf("unexpected log tail: %#v", lines)
	}
}

func TestReadLogTailHandlesTailLargerThanAvailableLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short.log")
	if err := os.WriteFile(path, []byte("line1\nline2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err := ReadLogTail(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "line1" || lines[1] != "line2" {
		t.Fatalf("unexpected log tail: %#v", lines)
	}
}

func TestReadLogTailHandlesNonPositiveTailCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "all.log")
	if err := os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err := ReadLogTail(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 || lines[0] != "line1" || lines[1] != "line2" || lines[2] != "line3" {
		t.Fatalf("unexpected all-lines tail: %#v", lines)
	}
}

func TestReadLogTailReadsLargeSparseFileSafely(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sparse.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("start\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if _, err := f.Seek(64*1024*1024, 0); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if _, err := f.WriteString("tail-1\ntail-2\ntail-3\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	lines, err := ReadLogTail(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "tail-2" || lines[1] != "tail-3" {
		t.Fatalf("unexpected sparse tail: %#v", lines)
	}
}

func TestReadLogTailMissingFile(t *testing.T) {
	_, err := ReadLogTail(filepath.Join(t.TempDir(), "missing.log"), 2)
	if err == nil {
		t.Fatal("expected error for missing file")
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

func TestRemoveStaleOrphanPIDFile(t *testing.T) {
	root := t.TempDir()
	system := &stubSystem{running: map[int]bool{}, cmdlines: map[int][]string{}}
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}

	// Write an orphan .pid file with no corresponding .json state file.
	orphanPID := filepath.Join(RunDir(root), "attach-lms.pid")
	if err := os.WriteFile(orphanPID, []byte("99999\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Process is not running → RemoveStale should remove the orphan.
	removed, err := RemoveStale(root, system)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 orphan removed, got %d", removed)
	}
	if _, err := os.Stat(orphanPID); !os.IsNotExist(err) {
		t.Fatalf("expected orphan pid file removed, stat err=%v", err)
	}
}

func TestRemoveStaleKeepsOrphanPIDFileForLiveProcess(t *testing.T) {
	root := t.TempDir()
	const livePID = 88888
	system := &stubSystem{running: map[int]bool{livePID: true}, cmdlines: map[int][]string{}}
	if err := os.MkdirAll(StateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(RunDir(root), 0o700); err != nil {
		t.Fatal(err)
	}

	// Orphan .pid for a still-running process.
	orphanPID := filepath.Join(RunDir(root), "attach-lms.pid")
	if err := os.WriteFile(orphanPID, []byte(fmt.Sprintf("%d\n", livePID)), 0o600); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveStale(root, system)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("expected 0 removed for live process, got %d", removed)
	}
	if _, err := os.Stat(orphanPID); err != nil {
		t.Fatalf("expected orphan pid file to remain, stat err=%v", err)
	}
}
