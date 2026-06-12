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
	bridge "github.com/origama/tubo/internal/app/bridge"
	attachauth "github.com/origama/tubo/internal/attachauth"
	catalog "github.com/origama/tubo/internal/catalog"
	cfgpkg "github.com/origama/tubo/internal/config"
	connectflow "github.com/origama/tubo/internal/connectflow"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	grantspkg "github.com/origama/tubo/internal/grants"
	launcher "github.com/origama/tubo/internal/launcher"
	logging "github.com/origama/tubo/internal/logging"
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

type globalCLIOptions struct {
	Quiet     bool
	Verbosity int
	LogLevel  string
}

func run(args []string) error {
	global, args, err := parseGlobalCLIOptions(args)
	if err != nil {
		return err
	}
	if len(args) > 1 {
		subGlobal, subArgs, err := parseGlobalCLIOptions(args[1:])
		if err != nil {
			return err
		}
		global = mergeGlobalCLIOptions(global, subGlobal)
		args = append([]string{args[0]}, subArgs...)
	}
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
	if commandName, role, roleArgs, ok, err := resolveRuntimeRole(args); err != nil {
		return err
	} else if ok {
		if err := logging.Configure(runtimeLoggingConfig(global)); err != nil {
			return err
		}
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
				return detachRoleCommand(commandName, role, roleArgs)
			}
			return runRole(commandName, role, roleArgs)
		}
		if shouldHandleImplicitBootstrap(args[0]) {
			cleanArgs, err := maybeImplicitJoinOrInit(args[0], role, roleArgs)
			if err != nil {
				return err
			}
			roleArgs = cleanArgs
		}
		return runRole(commandName, role, roleArgs)
	}
	if err := logging.Configure(logging.Config{Quiet: global.Quiet, Verbosity: global.Verbosity, LogLevel: global.LogLevel, Runtime: false}); err != nil {
		return err
	}
	switch args[0] {
	case "join":
		return joinCmd(args[1:])
	case "connect":
		cleanArgs, detach := stripDetachArgs(args[1:])
		cleanArgs, noInit := stripNoInitArgs(cleanArgs)
		connectLogging, cleanArgs, err := parseGlobalCLIOptions(cleanArgs)
		if err != nil {
			return err
		}
		mergedLogging := mergeGlobalCLIOptions(global, connectLogging)
		if err := logging.Configure(runtimeLoggingConfig(mergedLogging)); err != nil {
			return err
		}
		if err := ensureJoinedPublicNetwork("connect", noInit); err != nil {
			return err
		}
		if detach {
			return detachConnectCommand(cleanArgs, mergedLogging)
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
	case "peers":
		return peersCmd(args[1:])
	case "create":
		return localCreateCmd(args[1:])
	case "rotate":
		return localRotateCmd(args[1:])
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

func runtimeLoggingConfig(global globalCLIOptions) logging.Config {
	runtimeVerbosity := global.Verbosity
	if os.Getenv("TUBO_DETACHED_CHILD") == "1" && runtimeVerbosity < 1 && strings.TrimSpace(global.LogLevel) == "" {
		runtimeVerbosity = 1
	}
	return logging.Config{Quiet: global.Quiet, Verbosity: runtimeVerbosity, LogLevel: global.LogLevel, Runtime: true}
}

func connectLoggingArgs(global globalCLIOptions) []string {
	if global.Quiet {
		return []string{"--quiet"}
	}
	if level := strings.TrimSpace(global.LogLevel); level != "" {
		return []string{"--log-level", level}
	}
	if global.Verbosity <= 0 {
		return nil
	}
	args := make([]string, 0, global.Verbosity)
	for i := 0; i < global.Verbosity; i++ {
		args = append(args, "-v")
	}
	return args
}

func mergeGlobalCLIOptions(base, extra globalCLIOptions) globalCLIOptions {
	base.Quiet = base.Quiet || extra.Quiet
	base.Verbosity += extra.Verbosity
	if strings.TrimSpace(extra.LogLevel) != "" {
		base.LogLevel = extra.LogLevel
	}
	return base
}

func parseGlobalCLIOptions(args []string) (globalCLIOptions, []string, error) {
	var opts globalCLIOptions
	remaining := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			remaining = append(remaining, args[i:]...)
			break
		}
		switch {
		case arg == "--quiet":
			opts.Quiet = true
			continue
		case arg == "-v":
			opts.Verbosity++
			continue
		case arg == "-vv":
			opts.Verbosity += 2
			continue
		case arg == "-vvv":
			opts.Verbosity += 3
			continue
		case strings.HasPrefix(arg, "--log-level="):
			opts.LogLevel = strings.TrimSpace(strings.TrimPrefix(arg, "--log-level="))
			continue
		case arg == "--log-level":
			if i+1 >= len(args) {
				return opts, nil, errors.New("--log-level requires a value")
			}
			i++
			opts.LogLevel = strings.TrimSpace(args[i])
			continue
		}
		remaining = append(remaining, arg)
	}
	return opts, remaining, nil
}

