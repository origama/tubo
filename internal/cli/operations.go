package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/libp2p/go-libp2p/core/peer"
	"gopkg.in/yaml.v3"

	"github.com/origama/tubo-rebuilt/internal/domain"
	appRuntime "github.com/origama/tubo-rebuilt/internal/runtime"
	"github.com/origama/tubo-rebuilt/internal/store"
)

// extractGlobalFlags consumes the global flags anywhere in the argument list
// and returns the remaining args plus parsed values.
func extractGlobalFlags(args []string) ([]string, bool, int, string, error) {
	quiet, verbosity, level := false, 0, ""
	clean := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--quiet" || arg == "-q":
			quiet = true
		case len(arg) >= 2 && arg[0] == '-' && strings.Trim(arg[1:], "v") == "":
			verbosity += len(arg) - 1
		case strings.HasPrefix(arg, "--log-level="):
			level = strings.TrimPrefix(arg, "--log-level=")
		case arg == "--log-level":
			if i+1 >= len(args) {
				return nil, false, 0, "", errors.New("invalid_input: --log-level requires value")
			}
			i++
			level = args[i]
		default:
			clean = append(clean, arg)
		}
	}
	if level != "" {
		switch level {
		case "error", "warn", "info", "debug", "trace":
		default:
			return nil, false, 0, "", errors.New("invalid_input: --log-level must be error, warn, info, debug, or trace")
		}
	}
	return clean, quiet, verbosity, level, nil
}

func (a App) scopeUse(ctx context.Context, state *store.Store, f flags) error {
	if _, err := parseScope(f.args[0]); err != nil {
		return err
	}
	if err := state.UseScope(ctx, f.args[0]); err != nil {
		return err
	}
	return a.emit(f.bools["json"], map[string]any{"ref": "scope/" + f.args[0], "status": "current"}, "scope/"+f.args[0]+" current")
}

func scopeSyncProjection(ctx context.Context, state *store.Store, scope string) (map[string]any, error) {
	value := map[string]any{}
	lease, err := state.ScopeSyncLease(ctx, scope)
	if err != nil {
		return nil, err
	}
	if lease.Active {
		value["owner_ref"] = lease.HolderRef
		value["lease_expires_at"] = lease.ExpiresAt
	}
	freshness, err := state.ScopeFreshness(ctx, scope)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	if err == nil {
		if freshness.LastSuccess != nil {
			value["last_success"] = freshness.LastSuccess
		}
		if freshness.NextRetry != nil {
			value["next_retry"] = freshness.NextRetry
		}
		if freshness.ErrorClass != "" {
			value["error_class"] = freshness.ErrorClass
		}
	}
	return value, nil
}

func (a App) getServices(ctx context.Context, state *store.Store, f flags) error {
	values, err := state.Services(ctx)
	if err != nil {
		return err
	}
	rows := make([]map[string]any, 0, len(values))
	for _, value := range values {
		rows = append(rows, map[string]any{"ref": value.Ref.String(), "name": value.Ref.Name, "kind": value.Kind, "target": value.Target})
	}
	if f.bools["json"] {
		return json.NewEncoder(a.Out).Encode(map[string]any{"items": rows})
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(a.Out, "No services. Define one with: tubo service create <name> --target <url>")
		return err
	}
	return renderServiceTable(a.Out, rows)
}

func (a App) getScopes(ctx context.Context, state *store.Store, f flags) error {
	current, _ := state.CurrentScope(ctx)
	local, err := state.Scopes(ctx)
	if err != nil {
		return err
	}
	memberships, err := state.Memberships(ctx)
	if err != nil {
		return err
	}
	rows := []map[string]any{}
	seen := map[string]int{}
	for _, value := range local {
		sync, err := scopeSyncProjection(ctx, state, value.Name)
		if err != nil {
			return err
		}
		seen[value.Name] = len(rows)
		rows = append(rows, map[string]any{"ref": "scope/" + value.Name, "name": value.Name, "authority": true, "current": value.Name == current, "sync": sync})
	}
	for _, value := range memberships {
		if index, ok := seen[value.Scope]; ok {
			rows[index]["role"] = value.Role
			continue
		}
		sync, err := scopeSyncProjection(ctx, state, value.Scope)
		if err != nil {
			return err
		}
		rows = append(rows, map[string]any{"ref": "scope/" + value.Scope, "name": value.Scope, "authority": false, "role": value.Role, "current": value.Scope == current, "sync": sync})
	}
	if f.bools["json"] {
		return json.NewEncoder(a.Out).Encode(map[string]any{"items": rows})
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(a.Out, "No scopes. Create one with: tubo scope create <cluster/namespace>")
		return err
	}
	return renderScopeTable(a.Out, rows)
}

