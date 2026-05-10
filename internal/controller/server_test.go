package controller

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func newTestServer(t *testing.T) (*Server, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := OpenDB(filepath.Join(dir, "ctl.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db, zerolog.Nop()), db
}

func seedNode(t *testing.T, s *Server, id, secret, role string) {
	t.Helper()
	if err := s.nodes.Upsert(context.Background(), Node{
		ID:         id,
		Role:       role,
		SecretHash: HashSecret(secret),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
}

func sampleRequest(sourceID, runID string) ResultsRequest {
	lat := 12.5
	return ResultsRequest{
		SourceID:   sourceID,
		ProbeRunID: runID,
		ObservedAt: time.Now().UTC().Truncate(time.Second),
		Results: []ResultObservation{
			{TargetID: "hub-01", LatencyMs: &lat},
			{TargetID: "hub-02", Error: "timeout"},
		},
		SpoolMetadata: SpoolMetadata{
			SpoolDepth:            3,
			SpoolOldestAgeSeconds: 90,
			SpoolDropsTotal:       0,
			DrainAttempt:          1,
		},
	}
}

func doPostResults(t *testing.T, s *Server, nodeID, secret string, body any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/results", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	if nodeID != "" {
		req.Header.Set("X-NLM-Node-Id", nodeID)
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr.Result()
}

func TestHealthz(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

func TestPostResultsCreated(t *testing.T) {
	s, db := newTestServer(t)
	seedNode(t, s, "spoke-1", "topsecret", "spoke")

	resp := doPostResults(t, s, "spoke-1", "topsecret", sampleRequest("spoke-1", "run-1"))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 201; body=%s", resp.StatusCode, body)
	}

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM probe_results WHERE source_id = ? AND probe_run_id = ?`,
		"spoke-1", "run-1").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Errorf("inserted rows = %d, want 2", rows)
	}

	var depth int
	var drops int64
	if err := db.QueryRow(`SELECT spool_depth, spool_drops_total FROM nodes WHERE id = ?`, "spoke-1").
		Scan(&depth, &drops); err != nil {
		t.Fatal(err)
	}
	if depth != 3 || drops != 0 {
		t.Errorf("spool stats not persisted: depth=%d drops=%d", depth, drops)
	}
}

func TestPostResultsReplayReturns409(t *testing.T) {
	// Spec §3.4: replay of identical content returns 409 Conflict and the
	// agent treats it as terminal success.
	s, _ := newTestServer(t)
	seedNode(t, s, "spoke-1", "topsecret", "spoke")
	body := sampleRequest("spoke-1", "run-1")

	resp1 := doPostResults(t, s, "spoke-1", "topsecret", body)
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("first POST status = %d, want 201", resp1.StatusCode)
	}

	resp2 := doPostResults(t, s, "spoke-1", "topsecret", body)
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("replay status = %d, want 409", resp2.StatusCode)
	}
}

func TestPostResultsUpdatesDropsOnReplay(t *testing.T) {
	// The spool snapshot should still be applied on a replay; the agent's
	// view is the freshest data we have.
	s, db := newTestServer(t)
	seedNode(t, s, "spoke-1", "topsecret", "spoke")
	body := sampleRequest("spoke-1", "run-1")

	resp1 := doPostResults(t, s, "spoke-1", "topsecret", body)
	_ = resp1.Body.Close()

	body.SpoolMetadata.SpoolDropsTotal = 7
	resp2 := doPostResults(t, s, "spoke-1", "topsecret", body)
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp2.StatusCode)
	}

	var drops int64
	if err := db.QueryRow(`SELECT spool_drops_total FROM nodes WHERE id = ?`, "spoke-1").Scan(&drops); err != nil {
		t.Fatal(err)
	}
	if drops != 7 {
		t.Errorf("drops_total = %d, want 7 (updated on replay)", drops)
	}
}

func TestPostResults401MissingAuth(t *testing.T) {
	s, _ := newTestServer(t)
	seedNode(t, s, "spoke-1", "topsecret", "spoke")

	resp := doPostResults(t, s, "", "", sampleRequest("spoke-1", "run-1"))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestPostResults401WrongSecret(t *testing.T) {
	s, _ := newTestServer(t)
	seedNode(t, s, "spoke-1", "topsecret", "spoke")

	resp := doPostResults(t, s, "spoke-1", "wrong", sampleRequest("spoke-1", "run-1"))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestPostResults401UnknownNode(t *testing.T) {
	s, _ := newTestServer(t)
	resp := doPostResults(t, s, "ghost", "anything", sampleRequest("ghost", "run-1"))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestPostResults422SourceMismatch(t *testing.T) {
	// Authenticated as spoke-1 but claims to be hub-2 — refuse.
	s, _ := newTestServer(t)
	seedNode(t, s, "spoke-1", "topsecret", "spoke")
	resp := doPostResults(t, s, "spoke-1", "topsecret", sampleRequest("hub-2", "run-1"))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", resp.StatusCode)
	}
}

func TestPostResults422EmptyResults(t *testing.T) {
	s, _ := newTestServer(t)
	seedNode(t, s, "spoke-1", "topsecret", "spoke")
	body := sampleRequest("spoke-1", "run-1")
	body.Results = nil

	resp := doPostResults(t, s, "spoke-1", "topsecret", body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", resp.StatusCode)
	}
}

func TestPostResults422DuplicateTargetInBatch(t *testing.T) {
	s, _ := newTestServer(t)
	seedNode(t, s, "spoke-1", "topsecret", "spoke")
	body := sampleRequest("spoke-1", "run-1")
	body.Results = append(body.Results, ResultObservation{TargetID: "hub-01", Error: "x"})

	resp := doPostResults(t, s, "spoke-1", "topsecret", body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", resp.StatusCode)
	}
}

func TestPostResults422BadJSON(t *testing.T) {
	s, _ := newTestServer(t)
	seedNode(t, s, "spoke-1", "topsecret", "spoke")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/results", bytes.NewReader([]byte(`{not-json`)))
	req.Header.Set("X-NLM-Node-Id", "spoke-1")
	req.Header.Set("Authorization", "Bearer topsecret")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rr.Code)
	}
}

func TestVerifySecretConstantTime(t *testing.T) {
	n := Node{SecretHash: HashSecret("right")}
	if !n.VerifySecret("right") {
		t.Error("matching secret should verify")
	}
	if n.VerifySecret("wrong") {
		t.Error("wrong secret should not verify")
	}
	bad := Node{SecretHash: "not-hex"}
	if bad.VerifySecret("anything") {
		t.Error("malformed hash should not verify")
	}
}

func TestParseBearer(t *testing.T) {
	if _, err := parseBearer(""); err == nil {
		t.Error("empty header should error")
	}
	if _, err := parseBearer("Basic xyz"); err == nil {
		t.Error("non-bearer scheme should error")
	}
	tok, err := parseBearer("Bearer abc")
	if err != nil || tok != "abc" {
		t.Errorf("parseBearer = %q, %v", tok, err)
	}
	tok, err = parseBearer("bearer  spaced  ")
	if err != nil || tok != "spaced" {
		t.Errorf("parseBearer trim = %q, %v", tok, err)
	}
}
