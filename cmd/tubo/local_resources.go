package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	workspace "github.com/origama/tubo/internal/workspace"
)

type overlayView = workspace.OverlayView

type clusterView = workspace.ClusterView

type namespaceView = workspace.NamespaceView

type secretView = workspace.SecretDescription

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
	fmt.Fprintln(w, "NAME\tCURRENT\tCLUSTER_ID\tDISCOVERY_PEERS\tCAPABILITIES\tNAMESPACES")
	for _, item := range items {
		current := ""
		if item.Current {
			current = "*"
		}
		peers := strings.Join(item.DiscoveryQueryPeers, ",")
		if peers == "" {
			peers = "-"
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
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", item.Name, current, clusterID, peers, caps, namespaces)
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

func printSecretViews(items []secretView) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TYPE\tSCOPE\tSTATUS\tKEY_ID\tEXPIRES\tFINGERPRINT\tFILE_STATUS")
	for _, item := range items {
		scope := item.Cluster + "/" + item.Namespace
		if scope == "/" {
			scope = "-"
		}
		expires := item.ExpiresAt
		if expires == "" {
			expires = "-"
		}
		fingerprint := item.Fingerprint
		if fingerprint == "" {
			fingerprint = "-"
		}
		fileStatus := item.FileStatus
		if fileStatus == "" {
			fileStatus = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", item.Type, scope, item.Status, item.KeyID, expires, fingerprint, fileStatus)
	}
	_ = w.Flush()
}

func printOverlayDescription(desc workspace.OverlayDescription) {
	fmt.Printf("Name: %s\n", desc.Name)
	fmt.Printf("Current: %t\n", desc.Current)
	fmt.Printf("Swarm key file: %s\n", desc.SwarmKeyFile)
	fmt.Println("Relays:")
	if len(desc.Relays) == 0 {
		fmt.Println("  - none")
	} else {
		for _, relay := range desc.Relays {
			fmt.Printf("  - %s\n", relay)
		}
	}
	fmt.Println("Bootstrap peers:")
	if len(desc.BootstrapPeers) == 0 {
		fmt.Println("  - none")
	} else {
		for _, peer := range desc.BootstrapPeers {
			fmt.Printf("  - %s\n", peer)
		}
	}
}

func printClusterDescription(desc workspace.ClusterDescription) {
	fmt.Printf("Name: %s\n", desc.Name)
	fmt.Printf("Current: %t\n", desc.Current)
	fmt.Printf("Cluster ID: %s\n", desc.ClusterID)
	fmt.Printf("Authority public key: %s\n", desc.AuthorityPublicKey)
	fmt.Println("Discovery query peers:")
	if len(desc.DiscoveryQueryPeers) == 0 {
		fmt.Println("  - none")
	} else {
		for _, peer := range desc.DiscoveryQueryPeers {
			fmt.Printf("  - %s\n", peer)
		}
	}
	fmt.Println("Capabilities:")
	if len(desc.Capabilities) == 0 {
		fmt.Println("  - none")
	} else {
		for _, cap := range desc.Capabilities {
			fmt.Printf("  - %s\n", cap)
		}
	}
	fmt.Println("Namespaces:")
	if len(desc.Namespaces) == 0 {
		fmt.Println("  - none")
	} else {
		for _, namespace := range desc.Namespaces {
			marker := ""
			if namespace.Current {
				marker = " (current)"
			}
			fmt.Printf("  - %s%s\n", namespace.Name, marker)
		}
	}
}

func printNamespaceDescription(desc workspace.NamespaceDescription) {
	fmt.Printf("Name: %s\n", desc.Name)
	fmt.Printf("Cluster: %s\n", desc.Cluster)
	fmt.Printf("Current cluster: %t\n", desc.CurrentCluster)
	fmt.Printf("Current namespace: %t\n", desc.CurrentNamespace)
	fmt.Printf("Current overlay: %s\n", desc.CurrentOverlay)
	fmt.Printf("Discovery: %s\n", desc.Discovery)
	fmt.Printf("Connect policy: %s\n", desc.ConnectPolicy)
	fmt.Printf("Public default: %t\n", desc.PublicDefault)
	fmt.Println("Metadata:")
	fmt.Println("  - namespace is defined locally in the current cluster")
	printSecretDescription("Current discovery secret", desc.DiscoverySecretCurrent)
	printSecretDescription("Previous discovery secret", desc.DiscoverySecretPrevious)
}

func printSecretDescription(label string, desc *workspace.SecretDescription) {
	fmt.Printf("%s:\n", label)
	if desc == nil {
		fmt.Println("  - none")
		return
	}
	fmt.Printf("  Type: %s\n", desc.Type)
	if desc.Cluster != "" {
		fmt.Printf("  Cluster: %s\n", desc.Cluster)
	}
	if desc.Namespace != "" {
		fmt.Printf("  Namespace: %s\n", desc.Namespace)
	}
	if desc.Status != "" {
		fmt.Printf("  Status: %s\n", desc.Status)
	}
	fmt.Printf("  Key ID: %s\n", desc.KeyID)
	fmt.Printf("  File: %s\n", desc.File)
	if desc.CreatedAt != "" {
		fmt.Printf("  Created at: %s\n", desc.CreatedAt)
	}
	if desc.ExpiresAt != "" {
		fmt.Printf("  Expires at: %s\n", desc.ExpiresAt)
	}
	if desc.Fingerprint != "" {
		fmt.Printf("  Fingerprint: %s\n", desc.Fingerprint)
	}
	fmt.Printf("  File status: %s\n", desc.FileStatus)
	fmt.Printf("  Permissions: %s\n", desc.PermissionState)
	if desc.Diagnostic != "" {
		fmt.Printf("  Diagnostic: %s\n", desc.Diagnostic)
	}
}

func printSecretScopeDescription(desc workspace.SecretScopeDescription) {
	fmt.Printf("Type: %s\n", desc.Type)
	fmt.Printf("Cluster: %s\n", desc.Cluster)
	fmt.Printf("Namespace: %s\n", desc.Namespace)
	printSecretDescription("Current discovery secret", desc.Current)
	printSecretDescription("Previous discovery secret", desc.Previous)
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
	ref, err := workspace.ParseRef(resource)
	if err != nil {
		return err
	}
	if _, err := localWorkspace().Use(*configPath, ref); err != nil {
		return err
	}
	fmt.Printf("updated current_%s: %s\n", ref.Kind, ref.Name)
	return nil
}

func localGetResource(resource string, configPath string, jsonOut bool) error {
	ws := localWorkspace()
	switch resource {
	case "overlays":
		items, err := ws.ListOverlays(configPath)
		if err != nil {
			return err
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
		items, err := ws.ListClusters(configPath)
		if err != nil {
			return err
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
		result, err := ws.ListNamespaces(configPath)
		if err != nil {
			return err
		}
		if jsonOut {
			return printJSON(struct {
				Cluster string          `json:"cluster"`
				Count   int             `json:"count"`
				Items   []namespaceView `json:"items"`
			}{Cluster: result.Cluster, Count: len(result.Items), Items: result.Items})
		}
		printNamespaceViews(result.Items)
		return nil
	case "secrets":
		items, err := ws.ListSecrets(configPath)
		if err != nil {
			return err
		}
		if jsonOut {
			return printJSON(struct {
				Count int          `json:"count"`
				Items []secretView `json:"items"`
			}{Count: len(items), Items: items})
		}
		printSecretViews(items)
		return nil
	default:
		return fmt.Errorf("unsupported local resource list %q", resource)
	}
}

func localDescribeResource(resource string, configPath string) error {
	ws := localWorkspace()
	ref, err := workspace.ParseRef(resource)
	if err != nil {
		return err
	}
	switch ref.Kind {
	case "overlay":
		desc, err := ws.DescribeOverlay(configPath, ref.Name)
		if err != nil {
			return err
		}
		printOverlayDescription(desc)
		return nil
	case "cluster":
		desc, err := ws.DescribeCluster(configPath, ref.Name)
		if err != nil {
			return err
		}
		printClusterDescription(desc)
		return nil
	case "namespace":
		desc, err := ws.DescribeNamespace(configPath, ref.Name)
		if err != nil {
			return err
		}
		printNamespaceDescription(desc)
		return nil
	case "secret":
		desc, err := ws.DescribeSecret(configPath, resource)
		if err != nil {
			return err
		}
		printSecretScopeDescription(desc)
		return nil
	case "pipe":
		scope, err := resolveServiceScopeFromConfig(configPath)
		if err != nil {
			return err
		}
		pipe, err := loadPipeDefinition(configPath, scope.Cluster, scope.Namespace, ref.Name)
		if err != nil {
			return err
		}
		printPipeDescription(pipeDefinitionViewFromDefinition(pipe))
		return nil
	default:
		return fmt.Errorf("unsupported describe resource %q", resource)
	}
}
