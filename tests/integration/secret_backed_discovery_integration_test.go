package integration_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	serviceapp "github.com/origama/tubo/internal/app/service"
	capability "github.com/origama/tubo/internal/capability"
	catalog "github.com/origama/tubo/internal/catalog"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/serviceidentity"
	"golang.org/x/crypto/ssh"
)

func TestSecretBackedDiscoveryObserveServicesHandlesMismatchAndRotation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
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
	serviceSeed := "secret-backed-discovery-service-seed"
	servicePeerID, err := p2p.PeerIDFromSeed(serviceSeed)
	if err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	capPath := filepath.Join(work, "membership.cap.json")
	membership, err := capability.SignMembershipCapability(capability.MembershipCapability{
		ClusterID:     "cluster-194",
		NamespaceID:   "team",
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
	membershipBytes, _ := json.MarshalIndent(membership, "", "  ")
	if err := os.WriteFile(capPath, append(membershipBytes, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	leaseReq, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{
		ClusterID:             "cluster-194",
		NamespaceID:           "team",
		ServiceID:             owner.ServiceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(owner.PublicKey),
		PublisherPeerID:       servicePeerID.String(),
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce},
		Nonce:                 "secret-backed-discovery-lease",
	}, owner.PrivateKey)
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

	oldRef, oldTopic, oldCtx := mustIntegrationDiscoveryRef(t, "cluster-194", "team")
	newRef, _, _ := mustIntegrationDiscoveryRef(t, "cluster-194", "team")

	serviceHealth := freePort(t)
	serviceP2P := freePort(t)
	serviceApp, err := serviceapp.New(ctx, serviceapp.Config{
		Listen:                   fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", serviceP2P),
		Seed:                     serviceSeed,
		ServiceName:              "myapi",
		ServiceID:                owner.ServiceID,
		Target:                   upstream.URL,
		HealthListen:             fmt.Sprintf("127.0.0.1:%d", serviceHealth),
		HeartbeatInterval:        500 * time.Millisecond,
		BootstrapRetryInterval:   500 * time.Millisecond,
		DiscoveryEnabled:         true,
		DiscoveryTopic:           oldTopic,
		DiscoveryMode:            discovery.ModeNamespaceV3.String(),
		DiscoveryClusterID:       "cluster-194",
		DiscoveryNamespaceID:     "team",
		DiscoveryContext:         oldCtx,
		AuthorityPublicKey:       authorityKey,
		MembershipCapabilityFile: capPath,
		ServicePublishLeaseFile:  leasePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- serviceApp.Start(ctx) }()
	defer func() {
		cancel()
		<-errCh
	}()
	waitUntil(t, 20*time.Second, func() bool { return httpOK(fmt.Sprintf("http://127.0.0.1:%d/healthz", serviceHealth)) }, "secret-backed service health")

	bootstrapPeers := []string{}
	for _, addr := range p2p.PeerAddrs(serviceApp.Host()) {
		bootstrapPeers = append(bootstrapPeers, addr)
	}

	observe := func(cfg cfgpkg.Config) ([]catalog.Service, error) {
		return catalog.ObserveServices(cfg, 5*time.Second, nil)
	}

	baseCfg := cfgpkg.Config{
		CurrentCluster:   "home",
		CurrentNamespace: "team",
		Network:          cfgpkg.Network{BootstrapPeers: bootstrapPeers},
		Clusters: map[string]cfgpkg.Cluster{
			"home": {
				ClusterID:                "cluster-194",
				AuthorityPublicKey:       authorityKey,
				MembershipCapabilityFile: capPath,
				Namespaces: map[string]cfgpkg.Namespace{
					"team": {Discovery: cfgpkg.NamespaceDiscoveryEnabled, DiscoverySecretCurrent: oldRef},
				},
			},
		},
	}

	services, err := observe(baseCfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.RequireService(services, "myapi"); err != nil {
		t.Fatalf("expected matching discovery state to observe service, got services=%#v err=%v", services, err)
	}

	mismatchRef, _, _ := mustIntegrationDiscoveryRef(t, "cluster-194", "team")
	mismatchCfg := baseCfg
	mismatchCluster := mismatchCfg.Clusters["home"]
	mismatchNS := mismatchCluster.Namespaces["team"]
	mismatchNS.DiscoverySecretCurrent = mismatchRef
	mismatchCluster.Namespaces["team"] = mismatchNS
	mismatchCfg.Clusters["home"] = mismatchCluster
	services, err = observe(mismatchCfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 0 {
		t.Fatalf("expected mismatched discovery state to observe no services, got %#v", services)
	}

	rotatedCfg := baseCfg
	rotatedCluster := rotatedCfg.Clusters["home"]
	rotatedNS := rotatedCluster.Namespaces["team"]
	rotatedNS.DiscoverySecretCurrent = newRef
	previous := *oldRef
	previous.ExpiresAt = time.Now().Add(30 * time.Second).UTC()
	rotatedNS.DiscoverySecretPrevious = &previous
	rotatedCluster.Namespaces["team"] = rotatedNS
	rotatedCfg.Clusters["home"] = rotatedCluster
	services, err = observe(rotatedCfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.RequireService(services, "myapi"); err != nil {
		t.Fatalf("expected rotated current+previous state to observe service during grace, got services=%#v err=%v", services, err)
	}

	expiredCfg := rotatedCfg
	expiredCluster := expiredCfg.Clusters["home"]
	expiredNS := expiredCluster.Namespaces["team"]
	expiredPrev := *expiredNS.DiscoverySecretPrevious
	expiredPrev.ExpiresAt = time.Now().Add(-time.Minute).UTC()
	expiredNS.DiscoverySecretPrevious = &expiredPrev
	expiredCluster.Namespaces["team"] = expiredNS
	expiredCfg.Clusters["home"] = expiredCluster
	services, err = observe(expiredCfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 0 {
		t.Fatalf("expected expired previous discovery state to be ignored, got %#v", services)
	}
}
