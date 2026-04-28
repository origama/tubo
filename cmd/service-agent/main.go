package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"p2p-api-tunnel/internal/app/service"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := service.LoadConfigFromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	app, err := service.New(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	if err := app.Start(ctx); err != nil {
		log.Fatal(err)
	}
}
