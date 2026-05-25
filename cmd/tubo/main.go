package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	attachauth "github.com/origama/tubo/internal/attachauth"
	catalog "github.com/origama/tubo/internal/catalog"
	cfgpkg "github.com/origama/tubo/internal/config"
	connectflow "github.com/origama/tubo/internal/connectflow"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	grantspkg "github.com/origama/tubo/internal/grants"
	launcher "github.com/origama/tubo/internal/launcher"
	"github.com/origama/tubo/internal/networkbundle"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/serviceidentity"
	"github.com/origama/tubo/internal/trust"
	iversion "github.com/origama/tubo/internal/version"
	"gopkg.in/yaml.v3"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/multiformats/go-multiaddr"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tubo:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		printTopLevelHelp()
		return nil
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		if len(args) > 1 {
			return printCommandHelp(args[1])
		}
		printTopLevelHelp()
		return nil
	}
	if len(args) > 1 && (args[1] == "--help" || args[1] == "-h") {
		return printCommandHelp(args[0])
	}
	if role, roleArgs, ok, err := resolveRuntimeRole(args); err != nil {
		return err
	} else if ok {
		if shouldHandleDetach(args[0]) {
			cleanArgs, detach := stripDetachArgs(roleArgs)
			roleArgs = cleanArgs
			if shouldHandleImplicitBootstrap(args[0]) {
				cleanArgs, err := maybeImplicitJoinOrInit(args[0], role, roleArgs)
				if err != nil {
					return err
				}
				roleArgs = cleanArgs
			}
			if detach {
				return detachRoleCommand(args[0], role, roleArgs)
			}
			return runRole(role, roleArgs)
		}
		if shouldHandleImplicitBootstrap(args[0]) {
			cleanArgs, err := maybeImplicitJoinOrInit(args[0], role, roleArgs)
			if err != nil {
				return err
			}
			roleArgs = cleanArgs
		}
		return runRole(role, roleArgs)
	}
	switch args[0] {
	case "join":
		return joinCmd(args[1:])
	case "connect":
		cleanArgs, noInit := stripNoInitArgs(args[1:])
		if err := ensureJoinedPublicNetwork("connect", noInit); err != nil {
			return err
		}
		return connectCmd(cleanArgs)
	case "ps":
		return psCmd(args[1:])
	case "get":
		cleanArgs, noInit := stripNoInitArgs(args[1:])
		if err := ensureJoinedPublicNetwork("get", noInit); err != nil {
			return err
		}
		return getCmd(cleanArgs)
	case "describe":
		cleanArgs, noInit := stripNoInitArgs(args[1:])
		if err := ensureJoinedPublicNetwork("describe", noInit); err != nil {
			return err
		}
		return describeCmd(cleanArgs)
	case "inspect":
		cleanArgs, noInit := stripNoInitArgs(args[1:])
		if err := ensureJoinedPublicNetwork("inspect", noInit); err != nil {
			return err
		}
		return inspectCmd(cleanArgs)
	case "watch":
		cleanArgs, noInit := stripNoInitArgs(args[1:])
		if err := ensureJoinedPublicNetwork("watch", noInit); err != nil {
			return err
		}
		return watchCmd(cleanArgs)
	case "use":
		return localUseCmd(args[1:])
	case "share":
		return localShareCmd(args[1:])
	case "revoke":
		return revokeCmd(args[1:])
	case "grants":
		return grantsCmd(args[1:])
	case "create":
		return localCreateCmd(args[1:])
	case "logs":
		return logsCmd(args[1:])
	case "stop":
		return stopCmd(args[1:])
	case "rm":
		return rmCmd(args[1:])
	case "keygen":
		return keygen(args[1:])
	case "id":
		return id(args[1:])
	case "config":
		return configCmd(args[1:])
	case "doctor":
		return doctor(args[1:])
	case "init":
		return initCmd(args[1:])
	case "version":
		return versionCmd(args[1:])
	default:
		return usage()
	}
}

func resolveRuntimeRole(args []string) (string, []string, bool, error) {
	if len(args) == 0 {
		return "", nil, false, nil
	}
	switch args[0] {
	case "relay":
		if len(args) >= 2 && args[1] == "run" {
			return "", nil, false, errors.New("legacy command `tubo relay run` removed; use `tubo relay`")
		}
		return "relay", args[1:], true, nil
	case "edge", "service", "bridge":
		return "", nil, false, fmt.Errorf("legacy command `tubo %s run` removed; use intent-based commands (`attach`, `connect`, `gateway`, `relay`, `join`)", args[0])
	case "gateway":
		return "edge", args[1:], true, nil
	case "attach":
		attachArgs, err := rewriteAttachArgs(args[1:])
		if err != nil {
			return "", nil, false, err
		}
		attachArgs, err = ensureAttachRuntimeDefaults(attachArgs)
		if err != nil {
			return "", nil, false, err
		}
		return "service", attachArgs, true, nil
	default:
		return "", nil, false, nil
	}
}

func ensureAttachRuntimeDefaults(args []string) ([]string, error) {
	out := append([]string{}, args...)
	if hasLongFlag(out, "--config") {
		return out, nil
	}
	if !hasLongFlag(out, "--seed") {
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		out = append(out, "--seed", "attach-"+hex.EncodeToString(buf))
	}
	if !hasLongFlag(out, "--p2p-listen") {
		out = append(out, "--p2p-listen", "/ip4/0.0.0.0/tcp/0")
	}
	return out, nil
}

func rewriteAttachArgs(args []string) ([]string, error) {
	cleanArgs, port, hasPort, err := consumeLongFlag(args, "--port")
	if err != nil {
		return nil, err
	}
	args = cleanArgs
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		if hasPort {
			return nil, errors.New("attach --port requires a positional service name")
		}
		return args, nil
	}
	first := args[0]
	if strings.HasPrefix(first, "service/") {
		first = strings.TrimPrefix(first, "service/")
	}
	if isHTTPURL(first) {
		if hasPort {
			return nil, errors.New("attach cannot combine a positional URL target with --port")
		}
		if hasLongFlag(args[1:], "--target") {
			return nil, errors.New("attach target provided both positionally and via --target")
		}
		return append([]string{"--target", first}, args[1:]...), nil
	}
	if !hasPort {
		return nil, errors.New("attach positional shorthand requires --port, or pass an explicit target URL")
	}
	if hasLongFlag(args[1:], "--name") {
		return nil, errors.New("attach service name provided both positionally and via --name")
	}
	target := "http://127.0.0.1:" + port
	return append([]string{"--target", target, "--name", first}, args[1:]...), nil
}

func hasLongFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}
	return false
}

func consumeLongFlag(args []string, name string) ([]string, string, bool, error) {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == name {
			if i+1 >= len(args) {
				return nil, "", false, fmt.Errorf("%s requires a value", name)
			}
			return append(out, args[i+2:]...), args[i+1], true, nil
		}
		if strings.HasPrefix(arg, name+"=") {
			return append(out, args[i+1:]...), strings.TrimPrefix(arg, name+"="), true, nil
		}
		out = append(out, arg)
	}
	return out, "", false, nil
}

func isHTTPURL(value string) bool {
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}

func shouldHandleImplicitBootstrap(command string) bool {
	switch command {
	case "attach", "gateway", "relay":
		return true
	default:
		return false
	}
}

func shouldHandleDetach(command string) bool {
	switch command {
	case "attach", "gateway", "relay":
		return true
	default:
		return false
	}
}

func stripNoInitArgs(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	noInit := false
	for _, arg := range args {
		if arg == "--no-init" {
			noInit = true
			continue
		}
		out = append(out, arg)
	}
	return out, noInit
}

