package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadYAMLAndValidateService(t *testing.T) {
	y := `role: service
node:
  seed: s
  p2p_listen: /ip4/127.0.0.1/tcp/1
network:
  bootstrap_peers:
  - /ip4/1.2.3.4/tcp/4001/p2p/12D3KooWQbVQpzQ1r1o1YtA4ePpMTE4mZZwB9sJYQ9kJjMJzZxYb
service:
  name: api
  target: http://127.0.0.1:9000
heartbeat_interval: 5s
`
	p := t.TempDir() + "/c.yaml"
	if err := osWrite(p, y); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	c = Merge(Defaults(c.Role), c)
	if c.HeartbeatInterval.Duration() != 5*time.Second {
		t.Fatalf("duration not parsed")
	}
	if err := Validate(c); err != nil {
		t.Fatal(err)
	}
}
func TestEnvCSVAndMerge(t *testing.T) {
	g := func(k string) string {
		m := map[string]string{"BOOTSTRAP_PEERS": "a, b,,c", "SERVICE_NAME": "svc"}
		return m[k]
	}
	c := Merge(Defaults("service"), Env(g, "service"))
	if len(c.Network.BootstrapPeers) != 3 {
		t.Fatalf("csv=%v", c.Network.BootstrapPeers)
	}
	if c.Service.Name != "svc" {
		t.Fatal(c.Service.Name)
	}
}
func TestValidateRequired(t *testing.T) {
	c := Defaults("bridge")
	if err := Validate(c); err == nil || !strings.Contains(err.Error(), "service_addr") {
		t.Fatalf("err=%v", err)
	}
}
func TestMaskSecrets(t *testing.T) {
	c := Defaults("service")
	c.Network.PrivateKeyB64 = "secret"
	if Mask(c).Network.PrivateKeyB64 != "" {
		t.Fatal("secret not masked")
	}
}

func osWrite(p, s string) error { return os.WriteFile(p, []byte(s), 0600) }
