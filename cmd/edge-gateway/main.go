package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	edge "p2p-api-tunnel/internal/app/edge"
)

func main() {
	cfg, err := edge.LoadConfigFromEnv(os.Getenv)
	if err != nil {
		log.Fatalf("load edge config: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	app, err := edge.New(ctx, cfg)
	if err != nil {
		log.Fatalf("create gateway: %v", err)
	}

	if err := app.Start(ctx); err != nil {
		log.Fatalf("run gateway: %v", err)
	}
}
