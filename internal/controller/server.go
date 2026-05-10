package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog"

	"github.com/jscobbie73/netlatencymonitor/internal/chrony"
	"github.com/jscobbie73/netlatencymonitor/internal/litestream"
	"github.com/jscobbie73/netlatencymonitor/internal/metrics"
	"github.com/jscobbie73/netlatencymonitor/internal/ticket"
)

// Options configures a Server. Optional fields default to safe stubs so the
// minimal Phase 2 wiring still works (e.g. tests that don't care about
// chrony or Litestream).
type Options struct {
	// DBPath is the on-disk SQLite path; used by /metrics for db_size_bytes.
	DBPath string

	// Chrony queries the host clock offset. If nil, /readyz fails closed
	// per spec §3.2 (an unknown clock is worse than a known-skewed one).
	Chrony chrony.Querier

	// MaxClockDriftMS is the gate. <= 0 disables the drift check.
	MaxClockDriftMS float64

	// Litestream tracks replication health. If nil, treated as
	// "Litestream not configured" — /readyz still passes if Chrony does.
	Litestream *litestream.Tracker

	// MaxLitestreamLag is the gate.
	MaxLitestreamLag time.Duration

	// Tickets is the WS ticket store.
	Tickets *ticket.Store

	// WSRateLimit is the per-IP failed-validation rate limiter.
	WSRateLimit *ticket.RateLimiter

	// Metrics is the Prometheus registry. If nil, no /metrics endpoint
	// is mounted and counter increments are no-ops.
	Metrics *metrics.Metrics

	// MetricsToken, if non-empty, gates /metrics with `Authorization: Bearer`.
	MetricsToken string

	// AdminToken, if non-empty, enables /api/v1/admin/* endpoints. Empty
	// = admin API returns 503 (web UI prompts the operator to set it).
	AdminToken string
}

// Server bundles the controller's HTTP dependencies. Construct with New
// (minimal wiring) or NewWithOptions (full Phase 4 wiring).
type Server struct {
	db    *sql.DB
	nodes *NodeStore
	log   zerolog.Logger
	now   func() time.Time

	dbPath string

	chrony      chrony.Querier
	maxDriftMS  float64
	litestream  *litestream.Tracker
	maxLag      time.Duration
	tickets     *ticket.Store
	wsRateLimit *ticket.RateLimiter

	metrics      *metrics.Metrics
	metricsToken string

	adminToken string
}

// New wires a minimal Server. Use NewWithOptions for the full Phase 4 stack.
func New(db *sql.DB, log zerolog.Logger) *Server {
	return NewWithOptions(db, log, Options{Tickets: ticket.NewStore(0)})
}

// NewWithOptions wires a Server with the supplied dependencies.
func NewWithOptions(db *sql.DB, log zerolog.Logger, opt Options) *Server {
	s := &Server{
		db:           db,
		nodes:        NewNodeStore(db),
		log:          log,
		now:          func() time.Time { return time.Now().UTC() },
		dbPath:       opt.DBPath,
		chrony:       opt.Chrony,
		maxDriftMS:   opt.MaxClockDriftMS,
		litestream:   opt.Litestream,
		maxLag:       opt.MaxLitestreamLag,
		tickets:      opt.Tickets,
		wsRateLimit:  opt.WSRateLimit,
		metrics:      opt.Metrics,
		metricsToken: opt.MetricsToken,
		adminToken:   opt.AdminToken,
	}
	if s.tickets == nil {
		s.tickets = ticket.NewStore(0)
	}
	return s
}

