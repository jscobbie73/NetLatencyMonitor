package agent_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/jscobbie73/netlatencymonitor/internal/agent"
	"github.com/jscobbie73/netlatencymonitor/internal/config"
	"github.com/jscobbie73/netlatencymonitor/internal/controller"
	"github.com/jscobbie73/netlatencymonitor/internal/spool"
)

// fakeTargetsServer returns a tiny HTTP server that echoes the configured
// target list and version. version is read atomically so tests can flip it.
func fakeTargetsServer(t *testing.T, targets []controller.TargetEntry, version *int64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/targets", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(controller.TargetsResponse{
			Targets:        targets,
			TargetsVersion: atomic.LoadInt64(version),
		})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestTargetCacheRefreshesOnVersionChange(t *testing.T) {
	ctx := context.Background()
	v := int64(1)
	ts := fakeTargetsServer(t, []controller.TargetEntry{{ID: "hub-A", Address: "10.0.0.1:1"}}, &v)

	cache := agent.NewTargetCache(ts.URL, "spoke-1", "secret", time.Second, time.Hour)

	got, version, err := cache.Targets(ctx)
	if err != nil {
		t.Fatalf("first Targets: %v", err)
	}
	if version != 1 || len(got) != 1 {
		t.Errorf("first Targets = (v=%d, len=%d), want (1, 1)", version, len(got))
	}

	// Without version notice, should serve from cache.
	got2, _, _ := cache.Targets(ctx)
	if len(got2) != 1 {
		t.Errorf("cached Targets unexpected len=%d", len(got2))
	}

	// Now bump server version and notify.
	atomic.StoreInt64(&v, 2)
	cache.NoticeVersion(2)
	got3, version3, err := cache.Targets(ctx)
	if err != nil {
		t.Fatalf("post-bump Targets: %v", err)
	}
	if version3 != 2 || len(got3) != 1 {
		t.Errorf("post-bump = (v=%d, len=%d), want (2, 1)", version3, len(got3))
	}
}

func TestTargetCacheStaleOnFetchError(t *testing.T) {
	ctx := context.Background()
	v := int64(1)
	ts := fakeTargetsServer(t, []controller.TargetEntry{{ID: "hub-A", Address: "10.0.0.1:1"}}, &v)

	cache := agent.NewTargetCache(ts.URL, "spoke-1", "secret", time.Second, time.Hour)
	if _, _, err := cache.Targets(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Kill the server. NoticeVersion forces refresh, which will fail; the
	// cached list should still come back along with a non-nil error.
	ts.Close()
	cache.NoticeVersion(99)
	got, _, err := cache.Targets(ctx)
	if err == nil {
		t.Error("expected error from refresh against dead server")
	}
	if len(got) != 1 {
		t.Errorf("cached fallback len=%d, want 1", len(got))
	}
}

// TestSpokeUsesTargetCacheVersionRoundtrip wires the full Phase 5 flow:
// spoke probes one (env-static) target, posts to a real controller, the
// controller echoes its targets_version, the spoke's client notifies an
// attached cache. Verifies the integration path; the version-echo and the
// cache update behavior are checked, not the actual GET (the spoke uses
// StaticTargets here so no GET is made).
func TestSpokeReceivesTargetsVersionEcho(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	listenerAddr := startListener(t, ctx)
	baseURL, _ := startController(t)

	cfg := config.AgentConfig{
		NodeID:        "spoke-1",
		NodeSecret:    "topsecret",
		ControllerURL: baseURL,
		Targets:       []config.Target{{ID: "live", Address: listenerAddr}},
		ProbeTimeout:  500 * time.Millisecond,
		HTTPTimeout:   3 * time.Second,
	}

	dir := t.TempDir()
	sp, err := spool.Open(filepath.Join(dir, "spool.db"), spool.Config{
		DrainRows: 100, MaxAge: time.Hour, MaxSize: 100 * 1024 * 1024,
		BackoffBase: 30 * time.Second, BackoffMax: time.Hour,
	})
	if err != nil {
		t.Fatalf("Open spool: %v", err)
	}
	defer func() { _ = sp.Close() }()

	client := agent.NewClient(cfg.ControllerURL, cfg.NodeID, cfg.NodeSecret, cfg.HTTPTimeout)
	notifier := &countingNotifier{}
	client.SetVersionNotifier(notifier)

	sk := agent.NewSpoke(cfg, sp, client, agent.StaticTargets(cfg.Targets), zerolog.Nop())
	if _, err := sk.Run(ctx); err != nil {
		t.Fatalf("spoke: %v", err)
	}
	if got := atomic.LoadInt64(&notifier.calls); got != 1 {
		t.Errorf("VersionNotifier calls = %d, want 1", got)
	}
	if got := atomic.LoadInt64(&notifier.lastVersion); got <= 0 {
		t.Errorf("notified version = %d, want > 0", got)
	}
}

type countingNotifier struct {
	calls       int64
	lastVersion int64
}

func (n *countingNotifier) NoticeVersion(v int64) {
	atomic.AddInt64(&n.calls, 1)
	atomic.StoreInt64(&n.lastVersion, v)
}
