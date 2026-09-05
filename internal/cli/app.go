package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/origama/tubo-rebuilt/internal/authority"
	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/reachability"
	appRuntime "github.com/origama/tubo-rebuilt/internal/runtime"
	"github.com/origama/tubo-rebuilt/internal/store"
	"github.com/origama/tubo-rebuilt/internal/usecase"
)

const (
	usecaseRedemptionProtocol = "/tubo/exact-redemption/1"
	usecaseRenewalProtocol    = "/tubo/exact-renewal/1"
)

// App is the thin L5 parsing/presentation adapter over L4 use cases and the
// transactional local store. It owns no connectivity, authorization, or
// runtime semantics: command specs (spec.go) drive parsing and help, handlers
// in this package delegate to use cases, and renderers present results.
type App struct {
	Out, Err io.Writer
	// ProductVersion etc. are injected by the binary entry point.
	ProductVersion, ProtocolVersion, CommitSHA, BuildDate string
	Quiet                                                 bool
	Verbosity                                             int
	LogLevel                                              string
	// UseCases is the L4 composition root, wired by Run when the store opens.
	UseCases usecase.UseCases
	// fetchRecords, networkOperationObserver, resolveDefault and now are
	// test seams; nil values use production behavior.
	fetchRecords             func(context.Context, *store.Store, string, appRuntime.FetchRecordsOptions) error
	networkOperationObserver appRuntime.NetworkOperationObserver
	resolveDefault           func(context.Context) (*reachability.Provider, error)
	now                      func() time.Time
}

// progressf writes a progress/hint line to stderr unless --quiet suppressed
// it. stderr is the exclusive channel for progress, warnings and hints.
func (a App) progressf(format string, args ...any) {
	if a.Quiet {
		return
	}
	fmt.Fprintf(a.Err, format, args...)
}

// emit writes one primary result: JSON on stdout for --json commands, or the
// human form otherwise. Human results are compact and automation-hostile-free;
// JSON is exactly one value with no stderr or progress contamination.
func (a App) emit(jsonOutput bool, value any, human string) error {
	if jsonOutput {
		return json.NewEncoder(a.Out).Encode(value)
	}
	_, err := fmt.Fprintln(a.Out, human)
	return err
}

func (a App) Run(ctx context.Context, args []string) error {
	cleaned, quiet, verbosity, level, err := extractGlobalFlags(args)
	if err != nil {
		return err
	}
	args, a.Quiet, a.Verbosity, a.LogLevel = cleaned, quiet, verbosity, level
	_ = os.Setenv("TUBO_VERBOSITY", strconv.Itoa(verbosity))
	if quiet {
		_ = os.Setenv("TUBO_QUIET", "1")
	} else {
		_ = os.Unsetenv("TUBO_QUIET")
	}
	if level != "" {
		_ = os.Setenv("TUBO_LOG_LEVEL", level)
	}
	if len(args) == 0 {
		a.helpTop()
		return nil
	}
	if args[0] == "help" {
		if len(args) == 1 {
			a.helpTop()
			return nil
		}
		spec, rest, err := resolveCommand(args[1:])
		if err != nil {
			return err
		}
		if len(rest) > 0 && !hasHelpFlag(rest) {
			return fmt.Errorf("invalid_input: help: unexpected argument %q; see 'tubo %s --help'", rest[0], spec.id())
		}
		a.printHelp(spec)
		return nil
	}
	if args[0] == "--help" || args[0] == "-h" {
		a.helpTop()
		return nil
	}
	// Resolve and validate before any Store or network side effect, so
	// unknown commands, typos, and --help never touch persistence.
	spec, rest, err := resolveCommand(args)
	if err != nil {
		return err
	}
	if hasHelpFlag(rest) {
		a.printHelp(spec)
		return nil
	}
	f, err := parseCommand(spec, rest)
	if err != nil {
		return err
	}
	switch spec.id() {
	case "version":
		return a.version(f)
	case "keygen swarm":
		return a.keygenSwarm(f)
	}
	state, err := store.Open(ctx)
	if err != nil {
		return fmt.Errorf("persistence_failed: %w", err)
	}
	defer state.Close()

	// Wire the Journey A use-case composition root.
	a.UseCases = usecase.New(
		store.NewServiceRepoAdapter(state),
		store.NewPipeRepoAdapter(state),
	)
	a.UseCases.Secrets = state.SecretPath()
	a.UseCases.OwnerLocator = newOwnerRedemptionLocator(state, a.resolveDefault)
	a.UseCases.RedemptionProtocol = usecaseRedemptionProtocol
	a.UseCases.Redemption = appRuntime.NewExactRedemptionClient(state.SecretPath())
	a.UseCases.Renewal = appRuntime.NewExactRenewalClient(state.SecretPath())
	a.UseCases.Reach = nil
	a.UseCases.Starter = nil
	// Narrow durable/exact persistence ports (schema 12): the accepted
	// remote-Pipe binding is durable relationship knowledge; the lease cache
	// is ephemeral and fails closed on expiry.
	a.UseCases.Bindings = store.NewRemotePipeBindingAdapter(state)
	a.UseCases.LeaseCache = store.NewAccessLeaseCacheAdapter(state)

	if a.Verbosity > 0 && !a.Quiet {
		fmt.Fprintf(a.Err, "debug: command=%s home=%s\n", spec.id(), state.Home())
	}
	err = a.dispatch(ctx, state, spec, f)
	if err != nil {
		// Attach a deterministic next-action hint unless the error already
		// carries one (handlers set concrete ones where the ref is known).
		if _, ok := asCliError(err); !ok {
			return hintForClass(err)
		}
	}
	return err
}

