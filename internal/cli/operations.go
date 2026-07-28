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
	"github.com/origama/tubo-rebuilt/internal/store"
)

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
				return nil, false, 0, "", errors.New("--log-level requires value")
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
			return nil, false, 0, "", errors.New("--log-level must be error, warn, info, debug, or trace")
		}
	}
	return clean, quiet, verbosity, level, nil
}

func (a App) scopeUse(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 {
		return errors.New("usage: tubo scope use cluster/namespace")
	}
	if _, err := parseScope(f.args[0]); err != nil {
		return err
	}
	if err := state.UseScope(ctx, f.args[0]); err != nil {
		return err
	}
	return a.emit(f.bools["json"], map[string]any{"ref": "scope/" + f.args[0], "status": "current"}, "scope/"+f.args[0]+" current")
}

func (a App) getLocal(ctx context.Context, state *store.Store, f flags) error {
	kind := f.args[0]
	var items any
	var refs []string
	switch kind {
	case "services":
		values, err := state.Services(ctx)
		if err != nil {
			return err
		}
		rows := make([]map[string]any, 0, len(values))
		for _, value := range values {
			rows = append(rows, map[string]any{"ref": value.Ref.String(), "name": value.Ref.Name, "kind": value.Kind})
			refs = append(refs, value.Ref.String())
		}
		items = rows
	case "scopes":
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
			seen[value.Name] = len(rows)
			rows = append(rows, map[string]any{"ref": "scope/" + value.Name, "name": value.Name, "authority": true, "current": value.Name == current})
			refs = append(refs, "scope/"+value.Name)
		}
		for _, value := range memberships {
			if index, ok := seen[value.Scope]; ok {
				rows[index]["role"] = value.Role
				continue
			}
			rows = append(rows, map[string]any{"ref": "scope/" + value.Scope, "name": value.Scope, "authority": false, "role": value.Role, "current": value.Scope == current})
			refs = append(refs, "scope/"+value.Scope)
		}
		items = rows
	case "processes", "runs":
		values, err := runtimeViews(ctx, state)
		if err != nil {
			return err
		}
		items = values
		for _, value := range values {
			refs = append(refs, value.PrimaryRef)
		}
	case "peers":
		values, err := buildPeerViews(ctx, state)
		if err != nil {
			return err
		}
		if f.bools["json"] {
			return json.NewEncoder(a.Out).Encode(map[string]any{"items": values})
		}
		return renderPeerViews(a.Out, values)
	case "overlays":
		value, err := state.Config()
		if err != nil {
			return err
		}
		row := map[string]any{"ref": "overlay/" + value.CurrentContext, "relays": value.Relays, "force_reachability": value.ForceReachability, "current_scope": value.CurrentScope}
		items = []any{row}
		refs = []string{"overlay/" + value.CurrentContext}
	default:
		return errors.New("usage: tubo get <pipes|services|scopes|processes|peers|overlays>")
	}
	if f.bools["json"] {
		return json.NewEncoder(a.Out).Encode(map[string]any{"items": items})
	}
	if len(refs) == 0 {
		_, err := fmt.Fprintln(a.Out, "No resources.")
		return err
	}
	_, err := fmt.Fprintln(a.Out, strings.Join(refs, "\n"))
	return err
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
		return a.emit(jsonOutput, value, fmt.Sprintf("%s %s %s", value.Ref, value.Kind, value.Target))
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
		return a.emit(jsonOutput, value, raw)
	case strings.HasPrefix(raw, "overlay/"):
		value, err := state.Config()
		if err != nil {
			return err
		}
		if raw != "overlay/"+value.CurrentContext {
			return fmt.Errorf("%w: %s", domain.ErrNotFound, raw)
		}
		view := map[string]any{"ref": raw, "relays": value.Relays, "force_reachability": value.ForceReachability, "current_scope": value.CurrentScope, "swarm_key": "configured"}
		return a.emit(jsonOutput, view, raw)
	case strings.HasPrefix(raw, "session/"):
		value, err := state.Session(ctx, strings.TrimPrefix(raw, "session/"))
		if err != nil {
			return err
		}
		return a.emit(jsonOutput, value, fmt.Sprintf("session/%s %s", value.ID, value.InitialTransportPath))
	case strings.HasPrefix(raw, "peer/"):
		value, err := state.PeerAlias(ctx, strings.TrimPrefix(raw, "peer/"))
		if err != nil {
			return err
		}
		return a.emit(jsonOutput, value, fmt.Sprintf("peer/%s %s", value.PeerID, value.Name))
	}
	return fmt.Errorf("unsupported resource %q", raw)
}

