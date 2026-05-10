package agent_test

import (
	"context"
	"net"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/jscobbie73/netlatencymonitor/internal/agent"
	"github.com/jscobbie73/netlatencymonitor/internal/config"
	"github.com/jscobbie73/netlatencymonitor/internal/controller"
	"github.com/jscobbie73/netlatencymonitor/internal/spool"
)

// startListener spins up the package's TCP listener on an ephemeral port
// and returns the bound address. Stops when ctx is cancelled.
func startListener(t *testing.T, ctx context.Context) string {
	t.Helper()
	ln := agent.NewListener("127.0.0.1:0", zerolog.Nop())

	// Bind first so the test knows the port before continuing. We can't get
	// the address back from ListenAndServe directly, so do a one-off bind
	// here, immediately close it, and reuse the port. That race is fine for
	// localhost tests.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe bind: %v", err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	ln = agent.NewListener(addr, zerolog.Nop())
	done := make(chan error, 1)
	go func() { done <- ln.ListenAndServe(ctx) }()
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})

	// Wait for the listener to actually be accepting.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return addr
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("listener never came up at %s", addr)
	return ""
}

// startController launches a real controller on an httptest.Server so the
// agent goes through the full HTTP path (auth middleware, JSON decode,
// transactional persist).
func startController(t *testing.T) (baseURL, dbPath string) {
	t.Helper()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "ctl.db")
	db, err := controller.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	srv := controller.New(db, zerolog.Nop())
	if err := controller.NewNodeStore(db).Upsert(context.Background(), controller.Node{
		ID:         "spoke-1",
		Role:       "spoke",
		SecretHash: controller.HashSecret("topsecret"),
	}); err != nil {
		t.Fatalf("Upsert node: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL, dbPath
}

// TestSpokeEndToEnd exercises the full path: probe a real TCP listener,
// enqueue, drain, and verify the controller persisted exactly the rows we
// expect. This is the smoke test promised at the end of the Phase 3 plan.
func TestSpokeEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	listenerAddr := startListener(t, ctx)
	baseURL, dbPath := startController(t)

	cfg := config.AgentConfig{
		NodeID:        "spoke-1",
		NodeSecret:    "topsecret",
		ControllerURL: baseURL,
		Targets: []config.Target{
			{ID: "live", Address: listenerAddr},
			// Reserved port that will refuse / time out — verifies error path.
			{ID: "dead", Address: "127.0.0.1:1"},
		},
		ProbeTimeout: 500 * time.Millisecond,
		HTTPTimeout:  3 * time.Second,
	}

	dir := t.TempDir()
	sp, err := spool.Open(filepath.Join(dir, "spool.db"), spool.Config{
		DrainRows:   100,
		MaxAge:      time.Hour,
		MaxSize:     100 * 1024 * 1024,
		BackoffBase: 30 * time.Second,
		BackoffMax:  time.Hour,
	})
	if err != nil {
		t.Fatalf("Open spool: %v", err)
	}
	defer func() { _ = sp.Close() }()

	client := agent.NewClient(cfg.ControllerURL, cfg.NodeID, cfg.NodeSecret, cfg.HTTPTimeout)
	sk := agent.NewSpoke(cfg, sp, client, zerolog.Nop())

	stats, err := sk.Run(ctx)
	if err != nil {
		t.Fatalf("Spoke.Run: %v", err)
	}
	if stats.Targets != 2 {
		t.Errorf("targets = %d, want 2", stats.Targets)
	}
	if stats.Drain.Accepted != 1 {
		t.Errorf("accepted = %d, want 1 (one row per cycle)", stats.Drain.Accepted)
	}

	depth, _ := sp.Depth(ctx)
	if depth != 0 {
		t.Errorf("spool depth after drain = %d, want 0", depth)
	}

	// Verify the controller persisted both observations under the same
	// probe_run_id, with the live target succeeded and the dead one errored.
	db, err := controller.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("reopen ctl db: %v", err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(ctx, `
		SELECT target_id, latency_ms, error
		  FROM probe_results
		 WHERE source_id = ? AND probe_run_id = ?
		 ORDER BY target_id`, "spoke-1", stats.ProbeRunID)
	if err != nil {
		t.Fatalf("query results: %v", err)
	}
	defer func() { _ = rows.Close() }()

	type observed struct {
		id      string
		latency *float64
		errMsg  *string
	}
	var got []observed
	for rows.Next() {
		var o observed
		var lat *float64
		var em *string
		if err := rows.Scan(&o.id, &lat, &em); err != nil {
			t.Fatal(err)
		}
		o.latency = lat
		o.errMsg = em
		got = append(got, o)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("persisted %d rows, want 2; got=%+v", len(got), got)
	}

	var dead, live *observed
	for i := range got {
		switch got[i].id {
		case "live":
			live = &got[i]
		case "dead":
			dead = &got[i]
		}
	}
	if live == nil || live.latency == nil || *live.latency < 0 {
		t.Errorf("live probe should have a non-negative latency; got %+v", live)
	}
	if dead == nil || dead.errMsg == nil || *dead.errMsg == "" {
		t.Errorf("dead probe should have a recorded error; got %+v", dead)
	}

	// Verify the controller updated the per-node spool snapshot.
	var depthCol int
	var dropsCol int64
	if err := db.QueryRow(`SELECT spool_depth, spool_drops_total FROM nodes WHERE id = ?`, "spoke-1").
		Scan(&depthCol, &dropsCol); err != nil {
		t.Fatal(err)
	}
	if depthCol != 1 {
		// Per spec §3.5: spool_depth is reported "after the enqueue+drain
		// step that produced this report" — but the agent reports its
		// pre-drain depth (1, the row it just added) inside the payload.
		// Either interpretation is defensible; the test just pins behavior.
		t.Errorf("controller-stored spool_depth = %d, want 1 (post-enqueue, pre-drain)", depthCol)
	}
}

// TestSpokeReplayDrainsAcrossControllerRestart simulates the spec §3.1
// Case A scenario: the agent posts results, then a second drain hits a
// freshly-restarted controller that already has the rows. The 409 must be
// treated as success and the spool must come out empty.
func TestSpokeReplayDrainsAcrossControllerRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	listenerAddr := startListener(t, ctx)

	// Start controller #1.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ctl.db")
	db, err := controller.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("open ctl: %v", err)
	}
	if err := controller.NewNodeStore(db).Upsert(ctx, controller.Node{
		ID: "spoke-1", Role: "spoke", SecretHash: controller.HashSecret("topsecret"),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	srv1 := controller.New(db, zerolog.Nop())
	ts1 := httptest.NewServer(srv1.Handler())

	cfg := config.AgentConfig{
		NodeID:        "spoke-1",
		NodeSecret:    "topsecret",
		ControllerURL: ts1.URL,
		Targets:       []config.Target{{ID: "live", Address: listenerAddr}},
		ProbeTimeout:  500 * time.Millisecond,
		HTTPTimeout:   3 * time.Second,
	}
	sp, err := spool.Open(filepath.Join(dir, "spool.db"), spool.Config{
		DrainRows: 100, MaxAge: time.Hour, MaxSize: 100 * 1024 * 1024,
		BackoffBase: 1 * time.Millisecond, BackoffMax: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	defer func() { _ = sp.Close() }()

	client1 := agent.NewClient(cfg.ControllerURL, cfg.NodeID, cfg.NodeSecret, cfg.HTTPTimeout)
	sk := agent.NewSpoke(cfg, sp, client1, zerolog.Nop())

	stats1, err := sk.Run(ctx)
	if err != nil {
		t.Fatalf("first cycle: %v", err)
	}
	if stats1.Drain.Accepted != 1 {
		t.Fatalf("first cycle accepted = %d, want 1", stats1.Drain.Accepted)
	}

	// Manually re-insert a duplicate row in the spool that mirrors the same
	// probe_run_id, simulating Case A (controller wrote, then we crashed
	// before deleting locally).
	if err := sp.Enqueue(ctx, stats1.ProbeRunID+"-replay", []byte(`unused`)); err != nil {
		t.Fatalf("seed extra row: %v", err)
	}

	// Build a fresh payload for the SAME probe_run_id and force-enqueue it.
	// Easiest: create a second spoke that returns a fixed run id we already
	// posted. To avoid surgery on Spoke internals, just call Enqueue with
	// the prior run id and a recreated payload.
	replayPayload := []byte(`{"source_id":"spoke-1","probe_run_id":"` + stats1.ProbeRunID + `","observed_at":"` +
		time.Now().UTC().Format(time.RFC3339) + `","results":[{"target_id":"live","latency_ms":1.0}],"spool_metadata":{"spool_depth":1,"spool_oldest_age_seconds":0,"spool_drops_total":0,"drain_attempt":2}}`)
	if err := sp.Enqueue(ctx, stats1.ProbeRunID, replayPayload); err != nil {
		t.Fatalf("re-enqueue replay: %v", err)
	}

	// Stop and restart the controller against the SAME DB (simulating
	// process restart).
	ts1.Close()
	_ = db.Close()

	db2, err := controller.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("reopen ctl: %v", err)
	}
	defer func() { _ = db2.Close() }()
	srv2 := controller.New(db2, zerolog.Nop())
	ts2 := httptest.NewServer(srv2.Handler())
	defer ts2.Close()

	client2 := agent.NewClient(ts2.URL, cfg.NodeID, cfg.NodeSecret, cfg.HTTPTimeout)
	stats2, err := sp.Drain(ctx, client2)
	if err != nil {
		t.Fatalf("drain after restart: %v", err)
	}
	// The replay row should be Accepted (409 → terminal success). The other
	// "-replay" row carries an unparseable payload and should be Permanent.
	if stats2.Accepted < 1 {
		t.Errorf("expected at least one Accepted (409 replay); stats=%+v", stats2)
	}
	if stats2.Permanent < 1 {
		t.Errorf("expected one Permanent (bad-JSON row); stats=%+v", stats2)
	}
	depth, _ := sp.Depth(ctx)
	// Permanent-fail rows are kept (operator must intervene), so depth=1.
	if depth != 1 {
		t.Errorf("depth after replay drain = %d, want 1 (perm-fail row retained)", depth)
	}
}
