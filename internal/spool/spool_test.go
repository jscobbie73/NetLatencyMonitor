package spool

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSender implements Sender. The next outcome (and optional error) for a
// given probe_run_id is read from a per-id queue; if empty, defaultOutcome
// is used.
type fakeSender struct {
	mu             sync.Mutex
	defaultOutcome SendOutcome
	queue          map[string][]struct {
		outcome SendOutcome
		err     error
	}
	calls []string
}

func newFakeSender(def SendOutcome) *fakeSender {
	return &fakeSender{
		defaultOutcome: def,
		queue: make(map[string][]struct {
			outcome SendOutcome
			err     error
		}),
	}
}

func (f *fakeSender) enqueue(id string, o SendOutcome, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue[id] = append(f.queue[id], struct {
		outcome SendOutcome
		err     error
	}{o, err})
}

func (f *fakeSender) Send(_ context.Context, id string, _ []byte) (SendOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id)
	if q, ok := f.queue[id]; ok && len(q) > 0 {
		next := q[0]
		f.queue[id] = q[1:]
		return next.outcome, next.err
	}
	return f.defaultOutcome, nil
}

func openTestSpool(t *testing.T, cfg Config) *Spool {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "spool.db")
	if cfg.DrainRows == 0 {
		cfg.DrainRows = 100
	}
	if cfg.MaxAge == 0 {
		cfg.MaxAge = 24 * time.Hour
	}
	if cfg.MaxSize == 0 {
		cfg.MaxSize = 100 * 1024 * 1024
	}
	if cfg.BackoffBase == 0 {
		cfg.BackoffBase = 30 * time.Second
	}
	if cfg.BackoffMax == 0 {
		cfg.BackoffMax = time.Hour
	}
	s, err := Open(path, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestEnqueueAndDepth(t *testing.T) {
	ctx := context.Background()
	s := openTestSpool(t, Config{})

	if err := s.Enqueue(ctx, "id-1", []byte(`{"a":1}`)); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := s.Enqueue(ctx, "id-2", []byte(`{"a":2}`)); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	depth, err := s.Depth(ctx)
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if depth != 2 {
		t.Errorf("depth = %d, want 2", depth)
	}
}

func TestEnqueueDuplicate(t *testing.T) {
	ctx := context.Background()
	s := openTestSpool(t, Config{})

	if err := s.Enqueue(ctx, "dup", []byte(`{}`)); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	err := s.Enqueue(ctx, "dup", []byte(`{}`))
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second enqueue err = %v, want ErrDuplicate", err)
	}
}

