package processes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	cfgpkg "github.com/origama/tubo/internal/config"
)

type State struct {
	ID                      string   `json:"id"`
	Kind                    string   `json:"kind"`
	ResourceKind            string   `json:"resource_kind,omitempty"`
	Command                 string   `json:"command"`
	Name                    string   `json:"name"`
	Service                 string   `json:"service,omitempty"`
	ServiceKind             string   `json:"service_kind,omitempty"`
	ServiceID               string   `json:"service_id,omitempty"`
	PeerID                  string   `json:"peer_id,omitempty"`
	Cluster                 string   `json:"cluster,omitempty"`
	Namespace               string   `json:"namespace,omitempty"`
	ConnectPolicy           string   `json:"connect_policy,omitempty"`
	GrantEndpointEnabled    bool     `json:"grant_endpoint_enabled,omitempty"`
	GrantProtocol           string   `json:"grant_protocol,omitempty"`
	Local                   string   `json:"local,omitempty"`
	Target                  string   `json:"target,omitempty"`
	Path                    string   `json:"path,omitempty"`
	SelectedAddr            string   `json:"selected_addr,omitempty"`
	SelectedPath            string   `json:"selected_path,omitempty"`
	PID                     int      `json:"pid"`
	StartedAt               string   `json:"started_at"`
	LogFile                 string   `json:"log_file"`
	StateFile               string   `json:"state_file"`
	PIDFile                 string   `json:"pid_file"`
	StatusURL               string   `json:"status_url,omitempty"`
	StatsURL                string   `json:"stats_url,omitempty"`
	Source                  string   `json:"source,omitempty"`
	CommandLine             []string `json:"command_line,omitempty"`
	StatusConfidence        string   `json:"status_confidence,omitempty"`
	RuntimeStatus           string   `json:"runtime_status,omitempty"`
	DegradedReason          string   `json:"degraded_reason,omitempty"`
	ConnectAccessExpiresAt  string   `json:"connect_access_expires_at,omitempty"`
	ConnectRefreshExpiresAt string   `json:"connect_refresh_expires_at,omitempty"`
	LastTunnelError         string   `json:"last_tunnel_error,omitempty"`
	LastTunnelErrorAt       string   `json:"last_tunnel_error_at,omitempty"`
	LastTunnelHealthyAt     string   `json:"last_tunnel_healthy_at,omitempty"`
	PeerLivenessState       string   `json:"peer_liveness_state,omitempty"`
	PeerLivenessReason      string   `json:"peer_liveness_reason,omitempty"`
	LastPingRTT             string   `json:"last_ping_rtt,omitempty"`
	LastPingAt              string   `json:"last_ping_at,omitempty"`
	LastPingError           string   `json:"last_ping_error,omitempty"`
	LastPingErrorAt         string   `json:"last_ping_error_at,omitempty"`
	ConsecutivePingFailures int      `json:"consecutive_ping_failures,omitempty"`
	NetworkState            string   `json:"network_state,omitempty"`
	NetworkReason           string   `json:"network_reason,omitempty"`
	NetworkSince            string   `json:"network_since,omitempty"`
	LastNetworkError        string   `json:"last_network_error,omitempty"`
	LastNetworkErrorAt      string   `json:"last_network_error_at,omitempty"`
	LastNetworkRecoveredAt  string   `json:"last_network_recovered_at,omitempty"`
	LastRefreshError        string   `json:"last_refresh_error,omitempty"`
	NextRefreshRetryAt      string   `json:"next_refresh_retry_at,omitempty"`
}

type DetachedSpec struct {
	State     State
	ChildArgs []string
	HealthURL string
}

