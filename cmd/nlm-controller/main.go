// nlm-controller is the central NLM API + UI server (spec §3.2).
//
// Phase 4 wires:
//   - chrony fail-closed readiness gate
//   - Litestream tracker + Phase 6 journal watcher
//   - WebSocket ticket store + 30s pruner
//   - Per-IP ticket validation rate limiter
//   - /metrics with optional NLM_METRICS_TOKEN
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jscobbie73/netlatencymonitor/internal/chrony"
	"github.com/jscobbie73/netlatencymonitor/internal/config"
	"github.com/jscobbie73/netlatencymonitor/internal/controller"
	"github.com/jscobbie73/netlatencymonitor/internal/litestream"
	"github.com/jscobbie73/netlatencymonitor/internal/logging"
	"github.com/jscobbie73/netlatencymonitor/internal/metrics"
	"github.com/jscobbie73/netlatencymonitor/internal/ticket"
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	chronyq := &chrony.CommandQuerier{Binary: cfg.ChronycBinary, Timeout: 2 * time.Second}
	lstrack := litestream.NewTracker(cfg.MaxLitestreamLag())
	lswatch := litestream.NewWatcher(lstrack, litestream.WatcherConfig{
		// Scan back MaxLitestreamLag on startup so the tracker seeds LastSyncAt
		// from the journal before the first regular poll interval elapses.
		InitialLookback: cfg.MaxLitestreamLag(),
	})
	go lswatch.Run(ctx)

	tickets := ticket.NewStore(ticket.DefaultTTL)
	rl := ticket.NewRateLimiter(cfg.WSTicketRateLimit, ticket.DefaultWindow, ticket.DefaultBlockDuration)
	go tickets.RunPruner(ctx, ticket.DefaultPruneInterval)

	m := metrics.New()

	srv := controller.NewWithOptions(db, log, controller.Options{
		DBPath:           cfg.DBPath,
		Chrony:           chronyq,
		MaxClockDriftMS:  cfg.MaxClockDriftMS,
		Litestream:       lstrack,
		MaxLitestreamLag: cfg.MaxLitestreamLag(),
		Tickets:          tickets,
		WSRateLimit:      rl,
		Metrics:          m,
		MetricsToken:     cfg.MetricsToken,
		AdminToken:       cfg.AdminToken,
		WSAllowedOrigins: cfg.WSAllowedOrigins,
	})

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	go func() {
		log.Info().
			Str("listen", cfg.Listen).
			Str("db_path", cfg.DBPath).
			Float64("max_drift_ms", cfg.MaxClockDriftMS).
			Int("max_ls_lag_sec", cfg.MaxLitestreamLagSecs).
			Bool("metrics_token", cfg.MetricsToken != "").
			Msg("controller: starting")
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
