package workspace

import cfgpkg "github.com/origama/tubo/internal/config"

type Ref struct {
	Kind string
	Name string
}

type Scope struct {
	Cluster       string
	Namespace     string
	AllNamespaces bool
}

type OverlayView struct {
	Name           string   `json:"name"`
	Current        bool     `json:"current"`
	Relays         []string `json:"relays,omitempty"`
	BootstrapPeers []string `json:"bootstrap_peers,omitempty"`
	SwarmKeyFile   string   `json:"swarm_key_file,omitempty"`
}

type ClusterView struct {
	Name               string   `json:"name"`
	Current            bool     `json:"current"`
	ClusterID          string   `json:"cluster_id,omitempty"`
	AuthorityPublicKey string   `json:"authority_public_key,omitempty"`
	Capabilities       []string `json:"capabilities,omitempty"`
	Namespaces         []string `json:"namespaces,omitempty"`
}

type NamespaceView struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
	Cluster string `json:"cluster"`
}

type OverlayDescription struct {
	Name           string
	Current        bool
	Relays         []string
	BootstrapPeers []string
	SwarmKeyFile   string
}

type ClusterDescription struct {
	Name               string
	Current            bool
	ClusterID          string
	AuthorityPublicKey string
	Capabilities       []string
	Namespaces         []ClusterNamespaceDescription
}

type ClusterNamespaceDescription struct {
	Name    string
	Current bool
}

type SecretDescription struct {
	Type            string `json:"type"`
	Cluster         string `json:"cluster,omitempty"`
	Namespace       string `json:"namespace,omitempty"`
	Status          string `json:"status,omitempty"`
	KeyID           string `json:"key_id,omitempty"`
	File            string `json:"file,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	Fingerprint     string `json:"fingerprint,omitempty"`
	FileStatus      string `json:"file_status,omitempty"`
	PermissionState string `json:"permission_state,omitempty"`
	Diagnostic      string `json:"diagnostic,omitempty"`
}

type SecretScopeDescription struct {
	Type      string             `json:"type"`
	Cluster   string             `json:"cluster"`
	Namespace string             `json:"namespace"`
	Current   *SecretDescription `json:"current,omitempty"`
	Previous  *SecretDescription `json:"previous,omitempty"`
}

type NamespaceDescription struct {
	Name                    string
	Cluster                 string
	CurrentCluster          bool
	CurrentNamespace        bool
	CurrentOverlay          string
	Discovery               cfgpkg.NamespaceDiscovery
	ConnectPolicy           cfgpkg.ConnectPolicy
	PublicDefault           bool
	DiscoverySecretCurrent  *SecretDescription
	DiscoverySecretPrevious *SecretDescription
}

type NamespaceList struct {
	Cluster string
	Items   []NamespaceView
}

type ServiceContext struct {
	Config      cfgpkg.Config
	ClusterName string
	Namespace   string
	Name        string
	Cluster     cfgpkg.Cluster
	Service     cfgpkg.NamespaceService
}

type EnsureServiceResult struct {
	Config  cfgpkg.Config
	Context ServiceContext
	Created bool
	Changed bool
}

type CreateServiceResult struct {
	Context       ServiceContext
	AlreadyExists bool
}
