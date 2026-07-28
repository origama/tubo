package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

var (
	productVersion  = "0.1.0-dev"
	protocolVersion = "2.0"
	commitSHA       = "dev"
	buildDate       = "unknown"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tubo:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp()
		return nil
	}
	switch args[0] {
	case "version":
		return versionCommand(args[1:])
	case "attach", "connect", "relay", "gateway", "join", "service", "pipe", "scope", "authority", "share", "get", "describe", "inspect", "watch", "ps", "top", "logs", "start", "restart", "stop", "rm", "keygen":
		return fmt.Errorf("%s is not implemented in the bootstrap repository", strings.Join(args, " "))
	default:
		return fmt.Errorf("unknown command %q; run `tubo help`", args[0])
	}
}

func versionCommand(args []string) error {
	jsonOutput := false
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonOutput = true
		case "--help", "-h":
			fmt.Println("Usage: tubo version [--json]")
			return nil
		default:
			return fmt.Errorf("version: unknown argument %q", arg)
		}
	}
	value := struct {
		ProductVersion  string `json:"product_version"`
		ProtocolVersion string `json:"protocol_version"`
		CommitSHA       string `json:"commit_sha"`
		BuildDate       string `json:"build_date"`
	}{productVersion, protocolVersion, commitSHA, buildDate}
	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(value)
	}
	fmt.Printf("tubo %s (protocol %s, commit %s, built %s)\n", value.ProductVersion, value.ProtocolVersion, value.CommitSHA, value.BuildDate)
	return nil
}

func printHelp() {
	fmt.Print(`tubo — private connectivity through composable pipes

Usage:
  tubo attach <target> --name <name> [-d]
  tubo connect <pipe-name|pipe/id> [--local host:port] [-d]
  tubo service create <name> --target <url>
  tubo pipe create <name> --ingress <kind> --target <resource>
  tubo start|restart|stop|rm pipe/<name>
  tubo get pipes
  tubo inspect pipe/<name>
  tubo ps
  tubo relay
  tubo gateway
  tubo scope create <cluster>/<namespace>
  tubo scope invite <cluster>/<namespace> --role <member|viewer>
  tubo scope join --token <invite>
  tubo authority start <cluster>

Model:
  Service = local terminal; never advertised
  Pipe    = persistent directed connectivity resource
  Session = active traversal of one or more pipes

Status:
  This bootstrap repository currently implements only domain validation,
  version output, documentation, and test contracts.
`)
}
