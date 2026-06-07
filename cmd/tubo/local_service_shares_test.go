package main

import (
	"crypto/ed25519"
	crand "crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/p2p"
)

func TestMintServiceShareArtifactsKeepsGrantServiceExplicit(t *testing.T) {
	_, authPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes, err := x509.MarshalPKCS8PrivateKey(authPriv)
	if err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(t.TempDir(), "authority.key")
	if err := os.WriteFile(authPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pemBytes}), 0600); err != nil {
		t.Fatal(err)
	}
	relayHost, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "share-mint-relay", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relayHost.Close()
	cfg := cfgpkg.Config{
		CurrentCluster:   "home",
		CurrentNamespace: "default",
		Network: cfgpkg.Network{
			RelayPeers: []string{p2p.PeerAddrs(relayHost)[0]},
		},
		Clusters: map[string]cfgpkg.Cluster{
			"home": {
				ClusterID:               "cluster-123",
				AuthorityPrivateKeyFile: authPath,
				Namespaces: map[string]cfgpkg.Namespace{
					"default": {},
				},
			},
		},
	}
	cluster := cfg.Clusters["home"]
	cluster.AuthorityPublicKey = mustClusterAuthorityPublicKey(t, authPriv)
	svc := cfgpkg.NamespaceService{ServiceID: "svc-123", ServiceSeed: "service-seed", Kind: cfgpkg.ServiceKindHTTP}
	artifacts, err := mintServiceShareArtifacts("/tmp/config.yaml", cfg, cluster, "home", "default", "myapi", svc, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := parseAndVerifyServiceShareToken(artifacts.Token)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ServiceEndpoint.PeerID == "" || len(payload.ServiceEndpoint.Addresses) == 0 {
		t.Fatalf("missing service endpoint: %#v", payload.ServiceEndpoint)
	}
	if payload.GrantService.Protocol != "" || len(payload.GrantService.Peers) != 0 {
		t.Fatalf("grant service should be omitted unless explicit, got %#v", payload.GrantService)
	}
}

func mustClusterAuthorityPublicKey(t *testing.T, priv ed25519.PrivateKey) string {
	t.Helper()
	pub, err := clusterAuthorityPublicKeyString(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}