// Handler returns an http.Handler with all routes mounted. Uses the Go 1.22
// http.ServeMux pattern syntax (METHOD + path).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)

	mux.Handle("POST /api/v1/results", s.bearerAuth(http.HandlerFunc(s.handleResults)))
	mux.Handle("GET /api/v1/targets", s.bearerAuth(http.HandlerFunc(s.handleTargets)))
	mux.Handle("POST /api/v1/ws-ticket", s.bearerAuth(http.HandlerFunc(s.handleWSTicket)))
	mux.HandleFunc("GET /api/v1/ws", s.handleWS)

	mux.Handle("GET /api/v1/admin/nodes", s.adminAuth(http.HandlerFunc(s.handleAdminListNodes)))
	mux.Handle("POST /api/v1/admin/nodes", s.adminAuth(http.HandlerFunc(s.handleAdminCreateNode)))
	mux.Handle("PATCH /api/v1/admin/nodes/{id}", s.adminAuth(http.HandlerFunc(s.handleAdminPatchNode)))
	mux.Handle("DELETE /api/v1/admin/nodes/{id}", s.adminAuth(http.HandlerFunc(s.handleAdminDeleteNode)))

	if s.metrics != nil {
		mux.Handle("GET /metrics", s.metrics.Handler(s.metricsToken, s.refreshMetrics))
	}
	return mux
}

// refreshMetrics is invoked at scrape time to update the on-demand gauges:
// DB footprint and Litestream lag. Cheap stats; safe to run synchronously.
func (s *Server) refreshMetrics() {
	if s.metrics == nil {
		return
	}
	s.metrics.DBSizeBytes.Set(float64(s.dbSize()))
	if s.litestream != nil {
		st := s.litestream.Status()
		s.metrics.LitestreamLagSeconds.Set(s.litestream.Lag().Seconds())
		if !st.LastSyncAt.IsZero() {
			s.metrics.LitestreamLastSyncTimestamp.Set(float64(st.LastSyncAt.Unix()))
		}
	}
}

// handleHealthz returns 200 unconditionally; it indicates process liveness
// per spec §3.2 (chrony / Litestream gates apply only to /readyz).
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz enforces the readiness gates from spec §3.2:
//
//   - chrony query MUST succeed and drift MUST be within MaxClockDriftMS.
//     If Chrony is nil, /readyz fails closed.
//   - If Litestream is wired, the service must be active and lag within
//     MaxLitestreamLag. Litestream nil = "not configured" = pass.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"status": "ok"}
	checks := map[string]any{}
	failed := false

	// Chrony: fail-closed if unconfigured per spec.
	if s.chrony == nil {
		checks["chrony"] = map[string]any{"ok": false, "error": "not configured (fail-closed)"}
		failed = true
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		drift, err := s.chrony.Drift(ctx)
		cancel()
		switch {
		case err != nil:
			checks["chrony"] = map[string]any{"ok": false, "error": err.Error()}
			failed = true
		case s.maxDriftMS > 0 && float64(drift) > s.maxDriftMS:
			checks["chrony"] = map[string]any{"ok": false, "drift_ms": float64(drift), "max_ms": s.maxDriftMS}
			failed = true
		default:
			checks["chrony"] = map[string]any{"ok": true, "drift_ms": float64(drift)}
			if s.metrics != nil {
				s.metrics.ClockDriftMS.Set(float64(drift))
			}
		}
	}

	// Litestream: if configured, enforce the gate.
	if s.litestream != nil {
		st := s.litestream.Status()
		lag := s.litestream.Lag()
		if !s.litestream.Healthy(s.maxLag) {
			checks["litestream"] = map[string]any{
				"ok":             false,
				"service_active": st.ServiceActive,
				"lag_seconds":    lag.Seconds(),
				"max_lag":        s.maxLag.Seconds(),
			}
			failed = true
		} else {
			checks["litestream"] = map[string]any{
				"ok":          true,
				"lag_seconds": lag.Seconds(),
			}
		}
		if s.metrics != nil {
			s.metrics.LitestreamLagSeconds.Set(lag.Seconds())
			if !st.LastSyncAt.IsZero() {
				s.metrics.LitestreamLastSyncTimestamp.Set(float64(st.LastSyncAt.Unix()))
			}
		}
	}

	resp["checks"] = checks
	status := http.StatusOK
	if failed {
		status = http.StatusServiceUnavailable
		resp["status"] = "degraded"
	}
	writeJSON(w, status, resp)
}

// dbSize returns the on-disk DB size, summing main + WAL + SHM. Used by the
// /metrics scrape; called rarely so the os.Stat cost is fine.
func (s *Server) dbSize() int64 {
	if s.dbPath == "" {
		return 0
	}
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		fi, err := os.Stat(s.dbPath + suffix)
		if err == nil {
			total += fi.Size()
		}
	}
	return total
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
