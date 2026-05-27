package workspace

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	cfgpkg "github.com/origama/tubo/internal/config"
)

type Workspace struct {
	store Store
}

func Open(store Store) *Workspace {
	if store == nil {
		store = FSStore{}
	}
	return &Workspace{store: store}
}

func ParseRef(resource string) (Ref, error) {
	parts := strings.SplitN(resource, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return Ref{}, fmt.Errorf("unsupported resource %q", resource)
	}
	switch parts[0] {
	case "overlay", "cluster", "namespace", "service":
		return Ref{Kind: parts[0], Name: parts[1]}, nil
	default:
		return Ref{}, fmt.Errorf("unsupported resource %q", resource)
	}
}

func (w *Workspace) LoadConfigOrError(path string) (cfgpkg.Config, error) {
	if path == "" {
		return cfgpkg.Config{}, errors.New("config path is required")
	}
	cfg, err := w.store.Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfgpkg.Config{}, fmt.Errorf("config not found at %s; run `tubo join` first or pass --config", path)
		}
		return cfgpkg.Config{}, err
	}
	return cfg, nil
}

func (w *Workspace) SaveConfig(path string, cfg cfgpkg.Config) error {
	if path == "" {
		return errors.New("config path is required")
	}
	return w.store.Save(path, cfg)
}

func ResolveScope(cfg cfgpkg.Config, clusterFlag, namespaceFlag string, allNamespaces bool) (Scope, error) {
	scope, err := cfgpkg.ResolveEffectiveScope(cfg, clusterFlag, namespaceFlag, allNamespaces)
	if err != nil {
		return Scope{}, err
	}
	return Scope{Cluster: scope.Cluster, Namespace: scope.Namespace, AllNamespaces: scope.AllNamespaces}, nil
}

func (w *Workspace) ListOverlays(configPath string) ([]OverlayView, error) {
	cfg, err := w.LoadConfigOrError(configPath)
	if err != nil {
		return nil, err
	}
	if len(cfg.Overlays) == 0 {
		return nil, errors.New("no overlays configured")
	}
	names := make([]string, 0, len(cfg.Overlays))
	for name := range cfg.Overlays {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]OverlayView, 0, len(names))
	for _, name := range names {
		overlay := cfg.Overlays[name]
		items = append(items, OverlayView{
			Name:           name,
			Current:        name == cfg.CurrentOverlay,
			Relays:         append([]string(nil), overlay.Relays...),
			BootstrapPeers: append([]string(nil), overlay.BootstrapPeers...),
			SwarmKeyFile:   overlay.SwarmKeyFile,
		})
	}
	return items, nil
}

func (w *Workspace) ListClusters(configPath string) ([]ClusterView, error) {
	cfg, err := w.LoadConfigOrError(configPath)
	if err != nil {
		return nil, err
	}
	if len(cfg.Clusters) == 0 {
		return nil, errors.New("no clusters configured")
	}
	names := make([]string, 0, len(cfg.Clusters))
	for name := range cfg.Clusters {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]ClusterView, 0, len(names))
	for _, name := range names {
		cluster := cfg.Clusters[name]
		namespaceNames := make([]string, 0, len(cluster.Namespaces))
		for namespace := range cluster.Namespaces {
			namespaceNames = append(namespaceNames, namespace)
		}
		sort.Strings(namespaceNames)
		items = append(items, ClusterView{
			Name:               name,
			Current:            name == cfg.CurrentCluster,
			ClusterID:          cluster.ClusterID,
			AuthorityPublicKey: cluster.AuthorityPublicKey,
			Capabilities:       append([]string(nil), cluster.Capabilities...),
			Namespaces:         namespaceNames,
		})
	}
	return items, nil
}

func (w *Workspace) ListNamespaces(configPath string) (NamespaceList, error) {
	cfg, err := w.LoadConfigOrError(configPath)
	if err != nil {
		return NamespaceList{}, err
	}
	if cfg.CurrentCluster == "" {
		return NamespaceList{}, errors.New("no current cluster selected; run `tubo use cluster/<name>` first")
	}
	cluster, ok := cfg.Clusters[cfg.CurrentCluster]
	if !ok {
		return NamespaceList{}, fmt.Errorf("current cluster %q not found in config", cfg.CurrentCluster)
	}
	if len(cluster.Namespaces) == 0 {
		return NamespaceList{}, fmt.Errorf("cluster %q has no namespaces configured", cfg.CurrentCluster)
	}
	names := make([]string, 0, len(cluster.Namespaces))
	for name := range cluster.Namespaces {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]NamespaceView, 0, len(names))
	for _, name := range names {
		items = append(items, NamespaceView{Name: name, Current: name == cfg.CurrentNamespace, Cluster: cfg.CurrentCluster})
	}
	return NamespaceList{Cluster: cfg.CurrentCluster, Items: items}, nil
}

func (w *Workspace) DescribeOverlay(configPath, name string) (OverlayDescription, error) {
	cfg, err := w.LoadConfigOrError(configPath)
	if err != nil {
		return OverlayDescription{}, err
	}
	overlay, ok := cfg.Overlays[name]
	if !ok {
		return OverlayDescription{}, fmt.Errorf("overlay %q not found", name)
	}
	return OverlayDescription{Name: name, Current: name == cfg.CurrentOverlay, Relays: append([]string(nil), overlay.Relays...), BootstrapPeers: append([]string(nil), overlay.BootstrapPeers...), SwarmKeyFile: overlay.SwarmKeyFile}, nil
}

