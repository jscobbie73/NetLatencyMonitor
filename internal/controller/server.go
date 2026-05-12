package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/jscobbie73/netlatencymonitor/internal/chrony"
	"github.com/jscobbie73/netlatencymonitor/internal/litestream"
	"github.com/jscobbie73/netlatencymonitor/internal/metrics"
	"github.com/jscobbie73/netlatencymonitor/internal/ticket"
	nlmui "github.com/jscobbie73/netlatencymonitor/internal/ui"
)

// Options configures a Server. Optional fields default to safe stubs so
// tests that don't care about chrony or Litestream can use New directly.
type Options struct {
	// DBPath is the on-disk SQLite path; used by /metrics for db_size_bytes.
	DBPath string

	// Chrony queries the host clock offset. If nil, /readyz fails closed
	// (an unknown clock is worse than a known-skewed one).
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

	// WSAllowedOrigins is the allowlist for WebSocket upgrade Origin checks.
	// Empty means allow same-host + no-origin (CLI) only.
	WSAllowedOrigins []string
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

	adminToken       string
	wsHub            *WSHub
	wsAllowedOrigins []string

	// dropsMu guards lastDropsByNode for concurrent ingest requests.
	dropsMu         sync.Mutex
	lastDropsByNode map[string]int64
}

// New wires a minimal Server with default options. Use NewWithOptions for
// full dependency injection (chrony, Litestream, metrics, etc.).
func New(db *sql.DB, log zerolog.Logger) *Server {
	return NewWithOptions(db, log, Options{Tickets: ticket.NewStore(0)})
}

// NewWithOptions wires a Server with the supplied dependencies.
func NewWithOptions(db *sql.DB, log zerolog.Logger, opt Options) *Server {
	s := &Server{
		db:               db,
		nodes:            NewNodeStore(db),
		log:              log,
		now:              func() time.Time { return time.Now().UTC() },
		dbPath:           opt.DBPath,
		chrony:           opt.Chrony,
		maxDriftMS:       opt.MaxClockDriftMS,
		litestream:       opt.Litestream,
		maxLag:           opt.MaxLitestreamLag,
		tickets:          opt.Tickets,
		wsRateLimit:      opt.WSRateLimit,
		metrics:          opt.Metrics,
		metricsToken:     opt.MetricsToken,
		adminToken:       opt.AdminToken,
		wsAllowedOrigins: opt.WSAllowedOrigins,
		lastDropsByNode:  make(map[string]int64),
	}
	if s.tickets == nil {
		s.tickets = ticket.NewStore(0)
	}
	s.wsHub = newWSHub()
	return s
}

// Handler returns an http.Handler with all routes mounted.
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

	mux.HandleFunc("GET /ui/login", s.handleUILogin)
	mux.HandleFunc("POST /ui/login", s.handleUILogin)
	mux.HandleFunc("POST /ui/logout", s.handleUILogout)

	mux.HandleFunc("GET /ui/", s.requireSession(s.handleUIDashboard))
	mux.HandleFunc("GET /ui/nodes", s.requireSession(s.handleUINodes))

	mux.HandleFunc("GET /ui/frag/matrix", s.requireSession(s.handleFragMatrix))
	mux.HandleFunc("GET /ui/frag/nodes/new", s.requireSession(s.handleFragNewNodeForm))
	mux.HandleFunc("POST /ui/frag/nodes", s.requireSession(s.handleFragCreateNode))
	mux.HandleFunc("DELETE /ui/frag/nodes/{id}", s.requireSession(s.handleFragDeleteNode))
	mux.HandleFunc("PATCH /ui/frag/nodes/{id}/disable", s.requireSession(s.handleFragDisableNode))
	mux.HandleFunc("PATCH /ui/frag/nodes/{id}/enable", s.requireSession(s.handleFragEnableNode))

	mux.HandleFunc("GET /api/v1/ui/ws", s.handleUIWS)

	// Static assets — strip "static/" prefix from the embedded FS so that
	// /static/style.css → fs:static/style.css resolves correctly.
	// fs.Sub over an embed.FS can only fail if "static" is not present in
	// the embedded tree, which is a programmer error caught at build time.
	staticFS, err := fs.Sub(nlmui.StaticFS, "static")
	if err != nil {
		panic("controller: embedded static FS missing 'static' directory: " + err.Error())
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

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

// readyzResponse is the JSON shape returned by GET /readyz.
type readyzResponse struct {
	Status string       `json:"status"`
	Checks readyzChecks `json:"checks"`
}

// readyzChecks holds per-subsystem readiness details.
type readyzChecks struct {
	Chrony     *chronyCheck     `json:"chrony,omitempty"`
	Litestream *litestreamCheck `json:"litestream,omitempty"`
}

// chronyCheck is the chrony sub-object in a /readyz response.
type chronyCheck struct {
	OK      bool    `json:"ok"`
	DriftMS float64 `json:"drift_ms,omitempty"`
	MaxMS   float64 `json:"max_ms,omitempty"`
	Error   string  `json:"error,omitempty"`
}

// litestreamCheck is the litestream sub-object in a /readyz response.
type litestreamCheck struct {
	OK            bool    `json:"ok"`
	ServiceActive bool    `json:"service_active,omitempty"`
	LagSeconds    float64 `json:"lag_seconds"`
	MaxLag        float64 `json:"max_lag,omitempty"`
}

// handleHealthz returns 200 unconditionally; chrony / Litestream gates apply
// only to /readyz.
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
	resp := readyzResponse{Status: "ok"}
	failed := false

	// Chrony: fail-closed if unconfigured per spec.
	if s.chrony == nil {
		resp.Checks.Chrony = &chronyCheck{OK: false, Error: "not configured (fail-closed)"}
		failed = true
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		drift, err := s.chrony.Drift(ctx)
		cancel()
		switch {
		case err != nil:
			resp.Checks.Chrony = &chronyCheck{OK: false, Error: err.Error()}
			failed = true
		case s.maxDriftMS > 0 && float64(drift) > s.maxDriftMS:
			resp.Checks.Chrony = &chronyCheck{OK: false, DriftMS: float64(drift), MaxMS: s.maxDriftMS}
			failed = true
		default:
			resp.Checks.Chrony = &chronyCheck{OK: true, DriftMS: float64(drift)}
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
			resp.Checks.Litestream = &litestreamCheck{
				OK:            false,
				ServiceActive: st.ServiceActive,
				LagSeconds:    lag.Seconds(),
				MaxLag:        s.maxLag.Seconds(),
			}
			failed = true
		} else {
			resp.Checks.Litestream = &litestreamCheck{
				OK:         true,
				LagSeconds: lag.Seconds(),
			}
		}
		if s.metrics != nil {
			s.metrics.LitestreamLagSeconds.Set(lag.Seconds())
			if !st.LastSyncAt.IsZero() {
				s.metrics.LitestreamLastSyncTimestamp.Set(float64(st.LastSyncAt.Unix()))
			}
		}
	}

	status := http.StatusOK
	if failed {
		status = http.StatusServiceUnavailable
		resp.Status = "degraded"
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