type View struct {
	ID                      string   `json:"id"`
	Name                    string   `json:"name"`
	Command                 string   `json:"command"`
	Status                  string   `json:"status"`
	StatusConfidence        string   `json:"status_confidence,omitempty"`
	PID                     int      `json:"pid"`
	ResourceKind            string   `json:"resource_kind,omitempty"`
	Service                 string   `json:"service,omitempty"`
	ServiceKind             string   `json:"service_kind,omitempty"`
	ServiceID               string   `json:"service_id,omitempty"`
	PeerID                  string   `json:"peer_id,omitempty"`
	Cluster                 string   `json:"cluster,omitempty"`
	Namespace               string   `json:"namespace,omitempty"`
	Local                   string   `json:"local,omitempty"`
	Target                  string   `json:"target,omitempty"`
	Path                    string   `json:"path,omitempty"`
	SelectedAddr            string   `json:"selected_addr,omitempty"`
	SelectedPath            string   `json:"selected_path,omitempty"`
	LogFile                 string   `json:"log_file"`
	StateFile               string   `json:"state_file"`
	PIDFile                 string   `json:"pid_file"`
	StatusURL               string   `json:"status_url,omitempty"`
	StatsURL                string   `json:"stats_url,omitempty"`
	StartedAt               string   `json:"started_at,omitempty"`
	Source                  string   `json:"source,omitempty"`
	CommandLine             []string `json:"command_line,omitempty"`
	DegradedReason          string   `json:"degraded_reason,omitempty"`
	ConnectAccessExpiresAt  string   `json:"connect_access_expires_at,omitempty"`
	ConnectRefreshExpiresAt string   `json:"connect_refresh_expires_at,omitempty"`
	LastTunnelError         string   `json:"last_tunnel_error,omitempty"`
	LastTunnelErrorAt       string   `json:"last_tunnel_error_at,omitempty"`
	LastTunnelHealthyAt     string   `json:"last_tunnel_healthy_at,omitempty"`
	PeerLivenessState       string   `json:"peer_liveness_state,omitempty"`
	PeerLivenessReason      string   `json:"peer_liveness_reason,omitempty"`
	LastPingRTT             string   `json:"last_ping_rtt,omitempty"`
	LastPingAt              string   `json:"last_ping_at,omitempty"`
	LastPingError           string   `json:"last_ping_error,omitempty"`
	LastPingErrorAt         string   `json:"last_ping_error_at,omitempty"`
	ConsecutivePingFailures int      `json:"consecutive_ping_failures,omitempty"`
	NetworkState            string   `json:"network_state,omitempty"`
	NetworkReason           string   `json:"network_reason,omitempty"`
	NetworkSince            string   `json:"network_since,omitempty"`
	LastNetworkError        string   `json:"last_network_error,omitempty"`
	LastNetworkErrorAt      string   `json:"last_network_error_at,omitempty"`
	LastNetworkRecoveredAt  string   `json:"last_network_recovered_at,omitempty"`
}

type System interface {
	PIDRunning(pid int) bool
	TerminatePID(pid int) error
	KillPID(pid int) error
	CommandLine(pid int) ([]string, bool)
}

type CommandConfigurer func(*exec.Cmd)

func StateDir(dataRoot string) string { return filepath.Join(dataRoot, "processes") }
func LogDir(dataRoot string) string   { return filepath.Join(dataRoot, "logs") }
func RunDir(dataRoot string) string   { return filepath.Join(dataRoot, "run") }

func BuildSpec(commandName string, cfg cfgpkg.Config, args []string, dataRoot string) (DetachedSpec, error) {
	var name, local, target, serviceName, statusAddr string
	switch commandName {
	case "attach":
		serviceName = cfg.Service.Name
		name = "attach-" + sanitizeName(serviceName)
		target = cfg.Service.Target
		statusAddr = cfg.HealthListen
	case "gateway":
		name = "gateway-default"
		local = cfg.Edge.Listen
		target = "swarm"
		statusAddr = cfg.Edge.AdminListen
	case "relay":
		name = "relay-default"
		local = cfg.Node.P2PListen
		statusAddr = cfg.Relay.HealthListen
	default:
		return DetachedSpec{}, fmt.Errorf("detach is not supported for %s", commandName)
	}
	if name == "" {
		return DetachedSpec{}, fmt.Errorf("unable to derive detached process name for %s", commandName)
	}
	statePath := filepath.Join(StateDir(dataRoot), name+".json")
	logPath := filepath.Join(LogDir(dataRoot), name+".log")
	pidPath := filepath.Join(RunDir(dataRoot), name+".pid")
	statusURL := ""
	statsURL := ""
	if statusAddr != "" {
		statusURL = "http://" + hostPortForHTTP(statusAddr) + "/healthz"
		statsURL = "http://" + hostPortForHTTP(statusAddr) + "/statsz"
	}
	return DetachedSpec{
		State: State{
			ID:           "process/" + name,
			Kind:         "process",
			ResourceKind: resourceKindForCommand(commandName),
			Command:      commandName,
			Name:         name,
			Service:      serviceName,
			Cluster:      cfg.CurrentCluster,
			Namespace:    cfg.CurrentNamespace,
			Local:        local,
			Target:       target,
			LogFile:      logPath,
			StateFile:    statePath,
			PIDFile:      pidPath,
			StatusURL:    statusURL,
			StatsURL:     statsURL,
		},
		ChildArgs: append([]string{commandName}, args...),
		HealthURL: statusURL,
	}, nil
}