func (a App) getOverlays(ctx context.Context, state *store.Store, f flags) error {
	value, err := state.Config()
	if err != nil {
		if os.IsNotExist(err) {
			return noOverlayResult(a, f)
		}
		return err
	}
	row := map[string]any{"ref": "overlay/" + value.CurrentContext, "relays": value.Relays, "force_reachability": value.ForceReachability, "current_scope": value.CurrentScope}
	if f.bools["json"] {
		return json.NewEncoder(a.Out).Encode(map[string]any{"items": []any{row}})
	}
	return renderOverlayTable(a.Out, row)
}

// noOverlayResult is the deterministic empty-state for overlay views: no
// config file exists yet. JSON stays a valid empty projection; the human form
// teaches the next action.
func noOverlayResult(a App, f flags) error {
	if f.bools["json"] {
		return json.NewEncoder(a.Out).Encode(map[string]any{"items": []any{}})
	}
	_, err := fmt.Fprintln(a.Out, "No overlay configured. Run: tubo join overlay --relay <multiaddr> --swarm-key <file>")
	return err
}

// inspect is the detailed human (or --json) view of one resource.
func (a App) inspect(ctx context.Context, state *store.Store, f flags) error {
	raw := f.args[0]
	if strings.HasPrefix(raw, "service/") || strings.HasPrefix(raw, "scope/") || strings.HasPrefix(raw, "overlay/") || strings.HasPrefix(raw, "session/") || strings.HasPrefix(raw, "peer/") {
		return a.inspectLocal(ctx, state, raw, f.bools["json"])
	}
	run, runErr := state.Run(ctx, raw)
	if strings.HasPrefix(raw, "relay/") || strings.HasPrefix(raw, "authority/") {
		if runErr != nil {
			return withTry(fmt.Errorf("not_found: %s has no live runtime", raw), "check 'tubo ps' and the resource's config")
		}
		if f.bools["json"] {
			return json.NewEncoder(a.Out).Encode(run)
		}
		return renderInspectRun(a.Out, run)
	}
	identifier := strings.TrimPrefix(raw, "pipe/")
	spec, _, _, localErr := state.Pipe(ctx, raw)
	if localErr != nil {
		spec, _, _, localErr = state.Pipe(ctx, identifier)
	}
	if localErr == nil {
		return a.inspectPipe(ctx, state, f, spec)
	}
	stored, err := state.Record(ctx, identifier)
	if err != nil {
		return err
	}
	record, err := appRuntime.ParseVerifiedRecord(ctx, state, stored)
	if err != nil {
		return err
	}
	path, candidates := advertisedPath(record.Addresses)
	value := inspectRemotePipeView{
		Ref:               "pipe/" + record.PipeID,
		ID:                record.PipeID,
		Name:              record.DisplayName,
		Kind:              "pipe",
		Scope:             record.Scope,
		PeerID:            record.RuntimePeerID,
		Status:            "available",
		LogicalPath:       []string{"pipe/" + record.PipeID},
		AuthorizationPath: []any{map[string]any{"scope": record.Scope.String(), "operation": "connect.access", "pipe_id": record.PipeID}},
		TransportSummary:  map[string]any{"current": path, "source": "advertised", "advertised_path": path, "advertised_candidates": candidates},
		Sessions:          []store.Session{},
		RecordAge:         recordAge(record, a.currentTime()),
		RecordFresh:       recordFresh(record, a.currentTime()),
	}
	if f.bools["json"] {
		return json.NewEncoder(a.Out).Encode(value)
	}
	return renderInspectRemotePipe(a.Out, value)
}

