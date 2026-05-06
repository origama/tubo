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
	"github.com/origama/tubo/internal/app/bridge"
	"github.com/origama/tubo/internal/app/edge"
	"github.com/origama/tubo/internal/app/relay"
	"github.com/origama/tubo/internal/app/service"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	"github.com/origama/tubo/internal/networkbundle"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/trust"
	iversion "github.com/origama/tubo/internal/version"
	"gopkg.in/yaml.v3"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
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
		return usage()
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
	case "topology":
		return topology(args[1:])
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
		attachArgs, err = ensureRuntimeSeed(attachArgs, "attach")
		if err != nil {
			return "", nil, false, err
		}
		return "service", attachArgs, true, nil
	default:
		return "", nil, false, nil
	}
}

func ensureRuntimeSeed(args []string, prefix string) ([]string, error) {
	if hasLongFlag(args, "--seed") {
		return args, nil
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	seed := prefix + "-" + hex.EncodeToString(buf)
	return append(append([]string{}, args...), "--seed", seed), nil
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
	return errors.New("usage: tubo <attach|connect|gateway|relay> [flags] | tubo join [<network-name>] [--bundle-url <url>] [flags] | tubo join --relay <multiaddr> --swarm-key <path> [flags] | tubo ps [flags] | tubo get <services|service/name|processes> [flags] | tubo describe <service/name|process/name> [flags] | tubo inspect <service/name|process/name> [flags] | tubo watch services [flags] | tubo logs <process/name> [flags] | tubo stop <process/name> [flags] | tubo rm --stale [flags] | init <role|topology> | keygen swarm | id from-seed | config <print|validate> | doctor | topology <render|commands> | version")
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
	c, _, err := resolveRoleConfig(role, args)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch role {
	case "edge":
		a, err := edge.New(ctx, edge.Config{HTTPListen: c.Edge.Listen, P2PListen: c.Node.P2PListen, Seed: c.Node.Seed, AdminListen: c.Edge.AdminListen, BootstrapPeers: c.Network.BootstrapPeers, RelayPeers: c.Network.RelayPeers, BootstrapRetryInterval: 5 * time.Second, DirectStreamTimeout: c.Edge.DirectStreamTimeout.Duration(), PrivateKeyFile: c.Network.PrivateKeyFile, PrivateKeyB64: c.Network.PrivateKeyB64})
		if err != nil {
			return err
		}
		return a.Start(ctx)
	case "service":
		a, err := service.New(ctx, service.Config{Listen: c.Node.P2PListen, Seed: c.Node.Seed, ServiceName: c.Service.Name, Target: c.Service.Target, HealthListen: c.HealthListen, PrivateKeyFile: c.Network.PrivateKeyFile, PrivateKeyB64: c.Network.PrivateKeyB64, BootstrapPeers: c.Network.BootstrapPeers, RelayPeers: c.Network.RelayPeers, Autorelay: c.Network.Autorelay, HolePunching: c.Network.HolePunching, ForceReachability: c.Network.ForceReachability, HeartbeatInterval: c.HeartbeatInterval.Duration(), BootstrapRetryInterval: 5 * time.Second})
		if err != nil {
			return err
		}
		return a.Start(ctx)
	case "relay":
		a, err := relay.New(ctx, relay.Config{Listen: c.Node.P2PListen, Seed: c.Node.Seed, HealthListen: c.Relay.HealthListen, PublicAddr: c.Relay.PublicAddr, PrivateKeyFile: c.Network.PrivateKeyFile, PrivateKeyB64: c.Network.PrivateKeyB64, EnableRelayService: c.Relay.EnableRelayService, EnableAutoNATService: c.Relay.EnableAutoNATService, EnableDiscoveryPubSub: c.Relay.EnableDiscoveryPubSub, ForceReachabilityPublic: c.Relay.ForceReachabilityPublic, PrintRunCommands: c.Relay.PrintRunCommands, MaxReservations: c.Relay.MaxReservations, MaxReservationsPerIP: c.Relay.MaxReservationsPerIP, MaxReservationsPerASN: c.Relay.MaxReservationsPerASN, MaxCircuitsPerPeer: c.Relay.MaxCircuitsPerPeer, BufferSize: c.Relay.BufferSize, ReservationTTL: c.Relay.ReservationTTL.Duration(), LimitDuration: c.Relay.LimitDuration.Duration(), LimitDataBytes: c.Relay.LimitDataBytes})
		if err != nil {
			return err
		}
		return a.Start(ctx)
	case "bridge":
		a, err := bridge.New(ctx, bridge.Config{Listen: c.Bridge.Listen, Seed: c.Node.Seed, P2PListen: c.Node.P2PListen, ServiceAddr: c.Bridge.ServiceAddr, ServiceSeed: c.Bridge.ServiceSeed, ServiceP2PListen: c.Bridge.ServiceP2PListen, PrivateKeyFile: c.Network.PrivateKeyFile, PrivateKeyB64: c.Network.PrivateKeyB64})
		if err != nil {
			return err
		}
		return a.Start(ctx)
	}
	return nil
}

type detachedProcessState struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Command   string `json:"command"`
	Name      string `json:"name"`
	Service   string `json:"service,omitempty"`
	Local     string `json:"local,omitempty"`
	Target    string `json:"target,omitempty"`
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
	LogFile   string `json:"log_file"`
	StateFile string `json:"state_file"`
	PIDFile   string `json:"pid_file"`
	StatusURL string `json:"status_url,omitempty"`
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	relayPeers = uniqueStrings(relayPeers)
	networkName := ""
	if fs.NArg() > 1 {
		return errors.New("usage: tubo join [<network-name>] [--bundle-url <url>] [flags]")
	}
	if fs.NArg() == 1 {
		networkName = fs.Arg(0)
	}
	manualMode := len(relayPeers) > 0 || *swarmKeyPath != "" || *swarmKeyB64 != ""
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
		printJoinResult("joined swarm config", result)
		return nil
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
	printJoinResult("joined network bundle", result)
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
	joined := cfgpkg.Merge(existing, cfgpkg.Config{Network: cfgpkg.Network{
		PrivateKeyFile: installedKeyPath,
		BootstrapPeers: relayPeers,
		RelayPeers:     relayPeers,
		Autorelay:      true,
		HolePunching:   true,
	}})
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

type detachedSpec struct {
	State     detachedProcessState
	ChildArgs []string
	HealthURL string
}

func detachRoleCommand(commandName, role string, args []string) error {
	cfg, _, err := resolveRoleConfig(role, args)
	if err != nil {
		return err
	}
	spec, err := buildDetachedSpec(commandName, cfg, args)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(spec.State.StateFile), 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(spec.State.LogFile), 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(spec.State.PIDFile), 0700); err != nil {
		return err
	}
	for _, path := range []string{spec.State.StateFile, spec.State.PIDFile} {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("detached process state already exists for %s", spec.State.ID)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	logFile, err := os.OpenFile(spec.State.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, spec.ChildArgs...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "TUBO_DETACHED_CHILD=1")
	configureDetachedCommand(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	spec.State.PID = cmd.Process.Pid
	spec.State.StartedAt = time.Now().UTC().Format(time.RFC3339)
	pidBytes := []byte(fmt.Sprintf("%d\n", spec.State.PID))
	if err := os.WriteFile(spec.State.PIDFile, pidBytes, 0600); err != nil {
		return err
	}
	stateBytes, err := json.MarshalIndent(spec.State, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(spec.State.StateFile, stateBytes, 0600); err != nil {
		return err
	}
	if err := waitForDetachedStart(cmd, spec.HealthURL, spec.State.LogFile, 5*time.Second); err != nil {
		_ = os.Remove(spec.State.PIDFile)
		_ = os.Remove(spec.State.StateFile)
		return err
	}
	printDetachedSummary(commandName, spec.State)
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

func buildDetachedSpec(commandName string, cfg cfgpkg.Config, args []string) (detachedSpec, error) {
	dataRoot := defaultTuboDataDir()
	var name, local, target, serviceName, statusAddr string
	switch commandName {
	case "attach":
		serviceName = cfg.Service.Name
		name = "attach-" + sanitizeProcessName(serviceName)
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
		return detachedSpec{}, fmt.Errorf("detach is not supported for %s", commandName)
	}
	if name == "" {
		return detachedSpec{}, fmt.Errorf("unable to derive detached process name for %s", commandName)
	}
	statePath := filepath.Join(dataRoot, "processes", name+".json")
	logPath := filepath.Join(dataRoot, "logs", name+".log")
	pidPath := filepath.Join(dataRoot, "run", name+".pid")
	statusURL := ""
	if statusAddr != "" {
		statusURL = "http://" + hostPortForHTTP(statusAddr) + "/healthz"
	}
	return detachedSpec{
		State: detachedProcessState{
			ID:        "process/" + name,
			Kind:      "process",
			Command:   commandName,
			Name:      name,
			Service:   serviceName,
			Local:     local,
			Target:    target,
			LogFile:   logPath,
			StateFile: statePath,
			PIDFile:   pidPath,
			StatusURL: statusURL,
		},
		ChildArgs: append([]string{commandName}, args...),
		HealthURL: statusURL,
	}, nil
}

func sanitizeProcessName(s string) string {
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

func waitForDetachedStart(cmd *exec.Cmd, healthURL, logPath string, timeout time.Duration) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Wait()
	}()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for {
		select {
		case err := <-errCh:
			if err == nil {
				return fmt.Errorf("detached process exited before becoming ready")
			}
			return fmt.Errorf("detached process exited early: %w\n%s", err, tailFile(logPath, 4096))
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

func tailFile(path string, max int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > max {
		b = b[len(b)-max:]
	}
	return string(b)
}

func printDetachedSummary(commandName string, state detachedProcessState) {
	switch commandName {
	case "attach":
		fmt.Printf("attached service %q\n", state.Service)
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

func processStateDir() string { return filepath.Join(defaultTuboDataDir(), "processes") }
func processLogDir() string   { return filepath.Join(defaultTuboDataDir(), "logs") }
func processRunDir() string   { return filepath.Join(defaultTuboDataDir(), "run") }

func listProcessViews(includeAll bool) ([]processView, error) {
	states, err := listProcessStates()
	if err != nil {
		return nil, err
	}
	items := make([]processView, 0, len(states))
	for _, state := range states {
		status := processStateStatus(state)
		if !includeAll && status != "running" {
			continue
		}
		items = append(items, processViewFromState(state, status))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

func listProcessStates() ([]detachedProcessState, error) {
	entries, err := os.ReadDir(processStateDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	states := make([]detachedProcessState, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(processStateDir(), entry.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var state detachedProcessState
		if err := json.Unmarshal(b, &state); err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, nil
}

func normalizeProcessRef(ref string) string {
	if strings.HasPrefix(ref, "process/") {
		return ref
	}
	return "process/" + ref
}

func loadProcessState(ref string) (detachedProcessState, string, error) {
	ref = normalizeProcessRef(ref)
	states, err := listProcessStates()
	if err != nil {
		return detachedProcessState{}, "", err
	}
	for _, state := range states {
		if state.ID == ref {
			return state, processStateStatus(state), nil
		}
	}
	return detachedProcessState{}, "", fmt.Errorf("unknown process %q", ref)
}

func processStateStatus(state detachedProcessState) string {
	if state.PID <= 0 {
		return "stale"
	}
	if _, err := os.Stat(state.PIDFile); err != nil {
		return "stale"
	}
	if pidRunning(state.PID) {
		return "running"
	}
	return "stale"
}

func processViewFromState(state detachedProcessState, status string) processView {
	return processView{
		ID:        state.ID,
		Name:      state.Name,
		Command:   state.Command,
		Status:    status,
		PID:       state.PID,
		Service:   state.Service,
		Local:     state.Local,
		Target:    state.Target,
		LogFile:   state.LogFile,
		StateFile: state.StateFile,
		PIDFile:   state.PIDFile,
		StatusURL: state.StatusURL,
		StartedAt: state.StartedAt,
	}
}

func printProcessesTable(items []processView) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCOMMAND\tSTATUS\tPID\tLOCAL\tTARGET")
	for _, item := range items {
		local := item.Local
		if local == "" {
			local = "-"
		}
		target := item.Target
		if target == "" {
			target = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", item.Name, item.Command, item.Status, item.PID, local, target)
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

func printLogTail(path string, lines int) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	parts := strings.Split(string(b), "\n")
	filtered := make([]string, 0, len(parts))
	for _, line := range parts {
		if line == "" {
			continue
		}
		filtered = append(filtered, line)
	}
	start := 0
	if lines > 0 && len(filtered) > lines {
		start = len(filtered) - lines
	}
	for _, line := range filtered[start:] {
		fmt.Println(line)
	}
	return nil
}

func followLogFile(ctx context.Context, path string) error {
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
				fmt.Print(string(buf))
				offset += int64(len(buf))
			}
		}
	}
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
	state, status, err := loadProcessState(fs.Arg(0))
	if err != nil {
		return err
	}
	if status != "running" {
		return fmt.Errorf("process %s is not running", state.ID)
	}
	if err := terminatePID(state.PID); err != nil {
		return err
	}
	if err := waitForProcessExit(state.PID, 5*time.Second); err != nil {
		if !*force {
			return err
		}
		if err := killPID(state.PID); err != nil {
			return err
		}
		if err := waitForProcessExit(state.PID, 2*time.Second); err != nil {
			return err
		}
	}
	_ = os.Remove(state.PIDFile)
	fmt.Printf("stopped %s\n", state.ID)
	return nil
}

func waitForProcessExit(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !pidRunning(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("process %d did not exit in time", pid)
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
	states, err := listProcessStates()
	if err != nil {
		return err
	}
	removed := 0
	for _, state := range states {
		if processStateStatus(state) == "running" {
			continue
		}
		for _, path := range []string{state.StateFile, state.PIDFile, state.LogFile} {
			if path == "" {
				continue
			}
			_ = os.Remove(path)
		}
		removed++
	}
	fmt.Printf("removed %d stale process artifacts\n", removed)
	return nil
}

type serviceResource struct {
	Kind             string   `json:"kind"`
	Name             string   `json:"name"`
	PeerID           string   `json:"peer_id"`
	Addresses        []string `json:"addresses"`
	DirectAddresses  []string `json:"direct_addresses"`
	RelayedAddresses []string `json:"relayed_addresses"`
	Status           string   `json:"status"`
	Path             string   `json:"path"`
	TTLSeconds       int64    `json:"ttl_seconds"`
	ExpiresInSeconds int64    `json:"expires_in_seconds"`
	Capabilities     []string `json:"capabilities"`
	RegisteredAt     string   `json:"registered_at"`
}

type discoveryLookupResult struct {
	Services []serviceResource        `json:"services"`
	Messages []string                 `json:"messages"`
	Mode     string                   `json:"mode"`
	Metadata *discoveryquery.Metadata `json:"metadata,omitempty"`
}

type serviceWatchEvent struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	PeerID string `json:"peer_id"`
	Path   string `json:"path"`
}

type processView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Command   string `json:"command"`
	Status    string `json:"status"`
	PID       int    `json:"pid"`
	Service   string `json:"service,omitempty"`
	Local     string `json:"local,omitempty"`
	Target    string `json:"target,omitempty"`
	LogFile   string `json:"log_file"`
	StateFile string `json:"state_file"`
	PIDFile   string `json:"pid_file"`
	StatusURL string `json:"status_url,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
}

type servicesAdminResponse struct {
	Count int               `json:"count"`
	Items []serviceResource `json:"items"`
}

type connectAttempt struct {
	Path   string `json:"path"`
	Addr   string `json:"addr"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type connectResult struct {
	Service  string           `json:"service"`
	Local    string           `json:"local"`
	Path     string           `json:"path"`
	Selected string           `json:"selected_addr,omitempty"`
	Direct   string           `json:"direct,omitempty"`
	Relay    string           `json:"relay,omitempty"`
	Attempts []connectAttempt `json:"attempts,omitempty"`
}

type connectCandidate struct {
	Path string
	Addr string
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
	serviceName := ""
	parseArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		serviceName = args[0]
		parseArgs = args[1:]
	}
	if err := fs.Parse(parseArgs); err != nil {
		return err
	}
	if serviceName == "" {
		if fs.NArg() != 1 {
			return errors.New("usage: tubo connect <service-name> [--local host:port] [flags]")
		}
		serviceName = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return errors.New("usage: tubo connect <service-name> [--local host:port] [flags]")
	}
	result, serviceView, err := discoverService(*configPath, serviceName, *timeout, *cachedOnly, *live)
	if err != nil {
		return fmt.Errorf("service %q not found; run `tubo get services` to inspect available services", serviceName)
	}
	serviceView = normalizeServiceResource(serviceView)
	listenAddr, localURL, err := chooseConnectLocal(*local)
	if err != nil {
		return err
	}
	cfg, err := loadDiscoveryConfig(*configPath)
	if err != nil {
		return err
	}
	bridgeCfg := bridge.Config{
		Listen:         listenAddr,
		Seed:           cfg.Node.Seed,
		P2PListen:      cfg.Node.P2PListen,
		PrivateKeyFile: cfg.Network.PrivateKeyFile,
		PrivateKeyB64:  cfg.Network.PrivateKeyB64,
	}
	if bridgeCfg.P2PListen == "" {
		bridgeCfg.P2PListen = "/ip4/127.0.0.1/tcp/0"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	selectedPath, selectedAddr, attempts, app, err := connectBridge(ctx, bridgeCfg, serviceView)
	if err != nil {
		return err
	}
	directMsg := connectDirectMessage(serviceView, attempts, selectedPath)
	relayMsg := connectRelayMessage(serviceView, selectedAddr, selectedPath)
	output := connectResult{Service: serviceName, Local: localURL, Path: selectedPath, Selected: selectedAddr, Direct: directMsg, Relay: relayMsg, Attempts: attempts}
	if *jsonOut {
		if err := printJSON(output); err != nil {
			return err
		}
	} else {
		printMessages(result.Messages)
		fmt.Printf("connected to service %q\n", serviceName)
		fmt.Printf("local: %s\n", localURL)
		fmt.Printf("path: %s\n", selectedPath)
		if directMsg != "" {
			fmt.Printf("direct: %s\n", directMsg)
		}
		if relayMsg != "" {
			fmt.Printf("relay: %s\n", relayMsg)
		}
		fmt.Println("press Ctrl+C to stop")
	}
	return app.Start(ctx)
}

func chooseConnectLocal(local string) (listenAddr string, localURL string, err error) {
	if local != "" {
		if _, _, splitErr := net.SplitHostPort(local); splitErr != nil {
			return "", "", fmt.Errorf("invalid --local %q: %w", local, splitErr)
		}
		return local, "http://" + local, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", err
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr, "http://" + addr, nil
}

func connectBridge(ctx context.Context, base bridge.Config, service serviceResource) (string, string, []connectAttempt, *bridge.App, error) {
	candidates, err := connectCandidates(service)
	if err != nil {
		return "", "", nil, nil, err
	}
	attempts := make([]connectAttempt, 0, len(candidates))
	for _, candidate := range candidates {
		cfg := base
		cfg.ServiceAddr = candidate.Addr
		app, err := bridge.New(ctx, cfg)
		if err != nil {
			attempts = append(attempts, connectAttempt{Path: candidate.Path, Addr: candidate.Addr, Status: "failed", Error: err.Error()})
			continue
		}
		attempts = append(attempts, connectAttempt{Path: candidate.Path, Addr: candidate.Addr, Status: "selected"})
		return candidate.Path, candidate.Addr, attempts, app, nil
	}
	return "", "", attempts, nil, fmt.Errorf("connect to service %q failed: %s", service.Name, summarizeConnectAttempts(attempts))
}

func connectCandidates(service serviceResource) ([]connectCandidate, error) {
	service = normalizeServiceResource(service)
	if len(service.DirectAddresses) == 0 && len(service.RelayedAddresses) == 0 {
		return nil, fmt.Errorf("service %q has no announced addresses", service.Name)
	}
	candidates := make([]connectCandidate, 0, len(service.DirectAddresses)+len(service.RelayedAddresses))
	for _, addr := range service.DirectAddresses {
		if isUnusableDirectAddress(addr) {
			continue
		}
		candidates = append(candidates, connectCandidate{Path: "direct", Addr: addr})
	}
	for _, addr := range service.RelayedAddresses {
		candidates = append(candidates, connectCandidate{Path: "relayed", Addr: addr})
	}
	return candidates, nil
}

func isUnusableDirectAddress(addr string) bool {
	return strings.Contains(addr, "/ip4/127.") || strings.Contains(addr, "/ip4/0.0.0.0/") || strings.Contains(addr, "/ip6/::1/") || strings.Contains(addr, "/ip6/::/") || strings.Contains(addr, "/dns4/localhost/") || strings.Contains(addr, "/dns6/localhost/")
}

func summarizeConnectAttempts(attempts []connectAttempt) string {
	parts := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		if attempt.Status == "selected" {
			parts = append(parts, fmt.Sprintf("%s succeeded", attempt.Path))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s failed (%s)", attempt.Path, attempt.Error))
	}
	if len(parts) == 0 {
		return "no dial attempts"
	}
	return strings.Join(parts, "; ")
}

func connectDirectMessage(service serviceResource, attempts []connectAttempt, selectedPath string) string {
	service = normalizeServiceResource(service)
	usableDirect := 0
	for _, addr := range service.DirectAddresses {
		if !isUnusableDirectAddress(addr) {
			usableDirect++
		}
	}
	if len(service.DirectAddresses) == 0 {
		return "unavailable, no direct addresses advertised"
	}
	if usableDirect == 0 {
		return "unavailable, only loopback/unspecified direct addresses advertised"
	}
	if selectedPath == "direct" {
		return "selected"
	}
	for _, attempt := range attempts {
		if attempt.Path == "direct" && attempt.Status == "failed" {
			return "attempted, failed"
		}
	}
	return "available"
}

func connectRelayMessage(service serviceResource, selectedAddr, selectedPath string) string {
	service = normalizeServiceResource(service)
	if len(service.RelayedAddresses) == 0 {
		return ""
	}
	if selectedPath == "direct" {
		return "available as fallback"
	}
	if selectedAddr != "" {
		return selectedAddr
	}
	return "selected"
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
		return errors.New("usage: tubo get <services|service/name> [flags]")
	}
	resource := args[0]
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	timeout := fs.Duration("timeout", defaultDiscoveryTimeout, "")
	jsonOut := fs.Bool("json", false, "")
	cachedOnly := fs.Bool("cached-only", false, "")
	live := fs.Bool("live", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
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
	}
	result, err := discoverServices(*configPath, *timeout, *cachedOnly, *live)
	if err != nil {
		return err
	}
	switch {
	case resource == "services":
		if *jsonOut {
			return printJSON(struct {
				Mode     string                   `json:"mode"`
				Messages []string                 `json:"messages"`
				Metadata *discoveryquery.Metadata `json:"metadata,omitempty"`
				Count    int                      `json:"count"`
				Items    []serviceResource        `json:"items"`
			}{Mode: result.Mode, Messages: result.Messages, Metadata: result.Metadata, Count: len(result.Services), Items: result.Services})
		}
		printMessages(result.Messages)
		printServicesTable(result.Services)
		return nil
	case strings.HasPrefix(resource, "service/"):
		name := strings.TrimPrefix(resource, "service/")
		result, service, err := discoverService(*configPath, name, *timeout, *cachedOnly, *live)
		if err != nil {
			return err
		}
		if *jsonOut {
			return printJSON(struct {
				Mode     string                   `json:"mode"`
				Messages []string                 `json:"messages"`
				Metadata *discoveryquery.Metadata `json:"metadata,omitempty"`
				Item     serviceResource          `json:"item"`
			}{Mode: result.Mode, Messages: result.Messages, Metadata: result.Metadata, Item: service})
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
		return errors.New("usage: tubo describe <service/name|process/name> [flags]")
	}
	resource := args[0]
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
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	result, service, err := discoverService(*configPath, strings.TrimPrefix(resource, "service/"), *timeout, *cachedOnly, *live)
	if err != nil {
		return err
	}
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
	_ = fs.Bool("json", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	result, service, err := discoverService(*configPath, strings.TrimPrefix(resource, "service/"), *timeout, *cachedOnly, *live)
	if err != nil {
		return err
	}
	return printJSON(struct {
		Mode     string                   `json:"mode"`
		Messages []string                 `json:"messages"`
		Metadata *discoveryquery.Metadata `json:"metadata,omitempty"`
		Item     serviceResource          `json:"item"`
	}{Mode: result.Mode, Messages: result.Messages, Metadata: result.Metadata, Item: service})
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
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	cfg, err := loadDiscoveryConfig(*configPath)
	if err != nil {
		return err
	}
	fmt.Printf("watching services for %s...\n", timeout.String())
	if !*live {
		if services, adminAddr, err := fetchLocalServiceCache(cfg); err == nil {
			fmt.Printf("using local cache from edge admin at %s\n", adminAddr)
			for _, service := range services {
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
	_, err = observeServices(cfg, *timeout, func(event serviceWatchEvent) {
		fmt.Printf("%s\tservice/%s\tpeer=%s\tpath=%s\n", strings.ToUpper(event.Type), event.Name, event.PeerID, event.Path)
	})
	return err
}

func discoverServices(configPath string, timeout time.Duration, cachedOnly, live bool) (discoveryLookupResult, error) {
	cfg, err := loadDiscoveryConfig(configPath)
	if err != nil {
		return discoveryLookupResult{}, err
	}
	if !live {
		if services, adminAddr, err := fetchLocalServiceCache(cfg); err == nil {
			return discoveryLookupResult{
				Services: services,
				Messages: []string{fmt.Sprintf("using local cache from edge admin at %s", adminAddr)},
				Mode:     "cache",
			}, nil
		}
		if cachedOnly {
			return discoveryLookupResult{}, errors.New("no local cache found")
		}
		if services, metadata, messages, err := fetchRemoteServiceCache(cfg, timeout); err == nil {
			messages = append([]string{"no local cache found"}, messages...)
			if len(services) > 0 {
				return discoveryLookupResult{Services: services, Messages: messages, Mode: "remote-query", Metadata: metadata}, nil
			}
			services, obsErr := observeServices(cfg, timeout, nil)
			if obsErr != nil {
				return discoveryLookupResult{}, obsErr
			}
			messages = append(messages, fmt.Sprintf("starting temporary observer for %s...", timeout.String()))
			return discoveryLookupResult{Services: services, Messages: messages, Mode: "live"}, nil
		} else {
			messages := []string{"no local cache found", fmt.Sprintf("remote discovery query failed: %v", err)}
			services, obsErr := observeServices(cfg, timeout, nil)
			if obsErr != nil {
				return discoveryLookupResult{}, obsErr
			}
			messages = append(messages, fmt.Sprintf("starting temporary observer for %s...", timeout.String()))
			return discoveryLookupResult{Services: services, Messages: messages, Mode: "live"}, nil
		}
	}
	services, err := observeServices(cfg, timeout, nil)
	if err != nil {
		return discoveryLookupResult{}, err
	}
	messages := []string{fmt.Sprintf("starting temporary observer for %s...", timeout.String())}
	if !live {
		messages = append([]string{"no local cache found"}, messages...)
	}
	return discoveryLookupResult{Services: services, Messages: messages, Mode: "live"}, nil
}

func discoverService(configPath, serviceName string, timeout time.Duration, cachedOnly, live bool) (discoveryLookupResult, serviceResource, error) {
	cfg, err := loadDiscoveryConfig(configPath)
	if err != nil {
		return discoveryLookupResult{}, serviceResource{}, err
	}
	if !live {
		if services, adminAddr, err := fetchLocalServiceCache(cfg); err == nil {
			service, err := requireService(services, serviceName)
			if err == nil {
				return discoveryLookupResult{Services: []serviceResource{service}, Messages: []string{fmt.Sprintf("using local cache from edge admin at %s", adminAddr)}, Mode: "cache"}, service, nil
			}
		}
		if cachedOnly {
			return discoveryLookupResult{}, serviceResource{}, errors.New("no local cache found")
		}
		if service, metadata, messages, err := fetchRemoteService(cfg, serviceName, timeout); err == nil {
			messages = append([]string{"no local cache found"}, messages...)
			return discoveryLookupResult{Services: []serviceResource{service}, Messages: messages, Mode: "remote-query", Metadata: metadata}, service, nil
		} else {
			messages := []string{"no local cache found", fmt.Sprintf("remote discovery query failed: %v", err)}
			services, obsErr := observeServices(cfg, timeout, nil)
			if obsErr != nil {
				return discoveryLookupResult{}, serviceResource{}, obsErr
			}
			service, obsErr := requireService(services, serviceName)
			if obsErr != nil {
				return discoveryLookupResult{}, serviceResource{}, obsErr
			}
			messages = append(messages, fmt.Sprintf("starting temporary observer for %s...", timeout.String()))
			return discoveryLookupResult{Services: []serviceResource{service}, Messages: messages, Mode: "live"}, service, nil
		}
	}
	services, err := observeServices(cfg, timeout, nil)
	if err != nil {
		return discoveryLookupResult{}, serviceResource{}, err
	}
	service, err := requireService(services, serviceName)
	if err != nil {
		return discoveryLookupResult{}, serviceResource{}, err
	}
	messages := []string{fmt.Sprintf("starting temporary observer for %s...", timeout.String())}
	if !live {
		messages = append([]string{"no local cache found"}, messages...)
	}
	return discoveryLookupResult{Services: []serviceResource{service}, Messages: messages, Mode: "live"}, service, nil
}

func loadDiscoveryConfig(path string) (cfgpkg.Config, error) {
	cfg, err := cfgpkg.LoadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfgpkg.Config{}, fmt.Errorf("config not found at %s; run `tubo join --relay ... --swarm-key ...` first or pass --config", path)
		}
		return cfgpkg.Config{}, err
	}
	if cfg.Network.PrivateKeyFile == "" && cfg.Network.PrivateKeyB64 == "" {
		return cfgpkg.Config{}, errors.New("config is missing swarm key settings; run `tubo join --relay ... --swarm-key ...` first")
	}
	if len(cfg.Network.BootstrapPeers) == 0 && len(cfg.Network.RelayPeers) == 0 {
		return cfgpkg.Config{}, errors.New("config is missing relay/bootstrap peers; run `tubo join --relay ... --swarm-key ...` first")
	}
	return cfg, nil
}

func fetchLocalServiceCache(cfg cfgpkg.Config) ([]serviceResource, string, error) {
	edgeCfg := cfgpkg.Merge(cfgpkg.Defaults("edge"), cfg)
	adminAddr := edgeCfg.Edge.AdminListen
	if adminAddr == "" {
		return nil, "", errors.New("edge admin listen address is not configured")
	}
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get("http://" + hostPortForHTTP(adminAddr) + "/services")
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("edge admin status %d", resp.StatusCode)
	}
	var payload servicesAdminResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, "", err
	}
	if payload.Items == nil {
		return nil, "", errors.New("edge admin did not return service details")
	}
	for i := range payload.Items {
		payload.Items[i] = normalizeServiceResource(payload.Items[i])
	}
	sortServiceResources(payload.Items)
	return payload.Items, adminAddr, nil
}

func fetchRemoteServiceCache(cfg cfgpkg.Config, timeout time.Duration) ([]serviceResource, *discoveryquery.Metadata, []string, error) {
	peers := uniqueStrings(append(append([]string{}, cfg.Network.BootstrapPeers...), cfg.Network.RelayPeers...))
	if len(peers) == 0 {
		return nil, nil, nil, errors.New("no bootstrap or relay peers configured")
	}
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}
	psk, _, err := p2p.LoadPrivateNetworkPSK(cfg.Network.PrivateKeyFile, cfg.Network.PrivateKeyB64)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load private network key: %w", err)
	}
	h, err := p2p.NewHostWithSeedAndPSK("/ip4/127.0.0.1/tcp/0", "", psk)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create remote query host: %w", err)
	}
	defer h.Close()
	var lastErr error
	for _, raw := range peers {
		info, err := p2p.AddrInfoFromString(raw)
		if err != nil {
			lastErr = fmt.Errorf("invalid bootstrap peer %q: %w", raw, err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		resp, err := discoveryquery.ListServices(ctx, h, info)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.Error != "" {
			lastErr = errors.New(resp.Error)
			continue
		}
		services := make([]serviceResource, 0, len(resp.Services))
		for _, service := range resp.Services {
			services = append(services, serviceResourceFromQueryService(service))
		}
		sortServiceResources(services)
		metadata := resp.Metadata
		messages := []string{fmt.Sprintf("querying discovery cache from %s %s", metadata.ServedByRole, metadata.ServedBy), fmt.Sprintf("received %d services", len(services))}
		return services, &metadata, messages, nil
	}
	if lastErr == nil {
		lastErr = errors.New("remote discovery query failed")
	}
	return nil, nil, nil, lastErr
}

func fetchRemoteService(cfg cfgpkg.Config, serviceName string, timeout time.Duration) (serviceResource, *discoveryquery.Metadata, []string, error) {
	peers := uniqueStrings(append(append([]string{}, cfg.Network.BootstrapPeers...), cfg.Network.RelayPeers...))
	if len(peers) == 0 {
		return serviceResource{}, nil, nil, errors.New("no bootstrap or relay peers configured")
	}
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}
	psk, _, err := p2p.LoadPrivateNetworkPSK(cfg.Network.PrivateKeyFile, cfg.Network.PrivateKeyB64)
	if err != nil {
		return serviceResource{}, nil, nil, fmt.Errorf("load private network key: %w", err)
	}
	h, err := p2p.NewHostWithSeedAndPSK("/ip4/127.0.0.1/tcp/0", "", psk)
	if err != nil {
		return serviceResource{}, nil, nil, fmt.Errorf("create remote query host: %w", err)
	}
	defer h.Close()
	var lastErr error
	for _, raw := range peers {
		info, err := p2p.AddrInfoFromString(raw)
		if err != nil {
			lastErr = fmt.Errorf("invalid bootstrap peer %q: %w", raw, err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		resp, err := discoveryquery.GetService(ctx, h, info, serviceName)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.Error != "" {
			lastErr = errors.New(resp.Error)
			continue
		}
		if resp.Service == nil {
			lastErr = errors.New("service not found")
			continue
		}
		service := serviceResourceFromQueryService(*resp.Service)
		metadata := resp.Metadata
		messages := []string{fmt.Sprintf("querying discovery cache from %s %s", metadata.ServedByRole, metadata.ServedBy), fmt.Sprintf("received service %s", service.Name)}
		return service, &metadata, messages, nil
	}
	if lastErr == nil {
		lastErr = errors.New("remote discovery query failed")
	}
	return serviceResource{}, nil, nil, lastErr
}

func observeServices(cfg cfgpkg.Config, timeout time.Duration, onEvent func(serviceWatchEvent)) ([]serviceResource, error) {
	peers := uniqueStrings(append(append([]string{}, cfg.Network.BootstrapPeers...), cfg.Network.RelayPeers...))
	if len(peers) == 0 {
		return nil, errors.New("no bootstrap or relay peers configured")
	}
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}
	psk, _, err := p2p.LoadPrivateNetworkPSK(cfg.Network.PrivateKeyFile, cfg.Network.PrivateKeyB64)
	if err != nil {
		return nil, fmt.Errorf("load private network key: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	h, err := p2p.NewHostWithSeedAndPSK("/ip4/127.0.0.1/tcp/0", "", psk)
	if err != nil {
		return nil, fmt.Errorf("create observer host: %w", err)
	}
	defer h.Close()
	ps, err := pubsub.NewGossipSub(ctx, h, pubsub.WithFloodPublish(true))
	if err != nil {
		return nil, fmt.Errorf("create observer gossipsub: %w", err)
	}
	topic, err := ps.Join(discovery.DiscoveryTopic)
	if err != nil {
		return nil, fmt.Errorf("join discovery topic: %w", err)
	}
	cache := discovery.NewCache(30*time.Second, time.Second)
	defer cache.Stop()
	sub := discovery.NewPubSubSubscriber(topic, cache)
	stopCh := sub.Start(ctx)
	defer close(stopCh)
	for _, raw := range peers {
		info, err := p2p.AddrInfoFromString(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid bootstrap peer %q: %w", raw, err)
		}
		connectCtx, cancelConnect := context.WithTimeout(ctx, 5*time.Second)
		_ = h.Connect(connectCtx, info)
		cancelConnect()
	}
	if onEvent != nil {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case ev := <-sub.OnEvents():
					watchEvent := serviceWatchEvent{Type: ev.Type, Name: ev.ServiceName, PeerID: ev.PeerID.String(), Path: "unknown"}
					if entry, ok := cache.Resolve(ev.ServiceName); ok {
						watchEvent.Path = servicePathFromAddresses(entry.Addresses)
					}
					onEvent(watchEvent)
				}
			}
		}()
	}
	<-ctx.Done()
	if err := ctx.Err(); err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return nil, err
	}
	services := serviceResourcesFromEntries(cache.List())
	sortServiceResources(services)
	return services, nil
}

func serviceResourcesFromEntries(entries []*discovery.ServiceEntry) []serviceResource {
	services := make([]serviceResource, 0, len(entries))
	for _, entry := range entries {
		services = append(services, serviceResourceFromEntry(entry))
	}
	return services
}

func serviceResourceFromEntry(entry *discovery.ServiceEntry) serviceResource {
	expiresIn := time.Until(entry.Registered.Add(entry.TTL))
	if expiresIn < 0 {
		expiresIn = 0
	}
	return normalizeServiceResource(serviceResource{
		Kind:             "service",
		Name:             entry.ServiceName,
		PeerID:           entry.PeerID.String(),
		Addresses:        append([]string(nil), entry.Addresses...),
		Status:           "online",
		TTLSeconds:       int64(entry.TTL.Seconds()),
		ExpiresInSeconds: int64(expiresIn.Seconds()),
		Capabilities:     []string{},
		RegisteredAt:     entry.Registered.Format(time.RFC3339),
	})
}

func serviceResourceFromQueryService(service discoveryquery.Service) serviceResource {
	return normalizeServiceResource(serviceResource{
		Kind:             service.Kind,
		Name:             service.Name,
		PeerID:           service.PeerID,
		Addresses:        append([]string(nil), service.Addresses...),
		DirectAddresses:  append([]string(nil), service.DirectAddresses...),
		RelayedAddresses: append([]string(nil), service.RelayedAddresses...),
		Status:           service.Status,
		Path:             service.Path,
		TTLSeconds:       service.TTLSeconds,
		ExpiresInSeconds: service.ExpiresInSeconds,
		Capabilities:     append([]string(nil), service.Capabilities...),
		RegisteredAt:     service.RegisteredAt,
	})
}

func normalizeServiceResource(service serviceResource) serviceResource {
	addresses := append([]string(nil), service.Addresses...)
	if len(addresses) == 0 {
		addresses = append(addresses, service.DirectAddresses...)
		addresses = append(addresses, service.RelayedAddresses...)
	}
	direct, relayed := splitServiceAddresses(addresses)
	service.Addresses = addresses
	service.DirectAddresses = direct
	service.RelayedAddresses = relayed
	service.Path = servicePathFromAddresses(addresses)
	if service.Capabilities == nil {
		service.Capabilities = []string{}
	}
	return service
}

func splitServiceAddresses(addresses []string) (direct []string, relayed []string) {
	for _, addr := range addresses {
		if strings.Contains(addr, "/p2p-circuit/") {
			relayed = append(relayed, addr)
			continue
		}
		direct = append(direct, addr)
	}
	return direct, relayed
}

func servicePathFromAddresses(addresses []string) string {
	direct, relayed := splitServiceAddresses(addresses)
	switch {
	case len(direct) > 0:
		return "direct"
	case len(relayed) > 0:
		return "relayed"
	default:
		return "unknown"
	}
}

func sortServiceResources(items []serviceResource) {
	sort.Slice(items, func(i, j int) bool {
		return items[i].Name < items[j].Name
	})
}

func requireService(services []serviceResource, name string) (serviceResource, error) {
	for _, service := range services {
		if service.Name == name {
			return service, nil
		}
	}
	return serviceResource{}, fmt.Errorf("service %q not found", name)
}

func printServicesTable(services []serviceResource) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tPATH\tPEER\tCAPABILITIES")
	for _, service := range services {
		caps := "-"
		if len(service.Capabilities) > 0 {
			caps = strings.Join(service.Capabilities, ",")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", service.Name, service.Status, service.Path, service.PeerID, caps)
	}
	_ = w.Flush()
}

func printServiceDescription(service serviceResource, messages []string) {
	service = normalizeServiceResource(service)
	fmt.Printf("Name: %s\n", service.Name)
	fmt.Printf("Kind: %s\n", service.Kind)
	fmt.Printf("Status: %s\n", service.Status)
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
	if args[0] == "topology" {
		fs := flag.NewFlagSet("init topology", 0)
		out := fs.String("out", "topology.yaml", "")
		force := fs.Bool("force", false, "")
		_ = fs.Parse(args[1:])
		return write(*out, topoExample(), *force)
	}
	role := args[0]
	fs := flag.NewFlagSet("init", 0)
	out := fs.String("out", role+".yaml", "")
	force := fs.Bool("force", false, "")
	_ = fs.Parse(args[1:])
	return cfgpkg.WriteFile(*out, cfgpkg.Defaults(role), *force)
}

type Topology struct {
	Swarm struct {
		KeyFile string `yaml:"key_file"`
	} `yaml:"swarm"`
	Nodes map[string]map[string]any `yaml:"nodes"`
}

func topology(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	fs := flag.NewFlagSet("topology", 0)
	path := fs.String("config", "topology.yaml", "")
	out := fs.String("out", "generated", "")
	_ = fs.Parse(args[1:])
	b, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	var t Topology
	if err := yaml.Unmarshal(b, &t); err != nil {
		return err
	}
	if args[0] == "commands" {
		for name, n := range t.Nodes {
			fmt.Printf("tubo %s run --config generated/%s.yaml\n", n["role"], name)
		}
		return nil
	}
	if args[0] != "render" {
		return usage()
	}
	if err := os.MkdirAll(*out, 0755); err != nil {
		return err
	}
	peerIDs := make(map[string]string, len(t.Nodes))
	for name, n := range t.Nodes {
		seed := str(n, "seed", "")
		if seed == "" {
			continue
		}
		peerID, err := p2p.PeerIDFromSeed(seed)
		if err != nil {
			return fmt.Errorf("topology %s peer id: %w", name, err)
		}
		peerIDs[name] = peerID.String()
	}
	resolveRelayAddr := func(relayName string) (string, error) {
		relayNode, ok := t.Nodes[relayName]
		if !ok {
			return "", fmt.Errorf("unknown relay %q", relayName)
		}
		publicAddr := str(relayNode, "public_addr", "")
		if publicAddr == "" {
			return "", fmt.Errorf("relay %q is missing public_addr", relayName)
		}
		peerID := peerIDs[relayName]
		if peerID == "" {
			return "", fmt.Errorf("relay %q is missing seed", relayName)
		}
		if strings.Contains(publicAddr, "/p2p/") {
			return publicAddr, nil
		}
		return fmt.Sprintf("%s/p2p/%s", publicAddr, peerID), nil
	}
	for name, n := range t.Nodes {
		role := fmt.Sprint(n["role"])
		c := cfgpkg.Defaults(role)
		c.Network.PrivateKeyFile = t.Swarm.KeyFile
		c.Node.Seed = str(n, "seed", c.Node.Seed)
		relayRef := str(n, "relay", "")
		if relayRef != "" {
			relayAddr, err := resolveRelayAddr(relayRef)
			if err != nil {
				return err
			}
			c.Network.BootstrapPeers = []string{relayAddr}
			c.Network.RelayPeers = []string{relayAddr}
		}
		switch role {
		case "relay":
			c.Relay.PublicAddr = str(n, "public_addr", "")
		case "edge":
			c.Edge.Listen = str(n, "listen", c.Edge.Listen)
			c.Edge.AdminListen = str(n, "admin_listen", c.Edge.AdminListen)
		case "service":
			c.Service.Name = str(n, "service_name", name)
			c.Service.Target = str(n, "target", "")
		}
		if err := cfgpkg.WriteFile(filepath.Join(*out, name+".yaml"), c, true); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(*out, "docker-compose.generated.yaml"), []byte("services: {}\n"), 0644)
}
func str(m map[string]any, k, d string) string {
	if v, ok := m[k]; ok {
		return fmt.Sprint(v)
	}
	return d
}
func write(path, s string, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s exists (use --force)", path)
		}
	}
	return os.WriteFile(path, []byte(s), 0600)
}
func topoExample() string {
	return "swarm:\n  key_file: ./swarm.key\nnodes:\n  relay:\n    role: relay\n    seed: public-relay-seed\n    public_addr: /ip4/1.2.3.4/tcp/4001\n  edge:\n    role: edge\n    seed: edge-seed\n    listen: :8443\n    admin_listen: 127.0.0.1:8444\n    relay: relay\n  lmstudio:\n    role: service\n    seed: service-lmstudio-seed\n    service_name: lmstudio\n    target: http://127.0.0.1:1234\n    relay: relay\n"
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
