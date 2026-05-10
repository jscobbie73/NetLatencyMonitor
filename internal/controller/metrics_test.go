package controller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/jscobbie73/netlatencymonitor/internal/metrics"
	"github.com/jscobbie73/netlatencymonitor/internal/ticket"
)

func newMetricsServer(t *testing.T, token string) (*Server, *metrics.Metrics) {
	t.Helper()
	dir := t.TempDir()
	db, err := OpenDB(filepath.Join(dir, "ctl.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	m := metrics.New()
	srv := NewWithOptions(db, zerolog.Nop(), Options{
		Tickets:      ticket.NewStore(0),
		Metrics:      m,
		MetricsToken: token,
	})
	return srv, m
}

func TestMetricsScrapeNoToken(t *testing.T) {
	s, _ := newMetricsServer(t, "")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	// CounterVecs (nlm_ingest_total, nlm_node_*) only appear once a label
	// combination exists. We check the always-emitted Gauges/Counters here
	// and verify the Vecs in TestIngestIncrementsMetrics.
	for _, want := range []string{
		"nlm_clock_drift_ms",
		"nlm_litestream_lag_seconds",
		"nlm_active_websocket_connections",
		"nlm_db_size_bytes",
		"nlm_probe_results_total",
		"nlm_ws_tickets_issued_total",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metric %q not found in scrape", want)
		}
	}
}

func TestMetricsScrapeWithToken(t *testing.T) {
	s, _ := newMetricsServer(t, "secret-token")

	// Missing token → 401.
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("no-auth status = %d, want 401", rr.Code)
	}

	// Wrong token → 401.
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("wrong-token status = %d, want 401", rr.Code)
	}

	// Correct token → 200.
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("correct-token status = %d, want 200", rr.Code)
	}
}

func TestIngestIncrementsMetrics(t *testing.T) {
	s, m := newMetricsServer(t, "")
	if err := s.nodes.Upsert(context.Background(), Node{
		ID: "spoke-1", Role: "spoke", SecretHash: HashSecret("topsecret"),
	}); err != nil {
		t.Fatal(err)
	}

	resp := doPostResults(t, s, "spoke-1", "topsecret", sampleRequest("spoke-1", "run-1"))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("ingest status = %d, want 201", resp.StatusCode)
	}

	// Scrape and verify the counter advanced.
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	body, _ := io.ReadAll(rr.Body)
	got := string(body)
	if !strings.Contains(got, `nlm_ingest_total{node_id="spoke-1",status="2xx"}`) {
		t.Errorf("ingest counter line not in scrape; body=\n%s", got)
	}
	if !strings.Contains(got, `nlm_node_spool_depth{node_id="spoke-1"}`) {
		t.Errorf("spool_depth gauge not in scrape")
	}
	_ = m
}