func (a App) inspectPipe(ctx context.Context, state *store.Store, f flags, spec domain.PipeSpec) error {
	run, runErr := state.Run(ctx, spec.Ref.String())
	sessions, _ := state.Sessions(ctx, spec.Ref.String())
	authorizationPath := []any{}
	if spec.Scope != nil {
		authorizationPath = append(authorizationPath, map[string]any{"scope": spec.Scope.String(), "operation": "pipe.publish"})
	} else if record, ok := localAdvertisedRecord(ctx, state, spec); ok {
		authorizationPath = append(authorizationPath, map[string]any{"scope": record.Scope.String(), "operation": "connect.access", "pipe_id": record.PipeID})
	}
	status := localPipeStatus(run, runErr)
	value := inspectPipeView{
		Ref:               spec.Ref.String(),
		ID:                spec.PipeID,
		Kind:              "pipe",
		Ingress:           spec.Ingress,
		Target:            spec.Target.String(),
		Status:            status,
		Scope:             scopeString(spec.Scope),
		Visibility:        spec.Visibility,
		Access:            spec.Access,
		LogicalPath:       []string{spec.Ref.String(), spec.Target.String()},
		AuthorizationPath: authorizationPath,
		TransportSummary:  transportSummary(ctx, state, spec, sessions),
		Sessions:          sessions,
	}
	if record, ok := localAdvertisedRecord(ctx, state, spec); ok {
		value.RecordAge = recordAge(record, a.currentTime())
		value.RecordFresh = recordFresh(record, a.currentTime())
	}
	if f.bools["json"] {
		return json.NewEncoder(a.Out).Encode(value)
	}
	return renderInspectPipe(a.Out, value)
}

func scopeString(scope *domain.Scope) string {
	if scope == nil {
		return ""
	}
	return scope.String()
}

func (a App) inspectLocal(ctx context.Context, state *store.Store, raw string, jsonOutput bool) error {
	switch {
	case strings.HasPrefix(raw, "service/"):
		ref, err := domain.ParseRef(raw)
		if err != nil {
			return err
		}
		value, err := state.Service(ctx, ref)
		if err != nil {
			return err
		}
		if jsonOutput {
			return json.NewEncoder(a.Out).Encode(value)
		}
		return renderInspectService(a.Out, value)
	case strings.HasPrefix(raw, "scope/"):
		name := strings.TrimPrefix(raw, "scope/")
		if _, err := parseScope(name); err != nil {
			return err
		}
		current, _ := state.CurrentScope(ctx)
		value := map[string]any{"ref": raw, "name": name, "current": name == current}
		found := false
		if local, err := state.Scope(ctx, name); err == nil {
			value["authority"] = true
			value["authority_public"] = local.AuthorityPublic
			found = true
		}
		if member, err := state.Membership(ctx, name); err == nil {
			value["role"] = member.Role
			value["authority_public"] = member.AuthorityPublic
			found = true
		}
		if !found {
			return fmt.Errorf("%w: %s", domain.ErrNotFound, raw)
		}
		sync, err := scopeSyncProjection(ctx, state, name)
		if err != nil {
			return err
		}
		value["sync"] = sync
		if jsonOutput {
			return json.NewEncoder(a.Out).Encode(value)
		}
		return renderInspectScope(a.Out, value)
	case strings.HasPrefix(raw, "overlay/"):
		value, err := state.Config()
		if err != nil {
			return err
		}
		if raw != "overlay/"+value.CurrentContext {
			return fmt.Errorf("%w: %s", domain.ErrNotFound, raw)
		}
		view := map[string]any{"ref": raw, "relays": value.Relays, "force_reachability": value.ForceReachability, "current_scope": value.CurrentScope, "swarm_key": "configured"}
		if jsonOutput {
			return json.NewEncoder(a.Out).Encode(view)
		}
		return renderInspectOverlay(a.Out, view)
	case strings.HasPrefix(raw, "session/"):
		value, err := state.Session(ctx, strings.TrimPrefix(raw, "session/"))
		if err != nil {
			return err
		}
		if jsonOutput {
			return json.NewEncoder(a.Out).Encode(value)
		}
		return renderInspectSession(a.Out, value)
	case strings.HasPrefix(raw, "peer/"):
		value, err := state.PeerAlias(ctx, strings.TrimPrefix(raw, "peer/"))
		if err != nil {
			return err
		}
		if jsonOutput {
			return json.NewEncoder(a.Out).Encode(value)
		}
		return renderInspectPeer(a.Out, value)
	}
	return fmt.Errorf("invalid_input: unsupported resource %q", raw)
}