// dispatch routes a validated command to its handler. Every leaf commandSpec
// (see spec.go / leafPaths) must have a case here; a test cross-checks that.
func (a App) dispatch(ctx context.Context, state *store.Store, spec *commandSpec, f flags) error {
	switch spec.id() {
	case "attach":
		return a.attach(ctx, state, f)
	case "accept":
		return a.accept(ctx, state, f)
	case "connect":
		return a.connect(ctx, state, f)
	case "share":
		return a.share(ctx, state, f)
	case "service create":
		return a.serviceCreate(ctx, state, f)
	case "pipe create":
		return a.pipeCreate(ctx, state, f)
	case "start":
		return a.start(ctx, state, f)
	case "stop":
		return a.stop(ctx, state, f)
	case "restart":
		return a.restart(ctx, state, f)
	case "rm":
		return a.remove(ctx, state, f)
	case "logs":
		return a.logs(ctx, state, f)
	case "inspect":
		return a.inspect(ctx, state, f)
	case "get pipes":
		return a.getPipes(ctx, state, f)
	case "get services":
		return a.getServices(ctx, state, f)
	case "get scopes":
		return a.getScopes(ctx, state, f)
	case "get peers":
		return a.getPeers(ctx, state, f)
	case "get overlays":
		return a.getOverlays(ctx, state, f)
	case "watch pipes":
		return a.watchPipesCmd(ctx, state, f)
	case "ps":
		return a.ps(ctx, state, f)
	case "top":
		return a.top(ctx, state, f)
	case "peers alias":
		return a.peersAlias(ctx, state, f)
	case "config validate":
		return a.configValidate(ctx, state, f)
	case "config print":
		return a.configPrint(ctx, state, f)
	case "doctor":
		return a.doctor(ctx, state, f)
	case "supervise install":
		return a.superviseInstall(ctx, state, f)
	case "supervise status":
		return a.superviseStatus(ctx, state, f)
	case "supervise uninstall":
		return a.superviseUninstall(ctx, state, f)
	case "scope create":
		return a.scopeCreate(ctx, state, f)
	case "scope invite":
		return a.scopeInvite(ctx, state, f)
	case "scope join":
		return a.scopeJoin(ctx, state, f)
	case "scope use":
		return a.scopeUse(ctx, state, f)
	case "authority start":
		return a.authorityStart(ctx, state, f)
	case "relay":
		return a.relay(ctx, state, f)
	case "join overlay":
		return a.joinOverlay(ctx, state, f)
	case "revoke":
		return a.revoke(ctx, state, f)
	case "_run-pipe":
		return a.runPipeInternal(ctx, state, f)
	}
	return fmt.Errorf("internal error: unhandled command %q", spec.id())
}

func (a App) version(f flags) error {
	if len(f.args) > 0 {
		return fmt.Errorf("invalid_input: version: unexpected argument %q", f.args[0])
	}
	value := map[string]string{"product_version": a.ProductVersion, "protocol_version": a.ProtocolVersion, "commit_sha": a.CommitSHA, "build_date": a.BuildDate}
	return a.emit(f.bools["json"], value, fmt.Sprintf("tubo %s (protocol %s, commit %s, built %s)", a.ProductVersion, a.ProtocolVersion, a.CommitSHA, a.BuildDate))
}

func (a App) keygenSwarm(f flags) error {
	out, err := f.required("out")
	if err != nil {
		return err
	}
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return err
	}
	content := "/key/swarm/psk/1.0.0/\n/base16/\n" + hex.EncodeToString(data) + "\n"
	if err := os.WriteFile(out, []byte(content), 0o600); err != nil {
		return err
	}
	return a.emit(f.bools["json"], map[string]any{"path": out}, out)
}

