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
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/origama/tubo-rebuilt/internal/authority"
	"github.com/origama/tubo-rebuilt/internal/discovery"
	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/reachability"
	appRuntime "github.com/origama/tubo-rebuilt/internal/runtime"
	"github.com/origama/tubo-rebuilt/internal/store"
	"github.com/origama/tubo-rebuilt/internal/usecase"
)

type App struct {
	Out, Err                                              io.Writer
	ProductVersion, ProtocolVersion, CommitSHA, BuildDate string
	Quiet                                                 bool
	Verbosity                                             int
	LogLevel                                              string
	UseCases                                              usecase.UseCases
	fetchRecords                                          func(context.Context, *store.Store, string, appRuntime.FetchRecordsOptions) error
	networkOperationObserver                              appRuntime.NetworkOperationObserver
	resolveDefault                                        func(context.Context) (*reachability.Provider, error)
	now                                                   func() time.Time
}
type flags struct {
	values map[string]string
	bools  map[string]bool
	args   []string
}

func parse(args []string) (flags, error) {
	f := flags{values: map[string]string{}, bools: map[string]bool{}}
	boolean := map[string]bool{"json": true, "d": true, "one-time": true, "once": true, "force": true, "stale": true, "follow": true, "refresh": true, "cached-only": true}
	for i := 0; i < len(args); i++ {
		value := args[i]
		if value == "-d" || value == "-f" {
			f.bools[strings.TrimPrefix(value, "-")] = true
			continue
		}
		if !strings.HasPrefix(value, "--") {
			f.args = append(f.args, value)
			continue
		}
		key := strings.TrimPrefix(value, "--")
		if key == "live" || strings.HasPrefix(key, "live=") {
			return f, errors.New("invalid_input: --live is obsolete; use --refresh")
		}
		if boolean[key] {
			f.bools[key] = true
			continue
		}
		if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
			return f, fmt.Errorf("--%s requires value", key)
		}
		i++
		f.values[key] = args[i]
	}
	return f, nil
}
func (f flags) required(name string) (string, error) {
	value := f.values[name]
	if value == "" {
		return "", fmt.Errorf("--%s is required", name)
	}
	return value, nil
}
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
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		a.help()
		return nil
	}
	if args[0] == "version" {
		return a.version(args[1:])
	}
	if args[0] == "keygen" {
		return a.keygen(args[1:])
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
	a.UseCases.RedemptionProtocol = "/tubo/exact-redemption/1"
	a.UseCases.Redemption = nil // not wired for CLI-only commands
	a.UseCases.Reach = nil
	a.UseCases.Starter = nil

	if a.Verbosity > 0 && !a.Quiet {
		fmt.Fprintf(a.Err, "debug: command=%s home=%s\n", args[0], state.Home())
	}
	switch args[0] {
	case "join":
		return a.join(ctx, state, args[1:])
	case "scope":
		return a.scope(ctx, state, args[1:])
	case "authority":
		return a.authority(ctx, state, args[1:])
	case "relay":
		return a.relay(ctx, state, args[1:])
	case "service":
		return a.service(ctx, state, args[1:])
	case "pipe":
		return a.pipe(ctx, state, args[1:])
	case "start":
		return a.start(ctx, state, args[1:])
	case "connect":
		return a.connect(ctx, state, args[1:])
	case "share":
		return a.share(ctx, state, args[1:])
	case "revoke":
		return a.revoke(ctx, state, args[1:])
	case "attach":
		return a.attach(ctx, state, args[1:])
	case "get":
		return a.get(ctx, state, args[1:])
	case "watch":
		return a.watch(ctx, state, args[1:])
	case "inspect", "describe":
		return a.inspect(ctx, state, args[1:])
	case "ps":
		return a.ps(ctx, state, args[1:])
	case "top":
		return a.top(ctx, state, args[1:])
	case "logs":
		return a.logs(ctx, state, args[1:])
	case "peers":
		return a.peers(ctx, state, args[1:])
	case "config":
		return a.config(ctx, state, args[1:])
	case "doctor":
		return a.doctor(ctx, state, args[1:])
	case "supervise":
		return a.supervise(ctx, state, args[1:])
	case "restart":
		return a.restart(ctx, state, args[1:])
	case "rm":
		return a.remove(ctx, state, args[1:])
	case "stop":
		return a.stop(ctx, state, args[1:])
	case "_run-pipe":
		if len(args) != 2 {
			return errors.New("internal pipe runner requires ref")
		}
		return appRuntime.RunPipe(ctx, state, args[1])
	default:
		return fmt.Errorf("unknown command %q; run `tubo help`", args[0])
	}
}

