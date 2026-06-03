package main

import (
	"errors"
	"flag"
	"fmt"
	"time"
)

func localRotateCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tubo rotate secret/namespace-discovery/<cluster>/<namespace> --grace <duration> [flags]")
	}
	resource := args[0]
	fs := flag.NewFlagSet("rotate", flag.ContinueOnError)
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	grace := fs.Duration("grace", 24*time.Hour, "")
	jsonOut := fs.Bool("json", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	result, err := localWorkspace().RotateNamespaceDiscoverySecret(*configPath, resource, *grace)
	if err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(struct {
			Type      string `json:"type"`
			Cluster   string `json:"cluster"`
			Namespace string `json:"namespace"`
			Grace     string `json:"grace"`
			Current   any    `json:"current,omitempty"`
			Previous  any    `json:"previous,omitempty"`
		}{Type: result.Type, Cluster: result.Cluster, Namespace: result.Namespace, Grace: grace.String(), Current: result.Current, Previous: result.Previous})
	}
	fmt.Printf("rotated namespace discovery secret for %s/%s\n", result.Cluster, result.Namespace)
	fmt.Printf("grace: %s\n", grace.String())
	printSecretScopeDescription(result)
	return nil
}