func (a App) peersAlias(ctx context.Context, state *store.Store, f flags) error {
	if _, err := peer.Decode(f.args[0]); err != nil {
		return fmt.Errorf("invalid_input: invalid peer ID: %w", err)
	}
	name, err := f.required("name")
	if err != nil {
		return err
	}
	if len(name) > 63 || len(f.values["note"]) > 512 {
		return errors.New("invalid_input: alias name or note exceeds limit")
	}
	if strings.IndexFunc(name+f.values["note"], unicode.IsControl) >= 0 {
		return errors.New("invalid_input: alias name and note must not contain control characters")
	}
	value := store.PeerAlias{PeerID: f.args[0], Name: name, Note: f.values["note"]}
	if err := state.PutPeerAlias(ctx, value); err != nil {
		return err
	}
	return a.emit(f.bools["json"], map[string]any{"ref": "peer/" + value.PeerID, "name": value.Name, "status": "saved"}, "peer/"+value.PeerID+" saved as "+name)
}

func maskedConfig(value store.ConfigView) map[string]any {
	key := ""
	if value.SwarmKeyFile != "" {
		key = "<configured>"
	}
	return map[string]any{"version": value.Version, "current_context": value.CurrentContext, "current_scope": value.CurrentScope, "relays": value.Relays, "swarm_key_file": key, "force_reachability": value.ForceReachability}
}

func (a App) configValidate(ctx context.Context, state *store.Store, f flags) error {
	value, err := state.ValidateConfig()
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("invalid_input: config: no overlay configured; run 'tubo join overlay --relay <multiaddr> --swarm-key <file>'")
		}
		return fmt.Errorf("invalid_input: config: %w", err)
	}
	return a.emit(f.bools["json"], map[string]any{"status": "valid", "config": maskedConfig(value)}, "config valid")
}

func (a App) configPrint(ctx context.Context, state *store.Store, f flags) error {
	value, err := state.Config()
	if err != nil {
		return err
	}
	masked := maskedConfig(value)
	if f.bools["json"] {
		return json.NewEncoder(a.Out).Encode(masked)
	}
	data, err := yaml.Marshal(masked)
	if err != nil {
		return err
	}
	_, err = a.Out.Write(data)
	return err
}

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func (a App) doctor(ctx context.Context, state *store.Store, f flags) error {
	checks := []doctorCheck{}
	add := func(name string, err error) {
		if err != nil {
			checks = append(checks, doctorCheck{Name: name, Status: "warning", Detail: err.Error()})
		} else {
			checks = append(checks, doctorCheck{Name: name, Status: "ok"})
		}
	}
	_, configErr := state.ValidateConfig()
	if os.IsNotExist(configErr) {
		configErr = errors.New("no overlay configured; run 'tubo join overlay --relay <multiaddr> --swarm-key <file>'")
	}
	add("config", configErr)
	for _, path := range []string{state.Home(), filepath.Join(state.Home(), "state.db"), filepath.Join(state.Home(), "secrets")} {
		info, err := os.Stat(path)
		if err == nil && info.Mode().Perm()&0o077 != 0 {
			err = fmt.Errorf("permissions %o", info.Mode().Perm())
		}
		add("permissions:"+filepath.Base(path), err)
	}
	runs, runErr := state.Runs(ctx)
	add("state:runs", runErr)
	if runErr == nil {
		for _, run := range runs {
			identity, identityErr := store.ProcessIdentity(run.PID)
			if identityErr != nil || identity != run.ProcessIdentity {
				add("run:"+run.PrimaryRef, errors.New("stale process identity"))
				continue
			}
			if run.Status == "degraded" {
				add("run:"+run.PrimaryRef, errors.New("runtime degraded"))
			}
			if stats, statsErr := state.RuntimeStats(ctx, run.PrimaryRef); statsErr == nil {
				if time.Since(stats.UpdatedAt) > 3*time.Second {
					add("telemetry:"+run.PrimaryRef, errors.New("runtime telemetry stale"))
				}
				if (run.Kind == "pipe" || run.Kind == "authority") && stats.ConnectedPeers == 0 {
					add("network:"+run.PrimaryRef, errors.New("no connected libp2p peers"))
				}
			}
		}
	}
	services, serviceErr := state.Services(ctx)
	add("state:services", serviceErr)
	if serviceErr == nil {
		for _, service := range services {
			u, err := url.Parse(service.Target)
			if err == nil && u.Scheme != "unix" {
				conn, dialErr := net.DialTimeout("tcp", u.Host, 250*time.Millisecond)
				if dialErr == nil {
					_ = conn.Close()
				}
				err = dialErr
			}
			add("target:"+service.Ref.String(), err)
		}
	}
	status := "ok"
	for _, check := range checks {
		if check.Status != "ok" {
			status = "warning"
			break
		}
	}
	value := map[string]any{"status": status, "checks": checks}
	if f.bools["json"] {
		return json.NewEncoder(a.Out).Encode(value)
	}
	for _, check := range checks {
		line := check.Status + "\t" + check.Name
		if check.Detail != "" {
			line += "\t" + check.Detail
		}
		fmt.Fprintln(a.Out, line)
	}
	return nil
}