func stripDetachArgs(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	detach := false
	for _, arg := range args {
		if arg == "-d" || arg == "--detach" {
			detach = true
			continue
		}
		out = append(out, arg)
	}
	return out, detach
}

func usage() error {
	return errors.New("usage: tubo <attach|connect|gateway|relay|join|get|describe|inspect|watch|use|share|revoke|create|ps> [flags]; run `tubo help` or `tubo help <command>` for details; bundle-url is supported by `tubo join`")
}

func printTopLevelHelp() {
	fmt.Println(`tubo — publish and connect private HTTP/WebSocket services over libp2p

Usage:
  tubo attach <url> --name <service> [-d]
  tubo attach <service> --port <port> [-d]
  tubo connect <service> [--local 127.0.0.1:PORT]
  tubo connect --token <share-invite> [--local 127.0.0.1:PORT]
  tubo get services
  tubo use overlay/public
  tubo create cluster/home
  tubo share cluster/home --permission member
  tubo share service/myapp --expires 1h
  tubo share revoke <share-invite>
  tubo revoke <invite|session|service-access|publish> <id-or-service>
  tubo relay [-d]
  tubo gateway [-d]
  tubo join [overlay/public|tubo-public]

Common flow:
  # Machine with a local app
  tubo attach http://127.0.0.1:8080 --name myapp -d

  # Another machine
  tubo connect --token <share-invite> --local 127.0.0.1:9888
  curl http://127.0.0.1:9888/

Discovery and process management:
  tubo get services
  tubo describe service/myapp
  tubo inspect service/myapp --json
  tubo watch services
  tubo use overlay/public
  tubo create cluster/home
  tubo share cluster/home --permission member
  tubo ps
  tubo logs process/attach-myapp
  tubo stop process/attach-myapp

Notes:
  - First run auto-joins the signed public network bundle.
  - Use --no-init to disable implicit join.
  - HTTP and WebSocket upgrade traffic are both tunneled.

Help:
  tubo help <command>
  tubo <command> --help`)
}

func printCommandHelp(command string) error {
	switch command {
	case "attach":
		fmt.Println(`Usage:
  tubo attach <url> --name <service> [-d]
  tubo attach <service> --port <port> [-d]

Publish a local HTTP/WebSocket service into the Tubo network.

Examples:
  tubo attach http://127.0.0.1:8080 --name piweb -d
  tubo attach piweb --port 8080 -d

Flags:
  --name <service>          service name to publish
  --port <port>             shorthand target: http://127.0.0.1:<port>
  --target <url>            explicit target URL
  --p2p-listen <multiaddr>  libp2p listen addr (default for attach: /ip4/0.0.0.0/tcp/0)
  --seed <seed>             stable PeerID seed; auto-generated when omitted
  --heartbeat-interval <d>  announcement interval, default 15s
  -d, --detach              run in background
  --no-init                 fail instead of auto-joining the public bundle`)
	case "connect":
		fmt.Println(`Usage:
  tubo connect <service> [--local 127.0.0.1:PORT]
  tubo connect --token <share-invite> [--local 127.0.0.1:PORT]

Open a local HTTP/WebSocket listener to a named service.

Examples:
  tubo connect piweb --local 127.0.0.1:9888
  tubo connect piweb

Flags:
  --local <host:port>       local listener; random 127.0.0.1 port when omitted
  --timeout <duration>      discovery timeout, default 20s
  --live                    skip remote cache and observe pubsub live
  --cached-only             only use local edge cache
  --json                    print JSON result
  --no-init                 fail instead of auto-joining the public bundle

Path selection:
  - usable direct addresses are tried first
  - loopback/unspecified direct addresses are skipped from remote clients
  - relayed addresses are used as fallback
  - hole punching is enabled when relay metadata is available
  - an initial relayed path may later upgrade to a direct libp2p connection`)
	case "get":
		fmt.Println(`Usage:
  tubo get services [--json]
  tubo get service/<name> [--json]
  tubo get overlays [--json]
  tubo get clusters [--json]
  tubo get namespaces [--json]
  tubo get processes [--json]

Inspect local processes, local config resources, or services announced in the swarm.`)
	case "describe":
		fmt.Println(`Usage:
  tubo describe service/<name>
  tubo describe process/<name>
  tubo describe overlay/<name>
  tubo describe cluster/<name>
  tubo describe namespace/<name>

Show local config metadata or discovered resource details.`)
	case "relay":
		fmt.Println(`Usage:
  tubo relay [-d]

Run a public relay/bootstrap/discovery-cache node.

Flags:
  --listen <multiaddr>      default /ip4/0.0.0.0/tcp/4001
  --public-addr <multiaddr> advertised public relay address
  -d, --detach              run in background
  --no-init                 fail instead of auto-joining the public bundle`)
	case "gateway":
		fmt.Println(`Usage:
  tubo gateway [--listen :8443] [-d]

Run an HTTP ingress gateway that routes by discovered services.`)
	case "join":
		fmt.Println(`Usage:
  tubo join [overlay/public|tubo-public]
  tubo join overlay/manual --relay <multiaddr> --swarm-key <path>
  tubo join --relay <multiaddr> --swarm-key <path>
  tubo join --bundle-url <url>
  tubo join cluster/<name> --token <cluster-invite>
  tubo join <cluster-invite>

Install local overlay config, swarm key, or cluster membership. Does not start processes.`)
	case "use":
		fmt.Println(`Usage:
  tubo use overlay/<name>
  tubo use cluster/<name>
  tubo use namespace/<name>

Select a local overlay/cluster/namespace context in the config file.`)
	case "share":
		fmt.Println(`Usage:
  tubo share cluster/<name> [--permission member] [--namespace <name>] [--expires <duration>]
  tubo share service/<name> [--cluster <name>] [--namespace <name>] [--expires <duration>]
  tubo share revoke <share-invite>

Create a copyable cluster invitation or service-scoped connect token from local authority material.`)
	case "revoke":
		fmt.Println(`Usage:
  tubo revoke invite <invite-id-or-token> [--reason <text>]
  tubo revoke session <session-id> [--reason <text>]
  tubo revoke service-access <service-id-or-service/name> [--reason <text>]
  tubo revoke publish <service-id-or-service/name> [--reason <text>]

Record issuer-side revocation state for invite redemption, connect refresh, service access, or publish authorization.`)
	case "create":
		fmt.Println(`Usage:
  tubo create cluster/<name>
  tubo create namespace/<name>
  tubo create service/<name>

Create local clusters, namespaces, and namespace-scoped service identities in the current config.`)
	case "watch", "inspect", "ps", "logs", "stop", "rm", "version", "doctor", "config", "keygen", "id", "init":
		fmt.Printf("Run `tubo help` for common usage. Command %q keeps its existing flags.\n", command)
	default:
		return fmt.Errorf("unknown help topic %q", command)
	}
	return nil
}
func roleFlags(role string, args []string) (string, cfgpkg.Config, error) {
	fs := flag.NewFlagSet(role, flag.ContinueOnError)
	path := fs.String("config", "", "")
	non := fs.Bool("non-interactive", false, "")
	f := cfgpkg.Config{Role: role}
	common := func() {
		fs.StringVar(&f.Node.Seed, "seed", "", "")
		fs.StringVar(&f.Node.P2PListen, "p2p-listen", "", "")
		fs.Var(csvFlag{&f.Network.BootstrapPeers}, "bootstrap", "")
		fs.Var(csvFlag{&f.Network.RelayPeers}, "relay", "")
		fs.StringVar(&f.Network.PrivateKeyFile, "swarm-key", "", "")
	}
	switch role {
	case "relay":
		common()
		fs.StringVar(&f.Node.P2PListen, "listen", "", "")
		fs.StringVar(&f.Relay.PublicAddr, "public-addr", "", "")
	case "edge":
		common()
		fs.StringVar(&f.Edge.Listen, "listen", "", "")
		fs.StringVar(&f.Edge.AdminListen, "admin-listen", "", "")
		fs.Var(durationFlag{&f.Edge.DirectStreamTimeout}, "direct-stream-timeout", "")
	case "service":
		common()
		fs.StringVar(&f.Service.Name, "name", "", "")
		fs.StringVar(&f.Service.Target, "target", "", "")
		fs.Var(durationFlag{&f.HeartbeatInterval}, "heartbeat-interval", "")
	case "bridge":
		fs.StringVar(&f.Bridge.Listen, "listen", "", "")
		fs.StringVar(&f.Bridge.ServiceAddr, "service-addr", "", "")
		fs.StringVar(&f.Bridge.ServiceSeed, "service-seed", "", "")
		fs.StringVar(&f.Bridge.ServiceP2PListen, "service-p2p-listen", "", "")
		fs.StringVar(&f.Node.P2PListen, "p2p-listen", "", "")
		fs.StringVar(&f.Node.Seed, "seed", "", "")
		fs.StringVar(&f.Network.PrivateKeyFile, "swarm-key", "", "")
	}
	if err := fs.Parse(args); err != nil {
		return "", f, err
	}
	_ = *non
	return *path, f, nil
}

