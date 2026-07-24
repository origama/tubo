package workspace

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/p2p"
)

func (w *Workspace) CreateCluster(configPath, name string) (ClusterView, error) {
	var view ClusterView
	_, err := w.UpdateConfig(configPath, func(cfg *cfgpkg.Config) error {
		created, err := w.createClusterLocked(configPath, name, cfg)
		if err != nil {
			return err
		}
		view = created
		return nil
	})
	return view, err
}

func (w *Workspace) createClusterLocked(configPath, name string, cfg *cfgpkg.Config) (ClusterView, error) {
	if cfg.Clusters == nil {
		cfg.Clusters = make(map[string]cfgpkg.Cluster)
	}
	if _, exists := cfg.Clusters[name]; exists {
		return ClusterView{}, fmt.Errorf("cluster %q already exists", name)
	}
	paths := DerivePaths(configPath)
	if err := w.store.MkdirAll(paths.ClusterDir(name), 0o700); err != nil {
		return ClusterView{}, err
	}
	if err := w.store.MkdirAll(paths.NamespaceDir(name, "default"), 0o700); err != nil {
		return ClusterView{}, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return ClusterView{}, err
	}
	pubKey, err := ssh.NewPublicKey(pub)
	if err != nil {
		return ClusterView{}, err
	}
	pubAuthorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pubKey)))
	clusterID := clusterIDFromAuthorityKey(pubAuthorized)
	querySeed := discoveryQuerySeed(clusterID, "default")
	queryPeerID, err := p2p.PeerIDFromSeed(querySeed)
	if err != nil {
		return ClusterView{}, err
	}
	privPath := paths.ClusterAuthorityKey(name)
	capPath := paths.ClusterMembershipCapability(name)
	if err := writePrivateKey(w.store, privPath, priv); err != nil {
		return ClusterView{}, err
	}
	membership, err := capability.SignMembershipCapability(capability.MembershipCapability{
		ClusterID:     clusterID,
		NamespaceID:   "default",
		SubjectPeerID: queryPeerID.String(),
		Permissions: []string{
			capability.PermissionSubscribe,
			capability.PermissionList,
			capability.PermissionPublish,
			capability.PermissionConnect,
		},
		ExpiresAt: time.Now().Add(365 * 24 * time.Hour),
	}, priv)
	if err != nil {
		return ClusterView{}, err
	}
	if err := writeJSONFile(w.store, capPath, membership); err != nil {
		return ClusterView{}, err
	}
	defaultDiscoverySecret, err := writeNamespaceDiscoverySecret(w.store, paths.NamespaceDiscoveryCurrentSecret(name, "default"))
	if err != nil {
		return ClusterView{}, err
	}
	cfg.Clusters[name] = cfgpkg.Cluster{
		ClusterID:                clusterID,
		AuthorityPublicKey:       pubAuthorized,
		AuthorityPrivateKeyFile:  privPath,
		MembershipCapabilityFile: capPath,
		Namespaces: map[string]cfgpkg.Namespace{"default": {
			Discovery:              cfgpkg.NamespaceDiscoveryEnabled,
			DiscoverySecretCurrent: defaultDiscoverySecret,
			ConnectPolicy:          cfgpkg.ConnectPolicyNamespaceMember,
		}},
	}
	cfg.CurrentCluster = name
	cfg.CurrentNamespace = "default"
	// Ensure a stable node.seed so connect processes use the same peer id
	// that the cluster's membership capability is bound to.
	if strings.TrimSpace(cfg.Node.Seed) == "" {
		cfg.Node.Seed = querySeed
	}
	return ClusterView{Name: name, Current: true, ClusterID: clusterID, AuthorityPublicKey: pubAuthorized, Namespaces: []string{"default"}}, nil
}

func (w *Workspace) CreateNamespace(configPath, name string) (NamespaceView, error) {
	var view NamespaceView
	_, err := w.UpdateConfig(configPath, func(cfg *cfgpkg.Config) error {
		created, err := w.createNamespaceLocked(configPath, name, cfg)
		if err != nil {
			return err
		}
		view = created
		return nil
	})
	return view, err
}

