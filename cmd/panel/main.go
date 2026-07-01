// Command panel is the Kraken control-plane API: authentication, RBAC, the game
// spec catalog, server scheduling, and the source-of-truth state for the fleet.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/briggleman/kraken/internal/panel"
	"github.com/briggleman/kraken/internal/panel/api"
	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/secrets"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
	"github.com/briggleman/kraken/internal/panel/store/migrate"
	"github.com/briggleman/kraken/internal/panel/store/postgres"
	"github.com/briggleman/kraken/internal/shared/mtls"
	"github.com/briggleman/kraken/internal/shared/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("kraken-panel", version.String())
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("panel exited with error", "err", err)
		os.Exit(1)
	}
}

// ensureCA resolves the Agent-enrollment CA signing material. When an external
// CA is configured (KRAKEN_CA_CERT/KRAKEN_CA_KEY) it returns nil so api.New loads
// those files unchanged. Otherwise it loads a previously generated CA from the
// store, or generates and persists a new self-signed one. The in-memory dev store
// regenerates the CA on each restart (any enrolled Agent must re-enroll).
func ensureCA(ctx context.Context, st store.Store, cfg *config.Config, logger *slog.Logger) (cert, key []byte) {
	if cfg.CASigning() {
		return nil, nil
	}
	if c, k, err := st.GetCA(ctx); err == nil {
		logger.Info("loaded self-generated Agent-enrollment CA from store")
		return c, k
	}
	c, k, err := mtls.GenerateCA()
	if err != nil {
		logger.Error("could not generate Agent-enrollment CA — enrollment disabled", "err", err)
		return nil, nil
	}
	if err := st.SaveCA(ctx, c, k); err != nil {
		logger.Error("could not persist generated CA", "err", err)
	}
	logger.Warn("generated a self-signed Agent-enrollment CA (no KRAKEN_CA_CERT/KEY set); " +
		"persisted to the store — the in-memory dev store regenerates it on restart")
	return c, k
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx := context.Background()

	// Master key for encrypting at-rest DB secrets (API tokens, CA private key).
	secKey, fromEnv, err := config.ResolveSecretsKey(cfg.ConfigFile)
	if err != nil {
		return fmt.Errorf("resolve secrets key: %w", err)
	}
	cipher, err := secrets.New(secKey)
	if err != nil {
		return fmt.Errorf("init secrets cipher: %w", err)
	}
	if !fromEnv {
		logger.Warn("secrets key auto-generated to the config file — set KRAKEN_SECRETS_KEY (base64 of 32 bytes) for production")
	}

	var st store.Store
	switch {
	case cfg.UsesMemoryStore():
		logger.Warn("using in-memory store — data is not persisted (set KRAKEN_DATABASE_URL for Postgres)")
		st = memory.New()
	default:
		logger.Info("running database migrations")
		if err := migrate.Up(cfg.DatabaseURL); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		pg, err := postgres.New(ctx, cfg.DatabaseURL, postgres.WithCipher(cipher))
		if err != nil {
			return fmt.Errorf("connect postgres: %w", err)
		}
		defer func() { _ = pg.Close() }()
		st = pg
		logger.Info("using Postgres store (secrets encrypted at rest)")
	}
	if err := panel.Seed(ctx, st, cfg, logger); err != nil {
		return fmt.Errorf("seed: %w", err)
	}

	// Restart signal: a UI-driven config change (e.g. connecting Postgres) writes
	// the config file then asks the process to exit cleanly so a supervisor brings
	// it back up reading the new datastore.
	restart := make(chan struct{}, 1)
	requestRestart := func() {
		select {
		case restart <- struct{}{}:
		default:
		}
	}

	caCert, caKey := ensureCA(ctx, st, cfg, logger)
	srv := api.New(cfg, st, logger, api.WithCA(caCert, caKey), api.WithRestart(requestRestart))
	defer func() { _ = srv.Close() }()

	// Quickstart: register the co-located Agent as the "local" node so a fresh
	// single-host install reaches a running server without any CLI. No-op when
	// disabled or when a fleet already exists.
	srv.AutoRegisterLocalNode(ctx)

	// Background reconciler: keeps stored server state in sync with what the
	// Agents' crash watchdogs report (crash / auto-restart / ready transitions).
	reconcileCtx, stopReconcile := context.WithCancel(ctx)
	defer stopReconcile()
	srv.StartReconciler(reconcileCtx, 4*time.Second)

	// Background scheduler: runs due cron tasks (restart / backup / command).
	srv.StartScheduler(reconcileCtx, 30*time.Second)

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	errCh := make(chan error, 1)
	go func() {
		logger.Info("panel listening", "addr", cfg.HTTPAddr, "env", cfg.Env, "version", version.Version)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case sig := <-stop:
		logger.Info("shutting down", "signal", sig.String())
	case <-restart:
		logger.Info("restarting to apply new configuration")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	return nil
}
