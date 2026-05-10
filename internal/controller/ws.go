package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jscobbie73/netlatencymonitor/internal/ticket"
)

const (
	wsWriteWait  = 10 * time.Second
	wsPongWait   = 60 * time.Second
	wsPingPeriod = (wsPongWait * 9) / 10
)

// handleWSTicket implements POST /api/v1/ws-ticket per spec §3.3.
//
// Auth: bearer (handled by the surrounding bearerAuth middleware). The
// ticket is bound to the calling node so the WS upgrade can carry the same
// identity through.
//
// Response: {"ticket":"...", "expires_at":"RFC3339"}.
func (s *Server) handleWSTicket(w http.ResponseWriter, r *http.Request) {
	node, ok := nodeFromCtx(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tk, err := s.tickets.Issue(node.ID)
	if err != nil {
		s.log.Error().Err(err).Msg("ws-ticket: issue failed")
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if s.metrics != nil {
		s.metrics.WSTicketsIssued.Inc()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ticket":     tk.Value,
		"expires_at": tk.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// handleWS implements GET /api/v1/ws.
//
// The URL must carry ?ticket=<value>. Per spec §3.3:
//   - First use of a ticket: validates, deletes, allows upgrade.
//   - Second use: 401 (ticket no longer exists).
//   - Expired (>60s): 401.
//   - After NLM_WS_TICKET_RATE_LIMIT failures from a single source IP,
//     return 429 for 60s.
//
// The actual broadcast machinery (live UI updates) lands with the web UI
// in Phase 7; here we ship a stub that pongs and otherwise blocks.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)

	if s.wsRateLimit != nil && s.wsRateLimit.Blocked(ip) {
		s.wsReject(w, "ratelimit", http.StatusTooManyRequests, "too many ticket failures")
		return
	}

	tkValue := r.URL.Query().Get("ticket")
	if tkValue == "" {
		s.wsRejectAndCount(w, ip, "unknown", http.StatusUnauthorized, "ticket required")
		return
	}
	nodeID, err := s.tickets.Validate(tkValue)
	if err != nil {
		reason := "unknown"
		if errors.Is(err, ticket.ErrExpiredTicket) {
			reason = "expired"
		}
		s.wsRejectAndCount(w, ip, reason, http.StatusUnauthorized, "invalid ticket")
		return
	}
	if s.metrics != nil {
		s.metrics.WSTicketsUsed.Inc()
	}

	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     s.checkWSOrigin,
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote a response.
		s.log.Warn().Err(err).Str("node_id", nodeID).Msg("ws: upgrade failed")
		return
	}
	if s.metrics != nil {
		s.metrics.ActiveWebSocketConnections.Inc()
	}
	go s.serveWS(conn, nodeID)
}

func (s *Server) serveWS(conn *websocket.Conn, nodeID string) {
	defer func() {
		_ = conn.Close()
		if s.metrics != nil {
			s.metrics.ActiveWebSocketConnections.Dec()
		}
	}()

	conn.SetReadLimit(4096)
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
		return nil
	})

	// Send a hello so a client knows it's authenticated.
	hello, _ := json.Marshal(map[string]any{
		"type":    "hello",
		"node_id": nodeID,
		"phase":   "stub",
	})
	_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	if err := conn.WriteMessage(websocket.TextMessage, hello); err != nil {
		return
	}

	pingTicker := time.NewTicker(wsPingPeriod)
	defer pingTicker.Stop()

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
			}
			// Phase 7: route client subscription messages here.
		}
	}()

	for {
		select {
		case <-readDone:
			return
		case <-pingTicker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// checkWSOrigin enforces the WebSocket upgrade origin policy per spec §3.3:
//   - No Origin header (CLI / non-browser clients): always allowed.
//   - Origin host matches the request Host: allowed (same-origin browser).
//   - Origin is in s.wsAllowedOrigins: allowed (configured cross-origin).
//   - All other origins: rejected.
func (s *Server) checkWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // CLI / non-browser client
	}
	// Strip scheme to compare host portions.
	originHost := origin
	if i := strings.Index(origin, "://"); i >= 0 {
		originHost = origin[i+3:]
	}
	// Strip path/query from origin host.
	if i := strings.IndexAny(originHost, "/?#"); i >= 0 {
		originHost = originHost[:i]
	}
	if strings.EqualFold(originHost, r.Host) {
		return true // same-origin
	}
	for _, allowed := range s.wsAllowedOrigins {
		if strings.EqualFold(origin, allowed) {
			return true
		}
	}
	return false
}

func (s *Server) wsRejectAndCount(w http.ResponseWriter, ip, reason string, status int, msg string) {
	if s.wsRateLimit != nil {
		s.wsRateLimit.RecordFailure(ip)
	}
	s.wsReject(w, reason, status, msg)
}

func (s *Server) wsReject(w http.ResponseWriter, reason string, status int, msg string) {
	if s.metrics != nil {
		s.metrics.WSTicketsRejected.WithLabelValues(reason).Inc()
	}
	writeJSONError(w, status, msg)
}
