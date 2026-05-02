package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"os/signal"
	"p2p-api-tunnel/internal/app/bridge"
	"p2p-api-tunnel/internal/app/edge"
	"p2p-api-tunnel/internal/app/relay"
	"p2p-api-tunnel/internal/app/service"
	cfgpkg "p2p-api-tunnel/internal/config"
	"p2p-api-tunnel/internal/p2p"
	iversion "p2p-api-tunnel/internal/version"
	"path/filepath"
	"strings"
	"syscall"
	"time"
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
	return errors.New("usage: tubo <attach|gateway|relay> [flags] | tubo <relay|edge|service|bridge> run [flags] | init <role|topology> | keygen swarm | id from-seed | config <print|validate> | doctor | topology <render|commands> | version")
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
