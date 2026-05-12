// Package schema defines the shared wire types used by both the agent and
// the controller. Keeping them here prevents internal/agent from importing
// internal/controller (or vice versa) just to share JSON struct definitions.
//
// Layering intent:
//
//	config / logging / metrics / schema   ← foundational, no internal deps
//	spool / chrony / ticket / litestream  ← low-level services
//	agent / controller                    ← top-level domain packages
//	cmd/*                                 ← binaries
//
// Both agent and controller import schema; neither imports the other.
package schema

import "time"

// ResultsRequest is the agent → controller payload for POST /api/v1/results.
// One request = one probe cycle from one source = one or more (target, latency)
// observations sharing a probe_run_id.
type ResultsRequest struct {
	SourceID      string              `json:"source_id"`
	ProbeRunID    string              `json:"probe_run_id"`
	ObservedAt    time.Time           `json:"observed_at"`
	Results       []ResultObservation `json:"results"`
	SpoolMetadata SpoolMetadata       `json:"spool_metadata"`
}

// ResultObservation is one (target, latency) measurement within a request.
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

// TargetsResponse is the body of GET /api/v1/targets: active hubs minus self,
// plus a monotonic targets_version. Agents cache the version and refetch only
// when POST /api/v1/results echoes a new one.
type TargetsResponse struct {
	Targets        []TargetEntry `json:"targets"`
	TargetsVersion int64         `json:"targets_version"`
}

// TargetEntry is one (id, address) pair to probe.
type TargetEntry struct {
	ID      string `json:"id"`
	Address string `json:"address"`
}
