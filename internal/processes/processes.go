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
	"sync"
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
	PrimaryRef              string   `json:"primary_ref,omitempty"`
	PrimaryKind             string   `json:"primary_kind,omitempty"`
	PrimaryName             string   `json:"primary_name,omitempty"`
	PrimaryID               string   `json:"primary_id,omitempty"`
	Purpose                 string   `json:"purpose,omitempty"`
	Capabilities            []string `json:"capabilities,omitempty"`
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
	AdvertisementStatus     string   `json:"advertisement_status,omitempty"`
	AdvertisementReason     string   `json:"advertisement_reason,omitempty"`
	AuthorizationStatus     string   `json:"authorization_status,omitempty"`
	AuthorizationReason     string   `json:"authorization_reason,omitempty"`
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
	AdvertisementStatus     string   `json:"advertisement_status,omitempty"`
	AdvertisementReason     string   `json:"advertisement_reason,omitempty"`
	AuthorizationStatus     string   `json:"authorization_status,omitempty"`
	AuthorizationReason     string   `json:"authorization_reason,omitempty"`
	PID                     int      `json:"pid"`
	ResourceKind            string   `json:"resource_kind,omitempty"`
	Service                 string   `json:"service,omitempty"`
	ServiceKind             string   `json:"service_kind,omitempty"`
	ServiceID               string   `json:"service_id,omitempty"`
	PrimaryRef              string   `json:"primary_ref,omitempty"`
	PrimaryKind             string   `json:"primary_kind,omitempty"`
	PrimaryName             string   `json:"primary_name,omitempty"`
	PrimaryID               string   `json:"primary_id,omitempty"`
	Purpose                 string   `json:"purpose,omitempty"`
	Capabilities            []string `json:"capabilities,omitempty"`
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
			PrimaryKind:  primaryKindForCommand(commandName),
			PrimaryName:  primaryNameForCommand(commandName, serviceName),
			PrimaryRef:   primaryRefForCommand(commandName, serviceName),
			Purpose:      purposeForCommand(commandName),
			Capabilities: CapabilitiesForCommand(commandName),
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

