package controller

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/rs/zerolog"
)

// Server bundles the controller's HTTP dependencies. Construct with New.
type Server struct {
	db    *sql.DB
	nodes *NodeStore
	log   zerolog.Logger
	now   func() time.Time
}

// New wires a controller server around an already-open, already-migrated DB.
func New(db *sql.DB, log zerolog.Logger) *Server {
	return &Server{
		db:    db,
		nodes: NewNodeStore(db),
		log:   log,
		now:   func() time.Time { return time.Now().UTC() },
	}
}

// Handler returns an http.Handler with all routes mounted. Uses the Go 1.22
// http.ServeMux pattern syntax (METHOD + path).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)

	authed := s.bearerAuth(http.HandlerFunc(s.handleResults))
	mux.Handle("POST /api/v1/results", authed)

	return mux
}

// handleHealthz returns 200 unconditionally; it indicates process liveness
// per spec §3.2 (chrony / Litestream gates apply only to /readyz).
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz is a stub for Phase 2. The chrony fail-closed rule (§3.2)
// and Litestream lag check land in Phase 4.
func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "note": "phase-2 stub"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
