package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	"golang.org/x/crypto/ssh"
)

func localCreateCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tubo create <cluster/name|namespace/name|service/name> [flags]")
	}
	resource := args[0]
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	kind, name, err := parseLocalResourceRef(resource)
	if err != nil {
		return err
	}
	switch kind {
	case "cluster":
		return createLocalCluster(*configPath, name)
	case "namespace":
		return createLocalNamespace(*configPath, name)
	case "service":
		return createLocalService(*configPath, name)
	default:
		return fmt.Errorf("unsupported create resource %q", resource)
	}
}

func createLocalCluster(configPath, name string) error {
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return err
	}
	if cfg.Clusters == nil {
		cfg.Clusters = make(map[string]cfgpkg.Cluster)
	}
	if _, exists := cfg.Clusters[name]; exists {
		return fmt.Errorf("cluster %q already exists", name)
	}
	clusterDir := filepath.Join(filepath.Dir(configPath), "clusters", sanitizeProcessName(name))
	if err := os.MkdirAll(clusterDir, 0700); err != nil {
		return err
	}
	priv, pub, err := newClusterAuthorityKeyPair()
	if err != nil {
		return err
	}
	pubKey, err := ssh.NewPublicKey(pub)
	if err != nil {
		return err
	}
	pubAuthorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pubKey)))
	clusterID := clusterIDFromAuthorityKey(pubAuthorized)
	privPath := filepath.Join(clusterDir, "authority.key")
	capPath := filepath.Join(clusterDir, "membership.cap.json")
	if err := writeClusterAuthorityKey(privPath, priv); err != nil {
		return err
	}
	membership, err := capability.SignMembershipCapability(capability.MembershipCapability{
		ClusterID:     clusterID,
		NamespaceID:   "default",
		SubjectPeerID: clusterID,
		Permissions: []string{
			capability.PermissionSubscribe,
			capability.PermissionList,
			capability.PermissionPublish,
			capability.PermissionConnect,
		},
		ExpiresAt: time.Now().Add(365 * 24 * time.Hour),
	}, priv)
	if err != nil {
		return err
	}
	if err := writeCapabilityFile(capPath, membership); err != nil {
		return err
	}
	cfg.Clusters[name] = cfgpkg.Cluster{
		ClusterID:                clusterID,
		AuthorityPublicKey:       pubAuthorized,
		AuthorityPrivateKeyFile:  privPath,
		MembershipCapabilityFile: capPath,
		Namespaces:               map[string]cfgpkg.Namespace{"default": {}},
	}
	cfg.CurrentCluster = name
	cfg.CurrentNamespace = "default"
	if err := saveLocalConfig(configPath, cfg); err != nil {
		return err
	}
	fmt.Printf("created cluster %q\n", name)
	fmt.Printf("cluster id: %s\n", clusterID)
	fmt.Printf("authority public key: %s\n", pubAuthorized)
	fmt.Printf("authority key file: %s\n", privPath)
	fmt.Printf("membership capability file: %s\n", capPath)
	return nil
}

func createLocalNamespace(configPath, name string) error {
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return err
	}
	if cfg.CurrentCluster == "" {
		return errors.New("no current cluster selected; run `tubo use cluster/<name>` first")
	}
	cluster, ok := cfg.Clusters[cfg.CurrentCluster]
	if !ok {
		return fmt.Errorf("current cluster %q not found in config", cfg.CurrentCluster)
	}
	if cluster.Namespaces == nil {
		cluster.Namespaces = make(map[string]cfgpkg.Namespace)
	}
	if _, exists := cluster.Namespaces[name]; exists {
		return fmt.Errorf("namespace %q already exists in cluster %q", name, cfg.CurrentCluster)
	}
	cluster.Namespaces[name] = cfgpkg.Namespace{}
	cfg.Clusters[cfg.CurrentCluster] = cluster
	cfg.CurrentNamespace = name
	if err := saveLocalConfig(configPath, cfg); err != nil {
		return err
	}
	fmt.Printf("created namespace %q in cluster %q\n", name, cfg.CurrentCluster)
	return nil
}

func newClusterAuthorityKeyPair() (ed25519.PrivateKey, ed25519.PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return priv, pub, nil
}

func writeClusterAuthorityKey(path string, priv ed25519.PrivateKey) error {
	encoded, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: encoded})
	return nil
}

func writeCapabilityFile(path string, cap capability.MembershipCapability) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cap, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0600)
}

func clusterIDFromAuthorityKey(publicKey string) string {
	sum := sha256.Sum256([]byte(publicKey))
	return "cluster-" + fmt.Sprintf("%x", sum[:8])
}
