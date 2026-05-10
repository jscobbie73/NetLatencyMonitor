// nlm-agent runs in three modes per spec §3.1:
//
//	--mode spoke     run a probe + drain cycle (use with --once for the
//	                 systemd-timer model in §6, or omit --once for a long-
//	                 running daemon — Phase 5 will add the daemon loop)
//	--mode listener  long-running TCP target service for spokes to dial
//	--mode hub       Phase 5 — daemon that pulls its target list from the
//	                 controller and probes on a fixed interval
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jscobbie73/netlatencymonitor/internal/agent"
	"github.com/jscobbie73/netlatencymonitor/internal/config"
	"github.com/jscobbie73/netlatencymonitor/internal/logging"
	"github.com/jscobbie73/netlatencymonitor/internal/spool"
)

func main() {
	mode := flag.String("mode", "", "spoke | hub | listener")
	once := flag.Bool("once", false, "run one probe cycle and exit (spoke/hub only)")
	flag.Parse()

	log := logging.Init()

	if *mode == "" {
		log.Fatal().Msg("--mode required (spoke|hub|listener)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch *mode {
	case "spoke":
		if err := runSpoke(ctx, *once); err != nil {
			log.Fatal().Err(err).Msg("spoke failed")
		}
	case "listener":
		if err := runListener(ctx); err != nil {
			log.Fatal().Err(err).Msg("listener failed")
		}
	case "hub":
		log.Fatal().Msg("hub mode lands in Phase 5")
	default:
		log.Fatal().Str("mode", *mode).Msg("unknown mode")
	}
}

func runSpoke(ctx context.Context, once bool) error {
	log := logging.Init()
	cfg, err := config.LoadAgent()
	if err != nil {
		return fmt.Errorf("load agent config: %w", err)
	}
	spCfg, err := config.LoadSpool()
	if err != nil {
		return fmt.Errorf("load spool config: %w", err)
	}

	sp, err := spool.Open(spCfg.Path, spool.Config{
		DrainRows:   spCfg.DrainRows,
		MaxAge:      spCfg.MaxAge(),
		MaxSize:     spCfg.MaxSizeBytes(),
		BackoffBase: spCfg.BackoffBase(),
		BackoffMax:  spCfg.BackoffMax(),
	})
	if err != nil {
		return fmt.Errorf("open spool: %w", err)
	}
	defer func() { _ = sp.Close() }()

	client := agent.NewClient(cfg.ControllerURL, cfg.NodeID, cfg.NodeSecret, cfg.HTTPTimeout)
	sk := agent.NewSpoke(cfg, sp, client, log)

	if !once {
		// Daemon mode for spoke isn't part of v1.3 — the spoke is driven
		// by a systemd timer per spec §6. Surface that explicitly.
		return errors.New("spoke daemon mode not implemented; use --once with the nlm-spoke.timer unit")
	}

	log.Info().Str("node_id", cfg.NodeID).Int("targets", len(cfg.Targets)).Msg("spoke: running one cycle")
	cycleCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	_, err = sk.Run(cycleCtx)
	return err
}

func runListener(ctx context.Context) error {
	log := logging.Init()
	addr := os.Getenv("NLM_LISTENER_LISTEN")
	if addr == "" {
		addr = ":8444"
	}
	ln := agent.NewListener(addr, log)
	return ln.ListenAndServe(ctx)
}