func resourceLogPath(state *store.Store, raw string) (string, error) {
	ref, err := domain.ParseRef(raw)
	if err != nil {
		return "", err
	}
	switch ref.Kind {
	case domain.KindPipe, domain.KindAuthority, domain.KindRelay, domain.KindGateway:
	default:
		return "", errors.New("invalid_input: logs require pipe, authority, relay, or gateway resource")
	}
	return filepath.Join(state.Home(), "logs", strings.ReplaceAll(ref.String(), "/", "-")+".log"), nil
}

func readTail(path string, count int) ([]string, error) {
	if count < 0 || count > 10000 {
		return nil, errors.New("invalid_input: tail must be between 0 and 10000")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	start := int64(0)
	if info.Size() > 4<<20 {
		start = info.Size() - (4 << 20)
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	lines := []string{}
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > count && count >= 0 {
			lines = lines[len(lines)-count:]
		}
	}
	return lines, scanner.Err()
}

func (a App) logs(ctx context.Context, state *store.Store, f flags) error {
	path, err := resourceLogPath(state, f.args[0])
	if err != nil {
		return err
	}
	tail := 200
	if raw := f.values["tail"]; raw != "" {
		tail, err = strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("invalid_input: --tail: %w", err)
		}
	}
	lines, err := readTail(path, tail)
	if err != nil {
		if os.IsNotExist(err) {
			return withTry(fmt.Errorf("not_found: no log for %s yet", f.args[0]), "start it first: tubo start "+f.args[0])
		}
		return fmt.Errorf("invalid_input: cannot read log for %s: %v", f.args[0], err)
	}
	if f.bools["follow"] {
		if f.bools["json"] {
			return errors.New("invalid_input: --json cannot be combined with -f/--follow; 'watch pipes --json' provides NDJSON instead")
		}
	}
	if f.bools["json"] {
		return json.NewEncoder(a.Out).Encode(map[string]any{"ref": f.args[0], "lines": lines})
	}
	for _, line := range lines {
		fmt.Fprintln(a.Out, line)
	}
	if !f.bools["follow"] {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	reader := bufio.NewReader(file)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			for {
				line, readErr := reader.ReadString('\n')
				if line != "" {
					fmt.Fprint(a.Out, line)
				}
				if readErr != nil {
					if errors.Is(readErr, io.EOF) {
						break
					}
					return readErr
				}
			}
		}
	}
}

