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

type NamespaceDescription struct {
	Name             string
	Cluster          string
	CurrentCluster   bool
	CurrentNamespace bool
	CurrentOverlay   string
	Discovery        cfgpkg.NamespaceDiscovery
	ConnectPolicy    cfgpkg.ConnectPolicy
	PublicDefault    bool
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