func resolveRuntimeRole(args []string) (string, string, []string, bool, error) {
	if len(args) == 0 {
		return "", "", nil, false, nil
	}
	switch args[0] {
	case "relay":
		if len(args) >= 2 && args[1] == "run" {
			return "", "", nil, false, errors.New("legacy command `tubo relay run` removed; use `tubo relay`")
		}
		return "relay", "relay", args[1:], true, nil
	case "edge", "service", "bridge":
		return "", "", nil, false, fmt.Errorf("legacy command `tubo %s run` removed; use intent-based commands (`attach`, `connect`, `gateway`, `relay`, `join`)", args[0])
	case "gateway":
		return "gateway", "edge", args[1:], true, nil
	case "attach":
		attachArgs, err := rewriteAttachArgs(args[1:])
		if err != nil {
			return "", "", nil, false, err
		}
		attachArgs, err = ensureAttachRuntimeDefaults(attachArgs)
		if err != nil {
			return "", "", nil, false, err
		}
		return "attach", "service", attachArgs, true, nil
	default:
		return "", "", nil, false, nil
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
	first := strings.TrimPrefix(args[0], "service/")
	if isServiceTargetURL(first) {
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

func isServiceTargetURL(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "tcp://")
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
	return errors.New("usage: tubo <attach|connect|gateway|relay|join|get|describe|inspect|watch|use|share|revoke|create|rotate|ps> [flags]; run `tubo help` or `tubo help <command>` for details; bundle-url is supported by `tubo join`")
}

func printTopLevelHelp() {
	fmt.Println(`tubo — publish and connect private HTTP/WebSocket services over libp2p

Usage:
  tubo attach <url> --name <service> [-d]
  tubo attach <service> --port <port> [-d]
  tubo connect <service> [--local 127.0.0.1:PORT] [-d]
  tubo connect --token <share-invite> [--local 127.0.0.1:PORT] [-d]
  tubo get services
  tubo use overlay/public
  tubo create cluster/home
  tubo share cluster/home --role member
  tubo share service/myapp --expires 1h
  tubo share revoke <share-invite>
  tubo revoke <invite|session|service-access|publish> <id-or-service>
  tubo rotate secret/namespace-discovery/home/default --grace 24h
  tubo grants pending
  tubo peers alias <peer-id> --name <label>
  tubo relay [-d]
  tubo gateway [-d]
  tubo join [overlay/public|tubo-public]

Public default happy path (invite-only):
  # Machine with a local app
  tubo attach http://127.0.0.1:8080 --name myapp -d
  # then copy the printed share invite

  # Another machine
  tubo connect --token <share-invite> --local 127.0.0.1:9888
  curl http://127.0.0.1:9888/

Collaboration namespace flow:
  tubo create cluster/home
  tubo create namespace/team
  tubo attach http://127.0.0.1:8080 --name myapp -d
  tubo get services
  tubo connect myapp

Discovery and process management:
  tubo describe service/myapp
  tubo inspect service/myapp --json
  tubo watch services
  tubo use overlay/public
  tubo share cluster/home --role member
  tubo ps
  tubo logs process/attach-myapp
  tubo stop process/attach-myapp

Notes:
  - First run auto-joins the signed public network bundle.
  - Use --no-init to disable implicit join.
  - HTTP and WebSocket upgrade traffic are both tunneled.

Global flags:
  --quiet
  -v | -vv | -vvv
  --log-level error|warn|info|debug|trace

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

Scope behavior:
  - public default (tubo-public / home/default): unlisted + invite-only; attach prints a share invite for tubo connect --token ...
  - discovery-enabled custom/private namespace: discoverable collaboration service; peers with membership can use tubo connect <service>
  - private overlay: same product model, but with stronger transport isolation than the shared public overlay

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
  tubo connect <service> [--local 127.0.0.1:PORT] [-d]
  tubo connect --token <share-invite> [--local 127.0.0.1:PORT] [-d]

Open a local HTTP/WebSocket listener to a remote service.

Examples:
  tubo connect --token <share-invite> --local 127.0.0.1:9888
  tubo connect piweb

Connect modes:
  - tubo connect --token ... = invite path; does not require ambient discovery when the token carries a self-contained endpoint
  - tubo connect <service> = collaboration path; requires a discovery-enabled scope and the right namespace permissions

Logging:
  - -v / -vv / -vvv and --log-level can follow connect
  - detached child logs inherit the same verbosity controls

Flags:
  --local <host:port>       local listener; random 127.0.0.1 port when omitted
  --timeout <duration>      discovery timeout, default 20s
  --live                    skip remote cache and observe pubsub live
  --cached-only             only use local edge cache
  --json                    print JSON result
  -d, --detach              run in background and register in tubo ps
  --no-init                 fail instead of auto-joining the public bundle

Path selection:
  - usable direct addresses are tried first
  - loopback/unspecified direct addresses are skipped from remote clients
  - relayed addresses are used as fallback
  - hole punching is enabled when relay metadata is available
  - an initial relayed path may later upgrade to a direct libp2p connection
  - for raw TCP services, connect may do one short inline self-heal attempt before failing a new local connection when pre-stream setup breaks`)
	case "get":
		fmt.Println(`Usage:
  tubo get services [--json]
  tubo get service/<name> [--json]
  tubo get overlays [--json]
  tubo get clusters [--json]
  tubo get namespaces [--json]
  tubo get secrets [--json]
  tubo get processes [--json]

Inspect local processes, local config resources, or services announced in the swarm.`)
	case "describe":
		fmt.Println(`Usage:
  tubo describe service/<name>
  tubo describe process/<name>
  tubo describe overlay/<name>
  tubo describe cluster/<name>
  tubo describe namespace/<name>
  tubo describe secret/namespace-discovery/<cluster>/<namespace>

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
  tubo share cluster/<name> [--role member|viewer|grant-requester] [--namespace <name>] [--expires <duration>]
  tubo share service/<name> [--cluster <name>] [--namespace <name>] [--expires <duration>]
  tubo share revoke <share-invite>

Create a copyable cluster invitation or service-scoped connect token. share service/... uses local authority minting when available, otherwise it can delegate to the cluster grant service when the service owner holds a valid publish lease with share.mint. Service share invites are one-time at redemption time: one successful lease/session issuance, not one proxied HTTP request.`)
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
	case "rotate":
		fmt.Println(`Usage:
  tubo rotate secret/namespace-discovery/<cluster>/<namespace> --grace 24h [--json]

Rotate the managed namespace discovery secret using the current/previous model.`)
	case "grants":
		fmt.Println(`Usage:
  tubo grants pending [--wide] [--json] [--verbose]
  tubo grants history [--wide] [--json] [--all] [--verbose]
  tubo grants describe <request-id> [--wide] [--json]
  tubo grants approve <request-id> --ttl 7d
  tubo grants deny <request-id> --reason <reason>
  tubo grants request service/<name> --peer <multiaddr>

Manage publish-grant requests on the authority node.`)
	case "peers":
		fmt.Println(`Usage:
  tubo peers alias <peer-id> --name <label> [--note <note>] [--json]

Save a local operator-only label for a peer ID.`)
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

func runRole(commandName, role string, args []string) error {
	c, configPath, err := resolveRoleConfig(role, args)
	if err != nil {
		return err
	}
	printForegroundRuntimeNotice(commandName, role, c)
	spec, err := buildDetachedSpec(commandName, c, args)
	if err != nil {
		return err
	}
	if commandName == "attach" {
		updateAttachProcessState(&spec.State, c)
	}
	spec.State.LogFile = ""
	state, cleanup, err := registerCurrentProcess(spec.State)
	if err != nil {
		return err
	}
	defer func() {
		if cleanup != nil {
			if err := cleanup(); err != nil {
				logging.Warnf("foreground process cleanup failed: %v\n", err)
			}
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	_ = state
	log.Printf("%s started pid=%d", commandName, os.Getpid())
	err = launcher.Run(ctx, newRuntimeLauncher(), role, configPath, c)
	if err != nil {
		log.Printf("%s stopped with error: %v", commandName, err)
	} else {
		log.Printf("%s stopped", commandName)
	}
	return err
}

func printForegroundRuntimeNotice(commandName, role string, cfg cfgpkg.Config) {
	if commandName == "attach" {
		return
	}
	switch role {
	case "edge":
		logging.Warnf("gateway running in foreground; press Ctrl+C to stop\n")
	case "relay":
		logging.Warnf("relay running in foreground; press Ctrl+C to stop\n")
	case "service":
		if strings.TrimSpace(cfg.Service.Name) != "" {
			logging.Warnf("service %q running in foreground; press Ctrl+C to stop\n", cfg.Service.Name)
		} else {
			logging.Warnf("service running in foreground; press Ctrl+C to stop\n")
		}
	}
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
	logging.Resultf("%s\n", title)
	if result.NetworkName != "" {
		logging.Resultf("network: %s\n", result.NetworkName)
	}
	if result.NetworkID != "" {
		logging.Resultf("network id: %s\n", result.NetworkID)
	}
	if result.KeyID != "" {
		logging.Resultf("signature key: %s\n", result.KeyID)
	}
	logging.Resultf("config: %s\n", result.ConfigPath)
	for i, relayPeer := range result.RelayPeers {
		if i == 0 {
			logging.Resultf("relay: %s\n", relayPeer)
		} else {
			logging.Resultf("relay[%d]: %s\n", i+1, relayPeer)
		}
	}
	logging.Resultf("swarm key installed: %s\n", result.SwarmKeyPath)
	if result.Checked {
		logging.Resultf("relay check: ok\n")
	}
	logging.Resultf("\n")
	logging.Resultf("next:\n")
	if result.NetworkName == joinDefaultNetworkName {
		logging.Resultf("  tubo attach http://127.0.0.1:1234 --name my-service\n")
		logging.Resultf("  # on another machine, use the printed share invite\n")
		logging.Resultf("  tubo connect --token <share-invite>\n")
		logging.Resultf("  # optional: create a collaboration namespace for connect-by-name\n")
		logging.Resultf("  tubo create cluster/home\n")
		logging.Resultf("  tubo create namespace/team\n")
		return
	}
	logging.Resultf("  tubo create cluster/home\n")
	logging.Resultf("  tubo create namespace/team\n")
	logging.Resultf("  tubo attach http://127.0.0.1:1234 --name my-service\n")
	logging.Resultf("  tubo get services\n")
	logging.Resultf("  tubo connect my-service\n")
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
	var attachAuthz attachAuthorization
	if commandName == "attach" {
		authz, err := resolveAttachAuthorization(configPath, cfg)
		if err != nil {
			return err
		}
		cfg = authz.Config
		serviceID = authz.Service.ServiceID
		attachAuthz = authz
	}
	spec, err := buildDetachedSpec(commandName, cfg, args)
	if err != nil {
		return err
	}
	if serviceID != "" {
		spec.State.ServiceID = serviceID
	}
	if commandName == "attach" {
		spec.State.ResourceKind = "service"
		spec.State.ServiceKind = string(cfgpkg.NormalizeServiceKind(cfg.Service.Kind, cfg.Service.Target))
		if attachAuthz.ServicePeerID != "" {
			spec.State.PeerID = attachAuthz.ServicePeerID
		}
		updateAttachProcessState(&spec.State, cfg)
	}
	state, err := startDetachedProcess(spec)
	if err != nil {
		return err
	}
	if commandName == "attach" {
		printAttachShareHint(cfg, attachAuthz)
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
	logging.Progressf("No Tubo network configured.\n")
	logging.Progressf("Fetching default network bundle: %s\n", joinDefaultNetworkName)
	result, err := joinBundleMode(effectiveDefaultPublicBundleURL(), defaultTuboConfigDir(), false)
	if err != nil {
		return fmt.Errorf("implicit public join for %s failed: %w", command, err)
	}
	logging.Progressf("Signature verified: %s\n", result.KeyID)
	logging.Progressf("Joined network: %s\n\n", result.NetworkName)
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
		logging.Progressf("no tubo config found\n")
		logging.Progressf("created local config: %s\n", configPath)
		if createdKey {
			logging.Progressf("created private swarm key: %s\n", keyPath)
		}
		logging.Progressf("\n")
	}
	return nil
}

func printDetachedSummary(commandName string, state detachedProcessState) {
	switch commandName {
	case "attach":
		logging.Resultf("attached service %q\n", state.Service)
		if state.ServiceID != "" {
			logging.Resultf("service id: %s\n", state.ServiceID)
		}
	case "connect":
		logging.Resultf("connect tunnel for service %q\n", state.Service)
		if state.ServiceID != "" {
			logging.Resultf("service id: %s\n", state.ServiceID)
		}
	case "gateway":
		logging.Resultf("gateway running\n")
	case "relay":
		logging.Resultf("relay running\n")
	default:
		logging.Resultf("started %s\n", commandName)
	}
	logging.Resultf("id: %s\n", state.ID)
	if state.Local != "" {
		logging.Resultf("local: %s\n", state.Local)
	}
	logging.Resultf("pid: %d\n", state.PID)
	logging.Resultf("logs: %s\n", state.LogFile)
}

func processTTLColumn(item processView) string {
	if _, rem := formatProcessExpiry(item.ConnectRefreshExpiresAt); rem != "" {
		return rem
	}
	if _, rem := formatProcessExpiry(item.ConnectAccessExpiresAt); rem != "" {
		return rem
	}
	return "-"
}

func processResourceKind(item processView) string {
	if strings.TrimSpace(item.ResourceKind) != "" {
		return item.ResourceKind
	}
	switch item.Command {
	case "attach":
		return "service"
	case "connect":
		return "pipe"
	default:
		return "process"
	}
}

func processServiceKind(item processView) string {
	if strings.TrimSpace(item.ServiceKind) != "" {
		return item.ServiceKind
	}
	return "-"
}

func printProcessList(items []processView, wide bool) {
	if !wide {
		fmt.Println("Running Tubo processes")
		fmt.Println()
		printProcessesCompactTable(items)
		return
	}
	printProcessesTable(items)
}

func printProcessesCompactTable(items []processView) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tSTATUS\tSERVICE\tLOCAL\tTARGET\tPATH\tTTL")
	for _, item := range items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", item.Name, processResourceKind(item), item.Status, displayValue(item.Service), displayValue(item.Local), displayValue(item.Target), summarizeProcessPath(item.Path), processTTLColumn(item))
	}
	_ = w.Flush()
}

func printProcessesTable(items []processView) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tCOMMAND\tSERVICE KIND\tSERVICE ID\tSCOPE\tSTATUS\tPATH\tTTL\tPID\tLOCAL\tTARGET")
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
		path := item.Path
		if path == "" {
			path = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", item.Name, processResourceKind(item), item.Command, processServiceKind(item), displayServiceID(item.ServiceID), scope, item.Status, path, processTTLColumn(item), item.PID, local, target)
	}
	_ = w.Flush()
}

func summarizeProcessPath(path string) string {
	path = strings.TrimSpace(path)
	switch {
	case path == "":
		return "unknown"
	case strings.Contains(strings.ToLower(path), "relay"):
		return "relay"
	case strings.Contains(strings.ToLower(path), "direct"):
		return "direct"
	default:
		return path
	}
}

func formatProcessExpiry(raw string) (string, string) {
	if strings.TrimSpace(raw) == "" {
		return "", ""
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw, "unknown"
	}
	remaining := ts.Sub(time.Now().UTC())
	if remaining <= 0 {
		return ts.UTC().Format(time.RFC3339), "expired"
	}
	return ts.UTC().Format(time.RFC3339), remaining.Round(time.Second).String()
}

func printProcessDescription(state detachedProcessState, status string) {
	fmt.Printf("Name: %s\n", state.Name)
	fmt.Printf("Kind: %s\n", state.Kind)
	if state.ResourceKind != "" {
		fmt.Printf("Resource kind: %s\n", state.ResourceKind)
	}
	fmt.Printf("Command: %s\n", state.Command)
	fmt.Printf("Status: %s\n", status)
	if state.StatusConfidence != "" {
		fmt.Printf("Status confidence: %s\n", state.StatusConfidence)
	}
	fmt.Printf("PID: %d\n", state.PID)
	if state.Source != "" {
		fmt.Printf("Source: %s\n", state.Source)
	}
	if state.Service != "" {
		fmt.Printf("Service: %s\n", state.Service)
	}
	if state.ServiceKind != "" {
		fmt.Printf("Service kind: %s\n", state.ServiceKind)
	}
	if state.ServiceID != "" {
		fmt.Printf("Service ID: %s\n", state.ServiceID)
	}
	if state.Command == "attach" || state.ResourceKind == "service" || state.GrantEndpointEnabled || state.GrantProtocol != "" || state.ConnectPolicy != "" {
		if state.GrantEndpointEnabled {
			fmt.Println("Grant endpoint: enabled")
		} else {
			fmt.Println("Grant endpoint: disabled")
		}
		if state.ConnectPolicy != "" {
			fmt.Printf("Connect policy: %s\n", state.ConnectPolicy)
		}
		if state.GrantProtocol != "" {
			fmt.Printf("Grant protocol: %s\n", state.GrantProtocol)
		}
	}
	if state.PeerID != "" {
		fmt.Printf("Peer ID: %s\n", state.PeerID)
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
	if state.Path != "" {
		fmt.Printf("Path: %s\n", state.Path)
	}
	if state.SelectedAddr != "" {
		fmt.Printf("Selected addr: %s\n", state.SelectedAddr)
	}
	if state.SelectedPath != "" {
		fmt.Printf("Selected path: %s\n", state.SelectedPath)
	}
	if len(state.CommandLine) > 0 {
		fmt.Printf("Command line: %s\n", strings.Join(state.CommandLine, " "))
	}
	fmt.Printf("Log file: %s\n", state.LogFile)
	fmt.Printf("State file: %s\n", state.StateFile)
	fmt.Printf("PID file: %s\n", state.PIDFile)
	if state.StatusURL != "" {
		fmt.Printf("Status URL: %s\n", state.StatusURL)
	}
	if state.RuntimeStatus != "" && state.RuntimeStatus != status {
		fmt.Printf("Runtime health: %s\n", state.RuntimeStatus)
	}
	if state.DegradedReason != "" {
		fmt.Printf("Runtime reason: %s\n", state.DegradedReason)
	}
	if ts, rem := formatProcessExpiry(state.ConnectAccessExpiresAt); ts != "" {
		fmt.Printf("Connect access expires at: %s\n", ts)
		fmt.Printf("Connect access expires in: %s\n", rem)
	}
	if ts, rem := formatProcessExpiry(state.ConnectRefreshExpiresAt); ts != "" {
		fmt.Printf("Connect refresh expires at: %s\n", ts)
		fmt.Printf("Connect refresh expires in: %s\n", rem)
	}
	if state.LastTunnelError != "" {
		fmt.Printf("Last tunnel error: %s\n", state.LastTunnelError)
	}
	if state.LastRefreshError != "" {
		fmt.Printf("Last refresh error: %s\n", state.LastRefreshError)
	}
	if ts, rem := formatProcessExpiry(state.NextRefreshRetryAt); ts != "" {
		fmt.Printf("Next refresh retry at: %s\n", ts)
		fmt.Printf("Next refresh retry in: %s\n", rem)
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
		if views, listErr := listProcessViews(false); listErr == nil && len(views) > 0 {
			names := make([]string, 0, len(views))
			for _, v := range views {
				names = append(names, v.Name)
			}
			return fmt.Errorf("%w\nhint: running processes are: %s", err, strings.Join(names, ", "))
		}
		return err
	}
	if strings.TrimSpace(state.LogFile) == "" {
		if state.Source == "systemd" {
			return fmt.Errorf("no Tubo-owned log file recorded for %s; try `journalctl --user-unit ...` or `tubo describe %s`", state.ID, state.ID)
		}
		return fmt.Errorf("no Tubo-owned log file recorded for %s; try `tubo describe %s` or check your external supervisor logs", state.ID, state.ID)
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
	preview, status, err := loadProcessState(fs.Arg(0))
	if err != nil {
		return err
	}
	if status != "running" && status != "degraded" {
		return fmt.Errorf("process %s is not running", preview.ID)
	}
	if preview.Source != "" && preview.Source != "tubo-detached" {
		logging.Warnf("stopping externally managed Tubo runtime %s via SIGTERM; use your supervisor to restart it\n", preview.ID)
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
	ClusterID        string                          `json:"cluster_id,omitempty"`
	NamespaceID      string                          `json:"namespace_id,omitempty"`
	ServiceKind      string                          `json:"service_kind,omitempty"`
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
	req, err := parseConnectCLIArgs(args)
	if err != nil {
		return err
	}
	requestLabel := strings.TrimSpace(req.ServiceRef)
	if requestLabel == "" {
		if strings.TrimSpace(req.Token) != "" {
			requestLabel = "share-invite"
		} else {
			requestLabel = "service"
		}
	}
	localLabel := strings.TrimSpace(req.Local)
	if localLabel == "" {
		localLabel = "auto"
	}
	logging.Progressf("connect starting service_ref=%q local=%s\n", requestLabel, localLabel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := connectflow.Resolve(ctx, newConnectWorkflow(), connectflow.Request{ConfigPath: req.ConfigPath, ServiceRef: req.ServiceRef, Token: req.Token, Cluster: req.Cluster, Namespace: req.Namespace, Local: req.Local, Timeout: req.Timeout, CachedOnly: req.CachedOnly, Live: req.Live})
	if err != nil {
		return err
	}
	scopeLabel := "-"
	if result.Scope != nil {
		scopeLabel = result.Scope.Cluster + "/" + result.Scope.Namespace
	}
	logging.Progressf("service resolved service_id=%s service_kind=%s peer=%s path=%s selected_addr=%s scope=%s\n", displayServiceID(result.ServiceID), displayValue(result.ServiceKind), displayValue(result.ServicePeerID), displayValue(result.Path), displayValue(result.SelectedAddr), scopeLabel)
	state := connectProcessState(req, result, result.LocalURL, "pipe")
	state.LogFile = ""
	state.StateFile = filepath.Join(processStateDir(), state.Name+".json")
	state.PIDFile = filepath.Join(processRunDir(), state.Name+".pid")
	state, cleanup, err := registerCurrentProcess(state)
	if err != nil {
		return err
	}
	defer func() {
		if cleanup != nil {
			if err := cleanup(); err != nil {
				logging.Warnf("foreground process cleanup failed: %v\n", err)
			}
		}
	}()
	output := fromConnectWorkflowResult(result)
	if err := updateProcessConnectState(state.StateFile, output); err != nil {
		logging.Warnf("connect state update failed: %v\n", err)
	}
	lastPath := result.Path
	result.App.SetStatusReporter(func(runtime bridge.RuntimeStatus) {
		if err := updateProcessRuntimeState(state.StateFile, runtime); err != nil {
			logging.Warnf("connect runtime status update failed: %v\n", err)
		}
		if notice, ok := bridge.ConnectPathTransitionMessage(lastPath, runtime.Path); ok {
			line := notice
			if runtime.SelectedPath != "" || runtime.SelectedAddr != "" {
				extra := make([]string, 0, 2)
				if runtime.SelectedPath != "" {
					extra = append(extra, "selected_path="+runtime.SelectedPath)
				}
				if runtime.SelectedAddr != "" {
					extra = append(extra, "selected_addr="+runtime.SelectedAddr)
				}
				line += " " + strings.Join(extra, " ")
			}
			if !req.JSONOut {
				fmt.Fprintln(os.Stdout, line)
			}
		}
		if runtime.Path != "" {
			lastPath = runtime.Path
		}
	})
	if req.JSONOut {
		if err := printJSON(output); err != nil {
			return err
		}
	} else {
		printMessages(result.Messages)
		fmt.Printf("connected to service %q\n", result.ServiceName)
		if result.ServiceKind != "" {
			fmt.Printf("service kind: %s\n", result.ServiceKind)
		}
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
		logging.Progressf("press Ctrl+C to stop\n")
	}
	return result.App.Start(ctx)
}

func psCmd(args []string) error {
	fs := flag.NewFlagSet("ps", flag.ContinueOnError)
	all := fs.Bool("all", false, "")
	jsonOut := fs.Bool("json", false, "")
	wide := fs.Bool("wide", false, "")
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
	printProcessList(items, *wide)
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
	wide := fs.Bool("wide", false, "")
	cachedOnly := fs.Bool("cached-only", false, "")
	live := fs.Bool("live", false, "")
	cluster := fs.String("cluster", "", "")
	namespace := fs.String("namespace", "", "")
	namespaceShort := fs.String("n", "", "")
	allNamespaces := fs.Bool("all-namespaces", false, "")
	allNamespacesShort := fs.Bool("A", false, "")
	systemOnly := fs.Bool("system", false, "")
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
		printProcessList(items, *wide)
		return nil
	case resource == "overlays" || resource == "clusters" || resource == "namespaces" || resource == "secrets":
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
			result.Services = filterListedServices(result.Services, *systemOnly)
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
			printServiceList(result.Services, *wide, *systemOnly, serviceScopeLabelPtr(result.Scope))
			return nil
		}
		scope := scopes[0]
		catalogResult, err := catalog.DiscoverServicesWithConfig(cfg, *timeout, *cachedOnly, *live, toCatalogScope(scope))
		if err != nil {
			return err
		}
		result := fromCatalogLookupResult(catalogResult)
		result.Services = filterListedServices(result.Services, *systemOnly)
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
		printServiceList(result.Services, *wide, *systemOnly, serviceScopeLabel(scope))
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
	if strings.HasPrefix(resource, "overlay/") || strings.HasPrefix(resource, "cluster/") || strings.HasPrefix(resource, "namespace/") || strings.HasPrefix(resource, "secret/") {
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
	logging.Progressf("watching services for %s...\n", timeout.String())
	if !*live {
		if services, adminAddr, err := catalog.FetchLocalServiceCache(scopedCfg); err == nil {
			logging.Progressf("using local cache from edge admin at %s\n", adminAddr)
			for _, service := range fromCatalogServices(services) {
				fmt.Printf("CURRENT\tservice/%s\tpeer=%s\tpath=%s\n", service.Name, service.PeerID, service.Path)
			}
			if *cachedOnly {
				return nil
			}
			logging.Progressf("also observing swarm live for %s...\n", timeout.String())
		} else if *cachedOnly {
			return errors.New("no local cache found")
		} else {
			logging.Progressf("no local cache found\n")
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

func filterListedServices(services []serviceResource, systemOnly bool) []serviceResource {
	filtered := make([]serviceResource, 0, len(services))
	for _, service := range services {
		isSystem := isSystemServiceResource(service)
		if systemOnly {
			if !isSystem || !hasStrictSystemServiceScope(service) {
				continue
			}
		} else if isSystem {
			continue
		}
		filtered = append(filtered, service)
	}
	return filtered
}

func isSystemServiceResource(service serviceResource) bool {
	kind := strings.TrimSpace(service.Kind)
	if kind == "" {
		kind = "service"
	}
	return kind != "service" || strings.TrimSpace(service.ServiceKind) == "grant-service"
}

func hasStrictSystemServiceScope(service serviceResource) bool {
	return strings.TrimSpace(service.ClusterID) != "" && strings.TrimSpace(service.NamespaceID) != ""
}

func printServiceList(services []serviceResource, wide bool, systemOnly bool, scopeLabel string) {
	if !wide {
		if systemOnly {
			fmt.Printf("System services in %s\n\n", scopeLabel)
			printSystemServicesCompactTable(services)
			return
		}
		fmt.Printf("Services in %s\n\n", scopeLabel)
		printServicesCompactTable(services)
		return
	}
	if systemOnly {
		printSystemServicesTable(services)
		return
	}
	printServicesTable(services)
}

func printServicesCompactTable(services []serviceResource) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SERVICE\tKIND\tACCESS\tSTATUS\tROUTE\tPEER\tEXPIRES")
	for _, service := range services {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", displayValue(service.Name), displayServiceKind(service), displayServiceConnectPolicy(service), displayValue(service.Status), serviceRouteSummary(service), servicePeerSummary(service.PeerID), serviceExpiresSummary(service))
	}
	_ = w.Flush()
}

func printSystemServicesCompactTable(services []serviceResource) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tSTATUS\tPROTOCOL\tPEER\tADDRS")
	for _, service := range services {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", displayValue(service.Name), displayValue(service.Kind), displayValue(service.Status), serviceProtocolSummary(service), servicePeerSummary(service.PeerID), serviceAddressSummary(service))
	}
	_ = w.Flush()
}

func printServicesTable(services []serviceResource) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSERVICE ID\tSCOPE\tSERVICE KIND\tSTATUS\tACCESS\tPATH\tPEER\tCAPABILITIES")
	for _, service := range services {
		caps := "-"
		if len(service.Capabilities) > 0 {
			caps = strings.Join(service.Capabilities, ",")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", service.Name, displayServiceID(service.ServiceID), displayServiceScope(service), displayServiceKind(service), service.Status, displayServiceConnectPolicy(service), service.Path, service.PeerID, caps)
	}
	_ = w.Flush()
}

func printSystemServicesTable(services []serviceResource) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tSCOPE\tPROTOCOL\tSTATUS\tPEER\tADDRS")
	for _, service := range services {
		protocol := "-"
		if service.GrantService != nil && strings.TrimSpace(service.GrantService.Protocol) != "" {
			protocol = service.GrantService.Protocol
		}
		addrs := "-"
		if len(service.Addresses) > 0 {
			addrs = strings.Join(service.Addresses, ",")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", service.Name, displayValue(service.Kind), displayServiceScope(service), protocol, service.Status, displayValue(service.PeerID), addrs)
	}
	_ = w.Flush()
}

func serviceScopeLabel(scope serviceScope) string {
	if scope.AllNamespaces {
		if scope.Cluster != "" {
			return scope.Cluster + " across all namespaces"
		}
		return "all namespaces"
	}
	if scope.Cluster != "" && scope.Namespace != "" {
		return scope.Cluster + "/" + scope.Namespace
	}
	if scope.Cluster != "" {
		return scope.Cluster
	}
	if scope.Namespace != "" {
		return scope.Namespace
	}
	return "current scope"
}

func serviceScopeLabelPtr(scope *serviceScope) string {
	if scope == nil {
		return "current scope"
	}
	return serviceScopeLabel(*scope)
}

func serviceRouteSummary(service serviceResource) string {
	path := strings.ToLower(strings.TrimSpace(service.Path))
	switch {
	case path == "":
		switch {
		case len(service.DirectAddresses) > 0 && len(service.RelayedAddresses) > 0:
			return "mixed"
		case len(service.DirectAddresses) > 0:
			return "direct"
		case len(service.RelayedAddresses) > 0:
			return "relay"
		default:
			return "unknown"
		}
	case strings.Contains(path, "relay"):
		return "relay"
	case strings.Contains(path, "direct"):
		return "direct"
	default:
		return path
	}
}

func serviceProtocolSummary(service serviceResource) string {
	if service.GrantService != nil && strings.TrimSpace(service.GrantService.Protocol) != "" {
		return service.GrantService.Protocol
	}
	return "-"
}

func servicePeerSummary(peerID string) string {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return "-"
	}
	return abbreviateValue(peerID, 16)
}

func serviceExpiresSummary(service serviceResource) string {
	if service.ExpiresInSeconds <= 0 {
		return "-"
	}
	return (time.Duration(service.ExpiresInSeconds) * time.Second).Round(time.Second).String()
}

func serviceAddressSummary(service serviceResource) string {
	direct := len(service.DirectAddresses)
	relayed := len(service.RelayedAddresses)
	if direct == 0 && relayed == 0 {
		if len(service.Addresses) == 0 {
			return "-"
		}
		return fmt.Sprintf("%d addrs", len(service.Addresses))
	}
	parts := make([]string, 0, 2)
	if direct > 0 {
		parts = append(parts, summarizeCount(direct, "direct addr"))
	}
	if relayed > 0 {
		parts = append(parts, summarizeCount(relayed, "relay addr"))
	}
	return strings.Join(parts, ", ")
}

func summarizeCount(count int, label string) string {
	if count == 1 {
		return "1 " + label
	}
	return fmt.Sprintf("%d %ss", count, label)
}

func abbreviateValue(value string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	if max <= 0 || len(value) <= max {
		return value
	}
	if max <= 3 {
		return value[:max]
	}
	return value[:max-3] + "..."
}

func displayServiceID(serviceID string) string {
	if serviceID == "" {
		return "-"
	}
	return serviceID
}

func displayValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
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

func displayServiceKind(service serviceResource) string {
	if strings.TrimSpace(service.ServiceKind) == "" {
		return string(cfgpkg.ServiceKindHTTP)
	}
	return service.ServiceKind
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
	fmt.Printf("Service kind: %s\n", displayServiceKind(service))
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
		logging.Warnf("%s\n", message)
	}
	if len(messages) > 0 {
		logging.Warnf("\n")
	}
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
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
	if err := cfgpkg.Doctor(c); err != nil {
		return err
	}
	for _, warning := range doctorWarnings(c) {
		fmt.Println(warning)
	}
	return nil
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
