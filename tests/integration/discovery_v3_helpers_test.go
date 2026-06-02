package integration_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
)

func mustIntegrationDiscoveryRef(t *testing.T, clusterID, namespace string) (*cfgpkg.ManagedSecretRef, string, *discovery.NamespaceDiscoveryContext) {
	t.Helper()
	path := filepath.Join(t.TempDir(), clusterID+"-"+namespace+".discovery.secret")
	secret, err := cfgpkg.GenerateSecretBytes(cfgpkg.NamespaceDiscoverySecretLength)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, secret, 0600); err != nil {
		t.Fatal(err)
	}
	ref := &cfgpkg.ManagedSecretRef{Type: cfgpkg.SecretTypeNamespaceDiscovery, KeyID: "nsdk_test_" + namespace, File: path, CreatedAt: time.Now().UTC()}
	ctx := &discovery.NamespaceDiscoveryContext{ClusterID: clusterID, NamespaceID: namespace, KeyID: ref.KeyID, Secret: secret}
	topic, err := discovery.DeriveNamespaceTopicV3(*ctx)
	if err != nil {
		t.Fatal(err)
	}
	return ref, topic, ctx
}
