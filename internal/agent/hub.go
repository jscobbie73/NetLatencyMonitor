package agent

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/rs/zerolog"

	"github.com/jscobbie73/netlatencymonitor/internal/config"
	"github.com/jscobbie73/netlatencymonitor/internal/spool"
)

// Hub is the long-running agent mode for hub-role nodes. It probes its
// peer hubs on a fixed interval, drains the spool every cycle, and lets
// the TargetCache decide when to refetch /api/v1/targets.
type Hub struct {
	spoke         *Spoke
	probeInterval time.Duration
	startupJitter time.Duration
	log           zerolog.Logger
}

// NewHub builds a hub runner around an already-wired Spoke.
//
// probeInterval = how often to run a probe + drain cycle (NLM_HUB_PROBE_INTERVAL).
// startupJitter = max random delay before the first cycle to break thundering
// herds across a fleet (NLM_STARTUP_JITTER).
func NewHub(spoke *Spoke, probeInterval, startupJitter time.Duration, log zerolog.Logger) *Hub {
	if probeInterval <= 0 {
		probeInterval = 60 * time.Second
	}
	return &Hub{
		spoke:         spoke,
		probeInterval: probeInterval,
		startupJitter: startupJitter,
		log:           log,
	}
}

// Run blocks until ctx is cancelled, executing one Spoke.Run() per
// probeInterval. Errors from individual cycles are logged; we never give
// up on the daemon — operator decides when to stop us.
func (h *Hub) Run(ctx context.Context) error {
	if h.startupJitter > 0 {
		jitter := rand.N(h.startupJitter)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jitter):
		}
	}

	if err := h.runCycle(ctx); err != nil && !errors.Is(err, context.Canceled) {
		h.log.Warn().Err(err).Msg("hub: first cycle failed")
	}

	t := time.NewTicker(h.probeInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := h.runCycle(ctx); err != nil && !errors.Is(err, context.Canceled) {
				h.log.Warn().Err(err).Msg("hub: cycle failed")
			}
		}
	}
}

func (h *Hub) runCycle(ctx context.Context) error {
	cycleCtx, cancel := context.WithTimeout(ctx, h.probeInterval)
	defer cancel()
	_, err := h.spoke.Run(cycleCtx)
	return err
}

// HubDeps bundles the dependencies needed to construct a Hub.
type HubDeps struct {
	Cfg     config.AgentConfig
	Spool   *spool.Spool
	Sender  spool.Sender
	Source  TargetSource
	ProbeIv time.Duration
	Jitter  time.Duration
	Log     zerolog.Logger
}

// BuildHub constructs a Hub from HubDeps.
func BuildHub(d HubDeps) *Hub {
	sk := NewSpoke(d.Cfg, d.Spool, d.Sender, d.Source, d.Log)
	return NewHub(sk, d.ProbeIv, d.Jitter, d.Log)
}
