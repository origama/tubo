package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	serviceapp "github.com/origama/tubo/internal/app/service"
	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/connectflow"
	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	"github.com/origama/tubo/internal/p2p"
	"golang.org/x/crypto/ssh"
)

type a2AAgentCard struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Version     string `json:"version"`
}

func TestA2AAgentDiscoverySpike(t *testing.T) {
	mockCard, mockBaseURL := startA2AMockServer(t)

	swarmKeyPath := filepath.Join(t.TempDir(), "swarm.key")
	swarmKey, err := newSwarmKeyData()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(swarmKeyPath, swarmKey, 0o600); err != nil {
		t.Fatal(err)
	}
	psk, _, err := p2p.LoadPrivateNetworkPSK(swarmKeyPath, "")
	if err != nil {
		t.Fatal(err)
	}

	queryHost, err := p2p.NewHostWithSeedAndPSK("/ip4/0.0.0.0/tcp/0", "a2a-query-host", psk)
	if err != nil {
		t.Fatal(err)
	}
	queryCache := discovery.NewCache(5*time.Minute, time.Second)
	queryHost.SetStreamHandler(discoveryquery.ProtocolID, discoveryquery.HandleStream(queryHost, "relay", queryCache))
	t.Cleanup(func() {
		queryCache.Stop()
		_ = queryHost.Close()
	})

	serviceApp, err := serviceapp.New(context.Background(), serviceapp.Config{
		Listen:           "/ip4/0.0.0.0/tcp/0",
		Seed:             "a2a-service-seed",
		ServiceName:      "reviewer",
		ServiceKind:      string(cfgpkg.ServiceKindHTTP),
		Target:           mockBaseURL,
		HealthListen:     "",
		PrivateKeyFile:   swarmKeyPath,
		BootstrapPeers:   nil,
		RelayPeers:       nil,
		Autorelay:        false,
		HolePunching:     false,
		DiscoveryEnabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	serviceAddrs := p2p.PeerAddrs(serviceApp.Host())
	if len(serviceAddrs) == 0 {
		t.Fatal("service host has no dialable addresses")
	}
	t.Cleanup(func() {
		_ = serviceApp.Host().Close()
	})

	queryAddrs := p2p.PeerAddrs(queryHost)
	if len(queryAddrs) == 0 {
		t.Fatal("query host has no dialable addresses")
	}

	serviceID := "agent-reviewer"
	if err := queryCache.AddV2(serviceApp.Host().ID(), "cluster-a2a", "observability", serviceID, "reviewer", discovery.ResourceKindService, string(cfgpkg.ServiceKindHTTP), "", "", nil, serviceAddrs, []string{"protocol=a2a"}, 10*time.Minute); err != nil {
		t.Fatal(err)
	}

	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityPubKey, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	authorityPubKeyStr := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authorityPubKey)))
	membershipPath := mustWriteMembershipCapability(t, authorityPriv, capability.MembershipCapability{
		ClusterID:     "cluster-a2a",
		NamespaceID:   "observability",
		SubjectPeerID: "cluster-a2a",
		Permissions: []string{
			capability.PermissionSubscribe,
			capability.PermissionList,
			capability.PermissionPublish,
			capability.PermissionConnect,
		},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	discoverySecretRef := mustWriteNamespaceDiscoverySecretRef(t, "cluster-a2a", "observability")

	cfg := cfgpkg.Config{
		CurrentCluster:   "home",
		CurrentNamespace: "observability",
		Network: cfgpkg.Network{
			PrivateKeyFile: swarmKeyPath,
			BootstrapPeers: []string{firstRoutableAddr(queryAddrs)},
		},
		Clusters: map[string]cfgpkg.Cluster{
			"home": {
				ClusterID:                "cluster-a2a",
				AuthorityPublicKey:       authorityPubKeyStr,
				MembershipCapabilityFile: membershipPath,
				Namespaces: map[string]cfgpkg.Namespace{
					"observability": {
						Discovery:                cfgpkg.NamespaceDiscoveryEnabled,
						ConnectPolicy:            cfgpkg.ConnectPolicyNamespaceMember,
						DiscoverySecretCurrent:   discoverySecretRef,
						MembershipCapabilityFile: membershipPath,
					},
				},
			},
		},
	}
	configPath := filepath.Join(t.TempDir(), "a2a-spike.yaml")
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}

	lookupResult, service, err := discoverServiceWithConfig(cfg, 10*time.Second, false, false, serviceScope{Cluster: "home", Namespace: "observability"}, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if service.Name != "reviewer" || service.ServiceKind != string(cfgpkg.ServiceKindHTTP) || !containsString(service.Capabilities, "protocol=a2a") {
		t.Fatalf("unexpected discovery result: %#v", service)
	}
	if len(lookupResult.Messages) == 0 {
		t.Fatal("expected discovery messages from remote query")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	resolved, err := connectflow.Resolve(ctx, newConnectWorkflow(), connectflow.Request{ConfigPath: configPath, ServiceRef: "reviewer", Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.LocalURL == "" || resolved.App == nil {
		t.Fatalf("unexpected connect result: %#v", resolved)
	}

	bridgeErrCh := make(chan error, 1)
	go func() { bridgeErrCh <- resolved.App.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-bridgeErrCh; err != nil {
			t.Logf("bridge stopped: %v", err)
		}
	})

	client := &http.Client{Timeout: 2 * time.Second}
	card1 := mustFetchAgentCardViaTubo(t, client, resolved.LocalURL+"/.well-known/agent-card.json")
	card2 := mustFetchAgentCardViaTubo(t, client, resolved.LocalURL+"/.well-known/agent.json")
	if card1 != card2 {
		t.Fatalf("agent card mismatch between endpoints: %#v vs %#v", card1, card2)
	}
	if card1.Name != mockCard.Name || card1.Version != mockCard.Version || card1.URL != mockCard.URL {
		t.Fatalf("unexpected agent card through Tubo: %#v", card1)
	}
}

func startA2AMockServer(t *testing.T) (a2AAgentCard, string) {
	t.Helper()
	card := a2AAgentCard{
		Name:        "reviewer",
		Description: "Minimal A2A agent card used for the Tubo discovery spike.",
		Version:     "0.1.0",
	}
	mux := http.NewServeMux()
	var baseURL string
	handler := func(w http.ResponseWriter, r *http.Request) {
		if baseURL == "" {
			baseURL = "http://" + r.Host
			card.URL = baseURL
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(card)
	}
	mux.HandleFunc("/.well-known/agent-card.json", handler)
	mux.HandleFunc("/.well-known/agent.json", handler)
	server := &http.Server{Addr: "127.0.0.1:0", Handler: mux}
	ln, err := net.Listen("tcp", server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	baseURL = "http://" + ln.Addr().String()
	card.URL = baseURL
	return card, baseURL
}

func mustFetchAgentCardViaTubo(t *testing.T, client *http.Client, url string) a2AAgentCard {
	t.Helper()
	var lastErr error
	for i := 0; i < 100; i++ {
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(50 * time.Millisecond)
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("status %d body=%s", resp.StatusCode, string(body))
			time.Sleep(50 * time.Millisecond)
			continue
		}
		var card a2AAgentCard
		if err := json.Unmarshal(body, &card); err != nil {
			t.Fatalf("decode agent card: %v body=%s", err, body)
		}
		return card
	}
	t.Fatalf("fetch agent card failed: %v", lastErr)
	return a2AAgentCard{}
}

func firstRoutableAddr(addrs []string) string {
	for _, addr := range addrs {
		if !connectflow.IsUnusableDirectAddress(addr) {
			return addr
		}
	}
	if len(addrs) > 0 {
		return addrs[0]
	}
	return ""
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
