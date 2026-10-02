package cli

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/origama/tubo/internal/domain"
	"github.com/origama/tubo/internal/store"
)

type launchdPlan struct {
	Ref      string
	Label    string
	UnitPath string
	LogPath  string
	Args     []string
	Home     string
}

func launchdLabel(ref domain.Ref) string {
	return "io.github.origama.tubo." + string(ref.Kind) + "." + ref.Name
}

func launchdPlanFor(state *store.Store, executable string, ref domain.Ref, scope string) (launchdPlan, error) {
	if !filepath.IsAbs(executable) {
		return launchdPlan{}, errors.New("supervisor executable path must be absolute")
	}
	args := []string{executable}
	switch ref.Kind {
	case domain.KindPipe:
		args = append(args, "_run-pipe", ref.String())
	case domain.KindAuthority:
		if scope == "" {
			return launchdPlan{}, errors.New("--scope is required for authority supervision")
		}
		parsed, err := parseScope(scope)
		if err != nil {
			return launchdPlan{}, err
		}
		if parsed.Cluster != ref.Name {
			return launchdPlan{}, fmt.Errorf("authority %s does not match scope %s", ref, scope)
		}
		args = append(args, "authority", "start", ref.Name, "--scope", scope)
	default:
		return launchdPlan{}, errors.New("supervision supports pipe/<name> or authority/<cluster>")
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return launchdPlan{}, fmt.Errorf("resolve user home: %w", err)
	}
	label := launchdLabel(ref)
	return launchdPlan{Ref: ref.String(), Label: label, UnitPath: filepath.Join(userHome, "Library", "LaunchAgents", label+".plist"), LogPath: filepath.Join(state.Home(), "logs", strings.ReplaceAll(ref.String(), "/", "-")+".log"), Args: args, Home: state.Home()}, nil
}

func launchdPlist(plan launchdPlan) ([]byte, error) {
	if len(plan.Args) == 0 || plan.Label == "" || plan.Home == "" || plan.LogPath == "" {
		return nil, errors.New("incomplete launchd plan")
	}
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0"><dict>` + "\n")
	writeString := func(key, value string) error {
		if _, err := fmt.Fprintf(&b, "<key>%s</key>", xmlEscape(key)); err != nil {
			return err
		}
		_, err := fmt.Fprintf(&b, "<string>%s</string>\n", xmlEscape(value))
		return err
	}
	if err := writeString("Label", plan.Label); err != nil {
		return nil, err
	}
	b.WriteString("<key>ProgramArguments</key><array>")
	for _, arg := range plan.Args {
		if _, err := fmt.Fprintf(&b, "<string>%s</string>", xmlEscape(arg)); err != nil {
			return nil, err
		}
	}
	b.WriteString("</array>\n<key>EnvironmentVariables</key><dict>")
	for _, pair := range [][2]string{{"TUBO_HOME", plan.Home}, {"TUBO_RUN_SOURCE", "launchd"}, {"TUBO_LOG_LEVEL", "info"}} {
		if err := writeString(pair[0], pair[1]); err != nil {
			return nil, err
		}
	}
	b.WriteString("</dict>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><true/>\n<key>ThrottleInterval</key><integer>5</integer>\n")
	if err := writeString("StandardOutPath", plan.LogPath); err != nil {
		return nil, err
	}
	if err := writeString("StandardErrorPath", plan.LogPath); err != nil {
		return nil, err
	}
	b.WriteString("</dict></plist>\n")
	return b.Bytes(), nil
}