func StartDetached(spec DetachedSpec, executable string, env []string, system System, configure CommandConfigurer, timeout time.Duration) (State, error) {
	if err := os.MkdirAll(filepath.Dir(spec.State.StateFile), 0o700); err != nil {
		return State{}, err
	}
	if err := os.MkdirAll(filepath.Dir(spec.State.LogFile), 0o700); err != nil {
		return State{}, err
	}
	if err := os.MkdirAll(filepath.Dir(spec.State.PIDFile), 0o700); err != nil {
		return State{}, err
	}
	if existing, ok, err := readDetachedStateForStart(spec.State.ID, spec.State.StateFile, spec.State.PIDFile); err != nil {
		return State{}, err
	} else if ok {
		if conflict := detachedStateConflict(existing, spec.State); conflict != "" {
			return State{}, fmt.Errorf("detached process state conflict for %s: %s", spec.State.ID, conflict)
		}
		pidToCheck := existing.PID
		if pidToCheck <= 0 {
			if pidFromFile, pidErr := readPIDFile(spec.State.ID, spec.State.PIDFile); pidErr != nil {
				return State{}, pidErr
			} else if pidFromFile > 0 {
				pidToCheck = pidFromFile
			}
		}
		if system != nil && pidToCheck > 0 && system.PIDRunning(pidToCheck) {
			if len(existing.CommandLine) > 0 {
				if actual, ok := system.CommandLine(pidToCheck); ok && !commandLinesMatch(existing.CommandLine, actual) {
					return State{}, fmt.Errorf("detached process state conflict for %s: running process command line mismatch", spec.State.ID)
				}
			}
			return State{}, fmt.Errorf("detached process already running for %s (pid %d)", spec.State.ID, pidToCheck)
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

func readDetachedStateForStart(id, statePath, pidPath string) (State, bool, error) {
	if state, err := readStateIfExists(statePath); err == nil {
		return state, true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return State{}, false, err
	}
	if _, err := os.Stat(pidPath); err == nil {
		return State{}, false, fmt.Errorf("detached process state already exists for %s (orphan pid file found without state; run 'tubo rm --stale' or 'tubo stop' to clear it)", id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return State{}, false, err
	}
	return State{}, false, nil
}

func readPIDFile(id, path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("detached process state already exists for %s (invalid pid file; run 'tubo rm --stale' or 'tubo stop' to clear it)", id)
	}
	return pid, nil
}

func detachedStateConflict(existing, desired State) string {
	checks := []struct {
		label string
		have  string
		want  string
	}{
		{label: "command", have: existing.Command, want: desired.Command},
		{label: "name", have: existing.Name, want: desired.Name},
		{label: "resource kind", have: existing.ResourceKind, want: desired.ResourceKind},
		{label: "cluster", have: existing.Cluster, want: desired.Cluster},
		{label: "namespace", have: existing.Namespace, want: desired.Namespace},
		{label: "service", have: existing.Service, want: desired.Service},
		{label: "service kind", have: existing.ServiceKind, want: desired.ServiceKind},
		{label: "service id", have: existing.ServiceID, want: desired.ServiceID},
		{label: "local address", have: normalizeConnectLocal(existing.Local), want: normalizeConnectLocal(desired.Local)},
		{label: "target", have: strings.TrimSpace(existing.Target), want: strings.TrimSpace(desired.Target)},
	}
	for _, check := range checks {
		if conflict := exactStringFieldConflict(check.label, check.have, check.want); conflict != "" {
			return conflict
		}
	}
	return ""
}

func exactStringFieldConflict(label, have, want string) string {
	have = strings.TrimSpace(have)
	want = strings.TrimSpace(want)
	if want == "" {
		return ""
	}
	if have == want {
		return ""
	}
	return fmt.Sprintf("%s mismatch: have %q want %q", label, have, want)
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
			state = normalizeStateCapabilities(state)
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

	// A non-positive tail count means "no line limit" within the bounded scan
	// window below; it does not mean "read the whole file".
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
	if err := StopState(state, system, force); err != nil {
		return State{}, err
	}
	return state, nil
}

func StopState(state State, system System, force bool) error {
	pid, ok, err := runningPIDForState(state, system)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("process %s is not running", state.ID)
	}
	if err := system.TerminatePID(pid); err != nil {
		return err
	}
	if err := waitForExit(system, pid, 5*time.Second); err != nil {
		if !force {
			return err
		}
		if err := system.KillPID(pid); err != nil {
			return err
		}
		if err := waitForExit(system, pid, 2*time.Second); err != nil {
			return err
		}
	}
	_ = os.Remove(state.PIDFile)
	return nil
}

func runningPIDForState(state State, system System) (int, bool, error) {
	if system == nil {
		return 0, false, errors.New("process system unavailable")
	}
	if state.PID > 0 && system.PIDRunning(state.PID) {
		return state.PID, true, nil
	}
	if strings.TrimSpace(state.PIDFile) == "" {
		return 0, false, nil
	}
	raw, err := os.ReadFile(state.PIDFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, false, nil
	}
	if system.PIDRunning(pid) {
		return pid, true, nil
	}
	return 0, false, nil
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
	stateWriteMu.Lock()
	defer stateWriteMu.Unlock()
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	state, err := decodeState(b)
	if err != nil {
		return err
	}
	state = normalizeStateCapabilities(state)
	mutate(&state)
	return writeStateAtomic(path, state)
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

var stateWriteMu sync.Mutex

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
		state, err := readStateIfExists(path)
		if err != nil || !stateHasIdentity(state) {
			state = fallbackStateFromPath(dataRoot, path)
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

func decodeState(b []byte) (State, error) {
	var state State
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&state); err != nil {
		return State{}, err
	}
	return state, nil
}

func writeStateAtomic(path string, state State) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	stateBytes, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	stateBytes = append(stateBytes, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(stateBytes); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func stateHasIdentity(state State) bool {
	return strings.TrimSpace(state.ID) != "" && strings.TrimSpace(state.Name) != ""
}

func fallbackStateFromPath(dataRoot, path string) State {
	base := strings.TrimSuffix(filepath.Base(path), ".json")
	state := State{
		ID:        normalizeRef(base),
		Kind:      "process",
		Name:      base,
		Command:   commandFromProcessName(base),
		StateFile: path,
		PIDFile:   filepath.Join(RunDir(dataRoot), base+".pid"),
		LogFile:   filepath.Join(LogDir(dataRoot), base+".log"),
	}
	if pid, err := readPIDFile(state.ID, state.PIDFile); err == nil {
		state.PID = pid
	}
	if state.Command != "" {
		switch state.Command {
		case "attach":
			state.ResourceKind = "service"
		case "connect":
			state.ResourceKind = "pipe"
		default:
			state.ResourceKind = "process"
		}
		state.Purpose = purposeForCommand(state.Command)
		state.Capabilities = CapabilitiesForCommand(state.Command)
	}
	return state
}

func commandFromProcessName(name string) string {
	switch {
	case strings.HasPrefix(name, "connect-"):
		return "connect"
	case strings.HasPrefix(name, "attach-"):
		return "attach"
	case strings.HasPrefix(name, "grants-serve-"):
		return "grants serve"
	case strings.HasPrefix(name, "relay-"):
		return "relay"
	case strings.HasPrefix(name, "gateway-"):
		return "gateway"
	default:
		return ""
	}
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
	stateWriteMu.Lock()
	defer stateWriteMu.Unlock()
	if state.PIDFile != "" {
		if err := os.WriteFile(state.PIDFile, []byte(fmt.Sprintf("%d\n", state.PID)), 0o600); err != nil {
			return err
		}
	}
	return writeStateAtomic(state.StateFile, state)
}

func readStateIfExists(path string) (State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	state, err := decodeState(b)
	if err != nil {
		return State{}, err
	}
	return normalizeStateCapabilities(state), nil
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
	state = normalizeStateCapabilities(state)
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
		PrimaryRef:              state.PrimaryRef,
		PrimaryKind:             state.PrimaryKind,
		PrimaryName:             state.PrimaryName,
		PrimaryID:               state.PrimaryID,
		Purpose:                 state.Purpose,
		Capabilities:            append([]string(nil), state.Capabilities...),
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
		AdvertisementStatus:     state.AdvertisementStatus,
		AdvertisementReason:     state.AdvertisementReason,
		AuthorizationStatus:     state.AuthorizationStatus,
		AuthorizationReason:     state.AuthorizationReason,
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

func primaryKindForCommand(command string) string {
	switch command {
	case "attach":
		return "service"
	case "connect":
		return "service"
	default:
		return ""
	}
}

func primaryNameForCommand(command, serviceName string) string {
	switch command {
	case "attach", "connect":
		return serviceName
	default:
		return ""
	}
}

func primaryRefForCommand(command, serviceName string) string {
	kind := primaryKindForCommand(command)
	name := primaryNameForCommand(command, serviceName)
	if kind == "" || name == "" {
		return ""
	}
	return kind + "/" + name
}

func purposeForCommand(command string) string {
	switch command {
	case "attach":
		return "service-runtime"
	case "connect":
		return "pipe-runtime"
	case "gateway":
		return "gateway"
	case "relay":
		return "relay"
	default:
		return ""
	}
}

func normalizeStateCapabilities(state State) State {
	if len(state.Capabilities) > 0 {
		return state
	}
	caps := CapabilitiesForCommand(state.Command)
	if len(caps) == 0 {
		return state
	}
	state.Capabilities = append([]string(nil), caps...)
	return state
}

func CapabilitiesForCommand(command string) []string {
	switch command {
	case "attach":
		return []string{"publish", "discovery.cache", "discovery.query", "discovery.sync"}
	case "connect":
		return []string{"proxy", "client"}
	case "gateway":
		return []string{"gateway", "proxy"}
	case "relay":
		return []string{"relay", "discovery.cache", "discovery.query"}
	case "grants serve":
		return []string{"grant"}
	default:
		return nil
	}
}
