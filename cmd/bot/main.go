package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"futures/internal/app"
	"futures/internal/config"
	"futures/internal/storage"
	"futures/internal/telemetry"
)

func main() {
	configPath := flag.String("config", "config/config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		slog.Error("invalid config", "error", err)
		os.Exit(1)
	}

	// Set log level
	level := slog.LevelInfo
	switch cfg.App.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Database connections
	pool, err := storage.NewPostgresPool(ctx, cfg.Database.Postgres)
	if err != nil {
		slog.Error("connect postgres", "error", err)
		os.Exit(1)
	}

	redisClient, err := storage.NewRedisClient(cfg.Database.Redis)
	if err != nil {
		slog.Error("connect redis", "error", err)
		os.Exit(1)
	}

	tradeRepo := telemetry.NewInstrumentedTradeRepo(storage.NewPGTradeRepository(pool))
	riskRepo := storage.NewPGRiskRepository(pool)
	cache := telemetry.NewInstrumentedStateCache(storage.NewRedisStateCache(redisClient), 60)

	application, err := app.New(cfg)
	if err != nil {
		slog.Error("create app", "error", err)
		os.Exit(1)
	}

	if err := application.Run(ctx, pool, redisClient, tradeRepo, riskRepo, cache); err != nil {
		slog.Error("app error", "error", err)
		os.Exit(1)
	}
}
