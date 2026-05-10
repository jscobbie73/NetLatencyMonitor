package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ResultsRequest is the agent → controller payload for /api/v1/results.
// One request = one probe cycle from one source = one or more (target, latency)
// observations sharing a probe_run_id.
type ResultsRequest struct {
	SourceID      string              `json:"source_id"`
	ProbeRunID    string              `json:"probe_run_id"`
	ObservedAt    time.Time           `json:"observed_at"`
	Results       []ResultObservation `json:"results"`
	SpoolMetadata SpoolMetadata       `json:"spool_metadata"`
}

// ResultObservation is one (target, latency) measurement.
type ResultObservation struct {
	TargetID  string   `json:"target_id"`
	LatencyMs *float64 `json:"latency_ms"` // nil on failure
	Error     string   `json:"error"`      // empty on success
}

// SpoolMetadata mirrors the agent's spool snapshot per spec §3.5.
type SpoolMetadata struct {
	SpoolDepth            int   `json:"spool_depth"`
	SpoolOldestAgeSeconds int   `json:"spool_oldest_age_seconds"`
	SpoolDropsTotal       int64 `json:"spool_drops_total"`
	DrainAttempt          int   `json:"drain_attempt"`
}

// handleResults implements POST /api/v1/results per spec §3.4.
//
// Status code contract:
//
//	201 Created  — first successful write of this probe_run_id
//	409 Conflict — replay of identical (source_id, probe_run_id, target_id)
//	422          — payload malformed; agent must fix
//	401          — handled in middleware
//	5xx          — transient; agent must retry
func (s *Server) handleResults(w http.ResponseWriter, r *http.Request) {
	node, ok := nodeFromCtx(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req ResultsRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, "invalid json: "+err.Error())
		return
	}
	if err := validateResultsRequest(node.ID, &req); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	status, err := s.persistResults(r.Context(), &req)
	if err != nil {
		s.log.Error().Err(err).Str("node_id", node.ID).Str("probe_run_id", req.ProbeRunID).Msg("results: persist failed")
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Update the per-node spool snapshot regardless of new vs replay; the
	// agent's view of its own spool is still the freshest signal we have.
	stats := SpoolStats{
		Depth:         req.SpoolMetadata.SpoolDepth,
		OldestAgeSecs: req.SpoolMetadata.SpoolOldestAgeSeconds,
		DropsTotal:    req.SpoolMetadata.SpoolDropsTotal,
		ObservedAt:    time.Now().UTC(),
		HasOldest:     req.SpoolMetadata.SpoolDepth > 0,
	}
	if err := s.nodes.UpdateSpoolStats(r.Context(), node.ID, stats); err != nil {
		// Don't fail the request just because the bookkeeping update raced
		// with a node deletion; log and continue.
		s.log.Warn().Err(err).Str("node_id", node.ID).Msg("results: spool stats update failed")
	}

	s.log.Info().
		Str("node_id", node.ID).
		Str("probe_run_id", req.ProbeRunID).
		Int("targets", len(req.Results)).
		Int("status", status).
		Msg("results: ingest")

	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// persistResults writes the batch in a single transaction. Returns 201 on
// fresh write, 409 if any row collides on (source_id, probe_run_id, target_id).
//
// Per spec §3.4, replay of identical content returns 409. We treat any UNIQUE
// collision in this batch as a replay; the agent doesn't construct mixed
// batches (one batch == one probe cycle == one HTTP POST).
func (s *Server) persistResults(ctx context.Context, req *ResultsRequest) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO probe_results
		  (source_id, probe_run_id, target_id, observed_at, latency_ms, error)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, obs := range req.Results {
		var latency sql.NullFloat64
		if obs.LatencyMs != nil {
			latency.Valid = true
			latency.Float64 = *obs.LatencyMs
		}
		var errCol sql.NullString
		if obs.Error != "" {
			errCol.Valid = true
			errCol.String = obs.Error
		}
		_, err := stmt.ExecContext(ctx,
			req.SourceID, req.ProbeRunID, obs.TargetID,
			req.ObservedAt.UTC().Format("2006-01-02 15:04:05"),
			latency, errCol)
		if err != nil {
			if isUniqueViolation(err) {
				// Replay — confirm the collision is on this exact run id
				// (not some other constraint), then return 409.
				return http.StatusConflict, nil
			}
			return 0, fmt.Errorf("insert observation: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return http.StatusCreated, nil
}

func validateResultsRequest(authNodeID string, req *ResultsRequest) error {
	if req.SourceID == "" {
		return errors.New("source_id required")
	}
	if req.SourceID != authNodeID {
		// The bearer token authenticates a specific node; refuse to ingest
		// results claiming to be from a different source.
		return errors.New("source_id does not match authenticated node")
	}
	if req.ProbeRunID == "" {
		return errors.New("probe_run_id required")
	}
	if req.ObservedAt.IsZero() {
		return errors.New("observed_at required")
	}
	if len(req.Results) == 0 {
		return errors.New("results must be non-empty")
	}
	seen := make(map[string]struct{}, len(req.Results))
	for i, r := range req.Results {
		if r.TargetID == "" {
			return fmt.Errorf("results[%d].target_id required", i)
		}
		if _, dup := seen[r.TargetID]; dup {
			return fmt.Errorf("results[%d]: duplicate target_id %q", i, r.TargetID)
		}
		seen[r.TargetID] = struct{}{}
		if r.LatencyMs == nil && r.Error == "" {
			return fmt.Errorf("results[%d]: latency_ms or error required", i)
		}
		if r.LatencyMs != nil && *r.LatencyMs < 0 {
			return fmt.Errorf("results[%d]: latency_ms must be >= 0", i)
		}
	}
	return nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}