func resolveRoleConfig(role string, args []string) (cfgpkg.Config, string, error) {
	path, flags, err := roleFlags(role, args)
	if err != nil {
		return cfgpkg.Config{}, "", err
	}
	effectivePath := path
	if effectivePath == "" {
		defaultPath := defaultTuboConfigPath()
		if _, err := os.Stat(defaultPath); err == nil {
			effectivePath = defaultPath
		}
	}
	c, err := cfgpkg.Effective(role, effectivePath, os.Getenv, flags)
	if err != nil {
		return cfgpkg.Config{}, "", err
	}
	if err := cfgpkg.Validate(c); err != nil {
		return cfgpkg.Config{}, "", err
	}
	return c, effectivePath, nil
}

func runRole(role string, args []string) error {
	c, configPath, err := resolveRoleConfig(role, args)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return launcher.Run(ctx, newRuntimeLauncher(), role, configPath, c)
}

func startAttachPublishLeaseRenewal(ctx context.Context, configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string) {
	if strings.TrimSpace(svc.ServicePublishLeaseFile) == "" {
		return
	}
	resolver := newAttachAuthResolver()
	go func() {
		backoff := 5 * time.Second
		for {
			lease, err := readPublishLeaseFile(svc.ServicePublishLeaseFile)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("publish lease renewal: read failed: %v", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				continue
			}
			renewBefore := attachPublishLeaseRenewBefore(time.Until(lease.ExpiresAt.UTC()))
			wait := time.Until(lease.ExpiresAt.UTC().Add(-renewBefore))
			if wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			result, err := resolver.Renew(ctx, attachauth.RenewRequest{ConfigPath: configPath, Config: cfg, Service: svc, ServicePeerID: servicePeerID})
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("publish lease renewal failed: %v", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				continue
			}
			cfg = result.Config
			svc = result.Service
			if result.ServiceShareToken != "" {
				log.Printf("share invite refreshed for service %q", cfg.Service.Name)
			}
		}
	}()
}

type joinResult struct {
	NetworkName    string   `json:"network_name,omitempty"`
	NetworkID      string   `json:"network_id,omitempty"`
	KeyID          string   `json:"key_id,omitempty"`
	ConfigPath     string   `json:"config_path"`
	SwarmKeyPath   string   `json:"swarm_key_path"`
	RelayPeers     []string `json:"relay_peers"`
	BootstrapPeers []string `json:"bootstrap_peers"`
	Checked        bool     `json:"checked"`
}

var (
	joinDefaultNetworkName      = trust.DefaultPublicNetworkName
	joinDefaultPublicBundleURL  = trust.DefaultPublicNetworkBundleURL
	joinTrustedBundleSigningKey = trust.BundleSigningKeys
)

func effectiveDefaultPublicBundleURL() string {
	if override := strings.TrimSpace(os.Getenv("TUBO_DEFAULT_PUBLIC_BUNDLE_URL")); override != "" {
		return override
	}
	return joinDefaultPublicBundleURL
}

func joinCmd(args []string) error {
	if len(args) > 0 && (strings.HasPrefix(args[0], "cluster/") || isClusterInviteToken(args[0])) {
		return localJoinClusterInviteCmd(args)
	}
	overlayName := ""
	if len(args) > 0 && strings.HasPrefix(args[0], "overlay/") {
		overlayName = strings.TrimPrefix(args[0], "overlay/")
		args = args[1:]
	}
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	var relayPeers []string
	fs.Var(csvFlag{&relayPeers}, "relay", "")
	swarmKeyPath := fs.String("swarm-key", "", "")
	swarmKeyB64 := fs.String("swarm-key-b64", "", "")
	bundleURL := fs.String("bundle-url", "", "")
	configDir := fs.String("config-dir", defaultTuboConfigDir(), "")
	force := fs.Bool("force", false, "")
	jsonOut := fs.Bool("json", false, "")
	check := fs.Bool("check", false, "")
	token := fs.String("token", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	relayPeers = uniqueStrings(relayPeers)
	manualMode := len(relayPeers) > 0 || *swarmKeyPath != "" || *swarmKeyB64 != ""
	if overlayName == "manual" {
		if !manualMode {
			return errors.New("join overlay/manual requires --relay <multiaddr> and exactly one swarm key source")
		}
		manualMode = true
	}
	if overlayName != "" && overlayName != "public" && overlayName != "manual" {
		return fmt.Errorf("unknown overlay %q; use overlay/public or overlay/manual", overlayName)
	}
	_, _, inviteMode, inviteErr := parseClusterInviteJoin(fs.Args(), *token)
	if inviteErr != nil {
		return inviteErr
	}
	if inviteMode {
		if manualMode || *bundleURL != "" {
			return errors.New("cluster invite join cannot be combined with bundle/manual join flags")
		}
		return localJoinClusterInviteCmd(args)
	}
	networkName := ""
	if fs.NArg() > 1 {
		return errors.New("usage: tubo join [<network-name>] [--bundle-url <url>] [flags]")
	}
	if fs.NArg() == 1 {
		networkName = fs.Arg(0)
	}
	bundleMode := networkName != "" || *bundleURL != ""
	if manualMode && bundleMode {
		return errors.New("join manual flags (--relay/--swarm-key) cannot be combined with bundle mode")
	}
	if manualMode {
		result, err := joinManualMode(relayPeers, *swarmKeyPath, *swarmKeyB64, *configDir, *force, *check)
		if err != nil {
			return err
		}
		if *jsonOut {
			return printJSON(result)
		}
		printJoinResult(joinTitleFor(false, overlayName), result)
		return nil
	}
	if overlayName == "public" {
		networkName = joinDefaultNetworkName
	}
	if networkName == "" {
		networkName = joinDefaultNetworkName
	}
	resolvedBundleURL := *bundleURL
	if resolvedBundleURL == "" {
		if networkName != joinDefaultNetworkName {
			return fmt.Errorf("unknown network %q; use --bundle-url for custom networks", networkName)
		}
		resolvedBundleURL = effectiveDefaultPublicBundleURL()
	}
	result, err := joinBundleMode(resolvedBundleURL, *configDir, *force)
	if err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(result)
	}
	printJoinResult(joinTitleFor(true, overlayName), result)
	return nil
}