func StartDetached(spec DetachedSpec, executable string, env []string, configure CommandConfigurer, timeout time.Duration) (State, error) {
	if err := os.MkdirAll(filepath.Dir(spec.State.StateFile), 0o700); err != nil {
		return State{}, err
	}
	if err := os.MkdirAll(filepath.Dir(spec.State.LogFile), 0o700); err != nil {
		return State{}, err
	}
	if err := os.MkdirAll(filepath.Dir(spec.State.PIDFile), 0o700); err != nil {
		return State{}, err
	}
	for _, path := range []string{spec.State.StateFile, spec.State.PIDFile} {
		if _, err := os.Stat(path); err == nil {
			hint := ""
			if path == spec.State.PIDFile {
				// PID file exists but no state file: likely an orphan from a
				// previous run whose state was lost. Run 'tubo rm --stale' or
				// 'tubo stop' to clear it.
				hint = " (orphan pid file found without state; run 'tubo rm --stale' or 'tubo stop' to clear it)"
			}
			return State{}, fmt.Errorf("detached process state already exists for %s%s", spec.State.ID, hint)
		} else if !errors.Is(err, os.ErrNotExist) {
			return State{}, err
		}
	}
	logFile, err := os.OpenFile(spec.State.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return State{}, err
	}
	defer logFile.Close()
	cmd := exec.Command(executable, spec.ChildArgs...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.Env = env
	if configure != nil {
		configure(cmd)
	}
	if err := cmd.Start(); err != nil {
		return State{}, err
	}
	state := spec.State
	state.Source = firstNonEmpty(state.Source, "tubo-detached")
	state.CommandLine = append([]string{executable}, spec.ChildArgs...)
	state.StatusConfidence = confidenceLabel(state, true)
	state.PID = cmd.Process.Pid
	state.StartedAt = time.Now().UTC().Format(time.RFC3339)
	if err := os.WriteFile(state.PIDFile, []byte(fmt.Sprintf("%d\n", state.PID)), 0o600); err != nil {
		return State{}, err
	}
	stateBytes, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return State{}, err
	}
	if err := os.WriteFile(state.StateFile, stateBytes, 0o600); err != nil {
		return State{}, err
	}
	if err := waitForStart(cmd, spec.HealthURL, state.LogFile, timeout); err != nil {
		_ = os.Remove(state.PIDFile)
		_ = os.Remove(state.StateFile)
		return State{}, err
	}
	return state, nil
}