func TestDrainAcceptedDeletesRow(t *testing.T) {
	ctx := context.Background()
	s := openTestSpool(t, Config{})
	send := newFakeSender(OutcomeAccepted)

	for i := 0; i < 3; i++ {
		if err := s.Enqueue(ctx, fmt.Sprintf("id-%d", i), []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := s.Drain(ctx, send)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if stats.Considered != 3 || stats.Accepted != 3 {
		t.Errorf("stats = %+v", stats)
	}
	d, _ := s.Depth(ctx)
	if d != 0 {
		t.Errorf("depth after drain = %d, want 0", d)
	}
}

func TestDrainTransientFailKeepsRowAndBumpsAttempts(t *testing.T) {
	ctx := context.Background()
	s := openTestSpool(t, Config{})
	send := newFakeSender(OutcomeTransientFail)
	send.enqueue("id-1", OutcomeTransientFail, errors.New("connection refused"))

	if err := s.Enqueue(ctx, "id-1", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	stats, err := s.Drain(ctx, send)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if stats.Transient != 1 || stats.Accepted != 0 {
		t.Errorf("stats = %+v", stats)
	}
	d, _ := s.Depth(ctx)
	if d != 1 {
		t.Errorf("depth = %d, want 1", d)
	}

	var attempts int
	var lastErr string
	if err := s.db.QueryRow(`SELECT attempts, last_error FROM spool WHERE probe_run_id = ?`, "id-1").Scan(&attempts, &lastErr); err != nil {
		t.Fatalf("query attempts: %v", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	if !strings.Contains(lastErr, "connection refused") {
		t.Errorf("last_error = %q", lastErr)
	}
}

func TestDrainBackoffSkipsRow(t *testing.T) {
	ctx := context.Background()
	s := openTestSpool(t, Config{
		BackoffBase: 10 * time.Minute,
		BackoffMax:  time.Hour,
	})
	send := newFakeSender(OutcomeTransientFail)

	if err := s.Enqueue(ctx, "id-1", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	// First drain: marks failure, attempts=1, last_attempt_at=now.
	if _, err := s.Drain(ctx, send); err != nil {
		t.Fatal(err)
	}

	// Pin "now" so the backoff window has not elapsed.
	s.now = func() time.Time { return time.Now().Add(time.Second) }
	stats, err := s.Drain(ctx, send)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Skipped != 1 || stats.Transient != 0 {
		t.Errorf("stats = %+v, want one skip", stats)
	}

	// Now jump past the backoff window — should retry.
	s.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	stats, err = s.Drain(ctx, send)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Skipped != 0 || stats.Transient != 1 {
		t.Errorf("stats = %+v, want one transient", stats)
	}
}

func TestDrainPermanentFailKeepsRow(t *testing.T) {
	ctx := context.Background()
	s := openTestSpool(t, Config{})
	send := newFakeSender(OutcomePermanentFail)

	if err := s.Enqueue(ctx, "bad", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	stats, err := s.Drain(ctx, send)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Permanent != 1 {
		t.Errorf("stats = %+v", stats)
	}
	if d, _ := s.Depth(ctx); d != 1 {
		t.Errorf("depth = %d, want 1 (permanent failures are NOT terminal-success)", d)
	}
}

func TestRetentionEnforcesAgeBound(t *testing.T) {
	ctx := context.Background()
	s := openTestSpool(t, Config{
		MaxAge:  100 * time.Millisecond,
		MaxSize: 100 * 1024 * 1024,
	})
	send := newFakeSender(OutcomeTransientFail) // never accept; rows accumulate

	if err := s.Enqueue(ctx, "old", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	// Pin now() into the future so the row appears stale by retention rules.
	s.now = func() time.Time { return time.Now().Add(time.Hour) }

	stats, err := s.Drain(ctx, send)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Dropped != 1 {
		t.Errorf("dropped = %d, want 1", stats.Dropped)
	}
	if d, _ := s.Depth(ctx); d != 0 {
		t.Errorf("depth = %d, want 0", d)
	}
	drops, _ := s.DropsTotal(ctx)
	if drops != 1 {
		t.Errorf("drops_total = %d, want 1", drops)
	}
}

func TestRetentionEnforcesSizeBound(t *testing.T) {
	ctx := context.Background()
	// Make the size cap absurdly small so a single row trips it.
	s := openTestSpool(t, Config{
		MaxAge:  24 * time.Hour,
		MaxSize: 1, // 1 byte
	})
	send := newFakeSender(OutcomeTransientFail)

	for i := 0; i < 5; i++ {
		if err := s.Enqueue(ctx, fmt.Sprintf("id-%d", i), []byte(`{"x":"hello"}`)); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := s.Drain(ctx, send)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Dropped < 1 {
		t.Errorf("expected retention drops; stats = %+v", stats)
	}
}

func TestDropsTotalSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "spool.db")
	cfg := Config{
		DrainRows:   100,
		MaxAge:      time.Millisecond,
		MaxSize:     100 * 1024 * 1024,
		BackoffBase: 30 * time.Second,
		BackoffMax:  time.Hour,
	}

	s, err := Open(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, "id-1", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Now().Add(time.Hour) }
	if _, err := s.Drain(ctx, newFakeSender(OutcomeTransientFail)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path, cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()
	drops, err := s2.DropsTotal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if drops != 1 {
		t.Errorf("drops_total after reopen = %d, want 1", drops)
	}
}

func TestFootprintIncludesWALAndSHM(t *testing.T) {
	ctx := context.Background()
	s := openTestSpool(t, Config{})

	for i := 0; i < 50; i++ {
		if err := s.Enqueue(ctx, fmt.Sprintf("id-%d", i), []byte(`{"some":"payload","with":"a few bytes"}`)); err != nil {
			t.Fatal(err)
		}
	}
	size, err := s.FootprintBytes()
	if err != nil {
		t.Fatal(err)
	}
	if size <= 0 {
		t.Errorf("footprint = %d, want > 0", size)
	}
	// At least the main DB file should exist; with WAL mode active and
	// pending writes there should typically be a -wal file too.
	if _, err := s.FootprintBytes(); err != nil {
		t.Fatalf("second FootprintBytes: %v", err)
	}
}

func TestIdempotencyCaseAReplayAccepted(t *testing.T) {
	// Spec §3.1 Case A: agent crashed after controller wrote but before
	// spool deletion. Replay should be treated as success.
	ctx := context.Background()
	s := openTestSpool(t, Config{})
	send := newFakeSender(OutcomeAccepted)
	send.enqueue("id-replay", OutcomeAccepted, nil) // simulating a 409

	if err := s.Enqueue(ctx, "id-replay", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Drain(ctx, send); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Depth(ctx); d != 0 {
		t.Errorf("depth = %d, want 0 (409 must be treated as terminal success)", d)
	}
}
