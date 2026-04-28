package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"p2p-api-tunnel/internal/app/bridge"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := bridge.LoadConfigFromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	app, err := bridge.New(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	if err := app.Start(ctx); err != nil {
		log.Fatal(err)
	}
}
