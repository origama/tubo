package integration_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"github.com/origama/tubo/internal/app/edge"
	"github.com/origama/tubo/internal/app/service"
	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/p2p"
	"golang.org/x/crypto/ssh"
)

func TestClusterModeDiscoveryV2EndToEnd(t *testing.T) {
	dummy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"method":    r.Method,
			"path":      r.URL.Path,
			"raw_query": r.URL.RawQuery,
			"body_b64":  base64.StdEncoding.EncodeToString(body),
		})
	}))
	defer dummy.Close()

	clusterName := "home"
	namespaceName := "tenant-a"

	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	authorityKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH)))
	serviceID, serviceSeed := serviceIdentityForTest("cluster-123", namespaceName, "myapi")
	servicePeerID, err := p2p.PeerIDFromSeed(serviceSeed)
	if err != nil {
		t.Fatal(err)
	}
	membership, err := capability.SignMembershipCapability(capability.MembershipCapability{
		ClusterID:     "cluster-123",
		NamespaceID:   namespaceName,
		SubjectPeerID: servicePeerID.String(),
		Permissions: []string{
			capability.PermissionSubscribe,
			capability.PermissionList,
			capability.PermissionPublish,
			capability.PermissionConnect,
		},
		ExpiresAt: time.Now().Add(time.Hour),
	}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	membershipBytes, err := json.MarshalIndent(membership, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	capPath := filepath.Join(t.TempDir(), "membership.cap.json")
	if err := os.WriteFile(capPath, append(membershipBytes, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	claim, err := capability.SignServiceClaim(capability.ServiceClaim{
		ClusterID:     "cluster-123",
		NamespaceID:   namespaceName,
		ServiceID:     serviceID,
		SubjectPeerID: servicePeerID.String(),
		Permissions:   []string{capability.PermissionAttach, capability.PermissionAnnounce},
		ExpiresAt:     time.Now().Add(time.Hour),
	}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := json.MarshalIndent(claim, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(t.TempDir(), "service.claim.json")
	if err := os.WriteFile(claimPath, append(claimBytes, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := cfgpkg.Config{
		CurrentCluster:   clusterName,
		CurrentNamespace: namespaceName,
		Clusters: map[string]cfgpkg.Cluster{
			clusterName: {
				ClusterID:                "cluster-123",
				AuthorityPublicKey:       authorityKey,
				MembershipCapabilityFile: capPath,
				Namespaces: map[string]cfgpkg.Namespace{namespaceName: {Services: map[string]cfgpkg.NamespaceService{
					"myapi": {ServiceID: serviceID, ServiceSeed: serviceSeed, ServiceClaimFile: claimPath},
				}}},
			},
		},
	}
	runtime := cfg.DiscoveryRuntime()
	if runtime.Mode != cfgpkg.DiscoveryModeNamespaceV2 {
		t.Fatalf("expected cluster-mode discovery runtime, got %#v", runtime)
	}

	edgeHTTP := freePort(t)
	edgeAdmin := freePort(t)
	edgeP2P := freePort(t)
	serviceP2P := freePort(t)
	serviceHealth := freePort(t)

	edgeApp, err := edge.New(context.Background(), edge.Config{
		HTTPListen:             fmt.Sprintf("127.0.0.1:%d", edgeHTTP),
		AdminListen:            fmt.Sprintf("127.0.0.1:%d", edgeAdmin),
		P2PListen:              fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", edgeP2P),
		Seed:                   "edge-discovery-v2-seed",
		BootstrapRetryInterval: 500 * time.Millisecond,
		DirectStreamTimeout:    250 * time.Millisecond,
		AuthorityPublicKey:     authorityKey,
		DiscoveryTopic:         runtime.Topic,
		DiscoveryMode:          runtime.Mode.String(),
		DiscoveryClusterID:     runtime.ClusterID,
		DiscoveryNamespaceID:   runtime.NamespaceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	serviceApp, err := service.New(context.Background(), service.Config{
		Listen:                   fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", serviceP2P),
		Seed:                     serviceSeed,
		ServiceName:              "myapi",
		Target:                   dummy.URL,
		HealthListen:             fmt.Sprintf("127.0.0.1:%d", serviceHealth),
		HeartbeatInterval:        500 * time.Millisecond,
		BootstrapRetryInterval:   500 * time.Millisecond,
		DiscoveryTopic:           runtime.Topic,
		DiscoveryMode:            runtime.Mode.String(),
		DiscoveryClusterID:       runtime.ClusterID,
		DiscoveryNamespaceID:     runtime.NamespaceID,
		AuthorityPublicKey:       authorityKey,
		MembershipCapabilityFile: capPath,
		ServiceClaimFile:         claimPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errCh := make(chan error, 2)
	go func() { errCh <- edgeApp.Start(ctx) }()
	go func() { errCh <- serviceApp.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		for i := 0; i < 2; i++ {
			<-errCh
		}
	})

	waitUntil(t, 20*time.Second, func() bool {
		return httpOK(fmt.Sprintf("http://127.0.0.1:%d/healthz", edgeHTTP)) && httpOK(fmt.Sprintf("http://127.0.0.1:%d/healthz", serviceHealth))
	}, "cluster-mode health")

	edgeInfo := peer.AddrInfo{ID: edgeApp.Host().ID(), Addrs: mustMultiaddrs(t, p2p.PeerAddrs(edgeApp.Host())...)}
	serviceInfo := peer.AddrInfo{ID: serviceApp.Host().ID(), Addrs: mustMultiaddrs(t, p2p.PeerAddrs(serviceApp.Host())...)}
	connectCtx, cancelConnect := context.WithTimeout(ctx, 10*time.Second)
	if err := edgeApp.Host().Connect(connectCtx, serviceInfo); err != nil {
		t.Fatalf("edge connect service: %v", err)
	}
	if err := serviceApp.Host().Connect(connectCtx, edgeInfo); err != nil {
		t.Fatalf("service connect edge: %v", err)
	}
	cancelConnect()

	waitUntil(t, 30*time.Second, func() bool {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/services", edgeAdmin))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var payload struct {
			Count int `json:"count"`
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			return false
		}
		for _, item := range payload.Items {
			if item.Name == "myapi" {
				return true
			}
		}
		return false
	}, "cluster-mode discovery cache")

	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/discovery-v2?from=integration", edgeHTTP), strings.NewReader("hello-discovery-v2"))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "myapi"
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var echoed map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&echoed); err != nil {
		t.Fatalf("decode proxy body: %v", err)
	}
	if echoed["path"] != "/v1/discovery-v2" {
		t.Fatalf("unexpected proxied path: %#v", echoed)
	}
	if echoed["method"] != http.MethodPost {
		t.Fatalf("unexpected proxied method: %#v", echoed)
	}
}

func TestClusterModeDiscoveryV2RejectsServiceWithoutClaim(t *testing.T) {
	clusterName := "home"
	namespaceName := "tenant-a"

	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	authorityKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH)))
	_, serviceSeed := serviceIdentityForTest("cluster-123", namespaceName, "unclaimed")
	servicePeerID, err := p2p.PeerIDFromSeed(serviceSeed)
	if err != nil {
		t.Fatal(err)
	}
	membership, err := capability.SignMembershipCapability(capability.MembershipCapability{
		ClusterID:     "cluster-123",
		NamespaceID:   namespaceName,
		SubjectPeerID: servicePeerID.String(),
		Permissions: []string{
			capability.PermissionSubscribe,
			capability.PermissionList,
			capability.PermissionPublish,
			capability.PermissionConnect,
		},
		ExpiresAt: time.Now().Add(time.Hour),
	}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	membershipBytes, err := json.MarshalIndent(membership, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	capPath := filepath.Join(t.TempDir(), "membership.cap.json")
	if err := os.WriteFile(capPath, append(membershipBytes, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := cfgpkg.Config{
		CurrentCluster:   clusterName,
		CurrentNamespace: namespaceName,
		Clusters: map[string]cfgpkg.Cluster{clusterName: {
			ClusterID:                "cluster-123",
			AuthorityPublicKey:       authorityKey,
			MembershipCapabilityFile: capPath,
		}},
	}
	runtime := cfg.DiscoveryRuntime()
	edgeHTTP := freePort(t)
	edgeAdmin := freePort(t)
	edgeP2P := freePort(t)
	serviceP2P := freePort(t)
	serviceHealth := freePort(t)

	edgeApp, err := edge.New(context.Background(), edge.Config{
		HTTPListen:             fmt.Sprintf("127.0.0.1:%d", edgeHTTP),
		AdminListen:            fmt.Sprintf("127.0.0.1:%d", edgeAdmin),
		P2PListen:              fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", edgeP2P),
		Seed:                   "edge-discovery-v2-reject-seed",
		BootstrapRetryInterval: 500 * time.Millisecond,
		DirectStreamTimeout:    250 * time.Millisecond,
		AuthorityPublicKey:     authorityKey,
		DiscoveryTopic:         runtime.Topic,
		DiscoveryMode:          runtime.Mode.String(),
		DiscoveryClusterID:     runtime.ClusterID,
		DiscoveryNamespaceID:   runtime.NamespaceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	serviceApp, err := service.New(context.Background(), service.Config{
		Listen:                   fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", serviceP2P),
		Seed:                     serviceSeed,
		ServiceName:              "unclaimed",
		Target:                   "http://127.0.0.1:1",
		HealthListen:             fmt.Sprintf("127.0.0.1:%d", serviceHealth),
		HeartbeatInterval:        500 * time.Millisecond,
		BootstrapRetryInterval:   500 * time.Millisecond,
		DiscoveryTopic:           runtime.Topic,
		DiscoveryMode:            runtime.Mode.String(),
		DiscoveryClusterID:       runtime.ClusterID,
		DiscoveryNamespaceID:     runtime.NamespaceID,
		AuthorityPublicKey:       authorityKey,
		MembershipCapabilityFile: capPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errCh := make(chan error, 2)
	go func() { errCh <- edgeApp.Start(ctx) }()
	go func() { errCh <- serviceApp.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		for i := 0; i < 2; i++ {
			<-errCh
		}
	})

	waitUntil(t, 20*time.Second, func() bool {
		return httpOK(fmt.Sprintf("http://127.0.0.1:%d/healthz", edgeHTTP)) && httpOK(fmt.Sprintf("http://127.0.0.1:%d/healthz", serviceHealth))
	}, "claimless cluster-mode health")

	edgeInfo := peer.AddrInfo{ID: edgeApp.Host().ID(), Addrs: mustMultiaddrs(t, p2p.PeerAddrs(edgeApp.Host())...)}
	serviceInfo := peer.AddrInfo{ID: serviceApp.Host().ID(), Addrs: mustMultiaddrs(t, p2p.PeerAddrs(serviceApp.Host())...)}
	connectCtx, cancelConnect := context.WithTimeout(ctx, 10*time.Second)
	if err := edgeApp.Host().Connect(connectCtx, serviceInfo); err != nil {
		t.Fatalf("edge connect service: %v", err)
	}
	if err := serviceApp.Host().Connect(connectCtx, edgeInfo); err != nil {
		t.Fatalf("service connect edge: %v", err)
	}
	cancelConnect()

	time.Sleep(2 * time.Second)
	if got := edgeServiceCount(t, edgeAdmin); got != 0 {
		t.Fatalf("edge accepted claimless service announcement; cache count = %d, want 0", got)
	}

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", edgeHTTP), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "unclaimed"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected no route for claimless service, got %d", resp.StatusCode)
	}
}

func edgeServiceCount(t *testing.T, edgeAdminPort int) int {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/services", edgeAdminPort))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return payload.Count
}

func mustMultiaddrs(t *testing.T, raw ...string) []multiaddr.Multiaddr {
	t.Helper()
	out := make([]multiaddr.Multiaddr, 0, len(raw))
	for _, addr := range raw {
		m, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func serviceIdentityForTest(clusterID, namespaceID, serviceName string) (string, string) {
	sum := sha256.Sum256([]byte(clusterID + "\x00" + namespaceID + "\x00" + serviceName))
	return "service-" + fmt.Sprintf("%x", sum[:8]), "service-" + fmt.Sprintf("%x", sum[8:24])
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
