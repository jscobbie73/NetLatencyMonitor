package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/jscobbie73/netlatencymonitor/internal/config"
	"github.com/jscobbie73/netlatencymonitor/internal/controller"
	"github.com/jscobbie73/netlatencymonitor/internal/spool"
)

// Spoke is one probe-and-drain cycle as run by `nlm-agent --mode spoke`.
type Spoke struct {
	cfg      config.AgentConfig
	spool    *spool.Spool
	sender   spool.Sender
	log      zerolog.Logger
	now      func() time.Time
	newRunID func() (string, error)
}

// NewSpoke wires a Spoke around an open spool and a Sender.
func NewSpoke(cfg config.AgentConfig, sp *spool.Spool, sender spool.Sender, log zerolog.Logger) *Spoke {
	return &Spoke{
		cfg:    cfg,
		spool:  sp,
		sender: sender,
		log:    log,
		now:    func() time.Time { return time.Now().UTC() },
		newRunID: func() (string, error) {
			id, err := uuid.NewV7()
			if err != nil {
				return "", err
			}
			return id.String(), nil
		},
	}
}

// CycleStats reports what one Run() did.
type CycleStats struct {
	ProbeRunID string
	Targets    int
	Drain      spool.DrainStats
}

// Run runs one full cycle: probe targets, enqueue the result, then drain.
//
// Per spec §3.1, the drain step happens on every probe cycle so transient
// outages catch up on the very next tick once connectivity returns.
func (s *Spoke) Run(ctx context.Context) (CycleStats, error) {
	if len(s.cfg.Targets) == 0 {
		return CycleStats{}, errors.New("spoke: no probe targets configured")
	}

	runID, err := s.newRunID()
	if err != nil {
		return CycleStats{}, fmt.Errorf("spoke: gen run id: %w", err)
	}

	observedAt := s.now()
	results := RunProbes(ctx, s.cfg.Targets, s.cfg.ProbeTimeout)

	depth, _ := s.spool.Depth(ctx)
	oldestAge, _ := s.spool.OldestAge(ctx)
	drops, _ := s.spool.DropsTotal(ctx)

	req := controller.ResultsRequest{
		SourceID:   s.cfg.NodeID,
		ProbeRunID: runID,
		ObservedAt: observedAt,
		Results:    results,
		SpoolMetadata: controller.SpoolMetadata{
			SpoolDepth:            depth + 1, // post-enqueue, pre-drain (§3.5)
			SpoolOldestAgeSeconds: int(oldestAge.Seconds()),
			SpoolDropsTotal:       drops,
			DrainAttempt:          1,
		},
	}
	payload, err := EncodePayload(req)
	if err != nil {
		return CycleStats{}, fmt.Errorf("spoke: encode payload: %w", err)
	}

	if err := s.spool.Enqueue(ctx, runID, payload); err != nil {
		// A duplicate here means we crashed after enqueue last cycle and
		// somehow regenerated the same id — vanishingly unlikely with
		// UUIDv7. Log and continue to the drain step.
		if !errors.Is(err, spool.ErrDuplicate) {
			return CycleStats{}, fmt.Errorf("spoke: enqueue: %w", err)
		}
		s.log.Warn().Str("probe_run_id", runID).Msg("spoke: duplicate enqueue ignored")
	}

	stats, err := s.spool.Drain(ctx, s.sender)
	if err != nil {
		return CycleStats{ProbeRunID: runID, Targets: len(results)}, fmt.Errorf("spoke: drain: %w", err)
	}
	if stats.Dropped > 0 {
		s.log.Warn().Int("dropped", stats.Dropped).Msg("spoke: retention dropped rows")
	}
	if stats.Permanent > 0 {
		s.log.Warn().Int("permanent_failures", stats.Permanent).Msg("spoke: rows have permanent failures (likely 4xx)")
	}

	s.log.Info().
		Str("probe_run_id", runID).
		Int("targets", len(results)).
		Int("considered", stats.Considered).
		Int("accepted", stats.Accepted).
		Int("transient", stats.Transient).
		Int("permanent", stats.Permanent).
		Int("skipped", stats.Skipped).
		Int("dropped", stats.Dropped).
		Msg("spoke: cycle complete")

	return CycleStats{ProbeRunID: runID, Targets: len(results), Drain: stats}, nil
}