func (a App) startDetached(ctx context.Context, state *store.Store, ref string) (store.Run, error) {
	if err := supervisorAllowsCurrentSource(ctx, state, ref); err != nil {
		return store.Run{}, err
	}
	if existing, err := state.Run(ctx, ref); err == nil {
		if existing.Status == "ready" {
			identity, identityErr := store.ProcessIdentity(existing.PID)
			if identityErr == nil && identity == existing.ProcessIdentity {
				return existing, nil
			}
		}
		if existing.Status == "starting" {
			return store.Run{}, fmt.Errorf("conflict: %s is already starting", ref)
		}
		if err := state.DeleteRun(ctx, ref); err != nil {
			return store.Run{}, err
		}
	}
	if err := state.ReserveRun(ctx, ref, "pipe"); err != nil {
		return store.Run{}, err
	}
	reserved := true
	defer func() {
		if reserved {
			_ = state.DeleteRun(context.Background(), ref)
		}
	}()
	executable, err := os.Executable()
	if err != nil {
		return store.Run{}, err
	}
	logPath := filepath.Join(state.Home(), "logs", strings.ReplaceAll(ref, "/", "-")+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return store.Run{}, err
	}
	defer logFile.Close()
	command := exec.Command(executable, "_run-pipe", ref)
	command.Env = append(os.Environ(), "TUBO_RUN_SOURCE=detached")
	command.Stdout = logFile
	command.Stderr = logFile
	command.Stdin = nil
	if err := command.Start(); err != nil {
		return store.Run{}, err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		run, err := state.Run(ctx, ref)
		if err == nil && run.Status == "ready" && run.PID == command.Process.Pid {
			_ = command.Process.Release()
			reserved = false
			return run, nil
		}
		if command.ProcessState != nil && command.ProcessState.Exited() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = command.Process.Kill()
	_ = command.Wait()
	return store.Run{}, errors.New("readiness_failed: child did not become ready")
}

// runPipeInternal is the hidden _run-pipe entry used by detached children.
func (a App) runPipeInternal(ctx context.Context, state *store.Store, f flags) error {
	return appRuntime.RunPipe(ctx, state, f.args[0])
}

// start runs the pipe runtime in the foreground and returns when it stops.
func (a App) start(ctx context.Context, state *store.Store, f flags) error {
	if len(f.args) == 0 {
		return errors.New("invalid_input: start requires a pipe ref; see 'tubo start --help'")
	}
	ref := f.args[0]
	if f.bools["detach"] {
		a.progressf("starting %s...\n", ref)
		run, err := a.startDetached(ctx, state, ref)
		if err != nil {
			return err
		}
		return a.emit(f.bools["json"], map[string]any{"ref": ref, "status": "ready", "pid": run.PID}, fmt.Sprintf("%s ready (pid %d)", ref, run.PID))
	}
	if f.bools["json"] {
		return errors.New("invalid_input: start --json requires -d (foreground start has no JSON result); see 'tubo start --help'")
	}
	a.progressf("starting %s...\n", ref)
	a.progressf("Ctrl-C to stop\n")
	return runPipeForeground(ctx, state, ref)
}

// statusWord renders created/reused for a mutation result.
func statusWord(created bool) string {
	if created {
		return "created"
	}
	return "reused"
}

// emitAttach renders the attach result. run != nil means the Pipe is known
// ready (detached start); otherwise only the durable definitions are shown.
func (a App) emitAttach(jsonOutput bool, result usecase.AttachResult, run *store.Run) error {
	serviceStatus := statusWord(result.Created)
	pipeStatus := statusWord(result.PipeCreated)
	if jsonOutput {
		value := map[string]any{
			"service_ref":    result.ServiceRef.String(),
			"service_status": serviceStatus,
			"pipe_ref":       result.PipeRef.String(),
			"pipe_id":        result.PipeID,
			"pipe_status":    pipeStatus,
		}
		if run != nil {
			value["status"] = "ready"
			value["pid"] = run.PID
		}
		return json.NewEncoder(a.Out).Encode(value)
	}
	if _, err := fmt.Fprintf(a.Out, "service/%s %s\n", result.ServiceRef.Name, serviceStatus); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.Out, "pipe/%s %s\n", result.PipeRef.Name, pipeStatus); err != nil {
		return err
	}
	if run != nil {
		if _, err := fmt.Fprintf(a.Out, "pipe/%s ready (pid %d)\n", result.PipeRef.Name, run.PID); err != nil {
			return err
		}
	}
	return nil
}

