package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/omega-lb/omega-lb/internal/config"
	"github.com/omega-lb/omega-lb/internal/daemon"
)

func main() {
	cfgPath := flag.String("config", "/etc/omega-lb/config.yaml", "path to config file")
	flag.Parse()

	log, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	defer log.Sync() //nolint:errcheck

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal("failed to load config", zap.Error(err))
	}

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	d, err := daemon.New(cfg, log)
	if err != nil {
		log.Fatal("failed to create daemon", zap.Error(err))
	}

	log.Info("Omega-LB starting",
		zap.String("version", "0.1.0"),
		zap.String("mode", cfg.Mode),
	)

	if err := d.Run(ctx); err != nil && err != context.Canceled {
		log.Error("daemon exited with error", zap.Error(err))
		os.Exit(1)
	}
	log.Info("Omega-LB stopped cleanly")
}
