// Package controller implements the NLM controller HTTP API and SQLite-backed
// store. This file collects all JSON wire-format types (request/response
// bodies) in one place so that the shape of every endpoint is immediately
// apparent without hunting through handler files.
package controller

import (
	"embed"

	"github.com/jscobbie73/netlatencymonitor/internal/schema"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ── /api/v1/results (POST) ─────────────────────────────────────────────────
//
// Wire types aliased from internal/schema so agent and controller share one
// definition without either package importing the other.

// ResultsRequest is the agent → controller payload for POST /api/v1/results.
type ResultsRequest = schema.ResultsRequest

// ResultObservation is one (target, latency) measurement within a request.
type ResultObservation = schema.ResultObservation

// SpoolMetadata carries the agent's post-drain spool snapshot.
type SpoolMetadata = schema.SpoolMetadata

// resultsResponse is the JSON shape returned by POST /api/v1/results.
// TargetsVersion is omitted when the targets_version query fails.
type resultsResponse struct {
	Status         string `json:"status"`
	TargetsVersion *int64 `json:"targets_version,omitempty"`
}

// ── /api/v1/targets (GET) ─────────────────────────────────────────────────

// TargetsResponse is the body of GET /api/v1/targets.
type TargetsResponse = schema.TargetsResponse

// TargetEntry is one (id, address) pair in a TargetsResponse.
type TargetEntry = schema.TargetEntry

// ── /api/v1/ws-ticket (POST) + /api/v1/ws (GET) ──────────────────────────

// wsTicketResponse is the JSON shape returned by POST /api/v1/ws-ticket.
type wsTicketResponse struct {
	Ticket    string `json:"ticket"`
	ExpiresAt string `json:"expires_at"`
}

// wsHelloMessage is sent to the agent immediately after a successful WS upgrade.
type wsHelloMessage struct {
	Type   string `json:"type"`
	NodeID string `json:"node_id"`
}

// ── /api/v1/admin/nodes (GET, POST, PATCH, DELETE) ────────────────────────

// AdminNode is the JSON shape returned by admin node endpoints.
type AdminNode struct {
	ID       string `json:"id"`
	Role     string `json:"role"`
	Address  string `json:"address,omitempty"`
	Disabled bool   `json:"disabled"`
}

// adminListNodesResponse is the JSON shape returned by GET /api/v1/admin/nodes.
type adminListNodesResponse struct {
	Nodes []AdminNode `json:"nodes"`
}

// AdminCreateRequest is the body of POST /api/v1/admin/nodes. If Secret is
// empty, the controller mints a random 256-bit secret and returns it once.
type AdminCreateRequest struct {
	ID       string `json:"id"`
	Role     string `json:"role"`
	Address  string `json:"address,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	Secret   string `json:"secret,omitempty"`
}

// AdminCreateResponse is what POST /api/v1/admin/nodes returns. Secret is
// only present on create; it is never echoed by other endpoints.
type AdminCreateResponse struct {
	Node   AdminNode `json:"node"`
	Secret string    `json:"secret"`
}

// AdminPatchRequest is the body of PATCH /api/v1/admin/nodes/{id}. All
// fields are optional.
type AdminPatchRequest struct {
	Role     *string `json:"role,omitempty"`
	Address  *string `json:"address,omitempty"`
	Disabled *bool   `json:"disabled,omitempty"`
	Secret   *string `json:"secret,omitempty"`
}

// ── /readyz (GET) ─────────────────────────────────────────────────────────

type readyzResponse struct {
	Status string       `json:"status"`
	Checks readyzChecks `json:"checks"`
}

type readyzChecks struct {
	Chrony     *chronyCheck     `json:"chrony,omitempty"`
	Litestream *litestreamCheck `json:"litestream,omitempty"`
}

type chronyCheck struct {
	OK      bool    `json:"ok"`
	DriftMS float64 `json:"drift_ms,omitempty"`
	MaxMS   float64 `json:"max_ms,omitempty"`
	Error   string  `json:"error,omitempty"`
}

type litestreamCheck struct {
	OK            bool    `json:"ok"`
	ServiceActive bool    `json:"service_active,omitempty"`
	LagSeconds    float64 `json:"lag_seconds"`
	MaxLag        float64 `json:"max_lag,omitempty"`
}

// ── /api/v1/ui/ws (GET) ───────────────────────────────────────────────────

// ProbeEvent is the JSON message broadcast to UI WebSocket clients on 201.
type ProbeEvent struct {
	Type      string  `json:"type"`
	SourceID  string  `json:"source_id"`
	TargetID  string  `json:"target_id"`
	LatencyMS float64 `json:"latency_ms,omitempty"`
	Error     string  `json:"error,omitempty"`
}
