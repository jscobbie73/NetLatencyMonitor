package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/gorilla/websocket"

	nlmui "github.com/jscobbie73/netlatencymonitor/internal/ui"
)

const sessionCookie = "nlm_session"

// requireSession redirects unauthenticated requests to /ui/login. The session
// cookie stores the admin token directly (HttpOnly, SameSite=Strict).
func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || !constantTimeStringEq(c.Value, s.adminToken) {
			http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *Server) handleUILogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.renderHTML(w, r, nlmui.Login(""))
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	if s.adminToken == "" || !constantTimeStringEq(token, s.adminToken) {
		s.renderHTML(w, r, nlmui.Login("Invalid token."))
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isHTTPS(r),
	})
	http.Redirect(w, r, "/ui/", http.StatusSeeOther)
}

func (s *Server) handleUILogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:   sessionCookie,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
}

func (s *Server) handleUIDashboard(w http.ResponseWriter, r *http.Request) {
	matrix, err := s.queryMatrix(r.Context())
	if err != nil {
		s.log.Error().Err(err).Msg("ui: dashboard matrix query")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderHTML(w, r, nlmui.Dashboard(matrix))
}

func (s *Server) handleUINodes(w http.ResponseWriter, r *http.Request) {
	infos, err := s.listNodeInfos(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderHTML(w, r, nlmui.Nodes(infos, ""))
}

func (s *Server) handleFragMatrix(w http.ResponseWriter, r *http.Request) {
	matrix, err := s.queryMatrix(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderHTML(w, r, nlmui.MatrixTable(matrix))
}

func (s *Server) handleFragNewNodeForm(w http.ResponseWriter, r *http.Request) {
	s.renderHTML(w, r, nlmui.CreateNodeForm())
}

func (s *Server) handleFragCreateNode(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.FormValue("id"))
	role := strings.TrimSpace(r.FormValue("role"))
	address := strings.TrimSpace(r.FormValue("address"))
	secret := strings.TrimSpace(r.FormValue("secret"))

	if err := validateAdminNodeFields(id, role, address); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if secret == "" {
		var err error
		if secret, err = mintSecret(); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	n := Node{ID: id, Role: role, Address: address, SecretHash: HashSecret(secret)}
	if err := s.nodes.Upsert(r.Context(), n); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	infos, err := s.listNodeInfos(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Surface the one-time secret in the page banner above the updated table.
	s.renderHTML(w, r, nlmui.Nodes(infos, secret))
}

func (s *Server) handleFragDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.nodes.Delete(r.Context(), id); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// Empty body — htmx removes the row via hx-swap="outerHTML".
}

func (s *Server) handleFragDisableNode(w http.ResponseWriter, r *http.Request) {
	s.patchDisabled(w, r, true)
}

func (s *Server) handleFragEnableNode(w http.ResponseWriter, r *http.Request) {
	s.patchDisabled(w, r, false)
}

func (s *Server) patchDisabled(w http.ResponseWriter, r *http.Request, disable bool) {
	id := r.PathValue("id")
	node, err := s.nodes.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	node.Disabled = disable
	if err := s.nodes.Upsert(r.Context(), node); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderHTML(w, r, nlmui.NodeRow(nodeToInfo(node)))
}

func (s *Server) handleUIWS(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || !constantTimeStringEq(c.Value, s.adminToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	upgrader := websocket.Upgrader{
		ReadBufferSize:  512,
		WriteBufferSize: 4096,
		CheckOrigin:     s.checkWSOrigin,
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.serveUIClient(conn)
}

func (s *Server) renderHTML(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.Render(r.Context(), w); err != nil && r.Context().Err() == nil {
		// The response header is already sent; we cannot change the status code.
		// Log so the operator can see template rendering failures.
		// Skip logging when the context is cancelled (client disconnected).
		s.log.Error().Err(err).Msg("ui: render template failed")
	}
}

// broadcastResults fans probe observations out to all connected UI clients.
func (s *Server) broadcastResults(sourceID string, results []ResultObservation) {
	if s.wsHub == nil {
		return
	}
	for _, obs := range results {
		ev := ProbeEvent{
			Type:     "probe_result",
			SourceID: sourceID,
			TargetID: obs.TargetID,
			Error:    obs.Error,
		}
		if obs.LatencyMs != nil {
			ev.LatencyMS = *obs.LatencyMs
		}
		b, _ := json.Marshal(ev)
		s.wsHub.Broadcast(b)
	}
}

func (s *Server) listNodeInfos(ctx context.Context) ([]nlmui.NodeInfo, error) {
	nodes, err := s.nodes.List(ctx)
	if err != nil {
		return nil, err
	}
	infos := make([]nlmui.NodeInfo, len(nodes))
	for i, n := range nodes {
		infos[i] = nodeToInfo(n)
	}
	return infos, nil
}

func nodeToInfo(n Node) nlmui.NodeInfo {
	return nlmui.NodeInfo{
		ID:       n.ID,
		Role:     n.Role,
		Address:  n.Address,
		Disabled: n.Disabled,
	}
}