func stopRunAndWait(ctx context.Context, state *store.Store, ref string) error {
	run, err := state.Run(ctx, ref)
	if err != nil {
		return err
	}
	identity, identityErr := store.ProcessIdentity(run.PID)
	if identityErr != nil {
		return fmt.Errorf("not_found: process for %s is not running", ref)
	}
	if run.ProcessIdentity == "" || identity != run.ProcessIdentity {
		return errors.New("conflict: stale process identity; refusing to signal PID")
	}
	process, err := os.FindProcess(run.PID)
	if err != nil {
		return err
	}
	if err := process.Signal(os.Interrupt); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		current, err := store.ProcessIdentity(run.PID)
		if err != nil || current != run.ProcessIdentity {
			return state.DeleteRun(ctx, ref)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return errors.New("readiness_failed: process did not stop")
}

func (a App) restart(ctx context.Context, state *store.Store, f flags) error {
	ref := f.args[0]
	parsed, err := domain.ParseRef(ref)
	if err != nil || parsed.Kind != domain.KindPipe {
		return fmt.Errorf("invalid_input: restart requires a pipe ref like pipe/<name>; see 'tubo restart --help'")
	}
	if _, _, _, err := state.Pipe(ctx, ref); err != nil {
		return err
	}
	if err := supervisorAllowsCurrentSource(ctx, state, ref); err != nil {
		return err
	}
	if run, err := state.Run(ctx, ref); err == nil {
		identity, identityErr := store.ProcessIdentity(run.PID)
		if identityErr == nil && identity == run.ProcessIdentity {
			if err := stopRunAndWait(ctx, state, ref); err != nil {
				return err
			}
		} else if err := state.DeleteRun(ctx, ref); err != nil {
			return err
		}
	}
	a.progressf("restarting %s...\n", ref)
	run, err := a.startDetached(ctx, state, ref)
	if err != nil {
		return err
	}
	return a.emit(f.bools["json"], map[string]any{"ref": ref, "status": "ready", "pid": run.PID}, fmt.Sprintf("%s ready (pid %d)", ref, run.PID))
}

func staleRuns(ctx context.Context, state *store.Store) ([]string, error) {
	runs, err := state.Runs(ctx)
	if err != nil {
		return nil, err
	}
	removed := []string{}
	for _, run := range runs {
		if _, err := state.Supervisor(ctx, run.PrimaryRef); err == nil {
			continue
		} else if !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
		stale := false
		if run.PID <= 0 {
			stale = run.Status == "stopped" || time.Since(run.UpdatedAt) > 30*time.Second
		} else if identity, identityErr := store.ProcessIdentity(run.PID); identityErr != nil || identity != run.ProcessIdentity {
			stale = true
		}
		if stale {
			if err := state.DeleteRun(ctx, run.PrimaryRef); err != nil {
				return nil, err
			}
			removed = append(removed, run.PrimaryRef)
		}
	}
	return removed, nil
}

func (a App) remove(ctx context.Context, state *store.Store, f flags) error {
	if len(f.args) == 0 {
		return errors.New("invalid_input: rm requires a resource ref or --stale; see 'tubo rm --help'")
	}
	if f.bools["stale"] {
		refs, err := staleRuns(ctx, state)
		if err != nil {
			return err
		}
		return a.emit(f.bools["json"], map[string]any{"removed": refs, "count": len(refs)}, fmt.Sprintf("removed %d stale runs", len(refs)))
	}
	ref, err := domain.ParseRef(f.args[0])
	if err != nil {
		return err
	}
	if ref.Kind == domain.KindPipe {
		if err := supervisorAllowsCurrentSource(ctx, state, ref.String()); err != nil {
			return err
		}
		if run, runErr := state.Run(ctx, ref.String()); runErr == nil {
			identity, identityErr := store.ProcessIdentity(run.PID)
			live := identityErr == nil && identity == run.ProcessIdentity
			if live && !f.bools["force"] {
				return withTry(fmt.Errorf("conflict: %s is running", ref), "stop it first: tubo stop "+ref.String())
			}
			if live {
				if err := stopRunAndWait(ctx, state, ref.String()); err != nil {
					return err
				}
			} else if err := state.DeleteRun(ctx, ref.String()); err != nil {
				return err
			}
		}
		key, err := state.DeletePipe(ctx, ref.String())
		if err != nil {
			return err
		}
		if key != "" {
			if err := os.Remove(key); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("persistence_failed: pipe removed but secret cleanup failed: %w", err)
			}
		}
		return a.emit(f.bools["json"], map[string]any{"ref": ref.String(), "status": "removed"}, ref.String()+" removed")
	}
	if ref.Kind == domain.KindService {
		if err := state.DeleteService(ctx, ref.String()); err != nil {
			return err
		}
		return a.emit(f.bools["json"], map[string]any{"ref": ref.String(), "status": "removed"}, ref.String()+" removed")
	}
	return fmt.Errorf("invalid_input: rm supports service/<name> or pipe/<name>; see 'tubo rm --help'")
}

func (a App) stop(ctx context.Context, state *store.Store, f flags) error {
	ref := f.args[0]
	if err := supervisorAllowsCurrentSource(ctx, state, ref); err != nil {
		return err
	}
	run, err := state.Run(ctx, ref)
	if err != nil {
		return withTry(fmt.Errorf("not_found: %s is not running", ref), "start it first: tubo start "+ref)
	}
	identity, err := store.ProcessIdentity(run.PID)
	if err != nil {
		return fmt.Errorf("not_found: process for %s is not running", ref)
	}
	if run.ProcessIdentity == "" || identity != run.ProcessIdentity {
		return errors.New("conflict: stale process identity; refusing to signal PID")
	}
	process, err := os.FindProcess(run.PID)
	if err != nil {
		return err
	}
	if err := process.Signal(os.Interrupt); err != nil {
		return err
	}
	return a.emit(f.bools["json"], map[string]any{"ref": ref, "status": "stopping"}, ref+" stopping")
}