func RegisterCurrentProcess(dataRoot string, state State, system System) (State, func() error, error) {
	if state.ID == "" || state.Name == "" || state.Command == "" {
		return State{}, nil, fmt.Errorf("incomplete runtime process state")
	}
	state.PID = os.Getpid()
	if state.StartedAt == "" {
		state.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	state.Source = firstNonEmpty(state.Source, runtimeSourceFromEnv())
	state.StatusConfidence = confidenceLabel(state, system != nil && len(state.CommandLine) > 0)
	if err := ensureProcessDirs(state); err != nil {
		return State{}, nil, err
	}
	if existing, err := readStateIfExists(state.StateFile); err == nil && existing.PID != 0 && existing.PID != state.PID {
		if Status(existing, system) == "running" {
			return State{}, nil, fmt.Errorf("process state already exists for %s", state.ID)
		}
	}
	if err := writeProcessRegistration(state); err != nil {
		return State{}, nil, err
	}
	cleanup := func() error {
		if state.PIDFile == "" {
			return nil
		}
		if err := os.Remove(state.PIDFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return state, cleanup, nil
}

const processHealthProbeTimeout = 200 * time.Millisecond

func ListViews(dataRoot string, includeAll bool, system System) ([]View, error) {
	states, err := listStates(dataRoot)
	if err != nil {
		return nil, err
	}
	items := make([]View, 0, len(states))
	for _, state := range states {
		status, confidence := StatusDetails(state, system)
		if !includeAll && status == "stale" {
			continue
		}
		items = append(items, viewFromState(state, status, confidence))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

func LoadState(dataRoot, ref string, system System) (State, string, error) {
	ref = normalizeRef(ref)
	states, err := listStates(dataRoot)
	if err != nil {
		return State{}, "", err
	}
	for _, state := range states {
		if state.ID == ref {
			status, confidence := StatusDetails(state, system)
			state.StatusConfidence = confidence
			return state, status, nil
		}
	}
	return State{}, "", fmt.Errorf("unknown process %q", strings.TrimPrefix(ref, "process/"))
}

func StatusDetails(state State, system System) (string, string) {
	if state.PID <= 0 {
		return "stale", "none"
	}
	if _, err := os.Stat(state.PIDFile); err != nil {
		return "stale", "pid-file-missing"
	}
	baseStatus := "running"
	baseConfidence := "pid"
	if system != nil && !system.PIDRunning(state.PID) {
		return "stale", "pid-not-running"
	}
	if system != nil && len(state.CommandLine) > 0 {
		if actual, ok := system.CommandLine(state.PID); ok {
			if !commandLinesMatch(state.CommandLine, actual) {
				return "stale", "cmdline-mismatch"
			}
			baseConfidence = "pid+cmdline"
		} else {
			baseConfidence = "pid"
		}
	} else if system == nil && len(state.CommandLine) > 0 {
		baseConfidence = "cmdline-unverified"
	}
	if state.RuntimeStatus == "degraded" {
		return "degraded", baseConfidence + "+runtime"
	}
	if status, confidence, ok := healthStatus(state.StatusURL, baseConfidence); ok {
		return status, confidence
	}
	return baseStatus, baseConfidence
}

func healthStatus(url, baseConfidence string) (string, string, bool) {
	if strings.TrimSpace(url) == "" {
		return "", "", false
	}
	client := &http.Client{Timeout: processHealthProbeTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return "running", baseConfidence + "+healthz", true
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		return "degraded", baseConfidence + "+healthz-degraded", true
	}
	return "", "", false
}

func Status(state State, system System) string {
	status, _ := StatusDetails(state, system)
	return status
}

func ReadLogTail(path string, lines int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size == 0 {
		return nil, nil
	}

	const chunkSize = 32 * 1024
	const maxScanBytes = 8 * 1024 * 1024

	wantAll := lines <= 0
	remaining := lines
	tailCap := 64
	if lines > 0 && lines < tailCap {
		tailCap = lines
	}
	collectedRev := make([]string, 0, tailCap)
	var carry []byte
	var scanned int64
	offset := size
	for offset > 0 && scanned < maxScanBytes {
		if !wantAll && remaining <= 0 {
			break
		}
		readSize := chunkSize
		if int64(readSize) > offset {
			readSize = int(offset)
		}
		budgetLeft := maxScanBytes - scanned
		if budgetLeft <= 0 {
			break
		}
		if int64(readSize) > budgetLeft {
			readSize = int(budgetLeft)
		}
		if readSize <= 0 {
			break
		}
		offset -= int64(readSize)
		buf := make([]byte, readSize)
		n, err := f.ReadAt(buf, offset)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if n <= 0 {
			break
		}
		shortRead := n < readSize
		buf = buf[:n]
		scanned += int64(n)
		segment := make([]byte, 0, len(buf)+len(carry))
		segment = append(segment, buf...)
		segment = append(segment, carry...)
		carry = carry[:0]
		for {
			idx := bytes.LastIndexByte(segment, '\n')
			if idx < 0 {
				carry = append(carry[:0], segment...)
				break
			}
			if line := segment[idx+1:]; len(line) > 0 {
				collectedRev = append(collectedRev, string(line))
				if !wantAll {
					remaining--
					if remaining <= 0 {
						break
					}
				}
			}
			segment = segment[:idx]
		}
		if shortRead {
			break
		}
	}
	if len(carry) > 0 && (offset == 0 || len(collectedRev) == 0) {
		collectedRev = append(collectedRev, string(carry))
	}
	for i, j := 0, len(collectedRev)-1; i < j; i, j = i+1, j-1 {
		collectedRev[i], collectedRev[j] = collectedRev[j], collectedRev[i]
	}
	return collectedRev, nil
}

func FollowLog(ctx context.Context, path string, out io.Writer) error {
	var offset int64
	if info, err := os.Stat(path); err == nil {
		offset = info.Size()
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			f, err := os.Open(path)
			if err != nil {
				continue
			}
			_, _ = f.Seek(offset, io.SeekStart)
			buf, err := io.ReadAll(f)
			_ = f.Close()
			if err != nil {
				continue
			}
			if len(buf) > 0 {
				if _, err := out.Write(buf); err != nil {
					return err
				}
				offset += int64(len(buf))
			}
		}
	}
}

func Stop(dataRoot, ref string, system System, force bool) (State, error) {
	state, status, err := LoadState(dataRoot, ref, system)
	if err != nil {
		return State{}, err
	}
	if !isLiveStatus(status) {
		return State{}, fmt.Errorf("process %s is not running", state.ID)
	}
	if err := system.TerminatePID(state.PID); err != nil {
		return State{}, err
	}
	if err := waitForExit(system, state.PID, 5*time.Second); err != nil {
		if !force {
			return State{}, err
		}
		if err := system.KillPID(state.PID); err != nil {
			return State{}, err
		}
		if err := waitForExit(system, state.PID, 2*time.Second); err != nil {
			return State{}, err
		}
	}
	_ = os.Remove(state.PIDFile)
	return state, nil
}

func RemoveStale(dataRoot string, system System) (int, error) {
	entries, err := listStateEntries(dataRoot)
	if err != nil {
		return 0, err
	}
	removed := 0
	seenConnectAliases := make(map[string]bool)
	for _, entry := range entries {
		if isLiveStatus(Status(entry.state, system)) {
			continue
		}
		if strings.TrimSpace(entry.state.Command) == "connect" {
			key := connectAliasKey(entry.state)
			if seenConnectAliases[key] {
				continue
			}
			seenConnectAliases[key] = true
			for _, grouped := range entries {
				if isLiveStatus(Status(grouped.state, system)) || strings.TrimSpace(grouped.state.Command) != "connect" {
					continue
				}
				if connectAliasKey(grouped.state) != key {
					continue
				}
				removeConnectArtifacts(dataRoot, grouped.state)
			}
			removed++
			continue
		}
		if err := removeStateArtifacts(entry.path, entry.state); err == nil {
			removed++
		}
	}
	// Remove orphan .pid files that have no corresponding .json state file.
	orphanCount, err := removeOrphanPIDs(dataRoot, system)
	if err != nil {
		return removed, err
	}
	removed += orphanCount
	return removed, nil
}

// removeOrphanPIDs removes .pid files in RunDir that have no corresponding
// .json in StateDir and whose process is no longer running.
func removeOrphanPIDs(dataRoot string, system System) (int, error) {
	runDir := RunDir(dataRoot)
	pidEntries, err := os.ReadDir(runDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	stateDir := StateDir(dataRoot)
	removed := 0
	for _, e := range pidEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pid") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".pid")
		jsonPath := filepath.Join(stateDir, base+".json")
		if _, err := os.Stat(jsonPath); err == nil {
			// .json exists — handled by the main loop above.
			continue
		}
		// Orphan .pid: read the PID and check if alive.
		pidPath := filepath.Join(runDir, e.Name())
		raw, readErr := os.ReadFile(pidPath)
		if readErr != nil {
			continue
		}
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
		if parseErr != nil || pid <= 0 {
			// Unreadable PID file — remove it.
			if rerr := os.Remove(pidPath); rerr == nil {
				removed++
			}
			continue
		}
		if system != nil && system.PIDRunning(pid) {
			// Process is still alive; leave it alone.
			continue
		}
		if rerr := os.Remove(pidPath); rerr == nil {
			removed++
		}
	}
	return removed, nil
}

func isLiveStatus(status string) bool {
	return status == "running" || status == "degraded"
}

func UpdateState(path string, mutate func(*State)) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var state State
	if err := json.Unmarshal(b, &state); err != nil {
		return err
	}
	mutate(&state)
	updated, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, updated, 0o600)
}

func SummaryLogTail(path string, max int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > max {
		b = b[len(b)-max:]
	}
	return string(b)
}

func waitForStart(cmd *exec.Cmd, healthURL, logPath string, timeout time.Duration) error {
	errCh := make(chan error, 1)
	go func() { errCh <- cmd.Wait() }()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for {
		select {
		case err := <-errCh:
			if err == nil {
				return fmt.Errorf("detached process exited before becoming ready")
			}
			return fmt.Errorf("detached process exited early: %w\n%s", err, SummaryLogTail(logPath, 4096))
		default:
		}
		if healthURL != "" {
			if resp, err := client.Get(healthURL); err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		} else if time.Now().After(deadline.Add(-timeout + 500*time.Millisecond)) {
			return nil
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func waitForExit(system System, pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !system.PIDRunning(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("process %d did not exit in time", pid)
}

type stateEntry struct {
	path  string
	state State
}

func listStateEntries(dataRoot string) ([]stateEntry, error) {
	entries, err := os.ReadDir(StateDir(dataRoot))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	states := make([]stateEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(StateDir(dataRoot), entry.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var state State
		if err := json.Unmarshal(b, &state); err != nil {
			state = State{}
		}
		states = append(states, stateEntry{path: path, state: state})
	}
	return states, nil
}

func listStates(dataRoot string) ([]State, error) {
	entries, err := listStateEntries(dataRoot)
	if err != nil {
		return nil, err
	}
	states := make([]State, 0, len(entries))
	for _, entry := range entries {
		states = append(states, entry.state)
	}
	return states, nil
}

func normalizeRef(ref string) string {
	if strings.HasPrefix(ref, "process/") {
		return ref
	}
	return "process/" + ref
}

func ensureProcessDirs(state State) error {
	for _, path := range []string{state.StateFile, state.PIDFile, state.LogFile} {
		if path == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
	}
	return nil
}

func writeProcessRegistration(state State) error {
	if state.PIDFile != "" {
		if err := os.WriteFile(state.PIDFile, []byte(fmt.Sprintf("%d\n", state.PID)), 0o600); err != nil {
			return err
		}
	}
	stateBytes, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(state.StateFile, stateBytes, 0o600)
}

func readStateIfExists(path string) (State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(b, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func runtimeSourceFromEnv() string {
	for _, key := range []string{"INVOCATION_ID", "JOURNAL_STREAM", "NOTIFY_SOCKET"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return "systemd"
		}
	}
	return "foreground"
}

func commandLinesMatch(expected, actual []string) bool {
	if len(expected) != len(actual) {
		return false
	}
	for i := range expected {
		if expected[i] != actual[i] {
			return false
		}
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func confidenceLabel(state State, cmdlineAvailable bool) string {
	switch {
	case len(state.CommandLine) > 0 && cmdlineAvailable:
		return "pid+cmdline"
	case len(state.CommandLine) > 0:
		return "cmdline-unverified"
	default:
		return "pid"
	}
}

func viewFromState(state State, status, confidence string) View {
	return View{
		ID:                      state.ID,
		Name:                    state.Name,
		Command:                 state.Command,
		Status:                  status,
		StatusConfidence:        confidence,
		PID:                     state.PID,
		ResourceKind:            state.ResourceKind,
		Service:                 state.Service,
		ServiceKind:             state.ServiceKind,
		ServiceID:               state.ServiceID,
		PeerID:                  state.PeerID,
		Cluster:                 state.Cluster,
		Namespace:               state.Namespace,
		Local:                   state.Local,
		Target:                  state.Target,
		Path:                    state.Path,
		SelectedAddr:            state.SelectedAddr,
		SelectedPath:            state.SelectedPath,
		LogFile:                 state.LogFile,
		StateFile:               state.StateFile,
		PIDFile:                 state.PIDFile,
		StatusURL:               state.StatusURL,
		StatsURL:                state.StatsURL,
		StartedAt:               state.StartedAt,
		Source:                  state.Source,
		CommandLine:             append([]string(nil), state.CommandLine...),
		DegradedReason:          state.DegradedReason,
		ConnectAccessExpiresAt:  state.ConnectAccessExpiresAt,
		ConnectRefreshExpiresAt: state.ConnectRefreshExpiresAt,
		LastTunnelError:         state.LastTunnelError,
		LastTunnelErrorAt:       state.LastTunnelErrorAt,
		LastTunnelHealthyAt:     state.LastTunnelHealthyAt,
		PeerLivenessState:       state.PeerLivenessState,
		PeerLivenessReason:      state.PeerLivenessReason,
		LastPingRTT:             state.LastPingRTT,
		LastPingAt:              state.LastPingAt,
		LastPingError:           state.LastPingError,
		LastPingErrorAt:         state.LastPingErrorAt,
		ConsecutivePingFailures: state.ConsecutivePingFailures,
		NetworkState:            state.NetworkState,
		NetworkReason:           state.NetworkReason,
		NetworkSince:            state.NetworkSince,
		LastNetworkError:        state.LastNetworkError,
		LastNetworkErrorAt:      state.LastNetworkErrorAt,
		LastNetworkRecoveredAt:  state.LastNetworkRecoveredAt,
	}
}

func removeStateArtifacts(path string, state State) error {
	candidates := []string{path}
	for _, candidate := range []string{state.StateFile, state.PIDFile, state.LogFile} {
		if candidate == "" {
			continue
		}
		candidates = append(candidates, candidate)
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func removeConnectArtifacts(dataRoot string, state State) {
	for _, name := range connectArtifactNames(state) {
		for _, path := range []string{filepath.Join(StateDir(dataRoot), name+".json"), filepath.Join(RunDir(dataRoot), name+".pid"), filepath.Join(LogDir(dataRoot), name+".log")} {
			_ = os.Remove(path)
		}
	}
	for _, path := range []string{state.StateFile, state.PIDFile, state.LogFile} {
		if path == "" {
			continue
		}
		_ = os.Remove(path)
	}
}

func connectAliasKey(state State) string {
	service := strings.TrimSpace(state.Service)
	if service == "" {
		service = strings.TrimSpace(state.Name)
	}
	return service + "|" + normalizeConnectLocal(state.Local)
}

func connectArtifactNames(state State) []string {
	service := strings.TrimSpace(state.Service)
	if service == "" {
		service = strings.TrimSpace(state.Name)
	}
	if service == "" {
		service = "connect"
	}
	local := strings.TrimSpace(state.Local)
	canonical := connectProcessName(service, local)
	legacy := legacyConnectProcessName(service, local)
	if canonical == legacy {
		return []string{canonical}
	}
	return []string{canonical, legacy}
}

func connectProcessName(service, local string) string {
	name := "connect-" + sanitizeName(service)
	local = normalizeConnectLocal(local)
	if _, port, err := net.SplitHostPort(local); err == nil && strings.TrimSpace(port) != "" {
		return name + "-" + sanitizeName(port)
	}
	local = sanitizeName(local)
	if local == "" {
		return name
	}
	return name + "-" + local
}

func legacyConnectProcessName(service, local string) string {
	name := "connect-" + sanitizeName(service)
	local = sanitizeName(local)
	if local == "" {
		return name
	}
	return name + "-" + local
}

func normalizeConnectLocal(local string) string {
	local = strings.TrimSpace(local)
	for _, prefix := range []string{"tcp://", "http://", "https://"} {
		if strings.HasPrefix(local, prefix) {
			return strings.TrimPrefix(local, prefix)
		}
	}
	return local
}

func sanitizeName(s string) string {
	if s == "" {
		return "default"
	}
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return '-'
		default:
			return '-'
		}
	}, s)
	mapped = strings.Trim(mapped, "-")
	for strings.Contains(mapped, "--") {
		mapped = strings.ReplaceAll(mapped, "--", "-")
	}
	if mapped == "" {
		return "default"
	}
	return mapped
}

func hostPortForHTTP(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}

func resourceKindForCommand(command string) string {
	switch command {
	case "attach":
		return "service"
	case "connect":
		return "pipe"
	default:
		return "process"
	}
}