func joinManualMode(relayPeers []string, swarmKeyPath, swarmKeyB64, configDir string, force, check bool) (joinResult, error) {
	if len(relayPeers) == 0 {
		return joinResult{}, errors.New("join requires at least one --relay <multiaddr>")
	}
	if (swarmKeyPath == "" && swarmKeyB64 == "") || (swarmKeyPath != "" && swarmKeyB64 != "") {
		return joinResult{}, errors.New("join requires exactly one of --swarm-key or --swarm-key-b64")
	}
	for _, relayPeer := range relayPeers {
		if _, err := multiaddr.NewMultiaddr(relayPeer); err != nil {
			return joinResult{}, fmt.Errorf("join relay %q: %w", relayPeer, err)
		}
	}
	keyData, err := loadJoinSwarmKey(swarmKeyPath, swarmKeyB64)
	if err != nil {
		return joinResult{}, err
	}
	if err := validateSwarmKeyData(keyData); err != nil {
		return joinResult{}, err
	}
	if check {
		if err := checkJoinRelayPeers(relayPeers); err != nil {
			return joinResult{}, err
		}
	}
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return joinResult{}, err
	}
	configPath := filepath.Join(configDir, "config.yaml")
	installedKeyPath := filepath.Join(configDir, "swarm.key")
	if !force {
		for _, path := range []string{configPath, installedKeyPath} {
			if _, err := os.Stat(path); err == nil {
				return joinResult{}, fmt.Errorf("%s exists (use --force)", path)
			} else if !errors.Is(err, os.ErrNotExist) {
				return joinResult{}, err
			}
		}
	}
	existing, err := cfgpkg.LoadFile(configPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return joinResult{}, err
	}
	joined := cfgpkg.Merge(existing, cfgpkg.Config{
		CurrentOverlay:   "manual",
		CurrentCluster:   "home",
		CurrentNamespace: "default",
		Overlays: map[string]cfgpkg.Overlay{
			"manual": {
				Relays:         append([]string(nil), relayPeers...),
				BootstrapPeers: append([]string(nil), relayPeers...),
				SwarmKeyFile:   installedKeyPath,
			},
		},
		Clusters: map[string]cfgpkg.Cluster{
			"home": {
				Namespaces: map[string]cfgpkg.Namespace{
					"default": {},
				},
			},
		},
		Network: cfgpkg.Network{
			PrivateKeyFile: installedKeyPath,
			BootstrapPeers: append([]string(nil), relayPeers...),
			RelayPeers:     append([]string(nil), relayPeers...),
			Autorelay:      true,
			HolePunching:   true,
		},
	})
	joined.Network.PrivateKeyB64 = ""
	b, err := yaml.Marshal(joined)
	if err != nil {
		return joinResult{}, err
	}
	if err := os.WriteFile(installedKeyPath, keyData, 0600); err != nil {
		return joinResult{}, err
	}
	if err := os.WriteFile(configPath, b, 0600); err != nil {
		return joinResult{}, err
	}
	return joinResult{
		NetworkName:    "manual",
		ConfigPath:     configPath,
		SwarmKeyPath:   installedKeyPath,
		RelayPeers:     relayPeers,
		BootstrapPeers: relayPeers,
		Checked:        check,
	}, nil
}

func joinBundleMode(bundleURL, configDir string, force bool) (joinResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	bundleBytes, err := networkbundle.Fetch(ctx, bundleURL)
	if err != nil {
		return joinResult{}, err
	}
	bundle, err := networkbundle.Parse(bundleBytes)
	if err != nil {
		return joinResult{}, err
	}
	payloadBytes, keyID, err := networkbundle.Verify(bundle, joinTrustedBundleSigningKey)
	if err != nil {
		return joinResult{}, err
	}
	payload, err := networkbundle.DecodePayload(payloadBytes)
	if err != nil {
		return joinResult{}, err
	}
	installed, err := networkbundle.Install(payload, networkbundle.InstallOptions{ConfigDir: configDir, Force: force})
	if err != nil {
		return joinResult{}, err
	}
	return joinResult{
		NetworkName:    installed.NetworkName,
		NetworkID:      installed.NetworkID,
		KeyID:          keyID,
		ConfigPath:     installed.ConfigPath,
		SwarmKeyPath:   installed.SwarmKeyPath,
		RelayPeers:     installed.RelayPeers,
		BootstrapPeers: installed.BootstrapPeers,
	}, nil
}

func joinTitleFor(bundle bool, overlayName string) string {
	if bundle {
		if overlayName == "public" || overlayName == "" {
			return "joined public overlay"
		}
		return "joined overlay bundle"
	}
	if overlayName == "manual" || overlayName == "" {
		return "joined manual overlay"
	}
	return "joined overlay"
}

func printJoinResult(title string, result joinResult) {
	fmt.Println(title)
	if result.NetworkName != "" {
		fmt.Printf("network: %s\n", result.NetworkName)
	}
	if result.NetworkID != "" {
		fmt.Printf("network id: %s\n", result.NetworkID)
	}
	if result.KeyID != "" {
		fmt.Printf("signature key: %s\n", result.KeyID)
	}
	fmt.Printf("config: %s\n", result.ConfigPath)
	for i, relayPeer := range result.RelayPeers {
		if i == 0 {
			fmt.Printf("relay: %s\n", relayPeer)
		} else {
			fmt.Printf("relay[%d]: %s\n", i+1, relayPeer)
		}
	}
	fmt.Printf("swarm key installed: %s\n", result.SwarmKeyPath)
	if result.Checked {
		fmt.Println("relay check: ok")
	}
	fmt.Println()
	fmt.Println("next:")
	fmt.Println("  tubo get services")
	fmt.Println("  tubo attach http://127.0.0.1:1234 --name my-service")
	fmt.Println("  tubo connect lmstudio")
}

func loadJoinSwarmKey(path, b64 string) ([]byte, error) {
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return data, nil
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode --swarm-key-b64: %w", err)
	}
	return data, nil
}

func validateSwarmKeyData(data []byte) error {
	body := string(data)
	if !strings.Contains(body, "/key/swarm/psk/1.0.0/") || !strings.Contains(body, "/base16/") {
		return errors.New("invalid swarm key format")
	}
	return nil
}

func checkJoinRelayPeers(relayPeers []string) error {
	for _, relayPeer := range relayPeers {
		if err := checkJoinRelayPeer(relayPeer); err != nil {
			return fmt.Errorf("relay check failed for %s: %w", relayPeer, err)
		}
	}
	return nil
}

func checkJoinRelayPeer(relayPeer string) error {
	maddr, err := multiaddr.NewMultiaddr(relayPeer)
	if err != nil {
		return err
	}
	host, err := maddr.ValueForProtocol(multiaddr.P_IP4)
	if err != nil {
		host, err = maddr.ValueForProtocol(multiaddr.P_IP6)
		if err != nil {
			return errors.New("--check currently supports /ip4|ip6/.../tcp/... relay addresses")
		}
	}
	port, err := maddr.ValueForProtocol(multiaddr.P_TCP)
	if err != nil {
		return errors.New("--check currently supports /ip4|ip6/.../tcp/... relay addresses")
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 3*time.Second)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

func defaultTuboConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "tubo")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".tubo")
	}
	return filepath.Join(home, ".config", "tubo")
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func defaultTuboConfigPath() string {
	return filepath.Join(defaultTuboConfigDir(), "config.yaml")
}

func defaultTuboDataDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "tubo")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".tubo-data")
	}
	return filepath.Join(home, ".local", "share", "tubo")
}