func (w *Workspace) createNamespaceLocked(configPath, name string, cfg *cfgpkg.Config) (NamespaceView, error) {
	if cfg.CurrentCluster == "" {
		return NamespaceView{}, fmt.Errorf("no current cluster selected; run `tubo use cluster/<name>` first")
	}
	clusterName := cfg.CurrentCluster
	cluster, ok := cfg.Clusters[clusterName]
	if !ok {
		return NamespaceView{}, fmt.Errorf("current cluster %q not found in config", clusterName)
	}
	if cluster.ClusterID == "" || cluster.AuthorityPublicKey == "" || cluster.AuthorityPrivateKeyFile == "" {
		return NamespaceView{}, fmt.Errorf("current cluster %q is missing authority material", clusterName)
	}
	if cluster.Namespaces == nil {
		cluster.Namespaces = make(map[string]cfgpkg.Namespace)
	}
	if _, exists := cluster.Namespaces[name]; exists {
		return NamespaceView{}, fmt.Errorf("namespace %q already exists in cluster %q", name, clusterName)
	}
	priv, err := loadPrivateKey(w.store, cluster.AuthorityPrivateKeyFile)
	if err != nil {
		return NamespaceView{}, fmt.Errorf("load cluster authority key: %w", err)
	}
	querySeed := discoveryQuerySeed(cluster.ClusterID, name)
	queryPeerID, err := p2p.PeerIDFromSeed(querySeed)
	if err != nil {
		return NamespaceView{}, err
	}
	paths := DerivePaths(configPath)
	if err := w.store.MkdirAll(paths.NamespaceDir(clusterName, name), 0o700); err != nil {
		return NamespaceView{}, err
	}
	capPath := paths.NamespaceMembershipCapability(clusterName, name)
	membership, err := capability.SignMembershipCapability(capability.MembershipCapability{
		ClusterID:     cluster.ClusterID,
		NamespaceID:   name,
		SubjectPeerID: queryPeerID.String(),
		Permissions: []string{
			capability.PermissionSubscribe,
			capability.PermissionList,
			capability.PermissionPublish,
			capability.PermissionConnect,
		},
		ExpiresAt: time.Now().Add(365 * 24 * time.Hour),
	}, priv)
	if err != nil {
		return NamespaceView{}, err
	}
	if err := writeJSONFile(w.store, capPath, membership); err != nil {
		return NamespaceView{}, err
	}
	discoverySecret, err := writeNamespaceDiscoverySecret(w.store, paths.NamespaceDiscoveryCurrentSecret(clusterName, name))
	if err != nil {
		return NamespaceView{}, err
	}
	cluster.Namespaces[name] = cfgpkg.Namespace{
		MembershipCapabilityFile: capPath,
		Discovery:                cfgpkg.NamespaceDiscoveryEnabled,
		DiscoverySecretCurrent:   discoverySecret,
		ConnectPolicy:            cfgpkg.ConnectPolicyNamespaceMember,
	}
	cfg.Clusters[clusterName] = cluster
	cfg.CurrentNamespace = name
	return NamespaceView{Name: name, Current: true, Cluster: clusterName}, nil
}

func writePrivateKey(store Store, path string, priv ed25519.PrivateKey) error {
	encoded, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	if err := store.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})
	return store.WriteFile(path, block, 0o600)
}

func loadPrivateKey(store Store, path string) (ed25519.PrivateKey, error) {
	b, err := store.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("decode authority private key: invalid PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("decode authority private key: unexpected key type %T", parsed)
	}
	return priv, nil
}

func writeJSONFile(store Store, path string, value any) error {
	if err := store.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return store.WriteFile(path, b, 0o600)
}

func writeNamespaceDiscoverySecret(store Store, path string) (*cfgpkg.ManagedSecretRef, error) {
	secret, ref, err := cfgpkg.BuildNamespaceDiscoverySecretRef(path, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if err := store.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := store.WriteFile(path, secret, 0o600); err != nil {
		return nil, err
	}
	return ref, nil
}

func discoveryQuerySeed(clusterID, namespace string) string {
	return "discovery-query-" + strings.TrimSpace(clusterID) + "-" + strings.TrimSpace(namespace)
}

func clusterIDFromAuthorityKey(publicKey string) string {
	sum := sha256.Sum256([]byte(publicKey))
	return "cluster-" + fmt.Sprintf("%x", sum[:8])
}
