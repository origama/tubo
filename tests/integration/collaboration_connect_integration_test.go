package integration_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/libp2p/go-libp2p/core/peer"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bridgeapp "github.com/origama/tubo/internal/app/bridge"
	edgeapp "github.com/origama/tubo/internal/app/edge"
	serviceapp "github.com/origama/tubo/internal/app/service"
	capability "github.com/origama/tubo/internal/capability"
	catalog "github.com/origama/tubo/internal/catalog"
	cfgpkg "github.com/origama/tubo/internal/config"
	connectflow "github.com/origama/tubo/internal/connectflow"
	"github.com/origama/tubo/internal/discovery"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/serviceidentity"
	"golang.org/x/crypto/ssh"
)

type collaborationConnectDeps struct{}

func (collaborationConnectDeps) LoadConfig(path string) (cfgpkg.Config, error) {
	return cfgpkg.LoadFile(path)
}
func (collaborationConnectDeps) SetupShare(serviceRef, token, cluster, namespace string) (string, string, catalog.Scope, error) {
	return serviceRef, "", catalog.Scope{}, nil
}
func (collaborationConnectDeps) ParseServiceRef(ref string) (string, error) {
	return strings.TrimPrefix(ref, "service/"), nil
}
func (collaborationConnectDeps) IsServiceID(ref string) bool {
	return serviceidentity.ValidateServiceID(strings.TrimSpace(ref)) == nil
}
func (collaborationConnectDeps) ResolveScope(cfg cfgpkg.Config, cluster, namespace string) (catalog.Scope, error) {
	scope, err := cfgpkg.ResolveEffectiveScope(cfg, cluster, namespace, false)
	if err != nil {
		return catalog.Scope{}, err
	}
	return catalog.Scope{Cluster: scope.Cluster, Namespace: scope.Namespace}, nil
}
func (collaborationConnectDeps) ParseShareToken(string) (connectflow.ShareTokenInfo, error) {
	return connectflow.ShareTokenInfo{}, io.EOF
}
func (collaborationConnectDeps) EnsureShareInviteAvailable(string, connectflow.ShareTokenInfo) error {
	return nil
}
func (collaborationConnectDeps) ImportShareDiscoveryContext(cfg cfgpkg.Config, _ connectflow.ShareTokenInfo) (cfgpkg.Config, error) {
	return cfg, nil
}
func (collaborationConnectDeps) MarkShareInviteUsed(string, connectflow.ShareTokenInfo) error {
	return nil
}
func (collaborationConnectDeps) DiscoverService(cfg cfgpkg.Config, timeout time.Duration, cachedOnly, live bool, scope catalog.Scope, serviceName string) (catalog.LookupResult, catalog.Service, error) {
	return catalog.DiscoverServiceWithConfig(cfg, timeout, cachedOnly, live, scope, serviceName)
}
func (collaborationConnectDeps) DiscoverServiceExact(cfg cfgpkg.Config, timeout time.Duration, cachedOnly, live bool, scope catalog.Scope, serviceName, serviceID string) (catalog.LookupResult, catalog.Service, error) {
	return catalog.DiscoverServiceExactWithConfig(cfg, timeout, cachedOnly, live, scope, serviceName, serviceID)
}
func (collaborationConnectDeps) NewBridge(ctx context.Context, cfg bridgeapp.Config) (*bridgeapp.App, error) {
	return bridgeapp.New(ctx, cfg)
}

