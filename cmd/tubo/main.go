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
	"gopkg.in/yaml.v3"
	"net"
	"net/http"
	"os"
	"os/signal"
	"p2p-api-tunnel/internal/app/bridge"
	"p2p-api-tunnel/internal/app/edge"
	"p2p-api-tunnel/internal/app/relay"
	"p2p-api-tunnel/internal/app/service"
	cfgpkg "p2p-api-tunnel/internal/config"
	"p2p-api-tunnel/internal/discovery"
	"p2p-api-tunnel/internal/p2p"
	iversion "p2p-api-tunnel/internal/version"
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
		return runRole(role, roleArgs)
	}
	switch args[0] {
	case "join":
		return joinCmd(args[1:])
	case "get":
		return getCmd(args[1:])
	case "describe":
		return describeCmd(args[1:])
	case "inspect":
		return inspectCmd(args[1:])
	case "watch":
		return watchCmd(args[1:])
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
			return "relay", args[2:], true, nil
		}
		return "relay", args[1:], true, nil
	case "edge", "service", "bridge":
		if len(args) < 2 || args[1] != "run" {
			return "", nil, false, usage()
		}
		return args[0], args[2:], true, nil
	case "gateway":
		return "edge", args[1:], true, nil
	case "attach":
		attachArgs, err := rewriteAttachArgs(args[1:])
		if err != nil {
			return "", nil, false, err
		}
		return "service", attachArgs, true, nil
	default:
		return "", nil, false, nil
	}
}

func rewriteAttachArgs(args []string) ([]string, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return args, nil
	}
	if hasLongFlag(args[1:], "--target") {
		return nil, errors.New("attach target provided both positionally and via --target")
	}
	return append([]string{"--target", args[0]}, args[1:]...), nil
}

func hasLongFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}
	return false
}