// emitConnect renders the connect result. run != nil means the local Pipe is
// known ready (detached start).
func (a App) emitConnect(jsonOutput bool, result usecase.ConnectExactResult, run *store.Run) error {
	ready := run != nil
	// local is always the effective persisted listener from the application
	// result, never an echo of the requested input.
	local := result.LocalListen
	if jsonOutput {
		value := map[string]any{
			"ref":       result.PipeRef.String(),
			"id":        result.PipeID,
			"local":     local,
			"target_id": result.TargetPipeID,
		}
		if ready {
			value["status"] = "ready"
			value["pid"] = run.PID
		}
		return json.NewEncoder(a.Out).Encode(value)
	}
	if ready {
		if _, err := fmt.Fprintf(a.Out, "%s ready (pid %d)\n", result.PipeRef.String(), run.PID); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(a.Out, "local listen: %s\n", local); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(a.Out, "%s created (local listen: %s)\n", result.PipeRef.String(), local); err != nil {
			return err
		}
	}
	return nil
}

// attach defines a Service and a Pipe for a local target, then runs the Pipe.
func (a App) attach(ctx context.Context, state *store.Store, f flags) error {
	name := f.values["name"]
	target := f.args[0]
	result, err := a.UseCases.Attach(ctx, usecase.AttachInput{Name: name, Target: target})
	if err != nil {
		return err
	}
	if f.bools["detach"] {
		a.progressf("starting pipe/%s...\n", result.PipeRef.Name)
		run, err := a.startDetached(ctx, state, result.PipeRef.String())
		if err != nil {
			return err
		}
		if err := a.emitAttach(f.bools["json"], result, &run); err != nil {
			return err
		}
		a.progressf("share it: tubo share pipe/%s\n", result.PipeRef.Name)
		return nil
	}
	if err := a.emitAttach(f.bools["json"], result, nil); err != nil {
		return err
	}
	a.progressf("starting pipe/%s...\n", result.PipeRef.Name)
	a.progressf("Ctrl-C to stop\n")
	return runPipeForeground(ctx, state, result.PipeRef.String())
}

// share mints a one-time exact invite. The raw invite is the sole stdout
// result so command substitution keeps working; explanation and next steps go
// to stderr and never contaminate stdout.
func (a App) share(ctx context.Context, _ *store.Store, f flags) error {
	pipeRef := f.args[0]
	if !strings.HasPrefix(pipeRef, "pipe/") {
		return fmt.Errorf("invalid_input: share requires a pipe ref like pipe/<name>; see 'tubo share --help'")
	}
	result, err := a.UseCases.ShareExact(ctx, usecase.ShareExactInput{PipeRef: pipeRef})
	if err != nil {
		return err
	}
	expiresIn := result.ExpiresAt.Sub(a.currentTime())
	if expiresIn < 0 {
		expiresIn = 0
	}
	a.progressf("sharing %s\n", pipeRef)
	a.progressf("one-time invite; expires in %s\n", expiresIn.Round(time.Second))
	a.progressf("recipient runs: tubo accept --token <invite> (redeems once)\n")
	a.progressf("then, anytime: tubo connect --remote %s --local 127.0.0.1:PORT\n", result.PipeID)
	return a.emit(f.bools["json"], map[string]any{"pipe_id": result.PipeID, "invite": result.Invite, "expires_at": result.ExpiresAt}, result.Invite)
}

// accept redeems a one-time exact invite once and persists the durable
// accepted remote-Pipe binding. It creates no local Pipe, no ingress, and
// starts no runtime: acceptable access is reacquired later through the
// durable binding (tubo connect --remote <pipe-id>).
func (a App) accept(ctx context.Context, state *store.Store, f flags) error {
	reach, reachErr := appRuntime.NewExactReachabilityAcquirer(ctx, state, protocol.ID(usecaseRedemptionProtocol))
	if reachErr != nil {
		return fmt.Errorf("reachability_unavailable: %w", reachErr)
	}
	a.UseCases.Reach = reach
	result, err := a.UseCases.AcceptExactInvite(ctx, usecase.AcceptExactInviteInput{
		Invite: f.values["token"],
	})
	if err != nil {
		return err
	}
	a.progressf("accepted remote pipe %s\n", result.RemotePipeID)
	a.progressf("invite no longer needed; connect later: tubo connect --remote %s --local 127.0.0.1:PORT\n", result.RemotePipeID)
	return a.emit(f.bools["json"], map[string]any{"remote_pipe_id": result.RemotePipeID, "recipient_public": result.RecipientPublic, "lease_expires_at": result.LeaseExpiresAt}, result.RemotePipeID)
}

