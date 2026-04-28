package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/multiformats/go-multiaddr"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Role              string   `yaml:"role" json:"role"`
	Node              Node     `yaml:"node" json:"node"`
	Network           Network  `yaml:"network" json:"network"`
	Service           Service  `yaml:"service" json:"service"`
	Edge              Edge     `yaml:"edge" json:"edge"`
	Relay             Relay    `yaml:"relay" json:"relay"`
	Bridge            Bridge   `yaml:"bridge" json:"bridge"`
	HealthListen      string   `yaml:"health_listen" json:"health_listen"`
	HeartbeatInterval Duration `yaml:"heartbeat_interval" json:"heartbeat_interval"`
}

type Node struct {
	Seed      string `yaml:"seed" json:"seed"`
	P2PListen string `yaml:"p2p_listen" json:"p2p_listen"`
}
type Network struct {
	PrivateKeyFile    string   `yaml:"private_key_file" json:"private_key_file"`
	PrivateKeyB64     string   `yaml:"private_key_b64" json:"private_key_b64"`
	AllowedPeers      []string `yaml:"allowed_peers" json:"allowed_peers"`
	BootstrapPeers    []string `yaml:"bootstrap_peers" json:"bootstrap_peers"`
	RelayPeers        []string `yaml:"relay_peers" json:"relay_peers"`
	Autorelay         bool     `yaml:"autorelay" json:"autorelay"`
	HolePunching      bool     `yaml:"hole_punching" json:"hole_punching"`
	ForceReachability string   `yaml:"force_reachability" json:"force_reachability"`
}
type Service struct {
	Name   string `yaml:"name" json:"name"`
	Target string `yaml:"target" json:"target"`
}
type Edge struct {
	Listen              string   `yaml:"listen" json:"listen"`
	AdminListen         string   `yaml:"admin_listen" json:"admin_listen"`
	DirectStreamTimeout Duration `yaml:"direct_stream_timeout" json:"direct_stream_timeout"`
}
type Relay struct {
	PublicAddr              string   `yaml:"public_addr" json:"public_addr"`
	HealthListen            string   `yaml:"health_listen" json:"health_listen"`
	EnableRelayService      bool     `yaml:"enable_relay_service" json:"enable_relay_service"`
	EnableAutoNATService    bool     `yaml:"enable_autonat_service" json:"enable_autonat_service"`
	EnableDiscoveryPubSub   bool     `yaml:"enable_discovery_pubsub" json:"enable_discovery_pubsub"`
	ForceReachabilityPublic bool     `yaml:"force_reachability_public" json:"force_reachability_public"`
	MaxReservations         int      `yaml:"max_reservations" json:"max_reservations"`
	MaxReservationsPerIP    int      `yaml:"max_reservations_per_ip" json:"max_reservations_per_ip"`
	MaxReservationsPerASN   int      `yaml:"max_reservations_per_asn" json:"max_reservations_per_asn"`
	MaxCircuitsPerPeer      int      `yaml:"max_circuits_per_peer" json:"max_circuits_per_peer"`
	BufferSize              int      `yaml:"buffer_size" json:"buffer_size"`
	ReservationTTL          Duration `yaml:"reservation_ttl" json:"reservation_ttl"`
	LimitDuration           Duration `yaml:"limit_duration" json:"limit_duration"`
	LimitDataBytes          int64    `yaml:"limit_data_bytes" json:"limit_data_bytes"`
	PrintRunCommands        bool     `yaml:"print_run_commands" json:"print_run_commands"`
}
type Bridge struct {
	Listen           string `yaml:"listen" json:"listen"`
	ServiceAddr      string `yaml:"service_addr" json:"service_addr"`
	ServiceSeed      string `yaml:"service_seed" json:"service_seed"`
	ServiceP2PListen string `yaml:"service_p2p_listen" json:"service_p2p_listen"`
}

type Duration time.Duration

func (d Duration) Duration() time.Duration      { return time.Duration(d) }
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }
func (d Duration) MarshalYAML() (any, error) {
	if d == 0 {
		return "", nil
	}
	return time.Duration(d).String(), nil
}
func (d *Duration) UnmarshalYAML(v *yaml.Node) error {
	if v.Value == "" {
		*d = 0
		return nil
	}
	x, err := time.ParseDuration(v.Value)
	if err != nil {
		return err
	}
	*d = Duration(x)
	return nil
}

