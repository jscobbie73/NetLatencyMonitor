package controller

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
)

type ctxKey int

const ctxKeyNode ctxKey = iota

// nodeFromCtx returns the authenticated node for r, or false if none.
func nodeFromCtx(ctx context.Context) (Node, bool) {
	n, ok := ctx.Value(ctxKeyNode).(Node)
	return n, ok
}

// bearerAuth returns middleware that requires `Authorization: Bearer <secret>`
// matching a node row's secret_hash. The agent's id MUST be sent in the
// `X-NLM-Node-Id` header so the controller can do a single-row lookup
// without scanning every secret.
func (s *Server) bearerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.Header.Get("X-NLM-Node-Id")
		token, err := parseBearer(r.Header.Get("Authorization"))
		if err != nil || nodeID == "" {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		node, err := s.nodes.Get(r.Context(), nodeID)
		if errors.Is(err, ErrUnknownNode) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err != nil {
			s.log.Error().Err(err).Str("node_id", nodeID).Msg("auth: node lookup failed")
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !node.VerifySecret(token) {
			s.log.Warn().Str("node_id", nodeID).Str("remote", clientIP(r)).Msg("auth: bad secret")
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyNode, node)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func parseBearer(h string) (string, error) {
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", errors.New("missing bearer token")
	}
	return strings.TrimSpace(h[len(prefix):]), nil
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.IndexByte(v, ','); i >= 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
