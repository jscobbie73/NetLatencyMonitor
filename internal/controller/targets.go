package controller

import (
	"net/http"
)

// TargetsResponse is the body of GET /api/v1/targets per Phase 5 design:
// active hubs minus self, plus the monotonic targets_version. Agents cache
// the version and refetch only when POST /api/v1/results echoes a new one.
type TargetsResponse struct {
	Targets        []TargetEntry `json:"targets"`
	TargetsVersion int64         `json:"targets_version"`
}

// TargetEntry is one (id, address) pair to probe.
type TargetEntry struct {
	ID      string `json:"id"`
	Address string `json:"address"`
}

// handleTargets implements GET /api/v1/targets. The caller must already be
// authenticated by the bearer middleware. Self-exclusion uses the
// authenticated node id, not a query param, so a spoke can't enumerate
// targets for some other node.
func (s *Server) handleTargets(w http.ResponseWriter, r *http.Request) {
	node, ok := nodeFromCtx(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	hubs, version, err := s.nodes.HubTargetsFor(r.Context(), node.ID)
	if err != nil {
		s.log.Error().Err(err).Str("node_id", node.ID).Msg("targets: query failed")
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := TargetsResponse{TargetsVersion: version, Targets: make([]TargetEntry, 0, len(hubs))}
	for _, h := range hubs {
		out.Targets = append(out.Targets, TargetEntry{ID: h.ID, Address: h.Address})
	}
	writeJSON(w, http.StatusOK, out)
}