func (a App) peers(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 2 || f.args[0] != "alias" {
		return errors.New("usage: tubo peers alias <peer-id> --name <label> [--note <note>]")
	}
	if _, err := peer.Decode(f.args[1]); err != nil {
		return fmt.Errorf("invalid peer ID: %w", err)
	}
	name, err := f.required("name")
	if err != nil {
		return err
	}
	if len(name) > 63 || len(f.values["note"]) > 512 {
		return errors.New("alias name or note exceeds limit")
	}
	if strings.IndexFunc(name+f.values["note"], unicode.IsControl) >= 0 {
		return errors.New("alias name and note must not contain control characters")
	}
	value := store.PeerAlias{PeerID: f.args[1], Name: name, Note: f.values["note"]}
	if err := state.PutPeerAlias(ctx, value); err != nil {
		return err
	}
	return a.emit(f.bools["json"], map[string]any{"ref": "peer/" + value.PeerID, "name": value.Name, "status": "saved"}, "peer/"+value.PeerID+" saved")
}

func maskedConfig(value store.ConfigView) map[string]any {
	key := ""
	if value.SwarmKeyFile != "" {
		key = "<configured>"
	}
	return map[string]any{"version": value.Version, "current_context": value.CurrentContext, "current_scope": value.CurrentScope, "relays": value.Relays, "swarm_key_file": key, "force_reachability": value.ForceReachability}
}

func (a App) config(_ context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 {
		return errors.New("usage: tubo config <validate|print> [--json]")
	}
	switch f.args[0] {
	case "validate":
		value, err := state.ValidateConfig()
		if err != nil {
			return fmt.Errorf("invalid_input: config: %w", err)
		}
		return a.emit(f.bools["json"], map[string]any{"status": "valid", "config": maskedConfig(value)}, "config valid")
	case "print":
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
	default:
		return errors.New("usage: tubo config <validate|print> [--json]")
	}
}

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func (a App) doctor(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) > 0 {
		return errors.New("usage: tubo doctor [--json]")
	}
	checks := []doctorCheck{}
	add := func(name string, err error) {
		if err != nil {
			checks = append(checks, doctorCheck{Name: name, Status: "warning", Detail: err.Error()})
		} else {
			checks = append(checks, doctorCheck{Name: name, Status: "ok"})
		}
	}
	_, configErr := state.ValidateConfig()
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
		return "", errors.New("logs require pipe, authority, relay, or gateway resource")
	}
	return filepath.Join(state.Home(), "logs", strings.ReplaceAll(ref.String(), "/", "-")+".log"), nil
}

func readTail(path string, count int) ([]string, error) {
	if count < 0 || count > 10000 {
		return nil, errors.New("tail must be between 0 and 10000")
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

func (a App) logs(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 {
		return errors.New("usage: tubo logs <resource> [--tail N] [-f|--follow] [--json]")
	}
	path, err := resourceLogPath(state, f.args[0])
	if err != nil {
		return err
	}
	tail := 200
	if raw := f.values["tail"]; raw != "" {
		tail, err = strconv.Atoi(raw)
		if err != nil {
			return err
		}
	}
	lines, err := readTail(path, tail)
	if err != nil {
		return fmt.Errorf("not_found: log for %s: %w", f.args[0], err)
	}
	if f.bools["json"] {
		return json.NewEncoder(a.Out).Encode(map[string]any{"ref": f.args[0], "lines": lines})
	}
	for _, line := range lines {
		fmt.Fprintln(a.Out, line)
	}
	if !f.bools["f"] && !f.bools["follow"] {
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

func (a App) restart(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 {
		return errors.New("usage: tubo restart pipe/<name> [--json]")
	}
	ref, err := domain.ParseRef(f.args[0])
	if err != nil || ref.Kind != domain.KindPipe {
		return errors.New("restart requires pipe/<name>")
	}
	if _, _, _, err := state.Pipe(ctx, ref.String()); err != nil {
		return err
	}
	if err := supervisorAllowsCurrentSource(ctx, state, ref.String()); err != nil {
		return err
	}
	if run, err := state.Run(ctx, ref.String()); err == nil {
		identity, identityErr := store.ProcessIdentity(run.PID)
		if identityErr == nil && identity == run.ProcessIdentity {
			if err := stopRunAndWait(ctx, state, ref.String()); err != nil {
				return err
			}
		} else if err := state.DeleteRun(ctx, ref.String()); err != nil {
			return err
		}
	}
	run, err := a.startDetached(ctx, state, ref.String())
	if err != nil {
		return err
	}
	return a.emit(f.bools["json"], map[string]any{"ref": ref.String(), "status": "running", "pid": run.PID}, ref.String()+" restarted")
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

func (a App) remove(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if f.bools["stale"] {
		if len(f.args) > 0 {
			return errors.New("usage: tubo rm --stale [--json]")
		}
		refs, err := staleRuns(ctx, state)
		if err != nil {
			return err
		}
		return a.emit(f.bools["json"], map[string]any{"removed": refs, "count": len(refs)}, fmt.Sprintf("removed %d stale runs", len(refs)))
	}
	if len(f.args) != 1 {
		return errors.New("usage: tubo rm <service/name|pipe/name> [--force] [--json]")
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
				return fmt.Errorf("conflict: %s is running", ref)
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
	return errors.New("rm supports service/name or pipe/name")
}