func Defaults(role string) Config {
	c := Config{Role: role}
	c.Network.Autorelay = true
	c.Network.HolePunching = true
	c.HeartbeatInterval = Duration(15 * time.Second)
	switch role {
	case "edge":
		c.Node.P2PListen = "/ip4/0.0.0.0/tcp/4001"
		c.Edge.Listen = ":8443"
		c.Edge.AdminListen = "127.0.0.1:8444"
		c.Edge.DirectStreamTimeout = Duration(750 * time.Millisecond)
	case "service":
		c.Node.Seed = "service-demo-seed"
		c.Node.P2PListen = "/ip4/127.0.0.1/tcp/40123"
		c.Service.Name = "demo-service"
		c.Service.Target = "http://127.0.0.1:8000"
		c.HealthListen = "127.0.0.1:8091"
	case "relay":
		c.Node.Seed = "public-relay-seed"
		c.Node.P2PListen = "/ip4/0.0.0.0/tcp/4001"
		c.Relay.HealthListen = "127.0.0.1:8092"
		c.Relay.EnableRelayService = true
		c.Relay.EnableAutoNATService = true
		c.Relay.EnableDiscoveryPubSub = true
		c.Relay.ForceReachabilityPublic = true
		c.Relay.MaxReservations = 256
		c.Relay.MaxReservationsPerIP = 16
		c.Relay.MaxReservationsPerASN = 64
		c.Relay.MaxCircuitsPerPeer = 64
		c.Relay.BufferSize = 4096
		c.Relay.ReservationTTL = Duration(time.Hour)
		c.Relay.LimitDuration = Duration(5 * time.Minute)
		c.Relay.LimitDataBytes = 16 << 20
		c.Relay.PrintRunCommands = true
	case "bridge":
		c.Node.Seed = "bridge-demo-seed"
		c.Node.P2PListen = "/ip4/127.0.0.1/tcp/0"
		c.Bridge.Listen = "127.0.0.1:18081"
		c.Bridge.ServiceP2PListen = "/ip4/127.0.0.1/tcp/40123"
	}
	return c
}

