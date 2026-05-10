package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
)

func newAdminServer(t *testing.T, token string) *Server {
	t.Helper()
	dir := t.TempDir()
	db, err := OpenDB(filepath.Join(dir, "ctl.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewWithOptions(db, zerolog.Nop(), Options{AdminToken: token})
}

func adminReq(t *testing.T, s *Server, method, path, token string, body any) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rdr = bytes.NewReader(buf)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr.Result()
}

func TestAdminRequiresToken(t *testing.T) {
	s := newAdminServer(t, "secret")
	resp := adminReq(t, s, http.MethodGet, "/api/v1/admin/nodes", "", nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("missing token: status = %d, want 401", resp.StatusCode)
	}
}

func TestAdminWrongToken(t *testing.T) {
	s := newAdminServer(t, "secret")
	resp := adminReq(t, s, http.MethodGet, "/api/v1/admin/nodes", "wrong", nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401", resp.StatusCode)
	}
}

func TestAdminDisabledWhenNoToken(t *testing.T) {
	s := newAdminServer(t, "")
	resp := adminReq(t, s, http.MethodGet, "/api/v1/admin/nodes", "anything", nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("unconfigured admin: status = %d, want 503", resp.StatusCode)
	}
}

func TestAdminCreateAndList(t *testing.T) {
	s := newAdminServer(t, "tok")

	body := AdminCreateRequest{ID: "hub-01", Role: RoleHub, Address: "10.0.0.1:8444"}
	resp := adminReq(t, s, http.MethodPost, "/api/v1/admin/nodes", "tok", body)
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("create: status %d body=%s", resp.StatusCode, b)
	}
	var created AdminCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if created.Secret == "" {
		t.Error("expected minted secret in create response")
	}

	resp = adminReq(t, s, http.MethodGet, "/api/v1/admin/nodes", "tok", nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	var list struct {
		Nodes []AdminNode `json:"nodes"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&list)
	if len(list.Nodes) != 1 || list.Nodes[0].ID != "hub-01" {
		t.Errorf("list returned unexpected: %+v", list)
	}
}

func TestAdminCreateRejectsHubWithoutAddress(t *testing.T) {
	s := newAdminServer(t, "tok")
	resp := adminReq(t, s, http.MethodPost, "/api/v1/admin/nodes", "tok",
		AdminCreateRequest{ID: "hub-01", Role: RoleHub})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", resp.StatusCode)
	}
}

func TestAdminCreateConflict(t *testing.T) {
	s := newAdminServer(t, "tok")
	body := AdminCreateRequest{ID: "spoke-1", Role: RoleSpoke, Secret: "abc"}
	r1 := adminReq(t, s, http.MethodPost, "/api/v1/admin/nodes", "tok", body)
	_ = r1.Body.Close()
	r2 := adminReq(t, s, http.MethodPost, "/api/v1/admin/nodes", "tok", body)
	defer func() { _ = r2.Body.Close() }()
	if r2.StatusCode != http.StatusConflict {
		t.Errorf("conflict: status = %d, want 409", r2.StatusCode)
	}
}

func TestAdminPatchAndDelete(t *testing.T) {
	s := newAdminServer(t, "tok")
	r1 := adminReq(t, s, http.MethodPost, "/api/v1/admin/nodes", "tok",
		AdminCreateRequest{ID: "hub-01", Role: RoleHub, Address: "10.0.0.1:8444", Secret: "s"})
	_ = r1.Body.Close()

	disabled := true
	patchBody := AdminPatchRequest{Disabled: &disabled}
	r2 := adminReq(t, s, http.MethodPatch, "/api/v1/admin/nodes/hub-01", "tok", patchBody)
	defer func() { _ = r2.Body.Close() }()
	if r2.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(r2.Body)
		t.Fatalf("patch status %d body=%s", r2.StatusCode, b)
	}

	r3 := adminReq(t, s, http.MethodDelete, "/api/v1/admin/nodes/hub-01", "tok", nil)
	defer func() { _ = r3.Body.Close() }()
	if r3.StatusCode != http.StatusNoContent {
		t.Errorf("delete: status = %d, want 204", r3.StatusCode)
	}

	// Subsequent delete should 404.
	r4 := adminReq(t, s, http.MethodDelete, "/api/v1/admin/nodes/hub-01", "tok", nil)
	defer func() { _ = r4.Body.Close() }()
	if r4.StatusCode != http.StatusNotFound {
		t.Errorf("delete twice: status = %d, want 404", r4.StatusCode)
	}
}

func TestTargetsVersionBumpsOnHubMutations(t *testing.T) {
	s := newAdminServer(t, "tok")
	ctx := context.Background()

	v0, err := s.nodes.TargetsVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}

	r1 := adminReq(t, s, http.MethodPost, "/api/v1/admin/nodes", "tok",
		AdminCreateRequest{ID: "hub-01", Role: RoleHub, Address: "10.0.0.1:8444", Secret: "s"})
	_ = r1.Body.Close()

	v1, _ := s.nodes.TargetsVersion(ctx)
	if v1 <= v0 {
		t.Errorf("creating a hub should bump version: %d -> %d", v0, v1)
	}

	// Creating a spoke should NOT bump.
	r2 := adminReq(t, s, http.MethodPost, "/api/v1/admin/nodes", "tok",
		AdminCreateRequest{ID: "spoke-1", Role: RoleSpoke, Secret: "s"})
	_ = r2.Body.Close()
	v2, _ := s.nodes.TargetsVersion(ctx)
	if v2 != v1 {
		t.Errorf("creating a spoke should not bump version: %d -> %d", v1, v2)
	}

	// Disabling the hub should bump.
	disabled := true
	r3 := adminReq(t, s, http.MethodPatch, "/api/v1/admin/nodes/hub-01", "tok",
		AdminPatchRequest{Disabled: &disabled})
	_ = r3.Body.Close()
	v3, _ := s.nodes.TargetsVersion(ctx)
	if v3 <= v2 {
		t.Errorf("disabling a hub should bump version: %d -> %d", v2, v3)
	}

	// Deleting an already-disabled hub should NOT bump (it wasn't in the
	// active set).
	r4 := adminReq(t, s, http.MethodDelete, "/api/v1/admin/nodes/hub-01", "tok", nil)
	_ = r4.Body.Close()
	v4, _ := s.nodes.TargetsVersion(ctx)
	if v4 != v3 {
		t.Errorf("deleting disabled hub should not bump: %d -> %d", v3, v4)
	}
}
