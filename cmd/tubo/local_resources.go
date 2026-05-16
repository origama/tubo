package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	cfgpkg "github.com/origama/tubo/internal/config"
)

type overlayView struct {
	Name           string   `json:"name"`
	Current        bool     `json:"current"`
	Relays         []string `json:"relays,omitempty"`
	BootstrapPeers []string `json:"bootstrap_peers,omitempty"`
	SwarmKeyFile   string   `json:"swarm_key_file,omitempty"`
}

type clusterView struct {
	Name               string   `json:"name"`
	Current            bool     `json:"current"`
	ClusterID          string   `json:"cluster_id,omitempty"`
	AuthorityPublicKey string   `json:"authority_public_key,omitempty"`
	Capabilities       []string `json:"capabilities,omitempty"`
	Namespaces         []string `json:"namespaces,omitempty"`
}

type namespaceView struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
	Cluster string `json:"cluster"`
}

func loadLocalConfigOrError(path string) (cfgpkg.Config, error) {
	if path == "" {
		path = defaultTuboConfigPath()
	}
	cfg, err := cfgpkg.LoadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfgpkg.Config{}, fmt.Errorf("config not found at %s; run `tubo join` first or pass --config", path)
		}
		return cfgpkg.Config{}, err
	}
	return cfg, nil
}

func saveLocalConfig(path string, cfg cfgpkg.Config) error {
	if path == "" {
		path = defaultTuboConfigPath()
	}
	return cfgpkg.WriteFile(path, cfg, true)
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

func overlayViews(cfg cfgpkg.Config) []overlayView {
	if len(cfg.Overlays) == 0 {
		return nil
	}
	names := make([]string, 0, len(cfg.Overlays))
	for name := range cfg.Overlays {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]overlayView, 0, len(names))
	for _, name := range names {
		overlay := cfg.Overlays[name]
		items = append(items, overlayView{
			Name:           name,
			Current:        name == cfg.CurrentOverlay,
			Relays:         append([]string(nil), overlay.Relays...),
			BootstrapPeers: append([]string(nil), overlay.BootstrapPeers...),
			SwarmKeyFile:   overlay.SwarmKeyFile,
		})
	}
	return items
}

func clusterViews(cfg cfgpkg.Config) []clusterView {
	if len(cfg.Clusters) == 0 {
		return nil
	}
	names := make([]string, 0, len(cfg.Clusters))
	for name := range cfg.Clusters {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]clusterView, 0, len(names))
	for _, name := range names {
		cluster := cfg.Clusters[name]
		namespaceNames := make([]string, 0, len(cluster.Namespaces))
		for namespace := range cluster.Namespaces {
			namespaceNames = append(namespaceNames, namespace)
		}
		sort.Strings(namespaceNames)
		items = append(items, clusterView{
			Name:               name,
			Current:            name == cfg.CurrentCluster,
			ClusterID:          cluster.ClusterID,
			AuthorityPublicKey: cluster.AuthorityPublicKey,
			Capabilities:       append([]string(nil), cluster.Capabilities...),
			Namespaces:         namespaceNames,
		})
	}
	return items
}

func namespaceViews(cfg cfgpkg.Config) ([]namespaceView, error) {
	if cfg.CurrentCluster == "" {
		return nil, errors.New("no current cluster selected; run `tubo use cluster/<name>` first")
	}
	cluster, ok := cfg.Clusters[cfg.CurrentCluster]
	if !ok {
		return nil, fmt.Errorf("current cluster %q not found in config", cfg.CurrentCluster)
	}
	if len(cluster.Namespaces) == 0 {
		return nil, fmt.Errorf("cluster %q has no namespaces configured", cfg.CurrentCluster)
	}
	names := make([]string, 0, len(cluster.Namespaces))
	for name := range cluster.Namespaces {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]namespaceView, 0, len(names))
	for _, name := range names {
		items = append(items, namespaceView{Name: name, Current: name == cfg.CurrentNamespace, Cluster: cfg.CurrentCluster})
	}
	return items, nil
}