func (w *Workspace) DescribeCluster(configPath, name string) (ClusterDescription, error) {
	cfg, err := w.LoadConfigOrError(configPath)
	if err != nil {
		return ClusterDescription{}, err
	}
	cluster, ok := cfg.Clusters[name]
	if !ok {
		return ClusterDescription{}, fmt.Errorf("cluster %q not found", name)
	}
	namespaceNames := make([]string, 0, len(cluster.Namespaces))
	for namespace := range cluster.Namespaces {
		namespaceNames = append(namespaceNames, namespace)
	}
	sort.Strings(namespaceNames)
	namespaces := make([]ClusterNamespaceDescription, 0, len(namespaceNames))
	for _, namespace := range namespaceNames {
		namespaces = append(namespaces, ClusterNamespaceDescription{Name: namespace, Current: namespace == cfg.CurrentNamespace})
	}
	return ClusterDescription{Name: name, Current: name == cfg.CurrentCluster, ClusterID: cluster.ClusterID, AuthorityPublicKey: cluster.AuthorityPublicKey, Capabilities: append([]string(nil), cluster.Capabilities...), Namespaces: namespaces}, nil
}

func (w *Workspace) DescribeNamespace(configPath, name string) (NamespaceDescription, error) {
	cfg, err := w.LoadConfigOrError(configPath)
	if err != nil {
		return NamespaceDescription{}, err
	}
	if cfg.CurrentCluster == "" {
		return NamespaceDescription{}, errors.New("no current cluster selected; run `tubo use cluster/<name>` first")
	}
	cluster, ok := cfg.Clusters[cfg.CurrentCluster]
	if !ok {
		return NamespaceDescription{}, fmt.Errorf("current cluster %q not found in config", cfg.CurrentCluster)
	}
	if _, ok := cluster.Namespaces[name]; !ok {
		return NamespaceDescription{}, fmt.Errorf("namespace %q not found in cluster %q", name, cfg.CurrentCluster)
	}
	scope, err := cfgpkg.ResolveEffectiveScope(cfg, cfg.CurrentCluster, name, false)
	if err != nil {
		return NamespaceDescription{}, err
	}
	policy := cfgpkg.EffectiveScopePolicy(cfg, scope)
	return NamespaceDescription{Name: name, Cluster: cfg.CurrentCluster, CurrentCluster: true, CurrentNamespace: name == cfg.CurrentNamespace, CurrentOverlay: cfg.CurrentOverlay, Discovery: policy.Discovery, ConnectPolicy: policy.ConnectPolicy, PublicDefault: policy.PublicDefault}, nil
}

func (w *Workspace) Use(configPath string, ref Ref) (cfgpkg.Config, error) {
	cfg, err := w.LoadConfigOrError(configPath)
	if err != nil {
		return cfgpkg.Config{}, err
	}
	switch ref.Kind {
	case "overlay":
		overlay, ok := cfg.Overlays[ref.Name]
		if !ok {
			return cfgpkg.Config{}, fmt.Errorf("overlay %q not found", ref.Name)
		}
		cfg.CurrentOverlay = ref.Name
		applyOverlayToNetwork(&cfg, overlay)
	case "cluster":
		if _, ok := cfg.Clusters[ref.Name]; !ok {
			return cfgpkg.Config{}, fmt.Errorf("cluster %q not found", ref.Name)
		}
		cfg.CurrentCluster = ref.Name
	case "namespace":
		if cfg.CurrentCluster == "" {
			return cfgpkg.Config{}, errors.New("no current cluster selected; run `tubo use cluster/<name>` first")
		}
		cluster, ok := cfg.Clusters[cfg.CurrentCluster]
		if !ok {
			return cfgpkg.Config{}, fmt.Errorf("current cluster %q not found in config", cfg.CurrentCluster)
		}
		if _, ok := cluster.Namespaces[ref.Name]; !ok {
			return cfgpkg.Config{}, fmt.Errorf("namespace %q not found in cluster %q", ref.Name, cfg.CurrentCluster)
		}
		cfg.CurrentNamespace = ref.Name
	default:
		return cfgpkg.Config{}, fmt.Errorf("unsupported use resource %q/%s", ref.Kind, ref.Name)
	}
	if err := w.SaveConfig(configPath, cfg); err != nil {
		return cfgpkg.Config{}, err
	}
	return cfg, nil
}

func applyOverlayToNetwork(cfg *cfgpkg.Config, overlay cfgpkg.Overlay) {
	if overlay.SwarmKeyFile != "" {
		cfg.Network.PrivateKeyFile = overlay.SwarmKeyFile
	}
	if len(overlay.BootstrapPeers) > 0 {
		cfg.Network.BootstrapPeers = append([]string(nil), overlay.BootstrapPeers...)
	}
	if len(overlay.Relays) > 0 {
		cfg.Network.RelayPeers = append([]string(nil), overlay.Relays...)
	}
}
