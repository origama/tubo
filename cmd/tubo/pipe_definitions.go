package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	cfgpkg "github.com/origama/tubo/internal/config"
)

type pipeDefinitionView struct {
	Name         string   `json:"name"`
	Cluster      string   `json:"cluster,omitempty"`
	Namespace    string   `json:"namespace,omitempty"`
	ServiceRef   string   `json:"service_ref,omitempty"`
	ServiceID    string   `json:"service_id,omitempty"`
	ServiceKind  string   `json:"service_kind,omitempty"`
	Local        string   `json:"local,omitempty"`
	Path         string   `json:"path,omitempty"`
	SelectedAddr string   `json:"selected_addr,omitempty"`
	SelectedPath string   `json:"selected_path,omitempty"`
	CreatedAt    string   `json:"created_at,omitempty"`
	UpdatedAt    string   `json:"updated_at,omitempty"`
	Status       string   `json:"status"`
	Missing      []string `json:"missing,omitempty"`
}

type pipeDefinitionLocation struct {
	Cluster   string
	Namespace string
	Name      string
}

func persistPipeDefinitionFromConnect(configPath string, req connectCLIRequest, state detachedProcessState) (cfgpkg.NamespacePipe, bool, pipeDefinitionLocation, error) {
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return cfgpkg.NamespacePipe{}, false, pipeDefinitionLocation{}, err
	}
	clusterName := strings.TrimSpace(state.Cluster)
	if clusterName == "" {
		clusterName = strings.TrimSpace(req.Cluster)
	}
	namespaceName := strings.TrimSpace(state.Namespace)
	if namespaceName == "" {
		namespaceName = strings.TrimSpace(req.Namespace)
	}
	if clusterName == "" || namespaceName == "" {
		return cfgpkg.NamespacePipe{}, false, pipeDefinitionLocation{}, errors.New("pipe definition requires a cluster and namespace scope")
	}
	cluster, ok := cfg.Clusters[clusterName]
	if !ok {
		return cfgpkg.NamespacePipe{}, false, pipeDefinitionLocation{}, fmt.Errorf("cluster %q not found in config", clusterName)
	}
	if cluster.Namespaces == nil {
		return cfgpkg.NamespacePipe{}, false, pipeDefinitionLocation{}, fmt.Errorf("cluster %q has no namespaces configured", clusterName)
	}
	namespace, ok := cluster.Namespaces[namespaceName]
	if !ok {
		return cfgpkg.NamespacePipe{}, false, pipeDefinitionLocation{}, fmt.Errorf("namespace %q not found in cluster %q", namespaceName, clusterName)
	}
	if namespace.Pipes == nil {
		namespace.Pipes = map[string]cfgpkg.NamespacePipe{}
	}
	name := strings.TrimSpace(state.Name)
	if name == "" {
		return cfgpkg.NamespacePipe{}, false, pipeDefinitionLocation{}, errors.New("pipe name is required")
	}
	previous, existed := namespace.Pipes[name]
	now := time.Now().UTC()
	definition := cfgpkg.NamespacePipe{
		Name:         name,
		ServiceRef:   firstNonEmpty(strings.TrimSpace(req.ServiceRef), strings.TrimSpace(state.Service)),
		ServiceID:    strings.TrimSpace(state.ServiceID),
		ServiceKind:  cfgpkg.ServiceKind(strings.TrimSpace(state.ServiceKind)),
		ClusterID:    strings.TrimSpace(cluster.ClusterID),
		NamespaceID:  namespaceName,
		Local:        normalizeConnectProcessLocal(state.Local),
		Path:         strings.TrimSpace(state.Path),
		SelectedAddr: strings.TrimSpace(state.SelectedAddr),
		SelectedPath: strings.TrimSpace(state.SelectedPath),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if existed && !previous.CreatedAt.IsZero() {
		definition.CreatedAt = previous.CreatedAt
	}
	if existed && definition.ServiceRef == "" {
		definition.ServiceRef = previous.ServiceRef
	}
	if existed && definition.ServiceID == "" {
		definition.ServiceID = previous.ServiceID
	}
	if existed && definition.ServiceKind == "" {
		definition.ServiceKind = previous.ServiceKind
	}
	if existed && definition.Local == "" {
		definition.Local = previous.Local
	}
	if existed && definition.Path == "" {
		definition.Path = previous.Path
	}
	if existed && definition.SelectedAddr == "" {
		definition.SelectedAddr = previous.SelectedAddr
	}
	if existed && definition.SelectedPath == "" {
		definition.SelectedPath = previous.SelectedPath
	}
	namespace.Pipes[name] = definition
	cluster.Namespaces[namespaceName] = namespace
	cfg.Clusters[clusterName] = cluster
	if err := saveLocalConfig(configPath, cfg); err != nil {
		return cfgpkg.NamespacePipe{}, false, pipeDefinitionLocation{}, err
	}
	return previous, existed, pipeDefinitionLocation{Cluster: clusterName, Namespace: namespaceName, Name: name}, nil
}

func restorePipeDefinition(configPath, clusterName, namespaceName, name string, previous cfgpkg.NamespacePipe, existed bool) error {
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return err
	}
	cluster, ok := cfg.Clusters[clusterName]
	if !ok {
		return nil
	}
	namespace, ok := cluster.Namespaces[namespaceName]
	if !ok {
		return nil
	}
	if existed {
		if namespace.Pipes == nil {
			namespace.Pipes = map[string]cfgpkg.NamespacePipe{}
		}
		namespace.Pipes[name] = previous
	} else if len(namespace.Pipes) > 0 {
		delete(namespace.Pipes, name)
	}
	cluster.Namespaces[namespaceName] = namespace
	cfg.Clusters[clusterName] = cluster
	return saveLocalConfig(configPath, cfg)
}