func LoadFile(path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}
func WriteFile(path string, c Config, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s exists (use --force)", path)
		}
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}
func Merge(base, over Config) Config {
	b := base
	if over.Role != "" {
		b.Role = over.Role
	}
	if over.Node.Seed != "" {
		b.Node.Seed = over.Node.Seed
	}
	if over.Node.P2PListen != "" {
		b.Node.P2PListen = over.Node.P2PListen
	}
	if over.Network.PrivateKeyFile != "" {
		b.Network.PrivateKeyFile = over.Network.PrivateKeyFile
	}
	if over.Network.PrivateKeyB64 != "" {
		b.Network.PrivateKeyB64 = over.Network.PrivateKeyB64
	}
	if len(over.Network.AllowedPeers) > 0 {
		b.Network.AllowedPeers = over.Network.AllowedPeers
	}
	if len(over.Network.BootstrapPeers) > 0 {
		b.Network.BootstrapPeers = over.Network.BootstrapPeers
	}
	if len(over.Network.RelayPeers) > 0 {
		b.Network.RelayPeers = over.Network.RelayPeers
	}
	if over.Network.Autorelay {
		b.Network.Autorelay = true
	}
	if over.Network.HolePunching {
		b.Network.HolePunching = true
	}
	if over.Network.ForceReachability != "" {
		b.Network.ForceReachability = over.Network.ForceReachability
	}
	if over.Service.Name != "" {
		b.Service.Name = over.Service.Name
	}
	if over.Service.Target != "" {
		b.Service.Target = over.Service.Target
	}
	if over.HealthListen != "" {
		b.HealthListen = over.HealthListen
	}
	if over.HeartbeatInterval != 0 {
		b.HeartbeatInterval = over.HeartbeatInterval
	}
	if over.Edge.Listen != "" {
		b.Edge.Listen = over.Edge.Listen
	}
	if over.Edge.AdminListen != "" {
		b.Edge.AdminListen = over.Edge.AdminListen
	}
	if over.Edge.DirectStreamTimeout != 0 {
		b.Edge.DirectStreamTimeout = over.Edge.DirectStreamTimeout
	}
	if over.Relay.PublicAddr != "" {
		b.Relay.PublicAddr = over.Relay.PublicAddr
	}
	if over.Relay.HealthListen != "" {
		b.Relay.HealthListen = over.Relay.HealthListen
	}
	if over.Relay.EnableRelayService {
		b.Relay.EnableRelayService = true
	}
	if over.Relay.EnableAutoNATService {
		b.Relay.EnableAutoNATService = true
	}
	if over.Relay.EnableDiscoveryPubSub {
		b.Relay.EnableDiscoveryPubSub = true
	}
	if over.Relay.ForceReachabilityPublic {
		b.Relay.ForceReachabilityPublic = true
	}
	if over.Relay.MaxReservations != 0 {
		b.Relay.MaxReservations = over.Relay.MaxReservations
	}
	if over.Relay.MaxReservationsPerIP != 0 {
		b.Relay.MaxReservationsPerIP = over.Relay.MaxReservationsPerIP
	}
	if over.Relay.MaxReservationsPerASN != 0 {
		b.Relay.MaxReservationsPerASN = over.Relay.MaxReservationsPerASN
	}
	if over.Relay.MaxCircuitsPerPeer != 0 {
		b.Relay.MaxCircuitsPerPeer = over.Relay.MaxCircuitsPerPeer
	}
	if over.Relay.BufferSize != 0 {
		b.Relay.BufferSize = over.Relay.BufferSize
	}
	if over.Relay.ReservationTTL != 0 {
		b.Relay.ReservationTTL = over.Relay.ReservationTTL
	}
	if over.Relay.LimitDuration != 0 {
		b.Relay.LimitDuration = over.Relay.LimitDuration
	}
	if over.Relay.LimitDataBytes != 0 {
		b.Relay.LimitDataBytes = over.Relay.LimitDataBytes
	}
	if over.Relay.PrintRunCommands {
		b.Relay.PrintRunCommands = true
	}
	if over.Bridge.Listen != "" {
		b.Bridge.Listen = over.Bridge.Listen
	}
	if over.Bridge.ServiceAddr != "" {
		b.Bridge.ServiceAddr = over.Bridge.ServiceAddr
	}
	if over.Bridge.ServiceSeed != "" {
		b.Bridge.ServiceSeed = over.Bridge.ServiceSeed
	}
	if over.Bridge.ServiceP2PListen != "" {
		b.Bridge.ServiceP2PListen = over.Bridge.ServiceP2PListen
	}
	return b
}
func Env(getenv func(string) string, role string) Config {
	c := Config{Role: role}
	if v := first(getenv("NODE_SEED"), getenv("EDGE_SEED")); v != "" {
		c.Node.Seed = v
	}
	if v := first(getenv("P2P_LISTEN"), getenv("EDGE_P2P_LISTEN"), getenv("SERVICE_P2P_LISTEN"), getenv("BRIDGE_P2P_LISTEN")); v != "" {
		c.Node.P2PListen = v
	}
	c.Network.PrivateKeyFile = getenv("LIBP2P_PRIVATE_NETWORK_KEY")
	c.Network.PrivateKeyB64 = getenv("LIBP2P_PRIVATE_NETWORK_KEY_B64")
	c.Network.AllowedPeers = CSV(getenv("LIBP2P_ALLOWED_PEERS"))
	c.Network.BootstrapPeers = CSV(getenv("BOOTSTRAP_PEERS"))
	c.Network.RelayPeers = CSV(getenv("RELAY_PEERS"))
	if v := getenv("ENABLE_AUTORELAY"); v != "" {
		c.Network.Autorelay = parseBool(v)
	}
	if v := getenv("ENABLE_HOLE_PUNCHING"); v != "" {
		c.Network.HolePunching = parseBool(v)
	}
	if parseBool(getenv("FORCE_REACHABILITY_PRIVATE")) {
		c.Network.ForceReachability = "private"
	}
	if parseBool(getenv("FORCE_REACHABILITY_PUBLIC")) {
		c.Network.ForceReachability = "public"
		c.Relay.ForceReachabilityPublic = true
	}
	c.Service.Name = getenv("SERVICE_NAME")
	c.Service.Target = getenv("SERVICE_TARGET")
	c.HealthListen = getenv("SERVICE_HEALTH_LISTEN")
	if d := dur(getenv("HEARTBEAT_INTERVAL")); d != 0 {
		c.HeartbeatInterval = d
	}
	c.Edge.Listen = getenv("EDGE_LISTEN")
	c.Edge.AdminListen = getenv("EDGE_ADMIN_LISTEN")
	if d := dur(getenv("EDGE_DIRECT_STREAM_TIMEOUT")); d != 0 {
		c.Edge.DirectStreamTimeout = d
	}
	c.Relay.PublicAddr = getenv("RELAY_PUBLIC_ADDR")
	c.Relay.HealthListen = getenv("RELAY_HEALTH_LISTEN")
	setBool(getenv, "ENABLE_RELAY_SERVICE", &c.Relay.EnableRelayService)
	setBool(getenv, "ENABLE_AUTONAT_SERVICE", &c.Relay.EnableAutoNATService)
	setBool(getenv, "ENABLE_DISCOVERY_PUBSUB", &c.Relay.EnableDiscoveryPubSub)
	setBool(getenv, "PRINT_RUN_COMMANDS", &c.Relay.PrintRunCommands)
	c.Relay.MaxReservations = atoi(getenv("RELAY_MAX_RESERVATIONS"))
	c.Relay.MaxReservationsPerIP = atoi(getenv("RELAY_MAX_RESERVATIONS_PER_IP"))
	c.Relay.MaxReservationsPerASN = atoi(getenv("RELAY_MAX_RESERVATIONS_PER_ASN"))
	c.Relay.MaxCircuitsPerPeer = atoi(getenv("RELAY_MAX_CIRCUITS"))
	c.Relay.BufferSize = atoi(getenv("RELAY_BUFFER_SIZE"))
	c.Relay.LimitDataBytes = atoi64(getenv("RELAY_LIMIT_DATA_BYTES"))
	if d := dur(getenv("RELAY_RESERVATION_TTL")); d != 0 {
		c.Relay.ReservationTTL = d
	}
	if d := dur(getenv("RELAY_LIMIT_DURATION")); d != 0 {
		c.Relay.LimitDuration = d
	}
	c.Bridge.Listen = getenv("BRIDGE_LISTEN")
	c.Bridge.ServiceAddr = getenv("SERVICE_ADDR")
	c.Bridge.ServiceSeed = getenv("SERVICE_SEED")
	c.Bridge.ServiceP2PListen = getenv("SERVICE_P2P_LISTEN")
	return c
}
func Effective(role, path string, getenv func(string) string, flags Config) (Config, error) {
	fc, err := LoadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Merge(Merge(Merge(Defaults(role), fc), Env(getenv, role)), flags), nil
}
func Validate(c Config) error {
	if c.Role == "" {
		return fmt.Errorf("role is required")
	}
	if c.Node.P2PListen != "" {
		if _, err := multiaddr.NewMultiaddr(c.Node.P2PListen); err != nil {
			return fmt.Errorf("node.p2p_listen: %w", err)
		}
	}
	for _, a := range append(c.Network.BootstrapPeers, c.Network.RelayPeers...) {
		if _, err := multiaddr.NewMultiaddr(a); err != nil {
			return fmt.Errorf("multiaddr %q: %w", a, err)
		}
	}
	switch c.Role {
	case "service":
		if c.Service.Name == "" {
			return fmt.Errorf("service.name is required (set --name or SERVICE_NAME)")
		}
		if c.Service.Target == "" {
			return fmt.Errorf("service.target is required (set --target or SERVICE_TARGET)")
		}
		if _, err := url.ParseRequestURI(c.Service.Target); err != nil {
			return fmt.Errorf("service.target: %w", err)
		}
	case "edge":
		if c.Edge.Listen == "" || c.Edge.AdminListen == "" {
			return fmt.Errorf("edge.listen and edge.admin_listen are required")
		}
	case "relay":
		if c.Relay.PublicAddr != "" {
			if _, err := multiaddr.NewMultiaddr(c.Relay.PublicAddr); err != nil {
				return fmt.Errorf("relay.public_addr: %w", err)
			}
		}
	case "bridge":
		if c.Bridge.ServiceAddr == "" && c.Bridge.ServiceSeed == "" {
			return fmt.Errorf("bridge.service_addr or bridge.service_seed is required")
		}
	default:
		return fmt.Errorf("unknown role %q", c.Role)
	}
	return nil
}
func Doctor(c Config) error {
	if err := Validate(c); err != nil {
		return err
	}
	if c.Network.PrivateKeyFile != "" {
		if f, err := os.Open(c.Network.PrivateKeyFile); err != nil {
			return fmt.Errorf("read swarm key: %w", err)
		} else {
			_ = f.Close()
		}
	}
	if c.Edge.Listen != "" {
		_, _, _ = net.SplitHostPort(addrForSplit(c.Edge.Listen))
	}
	return nil
}
func Mask(c Config) Config { c.Network.PrivateKeyB64 = ""; return c }
func CSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
func first(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}
func parseBool(s string) bool { b, _ := strconv.ParseBool(s); return b }
func setBool(g func(string) string, k string, p *bool) {
	if v := g(k); v != "" {
		*p = parseBool(v)
	}
}
func atoi(s string) int     { i, _ := strconv.Atoi(s); return i }
func atoi64(s string) int64 { i, _ := strconv.ParseInt(s, 10, 64); return i }
func dur(s string) Duration {
	if s == "" {
		return 0
	}
	d, _ := time.ParseDuration(s)
	return Duration(d)
}
func addrForSplit(a string) string {
	if strings.HasPrefix(a, ":") {
		return "127.0.0.1" + a
	}
	return a
}