// connect redeems an exact invite (or uses an already-accepted durable
// binding) and runs a local ingress Pipe.
func (a App) connect(ctx context.Context, state *store.Store, f flags) error {
	token, remote := f.values["token"], f.values["remote"]
	if token != "" && remote != "" {
		return errors.New("invalid_input: connect accepts exactly one of --token or --remote; see 'tubo connect --help'")
	}
	// The carrier protocol depends on the exchange: redemption for a fresh
	// invite, renewal for an already-accepted remote Pipe.
	proto := usecaseRenewalProtocol
	if token != "" {
		proto = usecaseRedemptionProtocol
	}
	reach, reachErr := appRuntime.NewExactReachabilityAcquirer(ctx, state, protocol.ID(proto))
	if reachErr != nil {
		return fmt.Errorf("reachability_unavailable: %w", reachErr)
	}
	a.UseCases.Reach = reach

	result, err := a.UseCases.ConnectExact(ctx, usecase.ConnectExactInput{
		Invite:      token,
		RemotePipeID: remote,
		LocalListen: f.values["local"],
		Name:        f.values["name"],
	})
	if err != nil {
		return err
	}
	if f.bools["detach"] {
		a.progressf("starting %s...\n", result.PipeRef.String())
		run, err := a.startDetached(ctx, state, result.PipeRef.String())
		if err != nil {
			return err
		}
		if err := a.emitConnect(f.bools["json"], result, &run); err != nil {
			return err
		}
		a.progressf("stop:  tubo stop %s\n", result.PipeRef.String())
		a.progressf("remove: tubo rm %s\n", result.PipeRef.String())
		return nil
	}
	if err := a.emitConnect(f.bools["json"], result, nil); err != nil {
		return err
	}
	a.progressf("starting %s...\n", result.PipeRef.String())
	a.progressf("Ctrl-C to stop\n")
	return runPipeForeground(ctx, state, result.PipeRef.String())
}

func (a App) serviceCreate(ctx context.Context, state *store.Store, f flags) error {
	target, err := f.required("target")
	if err != nil {
		return err
	}
	spec, err := serviceSpec(f.args[0], target)
	if err != nil {
		return err
	}
	created, err := state.PutService(ctx, spec)
	if err != nil {
		return err
	}
	status := "reused"
	if created {
		status = "created"
	}
	return a.emit(f.bools["json"], map[string]any{"ref": spec.Ref.String(), "status": status}, spec.Ref.String()+" "+status)
}

func serviceSpec(name, target string) (domain.ServiceSpec, error) {
	ref, err := domain.NewRef(domain.KindService, name)
	if err != nil {
		return domain.ServiceSpec{}, err
	}
	scheme := strings.ToLower(strings.SplitN(target, ":", 2)[0])
	kind := domain.ServiceKind(scheme)
	if scheme == "https" {
		kind = domain.ServiceHTTP
	}
	spec := domain.ServiceSpec{Ref: ref, Kind: kind, Target: target}
	return spec, spec.Validate()
}

