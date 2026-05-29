package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"futures/internal/config"
	"futures/internal/dashboard"
	"futures/internal/storage"
)

//go:embed web
var webFS embed.FS

func main() {
	slog.SetDefault(
		slog.New(
			slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
				Level: slog.LevelInfo,
				ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
					return a
				},
			}),
		),
	)

	pgCfg := config.PostgresConfig{
		Host:     getenv("POSTGRES_HOST", "localhost"),
		Port:     getenvInt("POSTGRES_PORT", 5432),
		User:     mustenv("POSTGRES_USER"),
		Password: mustenv("POSTGRES_PASSWORD"),
		DBName:   mustenv("POSTGRES_DB"),
		SSLMode:  getenv("POSTGRES_SSLMODE", "disable"),
		MaxConns: 5,
	}

	port := getenv("DASHBOARD_PORT", "8080")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := storage.NewPostgresPool(ctx, pgCfg)
	if err != nil {
		slog.Error("connect postgres", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	subbed, err := fs.Sub(webFS, "web")
	if err != nil {
		slog.Error("embed sub", "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	dashboard.NewHandler(pool, http.FS(subbed)).Register(mux)

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		slog.Info("dashboard listening", "addr", fmt.Sprintf("http://localhost:%s", port))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down dashboard")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func mustenv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("required env var not set", "key", key)
		os.Exit(1)
	}
	return v
}