func TestCollaborationConnectByNameUsesAdvertisedGrantEndpoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{"path": r.URL.Path, "query": r.URL.RawQuery, "body": string(body)})
	}))
	defer upstream.Close()

	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	authorityKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH)))
	owner, err := serviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	serviceSeed := "collab-connect-service-seed"
	servicePeerID, err := p2p.PeerIDFromSeed(serviceSeed)
	if err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	ownerPath := filepath.Join(work, "service.owner.key")
	if err := serviceidentity.Save(ownerPath, owner.PrivateKey); err != nil {
		t.Fatal(err)
	}
	serviceCap, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: servicePeerID.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	serviceCapBytes, _ := json.MarshalIndent(serviceCap, "", "  ")
	serviceCapPath := filepath.Join(work, "service.membership.cap.json")
	if err := os.WriteFile(serviceCapPath, append(serviceCapBytes, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	clientCap, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: "cluster-123", Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	clientCapBytes, _ := json.MarshalIndent(clientCap, "", "  ")
	clientCapPath := filepath.Join(work, "client.membership.cap.json")
	if err := os.WriteFile(clientCapPath, append(clientCapBytes, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	leaseReq, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{ClusterID: "cluster-123", NamespaceID: "default", ServiceID: owner.ServiceID, ServicePublicKey: serviceidentity.EncodePublicKey(owner.PublicKey), PublisherPeerID: servicePeerID.String(), RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint}, Nonce: "collab-connect-publish"}, owner.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	leaseArtifacts, err := grantspkg.BuildPublishLeaseArtifacts(authorityPriv, leaseReq, "myapi", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaseBytes, _ := json.MarshalIndent(leaseArtifacts.Lease, "", "  ")
	leasePath := filepath.Join(work, "service.publish-lease.json")
	if err := os.WriteFile(leasePath, append(leaseBytes, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	edgeHTTP := freePort(t)
	edgeAdmin := freePort(t)
	edgeP2P := freePort(t)
	serviceP2P := freePort(t)
	serviceHealth := freePort(t)
	topic := discovery.NamespaceTopic("cluster-123", "default")

	edgeApp, err := edgeapp.New(ctx, edgeapp.Config{HTTPListen: fmt.Sprintf("127.0.0.1:%d", edgeHTTP), AdminListen: fmt.Sprintf("127.0.0.1:%d", edgeAdmin), P2PListen: fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", edgeP2P), Seed: "collab-connect-edge", BootstrapRetryInterval: 500 * time.Millisecond, DirectStreamTimeout: 250 * time.Millisecond, AuthorityPublicKey: authorityKey, DiscoveryTopic: topic, DiscoveryMode: discovery.ModeNamespaceV2.String(), DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default"})
	if err != nil {
		t.Fatal(err)
	}
	serviceApp, err := serviceapp.New(ctx, serviceapp.Config{Listen: fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", serviceP2P), Seed: serviceSeed, ServiceName: "myapi", ServiceID: owner.ServiceID, ServiceOwnerKeyFile: ownerPath, Target: upstream.URL, HealthListen: fmt.Sprintf("127.0.0.1:%d", serviceHealth), HeartbeatInterval: 500 * time.Millisecond, BootstrapRetryInterval: 500 * time.Millisecond, DiscoveryEnabled: true, DiscoveryTopic: topic, DiscoveryMode: discovery.ModeNamespaceV2.String(), DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", AuthorityPublicKey: authorityKey, ConnectPolicy: string(cfgpkg.ConnectPolicyNamespaceMember), MembershipCapabilityFile: serviceCapPath, ServicePublishLeaseFile: leasePath, ClusterName: "home"})
	if err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 2)
	go func() { errCh <- edgeApp.Start(ctx) }()
	go func() { errCh <- serviceApp.Start(ctx) }()
	defer func() {
		cancel()
		<-errCh
		<-errCh
	}()

	waitUntil(t, 20*time.Second, func() bool {
		return httpOK(fmt.Sprintf("http://127.0.0.1:%d/healthz", edgeHTTP)) && httpOK(fmt.Sprintf("http://127.0.0.1:%d/healthz", serviceHealth))
	}, "collaboration connect health")

	edgeInfo := peer.AddrInfo{ID: edgeApp.Host().ID(), Addrs: mustMultiaddrs(t, p2p.PeerAddrs(edgeApp.Host())...)}
	serviceInfo := peer.AddrInfo{ID: serviceApp.Host().ID(), Addrs: mustMultiaddrs(t, p2p.PeerAddrs(serviceApp.Host())...)}
	connectCtx, cancelConnect := context.WithTimeout(ctx, 10*time.Second)
	if err := edgeApp.Host().Connect(connectCtx, serviceInfo); err != nil {
		t.Fatal(err)
	}
	if err := serviceApp.Host().Connect(connectCtx, edgeInfo); err != nil {
		t.Fatal(err)
	}
	cancelConnect()
	waitUntil(t, 20*time.Second, func() bool {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/services", edgeAdmin))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var payload struct {
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
	}, "collaboration discovery cache")

	cfg := cfgpkg.Config{Node: cfgpkg.Node{Seed: "collab-connect-client", P2PListen: "/ip4/127.0.0.1/tcp/0"}, Edge: cfgpkg.Edge{AdminListen: fmt.Sprintf("127.0.0.1:%d", edgeAdmin)}, CurrentCluster: "home", CurrentNamespace: "default", Clusters: map[string]cfgpkg.Cluster{"home": {ClusterID: "cluster-123", AuthorityPublicKey: authorityKey, MembershipCapabilityFile: clientCapPath, Namespaces: map[string]cfgpkg.Namespace{"default": {MembershipCapabilityFile: clientCapPath}}}}}
	cfgPath := filepath.Join(work, "client.yaml")
	if err := cfgpkg.WriteFile(cfgPath, cfg, true); err != nil {
		t.Fatal(err)
	}

	result, err := connectflow.Resolve(ctx, collaborationConnectDeps{}, connectflow.Request{ConfigPath: cfgPath, ServiceRef: "myapi", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	bridgeCtx, bridgeCancel := context.WithCancel(ctx)
	defer bridgeCancel()
	bridgeErr := make(chan error, 1)
	go func() { bridgeErr <- result.App.Start(bridgeCtx) }()
	defer func() { bridgeCancel(); <-bridgeErr }()

	waitUntil(t, 10*time.Second, func() bool { return httpOK(result.LocalURL + "/healthz") }, "collaboration bridge health")
	resp, err := http.Get(result.LocalURL + "/v1/dummy?from=collaboration")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected status %d body=%s", resp.StatusCode, body)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["query"] != "from=collaboration" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}