func (a App) pipeCreate(ctx context.Context, state *store.Store, f flags) error {
	targetRaw, err := f.required("target")
	if err != nil {
		return err
	}
	target, err := domain.ParseRef(targetRaw)
	if err != nil {
		return err
	}
	ingressRaw, err := f.required("ingress")
	if err != nil {
		return err
	}
	ref, err := domain.NewRef(domain.KindPipe, f.args[0])
	if err != nil {
		return err
	}
	ownerPath := state.SecretPath("pipes", ref.Name+".key")
	owner, err := loadOrGeneratePrivate(ownerPath)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	spec := domain.PipeSpec{Ref: ref, PipeID: authority.PipeID(owner.Public().(ed25519.PublicKey)), Target: target, Visibility: domain.VisibilityPrivate, Access: domain.AccessInviteOnly, CreatedAt: now, UpdatedAt: now}
	if existing, existingKey, _, existingErr := state.Pipe(ctx, ref.String()); existingErr == nil {
		spec.PipeID = existing.PipeID
		spec.CreatedAt = existing.CreatedAt
		spec.UpdatedAt = existing.UpdatedAt
		ownerPath = existingKey
	} else if !errors.Is(existingErr, domain.ErrNotFound) {
		return existingErr
	}
	grant := ""
	if ingressRaw == "tubo" {
		scopeRaw, err := f.required("scope")
		if err != nil {
			return err
		}
		scope, err := parseScope(scopeRaw)
		if err != nil {
			return err
		}
		spec.Scope = scope
		spec.Ingress = domain.PipeIngress{Kind: domain.IngressTubo, Protocol: "raw"}
		spec.Visibility = domain.Visibility(f.values["visibility"])
		if spec.Visibility == "" {
			spec.Visibility = domain.VisibilityUnlisted
		}
		spec.Access = domain.AccessPolicy(f.values["access"])
		if spec.Access == "" {
			spec.Access = domain.AccessInviteOnly
		}
		scopeState, scopeErr := state.Scope(ctx, scopeRaw)
		if scopeErr == nil {
			authorityPrivate, err := authority.ReadPrivate(scopeState.AuthorityKeyFile)
			if err != nil {
				return err
			}
			publish, err := authority.NewToken("pipe.publish", *scope, authority.PublicString(owner.Public().(ed25519.PublicKey)), spec.PipeID, 30*24*time.Hour)
			if err != nil {
				return err
			}
			publish.PublishEpoch, err = state.RevocationEpoch(ctx, scopeRaw, "pipe-publish", spec.PipeID)
			if err != nil {
				return err
			}
			if err := publish.Sign(authorityPrivate); err != nil {
				return err
			}
			grant, err = authority.Encode(publish)
			if err != nil {
				return err
			}
		} else if errors.Is(scopeErr, domain.ErrNotFound) {
			h, _, err := appRuntime.OpenOverlayHost(ctx, state, fmt.Sprintf("publish-%d", os.Getpid()))
			if err != nil {
				return err
			}
			grant, err = appRuntime.RequestPublishGrant(ctx, state, h, scopeRaw, spec.PipeID)
			_ = h.Close()
			if err != nil {
				return err
			}
		} else {
			return scopeErr
		}
	} else if strings.HasPrefix(ingressRaw, "local://") {
		spec.Ingress = domain.PipeIngress{Kind: domain.IngressLocal, Listen: strings.TrimPrefix(ingressRaw, "local://")}
		spec.Visibility = domain.VisibilityPrivate
		spec.Access = domain.AccessMembers
	} else {
		return errors.New("invalid_input: ingress must be tubo or local://host:port")
	}
	created, err := state.PutPipe(ctx, spec, ownerPath, grant)
	if err != nil {
		return err
	}
	status := "reused"
	if created {
		status = "created"
	}
	return a.emit(f.bools["json"], map[string]any{"ref": spec.Ref.String(), "id": spec.PipeID, "status": status}, spec.Ref.String()+" "+status)
}