func (a App) version(args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) > 0 {
		return fmt.Errorf("version: unexpected argument %q", f.args[0])
	}
	value := map[string]string{"product_version": a.ProductVersion, "protocol_version": a.ProtocolVersion, "commit_sha": a.CommitSHA, "build_date": a.BuildDate}
	return a.emit(f.bools["json"], value, fmt.Sprintf("tubo %s (protocol %s, commit %s, built %s)", a.ProductVersion, a.ProtocolVersion, a.CommitSHA, a.BuildDate))
}
func (a App) keygen(args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 || f.args[0] != "swarm" {
		return errors.New("usage: tubo keygen swarm --out <file>")
	}
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
func (a App) join(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 || f.args[0] != "overlay" {
		return errors.New("usage: tubo join overlay --relay <multiaddr> --swarm-key <file>")
	}
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
		return errors.New("force-reachability must be private or public")
	}
	if err := state.PutOverlay(ctx, relay, absolute, reachability); err != nil {
		return err
	}
	return a.emit(f.bools["json"], map[string]any{"ref": "overlay/default", "relay": relay, "status": "configured"}, "overlay/default configured")
}

func (a App) scope(ctx context.Context, state *store.Store, args []string) error {
	if len(args) == 0 {
		return errors.New("scope subcommand required")
	}
	switch args[0] {
	case "create":
		return a.scopeCreate(ctx, state, args[1:])
	case "invite":
		return a.scopeInvite(ctx, state, args[1:])
	case "join":
		return a.scopeJoin(ctx, state, args[1:])
	case "use":
		return a.scopeUse(ctx, state, args[1:])
	default:
		return fmt.Errorf("unknown scope subcommand %q", args[0])
	}
}
func (a App) scopeCreate(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 {
		return errors.New("usage: tubo scope create cluster/namespace")
	}
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
func (a App) scopeInvite(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 {
		return errors.New("usage: tubo scope invite cluster/namespace --role member")
	}
	role, err := f.required("role")
	if err != nil {
		return err
	}
	if role != "member" && role != "viewer" && role != "publisher" && role != "forwarder" {
		return errors.New("invalid role")
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
		return errors.New("expires must be positive")
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
func (a App) scopeJoin(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
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

func (a App) relay(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
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

func (a App) authority(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 2 || f.args[0] != "start" {
		return errors.New("usage: tubo authority start <cluster> --scope cluster/namespace")
	}
	scope, err := f.required("scope")
	if err != nil {
		return err
	}
	if f.bools["d"] {
		return errors.New("detached authority start not yet supported; use supervisor")
	}
	ref := "authority/" + f.args[1]
	if err := reserveForeground(ctx, state, ref, "authority"); err != nil {
		return err
	}
	err = appRuntime.RunAuthority(ctx, state, f.args[1], scope)
	cleanupStarting(ctx, state, ref)
	return err
}
func (a App) service(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 2 || f.args[0] != "create" {
		return errors.New("usage: tubo service create <name> --target <url>")
	}
	target, err := f.required("target")
	if err != nil {
		return err
	}
	spec, err := serviceSpec(f.args[1], target)
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

func (a App) pipe(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 2 || f.args[0] != "create" {
		return errors.New("usage: tubo pipe create <name> --ingress <kind> --target <ref>")
	}
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
	ref, err := domain.NewRef(domain.KindPipe, f.args[1])
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
		return errors.New("ingress must be tubo or local://host:port")
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

func (a App) start(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 {
		return errors.New("usage: tubo start pipe/<name> [-d]")
	}
	ref, err := domain.ParseRef(f.args[0])
	if err != nil || ref.Kind != domain.KindPipe {
		return errors.New("start requires pipe/<name>")
	}
	if f.bools["d"] {
		run, err := a.startDetached(ctx, state, ref.String())
		if err != nil {
			return err
		}
		return a.emit(f.bools["json"], map[string]any{"ref": ref.String(), "status": "running", "pid": run.PID}, ref.String()+" running")
	}
	return runPipeForeground(ctx, state, ref.String())
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

func (a App) share(ctx context.Context, _ *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 || !strings.HasPrefix(f.args[0], "pipe/") {
		return errors.New("usage: tubo share pipe/<name> [--json]")
	}
	result, err := a.UseCases.ShareExact(ctx, usecase.ShareExactInput{
		PipeRef: f.args[0],
	})
	if err != nil {
		return err
	}
	return a.emit(f.bools["json"], map[string]any{"pipe_id": result.PipeID, "invite": result.Invite, "expires_at": result.ExpiresAt}, result.Invite)
}

func (a App) revoke(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 2 {
		return errors.New("usage: tubo revoke invite|session|pipe-access|pipe-publish <id>")
	}
	typeName, id := f.args[0], f.args[1]
	if typeName == "invite" {
		typeName = "share.invite"
	}
	if typeName != "share.invite" && typeName != "session" && typeName != "pipe-access" && typeName != "pipe-publish" {
		return errors.New("invalid revocation type")
	}
	scope, err := state.CurrentScope(ctx)
	if err != nil {
		return err
	}
	if strings.HasPrefix(id, "pipe/") {
		spec, _, _, pipeErr := state.Pipe(ctx, id)
		if pipeErr != nil {
			return pipeErr
		}
		id = spec.PipeID
		scope = spec.Scope.String()
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

func (a App) connect(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 && f.values["token"] == "" {
		return errors.New("usage: tubo connect <pipe-name|pipe/id> --local host:port [-d] | tubo connect --token <invite> --local host:port")
	}
	local, err := f.required("local")
	if err != nil {
		return err
	}
	// Journey A: --token path uses UseCases (no discovery, no Scope).
	if tokenValue := f.values["token"]; tokenValue != "" {
		// Wire reachability and redemption for the Journey A connect path.
		// These are constructed at the command handler boundary (not inside
		// the usecase) and injected into UseCases.
		reach, reachErr := appRuntime.NewExactReachabilityAcquirer(ctx, state, protocol.ID(a.UseCases.RedemptionProtocol))
		if reachErr != nil {
			return fmt.Errorf("reachability_unavailable: %w", reachErr)
		}
		a.UseCases.Reach = reach
		a.UseCases.Redemption = appRuntime.NewExactRedemptionClient(state.SecretPath())
		a.UseCases.ExactConnections = store.NewExactConnectionStateAdapter(state)

		result, err := a.UseCases.ConnectExact(ctx, usecase.ConnectExactInput{
			Invite:      tokenValue,
			LocalListen: local,
		})
		if err != nil {
			return err
		}
		if !f.bools["d"] {
			return runPipeForeground(ctx, state, result.PipeRef.String())
		}
		run, err := a.startDetached(ctx, state, result.PipeRef.String())
		if err != nil {
			return err
		}
		return a.emit(f.bools["json"], map[string]any{"ref": result.PipeRef.String(), "id": result.PipeID, "status": "running", "pid": run.PID}, result.PipeRef.String()+" running")
	}
	// Legacy scope-based path (preserved for backward compatibility).
	var record discovery.PipeRecord
	record, err = a.resolveRemote(ctx, state, f.args[0], f.values["scope"])
	if err != nil {
		return err
	}
	accessHost, _, hostErr := appRuntime.OpenOverlayHost(ctx, state, fmt.Sprintf("access-%d", os.Getpid()))
	if hostErr != nil {
		return hostErr
	}
	_, accessErr := appRuntime.RequestLease(ctx, state, accessHost, record.Scope.String(), record.PipeID)
	_ = accessHost.Close()
	if accessErr != nil {
		return accessErr
	}
	name := "connect-" + strings.TrimPrefix(record.PipeID, "pip_")
	if len(name) > 32 {
		name = name[:32]
	}
	ref, _ := domain.NewRef(domain.KindPipe, name)
	target, _ := domain.NewRef(domain.KindPipe, record.PipeID)
	ownerPath := state.SecretPath("pipes", ref.Name+".key")
	owner, err := loadOrGeneratePrivate(ownerPath)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	spec := domain.PipeSpec{Ref: ref, PipeID: authority.PipeID(owner.Public().(ed25519.PublicKey)), Ingress: domain.PipeIngress{Kind: domain.IngressLocal, Listen: local}, Target: target, Visibility: domain.VisibilityPrivate, Access: domain.AccessMembers, CreatedAt: now, UpdatedAt: now}
	if existing, existingKey, _, existingErr := state.Pipe(ctx, ref.String()); existingErr == nil {
		spec.PipeID = existing.PipeID
		spec.CreatedAt = existing.CreatedAt
		spec.UpdatedAt = existing.UpdatedAt
		ownerPath = existingKey
	} else if !errors.Is(existingErr, domain.ErrNotFound) {
		return existingErr
	}
	if _, err := state.PutPipe(ctx, spec, ownerPath, ""); err != nil {
		return err
	}
	if !f.bools["d"] {
		return runPipeForeground(ctx, state, ref.String())
	}
	run, err := a.startDetached(ctx, state, ref.String())
	if err != nil {
		return err
	}
	return a.emit(f.bools["json"], map[string]any{"ref": ref.String(), "id": spec.PipeID, "target_pipe_id": record.PipeID, "status": "running", "pid": run.PID}, ref.String()+" running")
}
func (a App) resolveRemote(ctx context.Context, state *store.Store, value, scope string) (discovery.PipeRecord, error) {
	id := strings.TrimPrefix(value, "pipe/")
	if strings.HasPrefix(id, "pip_") {
		var stored store.Record
		var err error
		if scope != "" {
			stored, err = state.RecordInScope(ctx, scope, id)
		} else {
			stored, err = state.Record(ctx, id)
		}
		if err == nil {
			return appRuntime.ParseVerifiedRecord(ctx, state, stored)
		}
		if strings.Contains(err.Error(), "multiple scopes") {
			return discovery.PipeRecord{}, err
		}
	}
	memberships, err := state.Memberships(ctx)
	if err != nil {
		return discovery.PipeRecord{}, err
	}
	var discoveryErr error
	if scope != "" {
		_, discoveryErr = appRuntime.FetchRecords(ctx, state, scope)
	} else {
		for _, membership := range memberships {
			if _, fetchErr := appRuntime.FetchRecords(ctx, state, membership.Scope); fetchErr != nil && discoveryErr == nil {
				discoveryErr = fetchErr
			}
		}
	}
	var matches []discovery.PipeRecord
	for _, membership := range memberships {
		records, recordsErr := state.Records(ctx, membership.Scope)
		if recordsErr != nil {
			return discovery.PipeRecord{}, recordsErr
		}
		for _, stored := range records {
			record, verifyErr := appRuntime.ParseVerifiedRecord(ctx, state, stored)
			if verifyErr != nil {
				return discovery.PipeRecord{}, verifyErr
			}
			if record.PipeID == id || record.DisplayName == id {
				matches = append(matches, record)
			}
		}
	}
	if len(matches) == 0 {
		if discoveryErr != nil {
			return discovery.PipeRecord{}, fmt.Errorf("discovery_unreachable: %w", discoveryErr)
		}
		return discovery.PipeRecord{}, fmt.Errorf("not_found: remote pipe %s", value)
	}
	if len(matches) > 1 {
		return discovery.PipeRecord{}, fmt.Errorf("conflict: display name %s is not unique; use pipe_id", value)
	}
	return matches[0], nil
}

func (a App) attach(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 {
		return errors.New("usage: tubo attach <target> --name <name> [-d]")
	}
	name, err := f.required("name")
	if err != nil {
		return err
	}
	result, err := a.UseCases.Attach(ctx, usecase.AttachInput{
		Name:   name,
		Target: f.args[0],
	})
	if err != nil {
		return err
	}
	if f.bools["d"] {
		startArgs := []string{"pipe/" + name, "-d"}
		if f.bools["json"] {
			startArgs = append(startArgs, "--json")
		}
		return a.start(ctx, state, startArgs)
	}
	return runPipeForeground(ctx, state, result.PipeRef.String())
}

const pipeCacheFreshTTL = 5 * time.Minute

func (a App) currentTime() time.Time {
	if a.now != nil {
		return a.now().UTC()
	}
	return time.Now().UTC()
}

type pipeRefreshError struct {
	Class string `json:"class"`
}

type pipeScopeStatus struct {
	Scope            string            `json:"scope"`
	CacheState       string            `json:"cache_state"`
	Source           string            `json:"source,omitempty"`
	LastSuccess      *time.Time        `json:"last_success,omitempty"`
	RefreshAttempted bool              `json:"refresh_attempted"`
	RefreshError     *pipeRefreshError `json:"refresh_error,omitempty"`
}

func pipeCacheStatus(value store.ScopeFreshness, found bool, now time.Time) pipeScopeStatus {
	status := pipeScopeStatus{Scope: value.Scope, CacheState: "missing", Source: value.Source, LastSuccess: value.LastSuccess}
	if !found {
		return status
	}
	status.Scope = value.Scope
	if value.Complete && value.Source == store.SnapshotSourcePinnedAuthority && value.LastSuccess != nil && !value.LastSuccess.After(now) && now.Sub(*value.LastSuccess) < pipeCacheFreshTTL {
		status.CacheState = "fresh"
	} else {
		status.CacheState = "stale"
	}
	return status
}

func pipeRefreshErrorClass(err error) string {
	message := err.Error()
	for _, class := range []string{"invalid_input", "not_found", "authorization_denied", "authority_unreachable", "discovery_unreachable", "discovery_retry_after", "persistence_failed", "partial_snapshot", "invalid_snapshot"} {
		if strings.Contains(message, class) {
			return class
		}
	}
	return "discovery_unreachable"
}

func (a App) refreshPipeRecords(ctx context.Context, state *store.Store, scope string, force bool) error {
	options := appRuntime.FetchRecordsOptions{Force: force, NetworkOperationObserver: a.networkOperationObserver}
	if a.fetchRecords != nil {
		return a.fetchRecords(ctx, state, scope, options)
	}
	_, err := appRuntime.FetchRecordsWithOptions(ctx, state, scope, options)
	return err
}

func (a App) get(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 {
		return errors.New("usage: tubo get <pipes|services|scopes|processes|peers|overlays>")
	}
	if f.args[0] != "pipes" {
		return a.getLocal(ctx, state, f)
	}
	if f.bools["refresh"] && f.bools["cached-only"] {
		return errors.New("invalid_input: --refresh and --cached-only cannot be used together")
	}
	scope := f.values["scope"]
	memberships, err := state.Memberships(ctx)
	if err != nil {
		return err
	}
	statuses := make([]pipeScopeStatus, 0, len(memberships))
	statusByScope := make(map[string]string, len(memberships))
	now := a.currentTime()
	for _, membership := range memberships {
		if scope != "" && membership.Scope != scope {
			continue
		}
		freshness, freshnessErr := state.ScopeFreshness(ctx, membership.Scope)
		found := freshnessErr == nil
		if freshnessErr != nil && !errors.Is(freshnessErr, domain.ErrNotFound) {
			return fmt.Errorf("persistence_failed: discovery freshness: %w", freshnessErr)
		}
		if !found {
			freshness.Scope = membership.Scope
		}
		status := pipeCacheStatus(freshness, found, now)
		if !f.bools["cached-only"] && (f.bools["refresh"] || status.CacheState != "fresh") {
			status.RefreshAttempted = true
			if refreshErr := a.refreshPipeRecords(ctx, state, membership.Scope, f.bools["refresh"]); refreshErr != nil {
				status.RefreshError = &pipeRefreshError{Class: pipeRefreshErrorClass(refreshErr)}
				updated, updatedErr := state.ScopeFreshness(ctx, membership.Scope)
				if updatedErr == nil {
					status = pipeCacheStatus(updated, true, a.currentTime())
					status.RefreshAttempted = true
					status.RefreshError = &pipeRefreshError{Class: pipeRefreshErrorClass(refreshErr)}
				}
			} else if updated, updatedErr := state.ScopeFreshness(ctx, membership.Scope); updatedErr == nil {
				status = pipeCacheStatus(updated, true, a.currentTime())
				status.RefreshAttempted = true
			}
		}
		statuses = append(statuses, status)
		statusByScope[membership.Scope] = status.CacheState
	}
	items, err := buildPipeViews(ctx, state, scope, statusByScope)
	if err != nil {
		return err
	}
	if f.bools["json"] {
		return json.NewEncoder(a.Out).Encode(map[string]any{"items": items, "scopes": statuses})
	}
	if err := renderPipeViews(a.Out, items); err != nil {
		return err
	}
	for _, status := range statuses {
		if status.RefreshError != nil {
			fmt.Fprintf(a.Err, "warning: discovery scope=%s cache=%s error_class=%s; showing verified cache\n", status.Scope, status.CacheState, status.RefreshError.Class)
		}
	}
	return nil
}

// pipeWatchEvent is one NDJSON record emitted by `tubo watch pipes --json`.
// It contains only a projection of verified local state; watch never opens a
// discovery socket or accepts an unverified remote event.
type pipeWatchEvent struct {
	EventType  string    `json:"event_type"`
	Scope      string    `json:"scope"`
	PipeID     string    `json:"pipe_id"`
	Name       string    `json:"name"`
	Changed    []string  `json:"changed,omitempty"`
	Item       *pipeView `json:"item,omitempty"`
	Previous   *pipeView `json:"previous,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

func (a App) watch(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 || f.args[0] != "pipes" {
		return errors.New("usage: tubo watch pipes [--scope cluster/namespace] [--interval 2s] [--json]")
	}
	interval := 2 * time.Second
	if raw := f.values["interval"]; raw != "" {
		interval, err = time.ParseDuration(raw)
		if err != nil || interval < 500*time.Millisecond {
			return errors.New("--interval must be a duration of at least 500ms")
		}
	}
	return a.watchPipes(ctx, state, f.values["scope"], interval, f.bools["json"])
}

// watchPipes polls the transactional local projection. Output is synchronous:
// a slow writer backpressures polling instead of accumulating a goroutine or
// an unbounded event queue.
func (a App) watchPipes(ctx context.Context, state *store.Store, scope string, interval time.Duration, jsonOutput bool) error {
	previous := map[string]pipeView{}
	poll := func() error {
		freshness, err := a.watchPipeFreshness(ctx, state, scope)
		if err != nil {
			return err
		}
		items, err := buildPipeViews(ctx, state, scope, freshness)
		if err != nil {
			return err
		}
		current := make(map[string]pipeView, len(items))
		for _, item := range items {
			current[pipeWatchKey(item)] = item
		}
		events := diffPipeViews(previous, current, a.currentTime())
		for _, event := range events {
			if err := a.emitPipeWatchEvent(jsonOutput, event); err != nil {
				return err
			}
		}
		previous = current
		return nil
	}
	if err := poll(); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := poll(); err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				return err
			}
		}
	}
}

func (a App) watchPipeFreshness(ctx context.Context, state *store.Store, scopeFilter string) (map[string]string, error) {
	memberships, err := state.Memberships(ctx)
	if err != nil {
		return nil, err
	}
	freshness := make(map[string]string, len(memberships))
	for _, membership := range memberships {
		if scopeFilter != "" && membership.Scope != scopeFilter {
			continue
		}
		value, err := state.ScopeFreshness(ctx, membership.Scope)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, fmt.Errorf("persistence_failed: discovery freshness: %w", err)
		}
		if err != nil {
			value.Scope = membership.Scope
		}
		freshness[membership.Scope] = pipeCacheStatus(value, err == nil, a.currentTime()).CacheState
	}
	return freshness, nil
}

func pipeWatchKey(item pipeView) string { return item.Scope + "\x00" + item.ID }

func diffPipeViews(previous, current map[string]pipeView, observedAt time.Time) []pipeWatchEvent {
	keys := make([]string, 0, len(previous)+len(current))
	seen := make(map[string]bool, len(previous)+len(current))
	for key := range previous {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range current {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	events := make([]pipeWatchEvent, 0, len(keys))
	for _, key := range keys {
		before, beforeOK := previous[key]
		after, afterOK := current[key]
		switch {
		case !beforeOK && afterOK:
			value := after
			events = append(events, pipeWatchEvent{EventType: "added", Scope: after.Scope, PipeID: after.ID, Name: after.Name, Item: &value, ObservedAt: observedAt})
		case beforeOK && !afterOK:
			value := before
			eventType := "removed"
			if before.ExpiresAt != nil && !before.ExpiresAt.After(observedAt) {
				eventType = "expired"
			}
			events = append(events, pipeWatchEvent{EventType: eventType, Scope: before.Scope, PipeID: before.ID, Name: before.Name, Previous: &value, ObservedAt: observedAt})
		case beforeOK && afterOK:
			changed := changedPipeViewFields(before, after)
			if len(changed) == 0 {
				continue
			}
			oldValue, newValue := before, after
			events = append(events, pipeWatchEvent{EventType: pipeWatchChangeType(before, after, observedAt), Scope: after.Scope, PipeID: after.ID, Name: after.Name, Changed: changed, Item: &newValue, Previous: &oldValue, ObservedAt: observedAt})
		}
	}
	return events
}

// pipeWatchChangeType preserves distinct degraded/recovered observations while
// keeping unrelated projection changes as updated. Status is local verified
// runtime state; source and freshness remain available in Changed as evidence.
func pipeWatchChangeType(before, after pipeView, observedAt time.Time) string {
	switch {
	case before.ExpiresAt != nil && !before.ExpiresAt.After(observedAt) && pipeViewHasNetworkSource(before) && !pipeViewHasNetworkSource(after):
		return "expired"
	case before.Status != "degraded" && after.Status == "degraded":
		return "degraded"
	case before.Status == "degraded" && after.Status != "degraded":
		return "recovered"
	default:
		return "updated"
	}
}

func pipeViewHasNetworkSource(item pipeView) bool {
	return item.Source == "network" || item.Source == "local+network"
}

func changedPipeViewFields(before, after pipeView) []string {
	changed := make([]string, 0, 3)
	if before.Status != after.Status {
		changed = append(changed, "status")
	}
	if before.Source != after.Source {
		changed = append(changed, "source")
	}
	if before.Freshness != after.Freshness {
		changed = append(changed, "freshness")
	}
	return changed
}

func (a App) emitPipeWatchEvent(jsonOutput bool, event pipeWatchEvent) error {
	if jsonOutput {
		return json.NewEncoder(a.Out).Encode(event)
	}
	item := event.Item
	if item == nil {
		item = event.Previous
	}
	if item == nil { // defensive: each event must contain its current or prior view.
		return errors.New("internal error: pipe watch event without view")
	}
	line := fmt.Sprintf("%s scope=%s pipe_id=%s name=%s status=%s source=%s freshness=%s", event.EventType, event.Scope, event.PipeID, event.Name, item.Status, item.Source, item.Freshness)
	if len(event.Changed) > 0 {
		line += " changed=" + strings.Join(event.Changed, ",")
	}
	_, err := fmt.Fprintln(a.Out, line)
	return err
}

func (a App) inspect(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 {
		return errors.New("usage: tubo inspect <resource>")
	}
	raw := f.args[0]
	if strings.HasPrefix(raw, "service/") || strings.HasPrefix(raw, "scope/") || strings.HasPrefix(raw, "overlay/") || strings.HasPrefix(raw, "session/") || strings.HasPrefix(raw, "peer/") {
		return a.inspectLocal(ctx, state, raw, f.bools["json"])
	}
	run, runErr := state.Run(ctx, raw)
	if runErr == nil && (strings.HasPrefix(raw, "relay/") || strings.HasPrefix(raw, "authority/")) {
		return a.emit(f.bools["json"], run, fmt.Sprintf("%s %s", raw, run.Status))
	}
	identifier := strings.TrimPrefix(raw, "pipe/")
	spec, _, _, localErr := state.Pipe(ctx, raw)
	if localErr != nil {
		spec, _, _, localErr = state.Pipe(ctx, identifier)
	}
	if localErr == nil {
		run, runErr = state.Run(ctx, spec.Ref.String())
		sessions, _ := state.Sessions(ctx, spec.Ref.String())
		authorizationPath := []any{}
		if spec.Scope != nil {
			authorizationPath = append(authorizationPath, map[string]any{"scope": spec.Scope.String(), "operation": "pipe.publish"})
		} else if record, ok := localAdvertisedRecord(ctx, state, spec); ok {
			authorizationPath = append(authorizationPath, map[string]any{"scope": record.Scope.String(), "operation": "connect.access", "pipe_id": record.PipeID})
		}
		status := localPipeStatus(run, runErr)
		value := map[string]any{"ref": spec.Ref.String(), "id": spec.PipeID, "kind": "pipe", "ingress": spec.Ingress, "target": spec.Target.String(), "status": status, "logical_path": []any{spec.Ref.String(), spec.Target.String()}, "authorization_path": authorizationPath, "transport_summary": transportSummary(ctx, state, spec, sessions), "sessions": sessions, "record_fresh": false}
		if record, ok := localAdvertisedRecord(ctx, state, spec); ok {
			value["record_age"] = recordAge(record, a.currentTime())
			value["record_fresh"] = recordFresh(record, a.currentTime())
		}
		return a.emit(f.bools["json"], value, fmt.Sprintf("%s %s", spec.Ref, status))
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
	value := map[string]any{"ref": "pipe/" + record.PipeID, "id": record.PipeID, "name": record.DisplayName, "kind": "pipe", "scope": record.Scope, "peer_id": record.RuntimePeerID, "status": "available", "logical_path": []any{"pipe/" + record.PipeID}, "authorization_path": []any{map[string]any{"scope": record.Scope.String(), "operation": "connect.access", "pipe_id": record.PipeID}}, "transport_summary": map[string]any{"current": path, "source": "advertised", "advertised_path": path, "advertised_candidates": candidates}, "sessions": []any{}, "record_age": recordAge(record, a.currentTime()), "record_fresh": recordFresh(record, a.currentTime())}
	return a.emit(f.bools["json"], value, "pipe/"+record.PipeID+" available")
}

type runtimeView struct {
	PrimaryRef      string  `json:"primary_ref"`
	Kind            string  `json:"kind"`
	PID             int     `json:"pid"`
	Source          string  `json:"source"`
	Status          string  `json:"status"`
	PeerID          string  `json:"peer_id,omitempty"`
	Address         string  `json:"address,omitempty"`
	Sessions        int     `json:"sessions"`
	BytesIn         int64   `json:"bytes_in"`
	BytesOut        int64   `json:"bytes_out"`
	NetworkBytesIn  int64   `json:"network_bytes_in"`
	NetworkBytesOut int64   `json:"network_bytes_out"`
	RateIn          float64 `json:"network_rate_in"`
	RateOut         float64 `json:"network_rate_out"`
	ConnectedPeers  int     `json:"connected_peers"`
	OpenConnections int     `json:"open_connections"`
	Path            string  `json:"path,omitempty"`
}

func runtimeViews(ctx context.Context, state *store.Store) ([]runtimeView, error) {
	runs, err := state.Runs(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]runtimeView, 0, len(runs))
	for _, run := range runs {
		item := runtimeView{PrimaryRef: run.PrimaryRef, Kind: run.Kind, PID: run.PID, Source: run.Source, Status: run.Status, PeerID: run.PeerID, Address: run.Address}
		if run.Kind == "pipe" {
			sessions, err := state.Sessions(ctx, run.PrimaryRef)
			if err != nil {
				return nil, err
			}
			item.Sessions = len(sessions)
			item.Path, _ = sessionPath(sessions)
			if item.Path == "" && strings.Contains(run.Address, "/p2p-circuit") {
				item.Path = "relayed"
			}
			if item.Path == "" {
				item.Path = "unknown"
			}
			for _, session := range sessions {
				item.BytesIn += session.BytesIn
				item.BytesOut += session.BytesOut
			}
		}
		if stats, err := state.RuntimeStats(ctx, run.PrimaryRef); err == nil {
			item.NetworkBytesIn = stats.BytesIn
			item.NetworkBytesOut = stats.BytesOut
			item.RateIn = stats.RateIn
			item.RateOut = stats.RateOut
			item.ConnectedPeers = stats.ConnectedPeers
			item.OpenConnections = stats.OpenConnections
		}
		items = append(items, item)
	}
	return items, nil
}

func renderRuntimeTable(out io.Writer, items []runtimeView) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "REF\tKIND\tSTATUS\tPID\tSESS\tAPP RX\tAPP TX\tNET RX\tNET TX\tRX/s\tTX/s\tPEERS\tCONNS\tPATH\tADDRESS"); err != nil {
		return err
	}
	for _, item := range items {
		address := item.Address
		if len(address) > 80 {
			address = address[:77] + "..."
		}
		path := item.Path
		if path == "" {
			path = "-"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\n", item.PrimaryRef, item.Kind, item.Status, item.PID, item.Sessions, formatBytes(item.BytesIn), formatBytes(item.BytesOut), formatBytes(item.NetworkBytesIn), formatBytes(item.NetworkBytesOut), formatRate(item.RateIn), formatRate(item.RateOut), item.ConnectedPeers, item.OpenConnections, path, address); err != nil {
			return err
		}
	}
	return w.Flush()
}

func formatRate(value float64) string {
	if value <= 0 {
		return "0B/s"
	}
	return formatBytes(int64(value)) + "/s"
}

func formatBytes(value int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	n := float64(value)
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%dB", value)
	}
	return fmt.Sprintf("%.1f%s", n, units[i])
}

func (a App) ps(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) > 0 {
		return errors.New("usage: tubo ps [--json]")
	}
	items, err := runtimeViews(ctx, state)
	if err != nil {
		return err
	}
	if f.bools["json"] {
		return json.NewEncoder(a.Out).Encode(map[string]any{"items": items})
	}
	return renderRuntimeTable(a.Out, items)
}

func (a App) top(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) > 0 {
		return errors.New("usage: tubo top [--interval 1s] [--once] [--json]")
	}
	interval := time.Second
	if raw := f.values["interval"]; raw != "" {
		interval, err = time.ParseDuration(raw)
		if err != nil || interval < 100*time.Millisecond {
			return errors.New("--interval must be a duration of at least 100ms")
		}
	}
	render := func(clear bool) error {
		items, err := runtimeViews(ctx, state)
		if err != nil {
			return err
		}
		if f.bools["json"] {
			return json.NewEncoder(a.Out).Encode(map[string]any{"observed_at": time.Now().UTC(), "items": items})
		}
		if clear {
			if _, err := fmt.Fprint(a.Out, "\033[H\033[2J"); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(a.Out, "Tubo runtime  %s  refresh %s  Ctrl-C exit\n\n", time.Now().Format(time.RFC3339), interval); err != nil {
			return err
		}
		return renderRuntimeTable(a.Out, items)
	}
	if err := render(false); err != nil {
		return err
	}
	if f.bools["once"] || f.bools["json"] {
		return nil
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := render(true); err != nil {
				return err
			}
		}
	}
}
func (a App) stop(ctx context.Context, state *store.Store, args []string) error {
	f, err := parse(args)
	if err != nil {
		return err
	}
	if len(f.args) != 1 {
		return errors.New("usage: tubo stop <resource>")
	}
	if err := supervisorAllowsCurrentSource(ctx, state, f.args[0]); err != nil {
		return err
	}
	run, err := state.Run(ctx, f.args[0])
	if err != nil {
		return err
	}
	identity, err := store.ProcessIdentity(run.PID)
	if err != nil {
		return fmt.Errorf("not_found: process for %s is not running", f.args[0])
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
	return a.emit(f.bools["json"], map[string]any{"ref": f.args[0], "status": "stopping"}, f.args[0]+" stopping")
}

func reserveForeground(ctx context.Context, state *store.Store, ref, kind string) error {
	if err := supervisorAllowsCurrentSource(ctx, state, ref); err != nil {
		return err
	}
	if existing, err := state.Run(ctx, ref); err == nil {
		if existing.Status == "ready" {
			identity, identityErr := store.ProcessIdentity(existing.PID)
			if identityErr == nil && identity == existing.ProcessIdentity {
				return fmt.Errorf("conflict: %s is already running", ref)
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
		return nil, errors.New("scope must be cluster/namespace")
	}
	value := &domain.Scope{Cluster: parts[0], Namespace: parts[1]}
	return value, value.Validate()
}
func (a App) help() {
	fmt.Fprint(a.Out, `tubo — private connectivity through composable pipes

Usage:
  tubo attach <target> --name <name> [-d]
  tubo connect <pipe-name|pipe/id> [--local host:port] [-d]
  tubo connect --token <share-invite> --local host:port [-d]
  tubo share pipe/<name> [--json]
  tubo revoke invite|session|pipe-access|pipe-publish <id>
  tubo service create <name> --target <url>
  tubo pipe create <name> --ingress <kind> --target <resource>
  tubo start|restart|stop|rm pipe/<name>
  tubo rm service/<name>
  tubo get pipes|services|scopes|processes|peers|overlays
  tubo watch pipes [--scope cluster/namespace] [--interval 2s] [--json]
  tubo inspect <pipe|service|scope|session|overlay|peer>/<name>
  tubo ps
  tubo top
  tubo logs <resource> [-f]
  tubo doctor
  tubo config validate|print
  tubo peers alias <peer-id> --name <label>
  tubo relay
  tubo scope create <cluster>/<namespace>
  tubo scope invite <cluster>/<namespace> --role <member|viewer|publisher|forwarder>
  tubo scope join --token <invite>
  tubo scope use <cluster>/<namespace>
  tubo authority start <cluster> --scope <cluster>/<namespace>
  tubo supervise install|status|uninstall <pipe/name|authority/cluster> [--scope <cluster>/<namespace>]

Global flags:
  --quiet  -v|-vv|-vvv  --log-level error|warn|info|debug|trace

Model:
  Service = local terminal; never advertised
  Pipe    = persistent directed connectivity resource
  Session = active traversal of one or more pipes
`)
}