func detachRoleCommand(commandName, role string, args []string) error {
	cfg, configPath, err := resolveRoleConfig(role, args)
	if err != nil {
		return err
	}
	serviceID := ""
	if commandName == "attach" {
		authz, err := resolveAttachAuthorization(configPath, cfg)
		if err != nil {
			return err
		}
		cfg = authz.Config
		serviceID = authz.Service.ServiceID
		printAttachShareHint(cfg, authz)
	}
	spec, err := buildDetachedSpec(commandName, cfg, args)
	if err != nil {
		return err
	}
	if serviceID != "" {
		spec.State.ServiceID = serviceID
	}
	state, err := startDetachedProcess(spec)
	if err != nil {
		return err
	}
	printDetachedSummary(commandName, state)
	return nil
}

func maybeImplicitJoinOrInit(command, role string, args []string) ([]string, error) {
	cleanArgs, noInit := stripNoInitArgs(args)
	switch command {
	case "attach", "gateway", "relay":
		if err := ensureJoinedPublicNetwork(command, noInit); err != nil {
			return nil, err
		}
	}
	return cleanArgs, nil
}

func ensureJoinedPublicNetwork(command string, noInit bool) error {
	configPath := defaultTuboConfigPath()
	if _, err := os.Stat(configPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if noInit {
		return fmt.Errorf("config not found at %s (--no-init set)", configPath)
	}
	if strings.EqualFold(os.Getenv("CI"), "true") {
		return fmt.Errorf("config not found at %s; implicit public join disabled in CI (use `tubo join`, `tubo init`, or pass --config)", configPath)
	}
	fmt.Println("No Tubo network configured.")
	fmt.Printf("Fetching default network bundle: %s\n", joinDefaultNetworkName)
	result, err := joinBundleMode(effectiveDefaultPublicBundleURL(), defaultTuboConfigDir(), false)
	if err != nil {
		return fmt.Errorf("implicit public join for %s failed: %w", command, err)
	}
	fmt.Printf("Signature verified: %s\n", result.KeyID)
	fmt.Printf("Joined network: %s\n", result.NetworkName)
	fmt.Println()
	return nil
}

func maybeImplicitInit(role string, args []string, noInit bool) error {
	configPath := defaultTuboConfigPath()
	if _, err := os.Stat(configPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if noInit {
		return fmt.Errorf("config not found at %s (--no-init set)", configPath)
	}
	if strings.EqualFold(os.Getenv("CI"), "true") {
		return fmt.Errorf("config not found at %s; implicit init disabled in CI (use `tubo join`, `tubo init`, or pass --config)", configPath)
	}
	_, flags, err := roleFlags(role, args)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(defaultTuboConfigDir(), 0700); err != nil {
		return err
	}
	keyPath := filepath.Join(defaultTuboConfigDir(), "swarm.key")
	createdKey := false
	if _, err := os.Stat(keyPath); errors.Is(err, os.ErrNotExist) {
		data, err := newSwarmKeyData()
		if err != nil {
			return err
		}
		if err := os.WriteFile(keyPath, data, 0600); err != nil {
			return err
		}
		createdKey = true
	} else if err != nil {
		return err
	}
	cfg := cfgpkg.Config{Network: cfgpkg.Network{
		PrivateKeyFile: keyPath,
		BootstrapPeers: append([]string(nil), flags.Network.BootstrapPeers...),
		RelayPeers:     append([]string(nil), flags.Network.RelayPeers...),
		Autorelay:      true,
		HolePunching:   true,
	}}
	if err := cfgpkg.WriteFile(configPath, cfg, false); err != nil {
		if !errors.Is(err, os.ErrExist) && !strings.Contains(err.Error(), "exists") {
			return err
		}
	} else {
		fmt.Println("no tubo config found")
		fmt.Printf("created local config: %s\n", configPath)
		if createdKey {
			fmt.Printf("created private swarm key: %s\n", keyPath)
		}
		fmt.Println()
	}
	return nil
}

func printDetachedSummary(commandName string, state detachedProcessState) {
	switch commandName {
	case "attach":
		fmt.Printf("attached service %q\n", state.Service)
		if state.ServiceID != "" {
			fmt.Printf("service id: %s\n", state.ServiceID)
		}
	case "gateway":
		fmt.Println("gateway running")
	case "relay":
		fmt.Println("relay running")
	default:
		fmt.Printf("started %s\n", commandName)
	}
	fmt.Printf("id: %s\n", state.ID)
	if state.Local != "" {
		fmt.Printf("local: %s\n", state.Local)
	}
	fmt.Printf("pid: %d\n", state.PID)
	fmt.Printf("logs: %s\n", state.LogFile)
}

func printProcessesTable(items []processView) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCOMMAND\tSERVICE ID\tSCOPE\tSTATUS\tPID\tLOCAL\tTARGET")
	for _, item := range items {
		local := item.Local
		if local == "" {
			local = "-"
		}
		target := item.Target
		if target == "" {
			target = "-"
		}
		scope := "-"
		if item.Cluster != "" || item.Namespace != "" {
			scope = item.Cluster + "/" + item.Namespace
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", item.Name, item.Command, displayServiceID(item.ServiceID), scope, item.Status, item.PID, local, target)
	}
	_ = w.Flush()
}

func printProcessDescription(state detachedProcessState, status string) {
	fmt.Printf("Name: %s\n", state.Name)
	fmt.Printf("Kind: %s\n", state.Kind)
	fmt.Printf("Command: %s\n", state.Command)
	fmt.Printf("Status: %s\n", status)
	fmt.Printf("PID: %d\n", state.PID)
	if state.Service != "" {
		fmt.Printf("Service: %s\n", state.Service)
	}
	if state.ServiceID != "" {
		fmt.Printf("Service ID: %s\n", state.ServiceID)
	}
	if state.Cluster != "" || state.Namespace != "" {
		fmt.Printf("Scope: %s/%s\n", state.Cluster, state.Namespace)
	}
	if state.Local != "" {
		fmt.Printf("Local: %s\n", state.Local)
	}
	if state.Target != "" {
		fmt.Printf("Target: %s\n", state.Target)
	}
	fmt.Printf("Log file: %s\n", state.LogFile)
	fmt.Printf("State file: %s\n", state.StateFile)
	fmt.Printf("PID file: %s\n", state.PIDFile)
	if state.StatusURL != "" {
		fmt.Printf("Status URL: %s\n", state.StatusURL)
	}
}

func logsCmd(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	follow := fs.Bool("f", false, "")
	fs.BoolVar(follow, "follow", false, "")
	tail := fs.Int("tail", 200, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: tubo logs [-f|--follow] [--tail N] <process/name>")
	}
	state, _, err := loadProcessState(fs.Arg(0))
	if err != nil {
		return err
	}
	if err := printLogTail(state.LogFile, *tail); err != nil {
		return err
	}
	if !*follow {
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return followLogFile(ctx, state.LogFile)
}

func stopCmd(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	force := fs.Bool("force", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: tubo stop [--force] <process/name>")
	}
	state, err := stopProcess(fs.Arg(0), *force)
	if err != nil {
		return err
	}
	fmt.Printf("stopped %s\n", state.ID)
	return nil
}

func rmCmd(args []string) error {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	stale := fs.Bool("stale", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*stale {
		return errors.New("usage: tubo rm --stale")
	}
	removed, err := removeStaleProcesses()
	if err != nil {
		return err
	}
	fmt.Printf("removed %d stale process artifacts\n", removed)
	return nil
}

type serviceResource struct {
	Kind             string                          `json:"kind"`
	Cluster          string                          `json:"cluster,omitempty"`
	Namespace        string                          `json:"namespace,omitempty"`
	Name             string                          `json:"name"`
	ServiceID        string                          `json:"service_id,omitempty"`
	ServicePublicKey string                          `json:"service_public_key,omitempty"`
	ConnectPolicy    string                          `json:"connect_policy,omitempty"`
	GrantService     *grantspkg.GrantServiceEndpoint `json:"grant_service,omitempty"`
	PeerID           string                          `json:"peer_id"`
	Addresses        []string                        `json:"addresses"`
	DirectAddresses  []string                        `json:"direct_addresses"`
	RelayedAddresses []string                        `json:"relayed_addresses"`
	Status           string                          `json:"status"`
	Path             string                          `json:"path"`
	TTLSeconds       int64                           `json:"ttl_seconds"`
	ExpiresInSeconds int64                           `json:"expires_in_seconds"`
	Capabilities     []string                        `json:"capabilities"`
	RegisteredAt     string                          `json:"registered_at"`
}

type discoveryLookupResult struct {
	Services []serviceResource        `json:"services"`
	Messages []string                 `json:"messages"`
	Mode     string                   `json:"mode"`
	Scope    *serviceScope            `json:"scope,omitempty"`
	Metadata *discoveryquery.Metadata `json:"metadata,omitempty"`
}

type serviceWatchEvent struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	PeerID string `json:"peer_id"`
	Path   string `json:"path"`
}

type servicesAdminResponse struct {
	Count int               `json:"count"`
	Items []serviceResource `json:"items"`
}

const defaultDiscoveryTimeout = 20 * time.Second

func connectCmd(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	local := fs.String("local", "", "")
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	timeout := fs.Duration("timeout", defaultDiscoveryTimeout, "")
	jsonOut := fs.Bool("json", false, "")
	cachedOnly := fs.Bool("cached-only", false, "")
	live := fs.Bool("live", false, "")
	token := fs.String("token", "", "")
	cluster := fs.String("cluster", "", "")
	namespace := fs.String("namespace", "", "")
	namespaceShort := fs.String("n", "", "")
	serviceRef := ""
	parseArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		serviceRef = args[0]
		parseArgs = args[1:]
	}
	if err := fs.Parse(parseArgs); err != nil {
		return err
	}
	if *namespace == "" {
		*namespace = *namespaceShort
	}
	if serviceRef == "" {
		switch fs.NArg() {
		case 0:
			if strings.TrimSpace(*token) == "" {
				return errors.New("usage: tubo connect [--token <share-invite>] <service-name> [--local host:port] [flags]")
			}
		case 1:
			serviceRef = fs.Arg(0)
		default:
			return errors.New("usage: tubo connect [--token <share-invite>] <service-name> [--local host:port] [flags]")
		}
	} else if fs.NArg() != 0 {
		return errors.New("usage: tubo connect [--token <share-invite>] <service-name> [--local host:port] [flags]")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := connectflow.Resolve(ctx, newConnectWorkflow(), connectflow.Request{ConfigPath: *configPath, ServiceRef: serviceRef, Token: *token, Cluster: *cluster, Namespace: *namespace, Local: *local, Timeout: *timeout, CachedOnly: *cachedOnly, Live: *live})
	if err != nil {
		return err
	}
	output := fromConnectWorkflowResult(result)
	if *jsonOut {
		if err := printJSON(output); err != nil {
			return err
		}
	} else {
		printMessages(result.Messages)
		fmt.Printf("connected to service %q\n", result.ServiceName)
		if result.ServiceID != "" {
			fmt.Printf("service id: %s\n", result.ServiceID)
		}
		fmt.Printf("local: %s\n", result.LocalURL)
		fmt.Printf("path: %s\n", result.Path)
		if result.Direct != "" {
			fmt.Printf("direct: %s\n", result.Direct)
		}
		if result.Relay != "" {
			fmt.Printf("relay: %s\n", result.Relay)
		}
		fmt.Println("press Ctrl+C to stop")
	}
	return result.App.Start(ctx)
}

func psCmd(args []string) error {
	fs := flag.NewFlagSet("ps", flag.ContinueOnError)
	all := fs.Bool("all", false, "")
	jsonOut := fs.Bool("json", false, "")
	kind := fs.String("kind", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, err := listProcessViews(*all)
	if err != nil {
		return err
	}
	if *kind != "" {
		filtered := make([]processView, 0, len(items))
		for _, item := range items {
			if item.Command == *kind {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	if *jsonOut {
		return printJSON(struct {
			Count int           `json:"count"`
			Items []processView `json:"items"`
		}{Count: len(items), Items: items})
	}
	printProcessesTable(items)
	return nil
}

func getCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tubo get <services|service/name|overlays|clusters|namespaces|processes> [flags]")
	}
	resource := args[0]
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	timeout := fs.Duration("timeout", defaultDiscoveryTimeout, "")
	jsonOut := fs.Bool("json", false, "")
	cachedOnly := fs.Bool("cached-only", false, "")
	live := fs.Bool("live", false, "")
	cluster := fs.String("cluster", "", "")
	namespace := fs.String("namespace", "", "")
	namespaceShort := fs.String("n", "", "")
	allNamespaces := fs.Bool("all-namespaces", false, "")
	allNamespacesShort := fs.Bool("A", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *namespace == "" {
		*namespace = *namespaceShort
	}
	useAllNamespaces := *allNamespaces || *allNamespacesShort
	switch {
	case resource == "processes":
		items, err := listProcessViews(false)
		if err != nil {
			return err
		}
		if *jsonOut {
			return printJSON(struct {
				Count int           `json:"count"`
				Items []processView `json:"items"`
			}{Count: len(items), Items: items})
		}
		printProcessesTable(items)
		return nil
	case resource == "overlays" || resource == "clusters" || resource == "namespaces":
		return localGetResource(resource, *configPath, *jsonOut)
	}
	cfg, err := catalog.LoadDiscoveryConfig(*configPath)
	if err != nil {
		return err
	}
	switch {
	case resource == "services":
		scopes, err := resolveAuthorizedServiceScopes(cfg, *cluster, *namespace, useAllNamespaces)
		if err != nil {
			return err
		}
		if useAllNamespaces {
			if *cachedOnly {
				return errors.New("--cached-only is not supported with `get services -A`")
			}
			result, err := discoverServicesAcrossScopes(cfg, *timeout, scopes)
			if err != nil {
				return err
			}
			if *jsonOut {
				return printJSON(struct {
					Mode     string                   `json:"mode"`
					Messages []string                 `json:"messages"`
					Scope    *serviceScope            `json:"scope,omitempty"`
					Metadata *discoveryquery.Metadata `json:"metadata,omitempty"`
					Count    int                      `json:"count"`
					Items    []serviceResource        `json:"items"`
				}{Mode: result.Mode, Messages: result.Messages, Scope: result.Scope, Metadata: result.Metadata, Count: len(result.Services), Items: result.Services})
			}
			printMessages(result.Messages)
			printServicesTable(result.Services)
			return nil
		}
		scope := scopes[0]
		catalogResult, err := catalog.DiscoverServicesWithConfig(cfg, *timeout, *cachedOnly, *live, toCatalogScope(scope))
		if err != nil {
			return err
		}
		result := fromCatalogLookupResult(catalogResult)
		if *jsonOut {
			return printJSON(struct {
				Mode     string                   `json:"mode"`
				Messages []string                 `json:"messages"`
				Scope    *serviceScope            `json:"scope,omitempty"`
				Metadata *discoveryquery.Metadata `json:"metadata,omitempty"`
				Count    int                      `json:"count"`
				Items    []serviceResource        `json:"items"`
			}{Mode: result.Mode, Messages: result.Messages, Scope: result.Scope, Metadata: result.Metadata, Count: len(result.Services), Items: result.Services})
		}
		printMessages(result.Messages)
		printServicesTable(result.Services)
		return nil
	case strings.HasPrefix(resource, "service/"):
		if useAllNamespaces {
			return errors.New("--all-namespaces is only supported with `get services`")
		}
		scopes, err := resolveAuthorizedServiceScopes(cfg, *cluster, *namespace, false)
		if err != nil {
			return err
		}
		name, err := parseServiceRef(resource)
		if err != nil {
			return err
		}
		serviceID := ""
		if isServiceID(name) {
			serviceID = name
			name = ""
		}
		catalogResult, catalogService, err := catalog.DiscoverServiceExactWithConfig(cfg, *timeout, *cachedOnly, *live, toCatalogScope(scopes[0]), name, serviceID)
		if err != nil {
			return err
		}
		result, service := fromCatalogLookupResult(catalogResult), fromCatalogService(catalogService)
		if *jsonOut {
			return printJSON(struct {
				Mode     string                   `json:"mode"`
				Messages []string                 `json:"messages"`
				Scope    *serviceScope            `json:"scope,omitempty"`
				Metadata *discoveryquery.Metadata `json:"metadata,omitempty"`
				Item     serviceResource          `json:"item"`
			}{Mode: result.Mode, Messages: result.Messages, Scope: result.Scope, Metadata: result.Metadata, Item: service})
		}
		printMessages(result.Messages)
		printServicesTable([]serviceResource{service})
		return nil
	default:
		return fmt.Errorf("unsupported get resource %q", resource)
	}
}

func describeCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tubo describe <service/name|process/name|overlay/name|cluster/name|namespace/name> [flags]")
	}
	resource := args[0]
	if strings.HasPrefix(resource, "overlay/") || strings.HasPrefix(resource, "cluster/") || strings.HasPrefix(resource, "namespace/") {
		fs := flag.NewFlagSet("describe", flag.ContinueOnError)
		configPath := fs.String("config", defaultTuboConfigPath(), "")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return localDescribeResource(resource, *configPath)
	}
	if strings.HasPrefix(resource, "process/") || !strings.Contains(resource, "/") {
		state, status, err := loadProcessState(resource)
		if err != nil {
			return err
		}
		printProcessDescription(state, status)
		return nil
	}
	if !strings.HasPrefix(resource, "service/") {
		return fmt.Errorf("unsupported describe resource %q", resource)
	}
	fs := flag.NewFlagSet("describe", flag.ContinueOnError)
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	timeout := fs.Duration("timeout", defaultDiscoveryTimeout, "")
	cachedOnly := fs.Bool("cached-only", false, "")
	live := fs.Bool("live", false, "")
	cluster := fs.String("cluster", "", "")
	namespace := fs.String("namespace", "", "")
	namespaceShort := fs.String("n", "", "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *namespace == "" {
		*namespace = *namespaceShort
	}
	cfg, err := catalog.LoadDiscoveryConfig(*configPath)
	if err != nil {
		return err
	}
	scopes, err := resolveAuthorizedServiceScopes(cfg, *cluster, *namespace, false)
	if err != nil {
		return err
	}
	name, err := parseServiceRef(resource)
	if err != nil {
		return err
	}
	serviceID := ""
	if isServiceID(name) {
		serviceID = name
		name = ""
	}
	catalogResult, catalogService, err := catalog.DiscoverServiceExactWithConfig(cfg, *timeout, *cachedOnly, *live, toCatalogScope(scopes[0]), name, serviceID)
	if err != nil {
		return err
	}
	result, service := fromCatalogLookupResult(catalogResult), fromCatalogService(catalogService)
	printMessages(result.Messages)
	printServiceDescription(service, result.Messages)
	return nil
}

func inspectCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tubo inspect <service/name|process/name> [flags]")
	}
	resource := args[0]
	if strings.HasPrefix(resource, "process/") || !strings.Contains(resource, "/") {
		state, status, err := loadProcessState(resource)
		if err != nil {
			return err
		}
		return printJSON(struct {
			Status string               `json:"status"`
			State  detachedProcessState `json:"state"`
		}{Status: status, State: state})
	}
	if !strings.HasPrefix(resource, "service/") {
		return fmt.Errorf("unsupported inspect resource %q", resource)
	}
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	timeout := fs.Duration("timeout", defaultDiscoveryTimeout, "")
	cachedOnly := fs.Bool("cached-only", false, "")
	live := fs.Bool("live", false, "")
	cluster := fs.String("cluster", "", "")
	namespace := fs.String("namespace", "", "")
	namespaceShort := fs.String("n", "", "")
	_ = fs.Bool("json", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *namespace == "" {
		*namespace = *namespaceShort
	}
	cfg, err := catalog.LoadDiscoveryConfig(*configPath)
	if err != nil {
		return err
	}
	scopes, err := resolveAuthorizedServiceScopes(cfg, *cluster, *namespace, false)
	if err != nil {
		return err
	}
	name, err := parseServiceRef(resource)
	if err != nil {
		return err
	}
	serviceID := ""
	if isServiceID(name) {
		serviceID = name
		name = ""
	}
	catalogResult, catalogService, err := catalog.DiscoverServiceExactWithConfig(cfg, *timeout, *cachedOnly, *live, toCatalogScope(scopes[0]), name, serviceID)
	if err != nil {
		return err
	}
	result, service := fromCatalogLookupResult(catalogResult), fromCatalogService(catalogService)
	return printJSON(struct {
		Mode     string                   `json:"mode"`
		Messages []string                 `json:"messages"`
		Scope    *serviceScope            `json:"scope,omitempty"`
		Metadata *discoveryquery.Metadata `json:"metadata,omitempty"`
		Item     serviceResource          `json:"item"`
	}{Mode: result.Mode, Messages: result.Messages, Scope: result.Scope, Metadata: result.Metadata, Item: service})
}

func watchCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tubo watch services [flags]")
	}
	if args[0] != "services" {
		return fmt.Errorf("unsupported watch resource %q", args[0])
	}
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	timeout := fs.Duration("timeout", defaultDiscoveryTimeout, "")
	cachedOnly := fs.Bool("cached-only", false, "")
	live := fs.Bool("live", false, "")
	cluster := fs.String("cluster", "", "")
	namespace := fs.String("namespace", "", "")
	namespaceShort := fs.String("n", "", "")
	allNamespaces := fs.Bool("all-namespaces", false, "")
	allNamespacesShort := fs.Bool("A", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *namespace == "" {
		*namespace = *namespaceShort
	}
	useAllNamespaces := *allNamespaces || *allNamespacesShort
	cfg, err := catalog.LoadDiscoveryConfig(*configPath)
	if err != nil {
		return err
	}
	scopes, err := resolveAuthorizedServiceScopes(cfg, *cluster, *namespace, useAllNamespaces)
	if err != nil {
		return err
	}
	if useAllNamespaces {
		if *cachedOnly {
			return errors.New("--cached-only is not supported with `watch services -A`")
		}
		result, err := discoverServicesAcrossScopes(cfg, *timeout, scopes)
		if err != nil {
			return err
		}
		printMessages(result.Messages)
		printServicesTable(result.Services)
		return nil
	}
	scope := scopes[0]
	scopedCfg := cfg
	scopedCfg.CurrentCluster = scope.Cluster
	scopedCfg.CurrentNamespace = scope.Namespace
	fmt.Printf("watching services for %s...\n", timeout.String())
	if !*live {
		if services, adminAddr, err := catalog.FetchLocalServiceCache(scopedCfg); err == nil {
			fmt.Printf("using local cache from edge admin at %s\n", adminAddr)
			for _, service := range fromCatalogServices(services) {
				fmt.Printf("CURRENT\tservice/%s\tpeer=%s\tpath=%s\n", service.Name, service.PeerID, service.Path)
			}
			if *cachedOnly {
				return nil
			}
			fmt.Printf("also observing swarm live for %s...\n", timeout.String())
		} else if *cachedOnly {
			return errors.New("no local cache found")
		} else {
			fmt.Println("no local cache found")
		}
	}
	_, err = catalog.ObserveServices(scopedCfg, *timeout, func(event catalog.WatchEvent) {
		watchEvent := fromCatalogWatchEvent(event)
		fmt.Printf("%s\tservice/%s\tpeer=%s\tpath=%s\n", strings.ToUpper(watchEvent.Type), watchEvent.Name, watchEvent.PeerID, watchEvent.Path)
	})
	return err
}

func isServiceID(ref string) bool {
	return serviceidentity.ValidateServiceID(strings.TrimSpace(ref)) == nil
}

func printServicesTable(services []serviceResource) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSERVICE ID\tSCOPE\tSTATUS\tACCESS\tPATH\tPEER\tCAPABILITIES")
	for _, service := range services {
		caps := "-"
		if len(service.Capabilities) > 0 {
			caps = strings.Join(service.Capabilities, ",")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", service.Name, displayServiceID(service.ServiceID), displayServiceScope(service), service.Status, displayServiceConnectPolicy(service), service.Path, service.PeerID, caps)
	}
	_ = w.Flush()
}

func displayServiceID(serviceID string) string {
	if serviceID == "" {
		return "-"
	}
	return serviceID
}

func displayServiceScope(service serviceResource) string {
	if service.Cluster == "" && service.Namespace == "" {
		return "-"
	}
	if service.Cluster == "" {
		return service.Namespace
	}
	if service.Namespace == "" {
		return service.Cluster
	}
	return service.Cluster + "/" + service.Namespace
}

func displayServiceConnectPolicy(service serviceResource) string {
	if strings.TrimSpace(service.ConnectPolicy) == "" {
		return "unknown"
	}
	return service.ConnectPolicy
}

func printServiceDescription(service serviceResource, messages []string) {
	service = normalizeServiceResource(service)
	fmt.Printf("Name: %s\n", service.Name)
	if service.ServiceID != "" {
		fmt.Printf("Service ID: %s\n", service.ServiceID)
	}
	fmt.Printf("Kind: %s\n", service.Kind)
	if service.Cluster != "" || service.Namespace != "" {
		fmt.Printf("Scope: %s/%s\n", service.Cluster, service.Namespace)
	}
	fmt.Printf("Status: %s\n", service.Status)
	fmt.Printf("Connect policy: %s\n", displayServiceConnectPolicy(service))
	fmt.Printf("Peer ID: %s\n", service.PeerID)
	fmt.Printf("Path: %s\n", service.Path)
	fmt.Printf("TTL: %ds\n", service.TTLSeconds)
	fmt.Printf("Expires in: %ds\n", service.ExpiresInSeconds)
	fmt.Println("Capabilities:")
	if len(service.Capabilities) == 0 {
		fmt.Println("  - none")
	} else {
		for _, cap := range service.Capabilities {
			fmt.Printf("  - %s\n", cap)
		}
	}
	fmt.Println("Dial policy:")
	switch {
	case len(service.DirectAddresses) > 0:
		fmt.Println("  preferred: direct")
		if len(service.RelayedAddresses) > 0 {
			fmt.Println("  fallback: relay")
		}
	case len(service.RelayedAddresses) > 0:
		fmt.Println("  preferred: relay")
		fmt.Println("  direct: unavailable")
	default:
		fmt.Println("  preferred: unknown")
	}
	fmt.Println("Grant service:")
	if service.GrantService == nil || len(service.GrantService.Peers) == 0 {
		fmt.Println("  - none")
	} else {
		fmt.Printf("  Protocol: %s\n", service.GrantService.Protocol)
		fmt.Println("  Peers:")
		for _, peer := range service.GrantService.Peers {
			fmt.Printf("    - %s\n", peer)
		}
	}
	fmt.Println("Addresses:")
	fmt.Println("  Direct:")
	if len(service.DirectAddresses) == 0 {
		fmt.Println("    - none")
	} else {
		for _, addr := range service.DirectAddresses {
			fmt.Printf("    - %s\n", addr)
		}
	}
	fmt.Println("  Relayed:")
	if len(service.RelayedAddresses) == 0 {
		fmt.Println("    - none")
	} else {
		for _, addr := range service.RelayedAddresses {
			fmt.Printf("    - %s\n", addr)
		}
	}
	fmt.Println("Observed from:")
	for _, msg := range messages {
		fmt.Printf("  - %s\n", msg)
	}
}

func printMessages(messages []string) {
	for _, message := range messages {
		fmt.Println(message)
	}
	if len(messages) > 0 {
		fmt.Println()
	}
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func hostPortForHTTP(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}

func newSwarmKeyData() ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	data := "/key/swarm/psk/1.0.0/\n/base16/\n" + hex.EncodeToString(b) + "\n"
	return []byte(data), nil
}

func keygen(args []string) error {
	if len(args) == 0 || args[0] != "swarm" {
		return usage()
	}
	fs := flag.NewFlagSet("keygen swarm", flag.ContinueOnError)
	out := fs.String("out", "swarm.key", "")
	force := fs.Bool("force", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if !*force {
		if _, err := os.Stat(*out); err == nil {
			return fmt.Errorf("%s exists (use --force)", *out)
		}
	}
	data, err := newSwarmKeyData()
	if err != nil {
		return err
	}
	return os.WriteFile(*out, data, 0600)
}
func id(args []string) error {
	if len(args) != 2 || args[0] != "from-seed" {
		return usage()
	}
	id, err := p2p.PeerIDFromSeed(args[1])
	if err != nil {
		return err
	}
	fmt.Println(id)
	return nil
}
func configCmd(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	path := fs.String("config", "", "")
	role := fs.String("role", "", "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	c, err := cfgpkg.LoadFile(*path)
	if err != nil {
		return err
	}
	if c.Role == "" {
		c.Role = *role
	}
	c = cfgpkg.Merge(cfgpkg.Defaults(c.Role), c)
	if args[0] == "validate" {
		return cfgpkg.Validate(c)
	}
	if args[0] == "print" {
		b, _ := yaml.Marshal(cfgpkg.Mask(c))
		fmt.Print(string(b))
		return nil
	}
	return usage()
}
func doctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	path := fs.String("config", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := cfgpkg.LoadFile(*path)
	if err != nil {
		return err
	}
	c = cfgpkg.Merge(cfgpkg.Defaults(c.Role), c)
	return cfgpkg.Doctor(c)
}
func versionCmd(args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	short := fs.Bool("short", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *short {
		fmt.Println(iversion.ProductVersion)
		return nil
	}
	fmt.Printf("tubo %s\n", iversion.ProductVersion)
	fmt.Printf("protocol %s\n", iversion.ProtocolVersion())
	fmt.Printf("commit %s\n", iversion.Commit)
	fmt.Printf("build_date %s\n", iversion.BuildDate)
	return nil
}
func initCmd(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	role := args[0]
	fs := flag.NewFlagSet("init", 0)
	out := fs.String("out", role+".yaml", "")
	force := fs.Bool("force", false, "")
	_ = fs.Parse(args[1:])
	return cfgpkg.WriteFile(*out, cfgpkg.Defaults(role), *force)
}

type csvFlag struct{ p *[]string }

func (c csvFlag) String() string {
	if c.p == nil {
		return ""
	}
	return strings.Join(*c.p, ",")
}
func (c csvFlag) Set(s string) error { *c.p = append(*c.p, cfgpkg.CSV(s)...); return nil }

type durationFlag struct{ p *cfgpkg.Duration }

func (d durationFlag) String() string { return d.p.Duration().String() }
func (d durationFlag) Set(s string) error {
	x, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d.p = cfgpkg.Duration(x)
	return nil
}
