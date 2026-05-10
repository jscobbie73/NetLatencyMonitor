// nlm-controller is the central NLM API + UI server (spec §3.2).
//
// Phase 2 wires the HTTP surface and SQLite registry/results store. Chrony
// gating, Litestream readiness, WebSocket ticket auth, /metrics, and the
// web UI land in subsequent phases.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jscobbie73/netlatencymonitor/internal/config"
	"github.com/jscobbie73/netlatencymonitor/internal/controller"
	"github.com/jscobbie73/netlatencymonitor/internal/logging"
)

func main() {
	log := logging.Init()

	cfg, err := config.LoadController()
	if err != nil {
		log.Fatal().Err(err).Msg("controller: bad config")
	}

	db, err := controller.OpenDB(cfg.DBPath)
	if err != nil {
		log.Fatal().Err(err).Str("db_path", cfg.DBPath).Msg("controller: open db")
	}
	defer func() { _ = db.Close() }()

	srv := controller.New(db, log)

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info().Str("listen", cfg.Listen).Str("db_path", cfg.DBPath).Msg("controller: starting")
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("controller: ListenAndServe")
		}
	}()

	<-ctx.Done()
	log.Info().Msg("controller: shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("controller: shutdown")
		os.Exit(1)
	}
}
