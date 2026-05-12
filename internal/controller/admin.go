package controller

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
)

// adminAuth gates routes with `Authorization: Bearer <NLM_ADMIN_TOKEN>`.
// Returns 503 when no admin token is configured so the misconfiguration is
// obvious rather than silently returning 401 with no hint.
func (s *Server) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.adminToken == "" {
			writeJSONError(w, http.StatusServiceUnavailable, "admin api disabled (NLM_ADMIN_TOKEN not set)")
			return
		}
		got, err := parseBearer(r.Header.Get("Authorization"))
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "missing bearer")
			return
		}
		if !constantTimeStringEq(got, s.adminToken) {
			writeJSONError(w, http.StatusUnauthorized, "bad admin token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func constantTimeStringEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// AdminNode is the JSON shape returned by admin endpoints.
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

// AdminCreateRequest is the body of POST /api/v1/admin/nodes.
type AdminCreateRequest struct {
	ID       string `json:"id"`
	Role     string `json:"role"`
	Address  string `json:"address,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	// Secret may be supplied by the operator. If empty, the controller mints
	// a random 256-bit secret and returns it ONCE in the create response.
	Secret string `json:"secret,omitempty"`
}

// AdminCreateResponse is what POST /api/v1/admin/nodes returns. Secret is
// only present on create; it is never echoed by other endpoints.
type AdminCreateResponse struct {
	Node   AdminNode `json:"node"`
	Secret string    `json:"secret"`
}

// AdminPatchRequest is the body of PATCH /api/v1/admin/nodes/{id}. All
// fields are optional; the operator can rotate the secret by passing a new
// "secret" string (or "" to keep the existing one).
type AdminPatchRequest struct {
	Role     *string `json:"role,omitempty"`
	Address  *string `json:"address,omitempty"`
	Disabled *bool   `json:"disabled,omitempty"`
	Secret   *string `json:"secret,omitempty"`
}

func (s *Server) handleAdminListNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.nodes.List(r.Context())
	if err != nil {
		s.log.Error().Err(err).Msg("admin: list nodes")
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]AdminNode, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, AdminNode{ID: n.ID, Role: n.Role, Address: n.Address, Disabled: n.Disabled})
	}
	writeJSON(w, http.StatusOK, adminListNodesResponse{Nodes: out})
}

func (s *Server) handleAdminCreateNode(w http.ResponseWriter, r *http.Request) {
	var req AdminCreateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, "invalid json: "+err.Error())
		return
	}
	if err := validateAdminNodeFields(req.ID, req.Role, req.Address); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if _, err := s.nodes.Get(r.Context(), req.ID); err == nil {
		writeJSONError(w, http.StatusConflict, "node already exists")
		return
	} else if !errors.Is(err, ErrUnknownNode) {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	secret := req.Secret
	if secret == "" {
		var err error
		secret, err = mintSecret()
		if err != nil {
			s.log.Error().Err(err).Msg("admin: mint secret")
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	n := Node{
		ID:         req.ID,
		Role:       req.Role,
		SecretHash: HashSecret(secret),
		Address:    req.Address,
		Disabled:   req.Disabled,
	}
	if err := s.nodes.Upsert(r.Context(), n); err != nil {
		s.log.Error().Err(err).Str("node_id", req.ID).Msg("admin: upsert")
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, AdminCreateResponse{
		Node:   AdminNode{ID: n.ID, Role: n.Role, Address: n.Address, Disabled: n.Disabled},
		Secret: secret,
	})
}

func (s *Server) handleAdminPatchNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusUnprocessableEntity, "missing id")
		return
	}
	cur, err := s.nodes.Get(r.Context(), id)
	if errors.Is(err, ErrUnknownNode) {
		writeJSONError(w, http.StatusNotFound, "unknown node")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var req AdminPatchRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, "invalid json: "+err.Error())
		return
	}

	next := cur
	if req.Role != nil {
		next.Role = *req.Role
	}
	if req.Address != nil {
		next.Address = *req.Address
	}
	if req.Disabled != nil {
		next.Disabled = *req.Disabled
	}
	if req.Secret != nil && *req.Secret != "" {
		next.SecretHash = HashSecret(*req.Secret)
	}
	if err := validateAdminNodeFields(next.ID, next.Role, next.Address); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := s.nodes.Upsert(r.Context(), next); err != nil {
		s.log.Error().Err(err).Str("node_id", id).Msg("admin: patch upsert")
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, AdminNode{
		ID: next.ID, Role: next.Role, Address: next.Address, Disabled: next.Disabled,
	})
}

func (s *Server) handleAdminDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusUnprocessableEntity, "missing id")
		return
	}
	if err := s.nodes.Delete(r.Context(), id); err != nil {
		if errors.Is(err, ErrUnknownNode) || errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "unknown node")
			return
		}
		s.log.Error().Err(err).Str("node_id", id).Msg("admin: delete")
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validateAdminNodeFields(id, role, address string) error {
	if id == "" {
		return errors.New("id required")
	}
	switch role {
	case RoleSpoke, RoleHub, RoleListener:
	default:
		return errors.New("role must be one of spoke, hub, listener")
	}
	if role == RoleHub && address == "" {
		return errors.New("hub role requires address (host:port)")
	}
	return nil
}

func mintSecret() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}
