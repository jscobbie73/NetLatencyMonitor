package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
)

func newTargetsServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	db, err := OpenDB(filepath.Join(dir, "ctl.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewWithOptions(db, zerolog.Nop(), Options{})
}

func getTargets(t *testing.T, s *Server, nodeID, secret string) (int, TargetsResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil)
	req.Header.Set("X-NLM-Node-Id", nodeID)
	req.Header.Set("Authorization", "Bearer "+secret)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	var out TargetsResponse
	_ = json.NewDecoder(rr.Body).Decode(&out)
	return rr.Code, out
}

func TestTargetsRequiresAuth(t *testing.T) {
	s := newTargetsServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestTargetsExcludesSelfAndDisabled(t *testing.T) {
	s := newTargetsServer(t)
	ctx := context.Background()

	mustUpsert := func(n Node) {
		t.Helper()
		if err := s.nodes.Upsert(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	mustUpsert(Node{ID: "spoke-1", Role: RoleSpoke, SecretHash: HashSecret("s")})
	mustUpsert(Node{ID: "hub-A", Role: RoleHub, Address: "10.0.0.1:8444", SecretHash: HashSecret("s")})
	mustUpsert(Node{ID: "hub-B", Role: RoleHub, Address: "10.0.0.2:8444", SecretHash: HashSecret("s")})
	mustUpsert(Node{ID: "hub-C", Role: RoleHub, Address: "10.0.0.3:8444", SecretHash: HashSecret("s"), Disabled: true})

	// spoke-1 sees both active hubs.
	code, body := getTargets(t, s, "spoke-1", "s")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if got, want := len(body.Targets), 2; got != want {
		t.Errorf("len = %d, want %d; got=%+v", got, want, body)
	}

	// hub-A sees only hub-B (self-excluded; hub-C disabled).
	code, body = getTargets(t, s, "hub-A", "s")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Targets) != 1 || body.Targets[0].ID != "hub-B" {
		t.Errorf("hub-A targets = %+v, want [hub-B]", body.Targets)
	}
}

func TestPostResultsEchoesTargetsVersion(t *testing.T) {
	// Verifies the version-piggyback contract from Phase 5.
	s, _ := newTestServer(t)
	seedNode(t, s, "spoke-1", "topsecret", RoleSpoke)

	resp := doPostResults(t, s, "spoke-1", "topsecret", sampleRequest("spoke-1", "run-1"))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var env struct {
		TargetsVersion int64 `json:"targets_version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.TargetsVersion <= 0 {
		t.Errorf("targets_version = %d, want > 0", env.TargetsVersion)
	}
}