func loadPipeDefinition(configPath, clusterName, namespaceName, name string) (cfgpkg.NamespacePipe, error) {
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return cfgpkg.NamespacePipe{}, err
	}
	cluster, ok := cfg.Clusters[clusterName]
	if !ok {
		return cfgpkg.NamespacePipe{}, fmt.Errorf("cluster %q not found in config", clusterName)
	}
	namespace, ok := cluster.Namespaces[namespaceName]
	if !ok {
		return cfgpkg.NamespacePipe{}, fmt.Errorf("namespace %q not found in cluster %q", namespaceName, clusterName)
	}
	if len(namespace.Pipes) == 0 {
		return cfgpkg.NamespacePipe{}, fmt.Errorf("no pipe definitions found in cluster %q namespace %q", clusterName, namespaceName)
	}
	pipe, ok := namespace.Pipes[name]
	if !ok {
		return cfgpkg.NamespacePipe{}, fmt.Errorf("pipe %q not found in cluster %q namespace %q", name, clusterName, namespaceName)
	}
	if pipe.Name == "" {
		pipe.Name = name
	}
	if pipe.ClusterID == "" {
		pipe.ClusterID = cluster.ClusterID
	}
	if pipe.NamespaceID == "" {
		pipe.NamespaceID = namespaceName
	}
	return pipe, nil
}

func pipeDefinitionMissingFields(def cfgpkg.NamespacePipe) []string {
	missing := make([]string, 0, 4)
	if strings.TrimSpace(def.ServiceRef) == "" && strings.TrimSpace(def.ServiceID) == "" {
		missing = append(missing, "service_ref/service_id")
	}
	if strings.TrimSpace(string(def.ServiceKind)) == "" {
		missing = append(missing, "service_kind")
	}
	if strings.TrimSpace(def.Local) == "" {
		missing = append(missing, "local")
	}
	return missing
}

func pipeDefinitionViewFromDefinition(def cfgpkg.NamespacePipe) pipeDefinitionView {
	missing := pipeDefinitionMissingFields(def)
	status := "ready"
	if len(missing) > 0 {
		status = "incomplete"
	}
	return pipeDefinitionView{
		Name:         def.Name,
		Cluster:      def.ClusterID,
		Namespace:    def.NamespaceID,
		ServiceRef:   def.ServiceRef,
		ServiceID:    def.ServiceID,
		ServiceKind:  string(def.ServiceKind),
		Local:        def.Local,
		Path:         def.Path,
		SelectedAddr: def.SelectedAddr,
		SelectedPath: def.SelectedPath,
		CreatedAt:    formatPipeTimestamp(def.CreatedAt),
		UpdatedAt:    formatPipeTimestamp(def.UpdatedAt),
		Status:       status,
		Missing:      missing,
	}
}

func formatPipeTimestamp(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339)
}

func printPipeDescription(view pipeDefinitionView) {
	fmt.Printf("Pipe %s\n", view.Name)
	fmt.Printf("  Status: %s\n", view.Status)
	if view.Cluster != "" || view.Namespace != "" {
		fmt.Printf("  Scope: %s/%s\n", view.Cluster, view.Namespace)
	}
	if view.ServiceRef != "" {
		fmt.Printf("  Service ref: %s\n", view.ServiceRef)
	}
	if view.ServiceID != "" {
		fmt.Printf("  Service ID: %s\n", view.ServiceID)
	}
	if view.ServiceKind != "" {
		fmt.Printf("  Service kind: %s\n", view.ServiceKind)
	}
	if view.Local != "" {
		fmt.Printf("  Local: %s\n", view.Local)
	}
	if view.Path != "" {
		fmt.Printf("  Path: %s\n", view.Path)
	}
	if view.SelectedAddr != "" {
		fmt.Printf("  Selected address: %s\n", view.SelectedAddr)
	}
	if view.SelectedPath != "" {
		fmt.Printf("  Selected path: %s\n", view.SelectedPath)
	}
	if view.CreatedAt != "" {
		fmt.Printf("  Created: %s\n", view.CreatedAt)
	}
	if view.UpdatedAt != "" {
		fmt.Printf("  Updated: %s\n", view.UpdatedAt)
	}
	if len(view.Missing) > 0 {
		fmt.Printf("  Missing: %s\n", strings.Join(view.Missing, ", "))
	}
}

func inspectPipeDefinition(configPath, resource string, jsonOut bool, clusterFlag, namespaceFlag string) error {
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return err
	}
	scope, err := resolveServiceScope(cfg, clusterFlag, namespaceFlag, false)
	if err != nil {
		return err
	}
	kind, name, err := parseLocalResourceRef(resource)
	if err != nil {
		return err
	}
	if kind != "pipe" {
		return fmt.Errorf("unsupported pipe resource %q", resource)
	}
	pipe, err := loadPipeDefinition(configPath, scope.Cluster, scope.Namespace, name)
	if err != nil {
		return err
	}
	view := pipeDefinitionViewFromDefinition(pipe)
	if jsonOut {
		return printJSON(struct {
			Status string             `json:"status"`
			Pipe   pipeDefinitionView `json:"pipe"`
		}{Status: view.Status, Pipe: view})
	}
	printPipeDescription(view)
	return nil
}
