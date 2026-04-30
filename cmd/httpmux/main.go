package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/mtimpe/httpmux/internal/auth"
	"github.com/mtimpe/httpmux/internal/config"
	"github.com/mtimpe/httpmux/internal/session"
	internalssh "github.com/mtimpe/httpmux/internal/ssh"
	"github.com/mtimpe/httpmux/internal/web"
)

func main() {
	configPath := flag.String("config", "httpmux.yaml", "path to config file")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	pool, err := internalssh.NewBastionPool(cfg.Bastion, cfg.SSH)
	if err != nil {
		slog.Error("failed to initialize bastion pool", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	authenticator := auth.New(cfg.Auth)
	mgr := session.NewManager(pool, cfg)
	defer mgr.Close()

	srv := web.New(cfg, authenticator, mgr)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	mgr.StartCleanup(ctx)

	slog.Info("starting httpmux", "listen", cfg.Server.Listen)
	if err := srv.ListenAndServe(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
	slog.Info("server stopped")
}
