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
	"path/filepath"
	"sort"
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
	switch args[0] {
	case "relay", "edge", "service", "bridge":
		if len(args) < 2 || args[1] != "run" {
			return usage()
		}
		return runRole(args[0], args[2:])
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
	default:
		return usage()
	}
}
func usage() error {
	return errors.New("usage: tubo <relay|edge|service|bridge> run | init <role|topology> | keygen swarm | id from-seed | config <print|validate> | doctor | topology <render|commands>")
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
		return printTopologyCommands(t, *out)
	}
	if args[0] != "render" {
		return usage()
	}
	return renderTopology(t, *out)
}

func renderTopology(t Topology, out string) error {
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	refs, err := topologyPeerAddrs(t)
	if err != nil {
		return err
	}
	var names []string
	for name := range t.Nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		n := t.Nodes[name]
		role := str(n, "role", "")
		if role == "" {
			return fmt.Errorf("topology node %q missing role", name)
		}
		c := cfgpkg.Defaults(role)
		c.Network.PrivateKeyFile = t.Swarm.KeyFile
		c.Node.Seed = str(n, "seed", c.Node.Seed)
		c.Node.P2PListen = str(n, "p2p_listen", c.Node.P2PListen)
		c.Network.BootstrapPeers = resolveTopologyRefs(t, refs, n, "bootstrap", "bootstraps")
		c.Network.RelayPeers = resolveTopologyRefs(t, refs, n, "relay", "relays")
		if len(c.Network.RelayPeers) > 0 && len(c.Network.BootstrapPeers) == 0 {
			c.Network.BootstrapPeers = append([]string(nil), c.Network.RelayPeers...)
		}
		switch role {
		case "relay":
			c.Relay.PublicAddr = str(n, "public_addr", c.Relay.PublicAddr)
		case "edge":
			c.Edge.Listen = str(n, "listen", c.Edge.Listen)
			c.Edge.AdminListen = str(n, "admin_listen", c.Edge.AdminListen)
		case "service":
			c.Service.Name = str(n, "service_name", name)
			c.Service.Target = str(n, "target", c.Service.Target)
			if c.HealthListen == "127.0.0.1:8091" {
				c.HealthListen = str(n, "health_listen", c.HealthListen)
			}
		case "bridge":
			c.Bridge.Listen = str(n, "listen", c.Bridge.Listen)
		default:
			return fmt.Errorf("topology node %q has unsupported role %q", name, role)
		}
		if err := cfgpkg.WriteFile(filepath.Join(out, name+".yaml"), c, true); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(out, "RUNBOOK.md"), []byte(topologyRunbook(t, out, refs)), 0644)
}
func topologyPeerAddrs(t Topology) (map[string]string, error) {
	refs := map[string]string{}
	for name, n := range t.Nodes {
		seed := str(n, "seed", "")
		if seed == "" {
			continue
		}
		addr := str(n, "public_addr", "")
		if addr == "" {
			addr = str(n, "advertise_addr", "")
		}
		if addr == "" {
			addr = str(n, "p2p_listen", "")
		}
		if addr == "" {
			addr = cfgpkg.Defaults(str(n, "role", "")).Node.P2PListen
		}
		if addr == "" {
			continue
		}
		peerID, err := p2p.PeerIDFromSeed(seed)
		if err != nil {
			return nil, fmt.Errorf("topology node %q peer id from seed: %w", name, err)
		}
		if !strings.Contains(addr, "/p2p/") {
			addr = strings.TrimRight(addr, "/") + "/p2p/" + peerID.String()
		}
		refs[name] = addr
	}
	return refs, nil
}

func resolveTopologyRefs(t Topology, refs map[string]string, n map[string]any, keys ...string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, key := range keys {
		for _, raw := range stringList(n, key) {
			resolved := raw
			if v, ok := refs[raw]; ok {
				resolved = v
			}
			if resolved == "" {
				continue
			}
			if _, ok := seen[resolved]; ok {
				continue
			}
			seen[resolved] = struct{}{}
			out = append(out, resolved)
		}
	}
	return out
}

func stringList(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s := fmt.Sprint(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	default:
		return []string{fmt.Sprint(x)}
	}
}

func printTopologyCommands(t Topology, out string) error {
	var names []string
	for name := range t.Nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		n := t.Nodes[name]
		role := str(n, "role", "")
		if role == "" {
			return fmt.Errorf("topology node %q missing role", name)
		}
		fmt.Printf("# %s (%s)\n", name, role)
		fmt.Printf("tubo %s run --config %s/%s.yaml\n\n", role, out, name)
	}
	return nil
}

func topologyRunbook(t Topology, out string, refs map[string]string) string {
	var b strings.Builder
	b.WriteString("# Generated tubo topology runbook\n\n")
	b.WriteString("This directory is intended for a multi-host deployment. Copy each YAML file to the machine that will run that node.\n\n")
	var names []string
	for name := range t.Nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	b.WriteString("## Peer addresses\n\n")
	for _, name := range names {
		if addr := refs[name]; addr != "" {
			fmt.Fprintf(&b, "- `%s`: `%s`\n", name, addr)
		}
	}
	b.WriteString("\n## Commands\n\n")
	for _, name := range names {
		role := str(t.Nodes[name], "role", "")
		fmt.Fprintf(&b, "### %s\n\n```bash\ntubo %s run --config %s.yaml\n```\n\n", name, role, name)
	}
	b.WriteString("If you run commands from the repository root instead, use `--config " + out + "/<node>.yaml`.\n")
	return b.String()
}

func str(m map[string]any, k, d string) string {
	if v, ok := m[k]; ok && v != nil {
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
	return "# topology.yaml describes nodes that usually run on separate machines.\n# `tubo topology render` produces one YAML per node plus a generated RUNBOOK.md;\n# it does not assume Docker Compose as the deployment target.\nswarm:\n  key_file: /etc/tubo/swarm.key\nnodes:\n  relay:\n    role: relay\n    seed: public-relay-seed\n    p2p_listen: /ip4/0.0.0.0/tcp/4001\n    public_addr: /ip4/1.2.3.4/tcp/4001\n  edge:\n    role: edge\n    seed: edge-seed\n    p2p_listen: /ip4/0.0.0.0/tcp/4001\n    listen: :8443\n    admin_listen: 127.0.0.1:8444\n    relay: relay\n  lmstudio:\n    role: service\n    seed: service-lmstudio-seed\n    p2p_listen: /ip4/0.0.0.0/tcp/40123\n    service_name: lmstudio\n    target: http://127.0.0.1:1234\n    relay: relay\n"
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