func (a App) scopeCreate(ctx context.Context, state *store.Store, f flags) error {
	scope, err := parseScope(f.args[0])
	if err != nil {
		return err
	}
	if _, err := state.Scope(ctx, scope.String()); err == nil {
		return fmt.Errorf("conflict: scope/%s already exists", scope)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	authorityPath := state.SecretPath("authorities", scope.Cluster+".key")
	private, err := authority.GeneratePrivate(authorityPath)
	if err != nil {
		return err
	}
	namespacePath := state.SecretPath("namespaces", scope.Cluster, scope.Namespace, "current.secret")
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	if err := writeSecretExclusive(namespacePath, []byte(base64.RawURLEncoding.EncodeToString(secret))); err != nil {
		return err
	}
	value := store.Scope{Name: scope.String(), AuthorityPublic: authority.PublicString(private.Public().(ed25519.PublicKey)), AuthorityKeyFile: authorityPath, NamespaceKeyFile: namespacePath}
	if err := state.PutScope(ctx, value); err != nil {
		_ = os.Remove(namespacePath)
		_ = os.Remove(authorityPath)
		return fmt.Errorf("persistence_failed: create scope: %w", err)
	}
	return a.emit(f.bools["json"], map[string]any{"ref": "scope/" + scope.String(), "status": "created"}, "scope/"+scope.String()+" created")
}

func (a App) scopeInvite(ctx context.Context, state *store.Store, f flags) error {
	role, err := f.required("role")
	if err != nil {
		return err
	}
	if role != "member" && role != "viewer" && role != "publisher" && role != "forwarder" {
		return errors.New("invalid_input: role must be member, viewer, publisher, or forwarder")
	}
	scopeValue, err := state.Scope(ctx, f.args[0])
	if err != nil {
		return err
	}
	scope, err := parseScope(scopeValue.Name)
	if err != nil {
		return err
	}
	run, err := state.Run(ctx, "authority/"+scope.Cluster)
	if err != nil || run.Status != "ready" {
		return errors.New("authority_unreachable: authority must be ready")
	}
	duration := 24 * time.Hour
	if raw := f.values["expires"]; raw != "" {
		duration, err = time.ParseDuration(raw)
		if err != nil {
			return err
		}
	}
	if duration <= 0 {
		return errors.New("invalid_input: expires must be positive")
	}
	private, err := authority.ReadPrivate(scopeValue.AuthorityKeyFile)
	if err != nil {
		return err
	}
	namespace, err := os.ReadFile(scopeValue.NamespaceKeyFile)
	if err != nil {
		return err
	}
	token, err := authority.NewToken("membership.invite", *scope, "invitee", "", duration)
	if err != nil {
		return err
	}
	token.Role = role
	token.AuthorityAddress = run.Address
	token.NamespaceKey = strings.TrimSpace(string(namespace))
	if err := token.Sign(private); err != nil {
		return err
	}
	encoded, err := authority.Encode(token)
	if err != nil {
		return err
	}
	return a.emit(f.bools["json"], map[string]any{"scope": scope.String(), "role": role, "token": encoded, "expires_at": token.ExpiresAt}, encoded)
}

func (a App) scopeJoin(ctx context.Context, state *store.Store, f flags) error {
	tokenValue, err := f.required("token")
	if err != nil {
		return err
	}
	token, err := authority.Decode(tokenValue)
	if err != nil {
		return err
	}
	public, err := authority.PublicFromString(token.Issuer)
	if err != nil {
		return err
	}
	if err := token.Verify(public, time.Now().UTC()); err != nil {
		return err
	}
	if token.Type != "membership.invite" || token.AuthorityAddress == "" || token.NamespaceKey == "" {
		return errors.New("invalid membership invite")
	}
	scope := token.Scope.String()
	clientPath := state.SecretPath("memberships", strings.ReplaceAll(scope, "/", "-")+".key")
	if _, err := os.Stat(clientPath); os.IsNotExist(err) {
		if _, err := authority.GeneratePrivate(clientPath); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	namespacePath := state.SecretPath("namespaces", token.Scope.Cluster, token.Scope.Namespace, "current.secret")
	if err := os.MkdirAll(filepath.Dir(namespacePath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(namespacePath, []byte(token.NamespaceKey), 0o600); err != nil {
		return err
	}
	membership := store.Membership{Scope: scope, Role: token.Role, Token: tokenValue, AuthorityPublic: token.Issuer, AuthorityAddress: token.AuthorityAddress, ClientKeyFile: clientPath}
	if err := state.PutMembership(ctx, membership); err != nil {
		return err
	}
	return a.emit(f.bools["json"], map[string]any{"ref": "scope/" + scope, "role": token.Role, "status": "joined"}, "scope/"+scope+" joined")
}

func (a App) authorityStart(ctx context.Context, state *store.Store, f flags) error {
	if f.bools["detach"] {
		return errors.New("invalid_input: detached authority start is not supported; use 'tubo supervise install authority/<cluster>'")
	}
	ref := "authority/" + f.args[0]
	if err := reserveForeground(ctx, state, ref, "authority"); err != nil {
		return err
	}
	err := appRuntime.RunAuthority(ctx, state, f.args[0], f.values["scope"])
	cleanupStarting(ctx, state, ref)
	return err
}

func (a App) relay(ctx context.Context, state *store.Store, f flags) error {
	listen, err := f.required("listen")
	if err != nil {
		return err
	}
	publicAddress, err := f.required("public-addr")
	if err != nil {
		return err
	}
	swarmKey := f.values["swarm-key"]
	if err := reserveForeground(ctx, state, "relay/default", "relay"); err != nil {
		return err
	}
	err = appRuntime.RunRelay(ctx, state, listen, publicAddress, swarmKey)
	cleanupStarting(ctx, state, "relay/default")
	return err
}

func (a App) joinOverlay(ctx context.Context, state *store.Store, f flags) error {
	relay, err := f.required("relay")
	if err != nil {
		return err
	}
	key, err := f.required("swarm-key")
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(key)
	if err != nil {
		return err
	}
	reachability := f.values["force-reachability"]
	if reachability == "" {
		reachability = "private"
	}
	if reachability != "private" && reachability != "public" {
		return errors.New("invalid_input: force-reachability must be private or public")
	}
	if err := state.PutOverlay(ctx, relay, absolute, reachability); err != nil {
		return err
	}
	return a.emit(f.bools["json"], map[string]any{"ref": "overlay/default", "relay": relay, "status": "configured"}, "overlay/default configured")
}

func (a App) revoke(ctx context.Context, state *store.Store, f flags) error {
	typeName, id := f.args[0], f.args[1]
	if typeName == "invite" {
		typeName = "share.invite"
	}
	if typeName != "share.invite" && typeName != "session" && typeName != "pipe-access" && typeName != "pipe-publish" {
		return fmt.Errorf("invalid_input: revoke type must be invite, session, pipe-access, or pipe-publish")
	}
	// Scope resolution. Scope-less exact Pipes (Journey A) revoke under a
	// scope equal to the exact Pipe ID, matching the store adapter used by the
	// ExactOpenAuthorizer and the renewal handler. An invited JTI is resolved
	// through its accepted redemption when present.
	var scope string
	switch {
	case strings.HasPrefix(id, "pipe/"):
		spec, _, _, pipeErr := state.Pipe(ctx, id)
		if pipeErr != nil {
			return pipeErr
		}
		id = spec.PipeID
		if spec.Scope != nil {
			scope = spec.Scope.String()
		} else {
			scope = spec.PipeID
		}
	case strings.HasPrefix(id, "pip_"):
		scope = id
	case typeName != "share.invite":
		current, err := state.CurrentScope(ctx)
		if err != nil {
			return err
		}
		scope = current
	default:
		// share.invite without a pipe ref: only resolvable for accepted
		// invites, which are exact (scope-less) and keyed by pipe.
		pipeID, ok, err := state.RedemptionPipeForInvite(ctx, id)
		if err != nil {
			return fmt.Errorf("persistence_failed: %w", err)
		}
		if !ok {
			return fmt.Errorf("invalid_input: invite %s was never accepted; nothing to revoke", id)
		}
		scope = pipeID
	}
	if _, membershipErr := state.Membership(ctx, scope); membershipErr == nil {
		h, _, hostErr := appRuntime.OpenOverlayHost(ctx, state, fmt.Sprintf("revoke-%d", os.Getpid()))
		if hostErr != nil {
			return hostErr
		}
		revokeErr := appRuntime.RequestRevoke(ctx, state, h, scope, typeName, id, 1)
		_ = h.Close()
		if revokeErr != nil {
			return revokeErr
		}
	} else if err := state.Revoke(ctx, scope, typeName, id, 1); err != nil {
		return fmt.Errorf("persistence_failed: %w", err)
	}
	return a.emit(f.bools["json"], map[string]any{"scope": scope, "subject_type": typeName, "subject_jti": id, "revoked": true}, "revoked "+typeName+"/"+id)
}

// currentTime returns the injected or real wall clock in UTC.
func (a App) currentTime() time.Time {
	if a.now != nil {
		return a.now().UTC()
	}
	return time.Now().UTC()
}

func reserveForeground(ctx context.Context, state *store.Store, ref, kind string) error {
	if err := supervisorAllowsCurrentSource(ctx, state, ref); err != nil {
		return err
	}
	if existing, err := state.Run(ctx, ref); err == nil {
		if existing.Status == "ready" {
			identity, identityErr := store.ProcessIdentity(existing.PID)
			if identityErr == nil && identity == existing.ProcessIdentity {
				return withTry(fmt.Errorf("conflict: %s is already running", ref), "stop it first: tubo stop "+ref)
			}
		}
		if existing.Status == "starting" {
			return fmt.Errorf("conflict: %s is already starting", ref)
		}
		if err := state.DeleteRun(ctx, ref); err != nil {
			return err
		}
	}
	return state.ReserveRun(ctx, ref, kind)
}

func cleanupStarting(ctx context.Context, state *store.Store, ref string) {
	if run, err := state.Run(ctx, ref); err == nil && run.Status == "starting" {
		_ = state.DeleteRun(ctx, ref)
	}
}

func runPipeForeground(ctx context.Context, state *store.Store, ref string) error {
	if err := reserveForeground(ctx, state, ref, "pipe"); err != nil {
		return err
	}
	err := appRuntime.RunPipe(ctx, state, ref)
	cleanupStarting(ctx, state, ref)
	return err
}

func writeSecretExclusive(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(path)
		return err
	}
	return file.Close()
}

func loadOrGeneratePrivate(path string) (ed25519.PrivateKey, error) {
	private, err := authority.ReadPrivate(path)
	if err == nil {
		return private, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	return authority.GeneratePrivate(path)
}

func parseScope(raw string) (*domain.Scope, error) {
	parts := strings.Split(raw, "/")
	if len(parts) != 2 {
		return nil, errors.New("invalid_input: scope must be cluster/namespace")
	}
	value := &domain.Scope{Cluster: parts[0], Namespace: parts[1]}
	return value, value.Validate()
}