func usage() error {
	return errors.New("usage: tubo <attach|gateway|relay> [flags] | tubo <relay|edge|service|bridge> run [flags] | tubo join --relay <multiaddr> --swarm-key <path> [flags] | tubo get <services|service/name> [flags] | tubo describe service/name [flags] | tubo inspect service/name [flags] | tubo watch services [flags] | init <role|topology> | keygen swarm | id from-seed | config <print|validate> | doctor | topology <render|commands> | version")
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
func runRole(role string, args []string) error {
	path, flags, err := roleFlags(role, args)
	if err != nil {
		return err
	}
	c, err := cfgpkg.Effective(role, path, os.Getenv, flags)
	if err != nil {
		return err
	}
	if err := cfgpkg.Validate(c); err != nil {
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

type joinResult struct {
	ConfigPath     string   `json:"config_path"`
	SwarmKeyPath   string   `json:"swarm_key_path"`
	RelayPeers     []string `json:"relay_peers"`
	BootstrapPeers []string `json:"bootstrap_peers"`
	Checked        bool     `json:"checked"`
}

func joinCmd(args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	var relayPeers []string
	fs.Var(csvFlag{&relayPeers}, "relay", "")
	swarmKeyPath := fs.String("swarm-key", "", "")
	swarmKeyB64 := fs.String("swarm-key-b64", "", "")
	configDir := fs.String("config-dir", defaultTuboConfigDir(), "")
	force := fs.Bool("force", false, "")
	jsonOut := fs.Bool("json", false, "")
	check := fs.Bool("check", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	relayPeers = uniqueStrings(relayPeers)
	if len(relayPeers) == 0 {
		return errors.New("join requires at least one --relay <multiaddr>")
	}
	if (*swarmKeyPath == "" && *swarmKeyB64 == "") || (*swarmKeyPath != "" && *swarmKeyB64 != "") {
		return errors.New("join requires exactly one of --swarm-key or --swarm-key-b64")
	}
	for _, relayPeer := range relayPeers {
		if _, err := multiaddr.NewMultiaddr(relayPeer); err != nil {
			return fmt.Errorf("join relay %q: %w", relayPeer, err)
		}
	}
	keyData, err := loadJoinSwarmKey(*swarmKeyPath, *swarmKeyB64)
	if err != nil {
		return err
	}
	if err := validateSwarmKeyData(keyData); err != nil {
		return err
	}
	if *check {
		if err := checkJoinRelayPeers(relayPeers); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(*configDir, 0700); err != nil {
		return err
	}
	configPath := filepath.Join(*configDir, "config.yaml")
	installedKeyPath := filepath.Join(*configDir, "swarm.key")
	if !*force {
		for _, path := range []string{configPath, installedKeyPath} {
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s exists (use --force)", path)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	existing, err := cfgpkg.LoadFile(configPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
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
		return err
	}
	if err := os.WriteFile(installedKeyPath, keyData, 0600); err != nil {
		return err
	}
	if err := os.WriteFile(configPath, b, 0600); err != nil {
		return err
	}
	result := joinResult{
		ConfigPath:     configPath,
		SwarmKeyPath:   installedKeyPath,
		RelayPeers:     relayPeers,
		BootstrapPeers: relayPeers,
		Checked:        *check,
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	fmt.Println("joined swarm config")
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
	return nil
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

type serviceResource struct {
	Kind             string   `json:"kind"`
	Name             string   `json:"name"`
	PeerID           string   `json:"peer_id"`
	Addresses        []string `json:"addresses"`
	Status           string   `json:"status"`
	Path             string   `json:"path"`
	TTLSeconds       int64    `json:"ttl_seconds"`
	ExpiresInSeconds int64    `json:"expires_in_seconds"`
	Capabilities     []string `json:"capabilities"`
	RegisteredAt     string   `json:"registered_at"`
}

type discoveryLookupResult struct {
	Services []serviceResource `json:"services"`
	Messages []string          `json:"messages"`
	Mode     string            `json:"mode"`
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

func getCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tubo get <services|service/name> [flags]")
	}
	resource := args[0]
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	timeout := fs.Duration("timeout", 5*time.Second, "")
	jsonOut := fs.Bool("json", false, "")
	cachedOnly := fs.Bool("cached-only", false, "")
	live := fs.Bool("live", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	result, err := discoverServices(*configPath, *timeout, *cachedOnly, *live)
	if err != nil {
		return err
	}
	switch {
	case resource == "services":
		if *jsonOut {
			return printJSON(struct {
				Mode     string            `json:"mode"`
				Messages []string          `json:"messages"`
				Count    int               `json:"count"`
				Items    []serviceResource `json:"items"`
			}{Mode: result.Mode, Messages: result.Messages, Count: len(result.Services), Items: result.Services})
		}
		printMessages(result.Messages)
		printServicesTable(result.Services)
		return nil
	case strings.HasPrefix(resource, "service/"):
		name := strings.TrimPrefix(resource, "service/")
		service, err := requireService(result.Services, name)
		if err != nil {
			return err
		}
		if *jsonOut {
			return printJSON(struct {
				Mode     string          `json:"mode"`
				Messages []string        `json:"messages"`
				Item     serviceResource `json:"item"`
			}{Mode: result.Mode, Messages: result.Messages, Item: service})
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
		return errors.New("usage: tubo describe service/name [flags]")
	}
	resource := args[0]
	if !strings.HasPrefix(resource, "service/") {
		return fmt.Errorf("unsupported describe resource %q", resource)
	}
	fs := flag.NewFlagSet("describe", flag.ContinueOnError)
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	timeout := fs.Duration("timeout", 5*time.Second, "")
	cachedOnly := fs.Bool("cached-only", false, "")
	live := fs.Bool("live", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	result, err := discoverServices(*configPath, *timeout, *cachedOnly, *live)
	if err != nil {
		return err
	}
	service, err := requireService(result.Services, strings.TrimPrefix(resource, "service/"))
	if err != nil {
		return err
	}
	printMessages(result.Messages)
	printServiceDescription(service, result.Messages)
	return nil
}

func inspectCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tubo inspect service/name [flags]")
	}
	resource := args[0]
	if !strings.HasPrefix(resource, "service/") {
		return fmt.Errorf("unsupported inspect resource %q", resource)
	}
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	timeout := fs.Duration("timeout", 5*time.Second, "")
	cachedOnly := fs.Bool("cached-only", false, "")
	live := fs.Bool("live", false, "")
	_ = fs.Bool("json", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	result, err := discoverServices(*configPath, *timeout, *cachedOnly, *live)
	if err != nil {
		return err
	}
	service, err := requireService(result.Services, strings.TrimPrefix(resource, "service/"))
	if err != nil {
		return err
	}
	return printJSON(struct {
		Mode     string          `json:"mode"`
		Messages []string        `json:"messages"`
		Item     serviceResource `json:"item"`
	}{Mode: result.Mode, Messages: result.Messages, Item: service})
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
	timeout := fs.Duration("timeout", 10*time.Second, "")
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
	sortServiceResources(payload.Items)
	return payload.Items, adminAddr, nil
}

func observeServices(cfg cfgpkg.Config, timeout time.Duration, onEvent func(serviceWatchEvent)) ([]serviceResource, error) {
	peers := uniqueStrings(append(append([]string{}, cfg.Network.BootstrapPeers...), cfg.Network.RelayPeers...))
	if len(peers) == 0 {
		return nil, errors.New("no bootstrap or relay peers configured")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
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
	ps, err := pubsub.NewGossipSub(ctx, h)
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
	return serviceResource{
		Kind:             "service",
		Name:             entry.ServiceName,
		PeerID:           entry.PeerID.String(),
		Addresses:        append([]string(nil), entry.Addresses...),
		Status:           "online",
		Path:             servicePathFromAddresses(entry.Addresses),
		TTLSeconds:       int64(entry.TTL.Seconds()),
		ExpiresInSeconds: int64(expiresIn.Seconds()),
		Capabilities:     []string{},
		RegisteredAt:     entry.Registered.Format(time.RFC3339),
	}
}

func servicePathFromAddresses(addresses []string) string {
	if len(addresses) == 0 {
		return "unknown"
	}
	for _, addr := range addresses {
		if strings.Contains(addr, "/p2p-circuit/") {
			return "relayed"
		}
	}
	return "direct"
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
	fmt.Println("Addresses:")
	if len(service.Addresses) == 0 {
		fmt.Println("  - none")
	} else {
		for _, addr := range service.Addresses {
			fmt.Printf("  - %s\n", addr)
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
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	data := "/key/swarm/psk/1.0.0/\n/base16/\n" + hex.EncodeToString(b) + "\n"
	return os.WriteFile(*out, []byte(data), 0600)
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

func (c csvFlag) String() string     { return strings.Join(*c.p, ",") }
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
