package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/origama/tubo/internal/cli"
)

var (
	productVersion  = "0.1.0-dev"
	protocolVersion = "2.1"
	commitSHA       = "dev"
	buildDate       = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app := cli.App{Out: os.Stdout, Err: os.Stderr, ProductVersion: productVersion, ProtocolVersion: protocolVersion, CommitSHA: commitSHA, BuildDate: buildDate}
	if err := app.Run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tubo:", err)
		if try := cli.Hint(err); try != "" {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, "Try:")
			fmt.Fprintln(os.Stderr, "  "+try)
		}
		os.Exit(1)
	}
}