func printOverlayViews(items []overlayView) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCURRENT\tRELAYS\tBOOTSTRAP_PEERS\tSWARM_KEY_FILE")
	for _, item := range items {
		current := ""
		if item.Current {
			current = "*"
		}
		relays := strings.Join(item.Relays, ",")
		if relays == "" {
			relays = "-"
		}
		bootstrap := strings.Join(item.BootstrapPeers, ",")
		if bootstrap == "" {
			bootstrap = "-"
		}
		swarmKey := item.SwarmKeyFile
		if swarmKey == "" {
			swarmKey = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", item.Name, current, relays, bootstrap, swarmKey)
	}
	_ = w.Flush()
}

func printClusterViews(items []clusterView) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCURRENT\tCLUSTER_ID\tCAPABILITIES\tNAMESPACES")
	for _, item := range items {
		current := ""
		if item.Current {
			current = "*"
		}
		caps := strings.Join(item.Capabilities, ",")
		if caps == "" {
			caps = "-"
		}
		namespaces := strings.Join(item.Namespaces, ",")
		if namespaces == "" {
			namespaces = "-"
		}
		clusterID := item.ClusterID
		if clusterID == "" {
			clusterID = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", item.Name, current, clusterID, caps, namespaces)
	}
	_ = w.Flush()
}

func printNamespaceViews(items []namespaceView) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCURRENT\tCLUSTER")
	for _, item := range items {
		current := ""
		if item.Current {
			current = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", item.Name, current, item.Cluster)
	}
	_ = w.Flush()
}

func describeOverlayResource(cfg cfgpkg.Config, name string) error {
	overlay, ok := cfg.Overlays[name]
	if !ok {
		return fmt.Errorf("overlay %q not found", name)
	}
	fmt.Printf("Name: %s\n", name)
	fmt.Printf("Current: %t\n", name == cfg.CurrentOverlay)
	fmt.Printf("Swarm key file: %s\n", overlay.SwarmKeyFile)
	fmt.Println("Relays:")
	if len(overlay.Relays) == 0 {
		fmt.Println("  - none")
	} else {
		for _, relay := range overlay.Relays {
			fmt.Printf("  - %s\n", relay)
		}
	}
	fmt.Println("Bootstrap peers:")
	if len(overlay.BootstrapPeers) == 0 {
		fmt.Println("  - none")
	} else {
		for _, peer := range overlay.BootstrapPeers {
			fmt.Printf("  - %s\n", peer)
		}
	}
	return nil
}

func describeClusterResource(cfg cfgpkg.Config, name string) error {
	cluster, ok := cfg.Clusters[name]
	if !ok {
		return fmt.Errorf("cluster %q not found", name)
	}
	namespaceNames := make([]string, 0, len(cluster.Namespaces))
	for namespace := range cluster.Namespaces {
		namespaceNames = append(namespaceNames, namespace)
	}
	sort.Strings(namespaceNames)
	fmt.Printf("Name: %s\n", name)
	fmt.Printf("Current: %t\n", name == cfg.CurrentCluster)
	fmt.Printf("Cluster ID: %s\n", cluster.ClusterID)
	fmt.Printf("Authority public key: %s\n", cluster.AuthorityPublicKey)
	fmt.Println("Capabilities:")
	if len(cluster.Capabilities) == 0 {
		fmt.Println("  - none")
	} else {
		for _, cap := range cluster.Capabilities {
			fmt.Printf("  - %s\n", cap)
		}
	}
	fmt.Println("Namespaces:")
	if len(namespaceNames) == 0 {
		fmt.Println("  - none")
	} else {
		for _, namespace := range namespaceNames {
			marker := ""
			if namespace == cfg.CurrentNamespace {
				marker = " (current)"
			}
			fmt.Printf("  - %s%s\n", namespace, marker)
		}
	}
	return nil
}

func describeNamespaceResource(cfg cfgpkg.Config, name string) error {
	if cfg.CurrentCluster == "" {
		return errors.New("no current cluster selected; run `tubo use cluster/<name>` first")
	}
	cluster, ok := cfg.Clusters[cfg.CurrentCluster]
	if !ok {
		return fmt.Errorf("current cluster %q not found in config", cfg.CurrentCluster)
	}
	if _, ok := cluster.Namespaces[name]; !ok {
		return fmt.Errorf("namespace %q not found in cluster %q", name, cfg.CurrentCluster)
	}
	fmt.Printf("Name: %s\n", name)
	fmt.Printf("Cluster: %s\n", cfg.CurrentCluster)
	fmt.Printf("Current cluster: %t\n", true)
	fmt.Printf("Current namespace: %t\n", name == cfg.CurrentNamespace)
	fmt.Printf("Current overlay: %s\n", cfg.CurrentOverlay)
	fmt.Println("Metadata:")
	fmt.Println("  - namespace is defined locally in the current cluster")
	return nil
}

func localUseCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tubo use <overlay/name|cluster/name|namespace/name> [flags]")
	}
	resource := args[0]
	fs := flag.NewFlagSet("use", flag.ContinueOnError)
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	cfg, err := loadLocalConfigOrError(*configPath)
	if err != nil {
		return err
	}
	kind, name, err := parseLocalResourceRef(resource)
	if err != nil {
		return err
	}
	switch kind {
	case "overlay":
		overlay, ok := cfg.Overlays[name]
		if !ok {
			return fmt.Errorf("overlay %q not found", name)
		}
		cfg.CurrentOverlay = name
		applyOverlayToNetwork(&cfg, overlay)
	case "cluster":
		if _, ok := cfg.Clusters[name]; !ok {
			return fmt.Errorf("cluster %q not found", name)
		}
		cfg.CurrentCluster = name
	case "namespace":
		if cfg.CurrentCluster == "" {
			return errors.New("no current cluster selected; run `tubo use cluster/<name>` first")
		}
		cluster, ok := cfg.Clusters[cfg.CurrentCluster]
		if !ok {
			return fmt.Errorf("current cluster %q not found in config", cfg.CurrentCluster)
		}
		if _, ok := cluster.Namespaces[name]; !ok {
			return fmt.Errorf("namespace %q not found in cluster %q", name, cfg.CurrentCluster)
		}
		cfg.CurrentNamespace = name
	default:
		return fmt.Errorf("unsupported use resource %q", resource)
	}
	if err := saveLocalConfig(*configPath, cfg); err != nil {
		return err
	}
	fmt.Printf("updated current_%s: %s\n", kind, name)
	return nil
}

func parseLocalResourceRef(resource string) (string, string, error) {
	parts := strings.SplitN(resource, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "", "", fmt.Errorf("unsupported resource %q", resource)
	}
	switch parts[0] {
	case "overlay", "cluster", "namespace", "service":
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("unsupported resource %q", resource)
	}
}

func localGetResource(resource string, configPath string, jsonOut bool) error {
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return err
	}
	switch resource {
	case "overlays":
		items := overlayViews(cfg)
		if len(items) == 0 {
			return errors.New("no overlays configured")
		}
		if jsonOut {
			return printJSON(struct {
				Count int           `json:"count"`
				Items []overlayView `json:"items"`
			}{Count: len(items), Items: items})
		}
		printOverlayViews(items)
		return nil
	case "clusters":
		items := clusterViews(cfg)
		if len(items) == 0 {
			return errors.New("no clusters configured")
		}
		if jsonOut {
			return printJSON(struct {
				Count int           `json:"count"`
				Items []clusterView `json:"items"`
			}{Count: len(items), Items: items})
		}
		printClusterViews(items)
		return nil
	case "namespaces":
		items, err := namespaceViews(cfg)
		if err != nil {
			return err
		}
		if jsonOut {
			return printJSON(struct {
				Cluster string          `json:"cluster"`
				Count   int             `json:"count"`
				Items   []namespaceView `json:"items"`
			}{Cluster: cfg.CurrentCluster, Count: len(items), Items: items})
		}
		printNamespaceViews(items)
		return nil
	default:
		return fmt.Errorf("unsupported local resource list %q", resource)
	}
}

func localDescribeResource(resource string, configPath string) error {
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return err
	}
	kind, name, err := parseLocalResourceRef(resource)
	if err != nil {
		return err
	}
	switch kind {
	case "overlay":
		return describeOverlayResource(cfg, name)
	case "cluster":
		return describeClusterResource(cfg, name)
	case "namespace":
		return describeNamespaceResource(cfg, name)
	default:
		return fmt.Errorf("unsupported describe resource %q", resource)
	}
}
