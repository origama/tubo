package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
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

type pipeDefinitionMutation struct {
	pipeDefinitionLocation
	Previous   cfgpkg.NamespacePipe
	Existed    bool
	Definition cfgpkg.NamespacePipe
}

var newPipeConfigRepository = cfgpkg.NewConfigRepository

var updatePipeConfig = func(configPath string, mutate cfgpkg.ConfigMutation) (cfgpkg.Config, error) {
	return newPipeConfigRepository(configPath).Update(context.Background(), mutate)
}

func persistPipeDefinitionFromConnect(configPath string, req connectCLIRequest, state detachedProcessState) (cfgpkg.NamespacePipe, bool, pipeDefinitionMutation, error) {
	if strings.TrimSpace(configPath) == "" {
		configPath = defaultTuboConfigPath()
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
		return cfgpkg.NamespacePipe{}, false, pipeDefinitionMutation{}, errors.New("pipe definition requires a cluster and namespace scope")
	}
	name := strings.TrimSpace(state.Name)
	if name == "" {
		return cfgpkg.NamespacePipe{}, false, pipeDefinitionMutation{}, errors.New("pipe name is required")
	}
	location := pipeDefinitionLocation{Cluster: clusterName, Namespace: namespaceName, Name: name}
	var mutation pipeDefinitionMutation
	_, err := updatePipeConfig(configPath, func(cfg *cfgpkg.Config) error {
		cluster, ok := cfg.Clusters[clusterName]
		if !ok {
			return fmt.Errorf("cluster %q not found in config", clusterName)
		}
		if cluster.Namespaces == nil {
			return fmt.Errorf("cluster %q has no namespaces configured", clusterName)
		}
		namespace, ok := cluster.Namespaces[namespaceName]
		if !ok {
			return fmt.Errorf("namespace %q not found in cluster %q", namespaceName, clusterName)
		}
		if namespace.Pipes == nil {
			namespace.Pipes = map[string]cfgpkg.NamespacePipe{}
		}
		previous, existed := namespace.Pipes[name]
		definition := pipeDefinitionFromConnect(cluster, namespaceName, name, req, state, previous, existed)
		if existed {
			if err := ensureCompatiblePipeDefinition(location, previous, definition); err != nil {
				return err
			}
		}
		namespace.Pipes[name] = definition
		cluster.Namespaces[namespaceName] = namespace
		cfg.Clusters[clusterName] = cluster
		mutation = pipeDefinitionMutation{pipeDefinitionLocation: location, Previous: previous, Existed: existed, Definition: definition}
		return nil
	})
	if err != nil {
		return cfgpkg.NamespacePipe{}, false, pipeDefinitionMutation{}, err
	}
	return mutation.Previous, mutation.Existed, mutation, nil
}

func pipeDefinitionFromConnect(cluster cfgpkg.Cluster, namespaceName, name string, req connectCLIRequest, state detachedProcessState, previous cfgpkg.NamespacePipe, existed bool) cfgpkg.NamespacePipe {
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
	if !existed {
		return definition
	}
	definition.CreatedAt = previous.CreatedAt
	if definition.CreatedAt.IsZero() {
		definition.CreatedAt = now
	}
	definition.ClusterID = firstNonEmpty(strings.TrimSpace(previous.ClusterID), definition.ClusterID)
	definition.NamespaceID = firstNonEmpty(strings.TrimSpace(previous.NamespaceID), definition.NamespaceID)
	definition.ServiceRef = firstNonEmpty(definition.ServiceRef, previous.ServiceRef)
	definition.ServiceID = firstNonEmpty(definition.ServiceID, previous.ServiceID)
	if definition.ServiceKind == "" {
		definition.ServiceKind = previous.ServiceKind
	}
	definition.Local = firstNonEmpty(definition.Local, previous.Local)
	definition.Path = firstNonEmpty(definition.Path, previous.Path)
	definition.SelectedAddr = firstNonEmpty(definition.SelectedAddr, previous.SelectedAddr)
	definition.SelectedPath = firstNonEmpty(definition.SelectedPath, previous.SelectedPath)
	return definition
}

func ensureCompatiblePipeDefinition(location pipeDefinitionLocation, previous, next cfgpkg.NamespacePipe) error {
	conflict := func(field, old, new string) error {
		if strings.TrimSpace(old) != "" && strings.TrimSpace(new) != "" && strings.TrimSpace(old) != strings.TrimSpace(new) {
			return fmt.Errorf("pipe %q in cluster %q namespace %q conflicts on %s (%q != %q)", location.Name, location.Cluster, location.Namespace, field, old, new)
		}
		return nil
	}
	if err := conflict("name", previous.Name, next.Name); err != nil {
		return err
	}
	if err := conflict("local listener", normalizeConnectProcessLocal(previous.Local), normalizeConnectProcessLocal(next.Local)); err != nil {
		return err
	}
	if err := conflict("service kind", string(previous.ServiceKind), string(next.ServiceKind)); err != nil {
		return err
	}
	if previous.ServiceID != "" || next.ServiceID != "" {
		return conflict("service id", previous.ServiceID, next.ServiceID)
	}
	return conflict("service reference", previous.ServiceRef, next.ServiceRef)
}