func xmlEscape(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

func launchdDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func launchctl(args ...string) error {
	output, err := exec.Command("launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func launchctlBootout(label string) error {
	err := launchctl("bootout", launchdDomain()+"/"+label)
	if err == nil {
		return nil
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "could not find service") || strings.Contains(text, "no such process") {
		return nil
	}
	return err
}

func supervisorAllowsCurrentSource(ctx context.Context, state *store.Store, ref string) error {
	supervisor, err := state.Supervisor(ctx, ref)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if os.Getenv("TUBO_RUN_SOURCE") == supervisor.Manager {
		return nil
	}
	return fmt.Errorf("conflict: %s is %s-supervised; use tubo supervise uninstall %s", ref, supervisor.Manager, ref)
}

func runProcessLive(run store.Run) bool {
	identity, err := store.ProcessIdentity(run.PID)
	return err == nil && run.ProcessIdentity != "" && identity == run.ProcessIdentity
}

func supervisorRunHealthy(run store.Run, source string) bool {
	return runProcessLive(run) && run.Source == source && (run.Status == "ready" || run.Status == "degraded")
}

func waitSupervisorReady(ctx context.Context, state *store.Store, ref string) (store.Run, error) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		run, err := state.Run(ctx, ref)
		if err == nil && supervisorRunHealthy(run, "launchd") {
			return run, nil
		}
		select {
		case <-ctx.Done():
			return store.Run{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return store.Run{}, errors.New("readiness_failed: launchd service did not become ready")
}

func (a App) superviseInstall(ctx context.Context, state *store.Store, f flags) error {
	ref, err := domain.ParseRef(f.args[0])
	if err != nil {
		return err
	}
	scope := f.values["scope"]
	jsonOutput := f.bools["json"]
	if goruntime.GOOS != "darwin" {
		return errors.New("unsupported: launchd supervision requires macOS")
	}
	if ref.Kind == domain.KindPipe {
		if _, _, _, err := state.Pipe(ctx, ref.String()); err != nil {
			return err
		}
	} else if ref.Kind == domain.KindAuthority {
		if _, err := state.Scope(ctx, scope); err != nil {
			return err
		}
	} else {
		return errors.New("invalid_input: supervision supports pipe/<name> or authority/<cluster>")
	}
	if _, err := state.Supervisor(ctx, ref.String()); err == nil {
		return fmt.Errorf("conflict: %s already has a supervisor", ref)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if existing, err := state.Run(ctx, ref.String()); err == nil {
		if runProcessLive(existing) {
			return fmt.Errorf("conflict: %s is already running; stop it before supervisor install", ref)
		}
		if err := state.DeleteRun(ctx, ref.String()); err != nil {
			return err
		}
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	plan, err := launchdPlanFor(state, executable, ref, scope)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(plan.UnitPath); err == nil {
		return fmt.Errorf("conflict: launchd unit already exists at %s", plan.UnitPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := launchdPlist(plan)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plan.UnitPath), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(plan.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := logFile.Close(); err != nil {
		return err
	}
	if err := os.Chmod(plan.LogPath, 0o600); err != nil {
		return err
	}
	file, err := os.OpenFile(plan.UnitPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(plan.UnitPath)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(plan.UnitPath)
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = launchctlBootout(plan.Label)
			_ = state.DeleteSupervisor(context.Background(), ref.String())
			_ = os.Remove(plan.UnitPath)
		}
	}()
	if err := launchctl("bootstrap", launchdDomain(), plan.UnitPath); err != nil {
		return err
	}
	if err := state.PutSupervisor(ctx, store.Supervisor{PrimaryRef: ref.String(), Manager: "launchd", Label: plan.Label, UnitPath: plan.UnitPath}); err != nil {
		return fmt.Errorf("persistence_failed: supervisor registry: %w", err)
	}
	run, err := waitSupervisorReady(ctx, state, ref.String())
	if err != nil {
		return err
	}
	cleanup = false
	return a.emit(jsonOutput, map[string]any{"ref": ref.String(), "manager": "launchd", "label": plan.Label, "status": "ready", "pid": run.PID}, ref.String()+" launchd ready")
}

func (a App) superviseStatus(ctx context.Context, state *store.Store, f flags) error {
	ref, err := domain.ParseRef(f.args[0])
	if err != nil {
		return err
	}
	jsonOutput := f.bools["json"]
	supervisor, err := state.Supervisor(ctx, ref.String())
	if err != nil {
		return err
	}
	loaded := false
	managerStatus := "not_checked"
	if supervisor.Manager == "launchd" && goruntime.GOOS == "darwin" {
		managerStatus = "not_loaded"
		if err := launchctl("print", launchdDomain()+"/"+supervisor.Label); err == nil {
			loaded, managerStatus = true, "loaded"
		}
	}
	run, runErr := state.Run(ctx, ref.String())
	healthy := runErr == nil && supervisorRunHealthy(run, supervisor.Manager)
	status := "not_loaded"
	if loaded && healthy {
		status = "ready"
	} else if loaded {
		status = "crash_loop_suspected"
	}
	value := map[string]any{"ref": ref.String(), "manager": supervisor.Manager, "label": supervisor.Label, "unit_path": supervisor.UnitPath, "manager_status": managerStatus, "status": status}
	if runErr == nil {
		value["pid"] = run.PID
		value["runtime_status"] = run.Status
		value["runtime_source"] = run.Source
	}
	return a.emit(jsonOutput, value, ref.String()+" supervisor "+status)
}

func (a App) superviseUninstall(ctx context.Context, state *store.Store, f flags) error {
	ref, err := domain.ParseRef(f.args[0])
	if err != nil {
		return err
	}
	jsonOutput := f.bools["json"]
	supervisor, err := state.Supervisor(ctx, ref.String())
	if err != nil {
		return err
	}
	if supervisor.Manager != "launchd" || goruntime.GOOS != "darwin" {
		return errors.New("unsupported: launchd supervisor can only be removed on macOS")
	}
	if err := launchctlBootout(supervisor.Label); err != nil {
		return err
	}
	if run, err := state.Run(ctx, ref.String()); err == nil && runProcessLive(run) {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if !runProcessLive(run) {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		if runProcessLive(run) {
			return errors.New("readiness_failed: launchd service did not stop")
		}
	}
	if err := state.DeleteSupervisor(ctx, ref.String()); err != nil {
		return fmt.Errorf("persistence_failed: supervisor registry: %w", err)
	}
	if run, err := state.Run(ctx, ref.String()); err == nil {
		if identity, identityErr := store.ProcessIdentity(run.PID); identityErr != nil || identity != run.ProcessIdentity {
			if err := state.DeleteRun(ctx, ref.String()); err != nil {
				return err
			}
		}
	}
	if err := os.Remove(supervisor.UnitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return a.emit(jsonOutput, map[string]any{"ref": ref.String(), "manager": "launchd", "status": "removed"}, ref.String()+" launchd supervisor removed")
}