func rollbackPipeDefinition(configPath string, mutation pipeDefinitionMutation) (bool, error) {
	changed := false
	_, err := updatePipeConfig(configPath, func(cfg *cfgpkg.Config) error {
		cluster, ok := cfg.Clusters[mutation.Cluster]
		if !ok {
			return nil
		}
		namespace, ok := cluster.Namespaces[mutation.Namespace]
		if !ok || namespace.Pipes == nil {
			return nil
		}
		current, ok := namespace.Pipes[mutation.Name]
		if !ok || !pipeDefinitionsEqual(current, mutation.Definition) {
			return nil
		}
		if mutation.Existed {
			namespace.Pipes[mutation.Name] = mutation.Previous
		} else {
			delete(namespace.Pipes, mutation.Name)
		}
		cluster.Namespaces[mutation.Namespace] = namespace
		cfg.Clusters[mutation.Cluster] = cluster
		changed = true
		return nil
	})
	return changed, err
}

func pipeDefinitionsEqual(a, b cfgpkg.NamespacePipe) bool {
	return reflect.DeepEqual(a, b)
}

func pipeDefinitionMatchesLoaded(stored, loaded cfgpkg.NamespacePipe) bool {
	if stored.Name == "" {
		stored.Name = loaded.Name
	}
	if stored.ClusterID == "" {
		stored.ClusterID = loaded.ClusterID
	}
	if stored.NamespaceID == "" {
		stored.NamespaceID = loaded.NamespaceID
	}
	return pipeDefinitionsEqual(stored, loaded)
}

func loadPipeDefinition(configPath, clusterName, namespaceName, name string) (cfgpkg.NamespacePipe, error) {
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return cfgpkg.NamespacePipe{}, err
	}
	return pipeDefinitionInConfig(cfg, clusterName, namespaceName, name)
}

func pipeDefinitionInConfig(cfg cfgpkg.Config, clusterName, namespaceName, name string) (cfgpkg.NamespacePipe, error) {
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

func locatePipeDefinition(cfg cfgpkg.Config, pipeName string) (pipeDefinitionLocation, cfgpkg.NamespacePipe, error) {
	type match struct {
		location pipeDefinitionLocation
		pipe     cfgpkg.NamespacePipe
	}
	var matches []match
	clusterNames := make([]string, 0, len(cfg.Clusters))
	for clusterName := range cfg.Clusters {
		clusterNames = append(clusterNames, clusterName)
	}
	sort.Strings(clusterNames)
	for _, clusterName := range clusterNames {
		cluster := cfg.Clusters[clusterName]
		namespaceNames := make([]string, 0, len(cluster.Namespaces))
		for namespaceName := range cluster.Namespaces {
			namespaceNames = append(namespaceNames, namespaceName)
		}
		sort.Strings(namespaceNames)
		for _, namespaceName := range namespaceNames {
			if _, ok := cluster.Namespaces[namespaceName].Pipes[pipeName]; !ok {
				continue
			}
			pipe, err := pipeDefinitionInConfig(cfg, clusterName, namespaceName, pipeName)
			if err != nil {
				return pipeDefinitionLocation{}, cfgpkg.NamespacePipe{}, err
			}
			matches = append(matches, match{location: pipeDefinitionLocation{Cluster: clusterName, Namespace: namespaceName, Name: pipeName}, pipe: pipe})
		}
	}
	if len(matches) == 0 {
		return pipeDefinitionLocation{}, cfgpkg.NamespacePipe{}, fmt.Errorf("pipe %q not found in config", pipeName)
	}
	if len(matches) > 1 {
		return pipeDefinitionLocation{}, cfgpkg.NamespacePipe{}, fmt.Errorf("pipe %q exists in multiple scopes; select or remove duplicate definitions", pipeName)
	}
	return matches[0].location, matches[0].pipe, nil
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
	kind, name, err := parseLocalResourceRef(resource)
	if err != nil {
		return err
	}
	if kind != "pipe" {
		return fmt.Errorf("unsupported pipe resource %q", resource)
	}
	scope, err := resolveServiceScope(cfg, clusterFlag, namespaceFlag, false)
	if err != nil {
		return err
	}
	pipe, err := pipeDefinitionInConfig(cfg, scope.Cluster, scope.Namespace, name)
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
