package litestream

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fakeQuerier records calls and returns configured responses.
type fakeQuerier struct {
	// responses maps the joined args string to output bytes.
	responses map[string][]byte
	// errors maps the joined args string to a non-nil error (non-zero exit).
	errors map[string]error
	// calls records every call for assertion.
	calls []string
}

func newFakeQuerier() *fakeQuerier {
	return &fakeQuerier{
		responses: make(map[string][]byte),
		errors:    make(map[string]error),
	}
}

func (f *fakeQuerier) setActive(service string, active bool) {
	key := strings.Join([]string{"systemctl", "is-active", "--quiet", service}, " ")
	if active {
		f.responses[key] = nil
		delete(f.errors, key)
	} else {
		f.errors[key] = context.DeadlineExceeded // any non-nil error
		delete(f.responses, key)
	}
}

func (f *fakeQuerier) setJournal(service, output string) {
	// Match regardless of --since value by using a prefix key.
	// We store under a stable prefix and do prefix matching in Query.
	key := "journalctl -u " + service
	f.responses[key] = []byte(output)
}

func (f *fakeQuerier) Query(_ context.Context, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	f.calls = append(f.calls, joined)

	// Exact match first.
	if err, ok := f.errors[joined]; ok {
		return nil, err
	}
	if out, ok := f.responses[joined]; ok {
		return out, nil
	}

	// Prefix match for journalctl (--since value varies per poll).
	for k, out := range f.responses {
		if strings.HasPrefix(joined, k) {
			return out, nil
		}
	}
	for k, err := range f.errors {
		if strings.HasPrefix(joined, k) {
			return nil, err
		}
	}
	return nil, nil
}

func TestWatcher_ServiceActiveTrue(t *testing.T) {
	tracker := NewTracker(5 * time.Minute)
	fq := newFakeQuerier()
	fq.setActive("nlm-litestream.service", true)

	w := NewWatcher(tracker, WatcherConfig{
		Service:      "nlm-litestream.service",
		PollInterval: time.Hour, // won't tick in this test
		Querier:      fq,
	})
	w.poll(context.Background())

	if !tracker.Status().ServiceActive {
		t.Fatal("expected ServiceActive=true after systemctl exits 0")
	}
}

func TestWatcher_ServiceActiveFalse(t *testing.T) {
	tracker := NewTracker(5 * time.Minute)
	fq := newFakeQuerier()
	fq.setActive("nlm-litestream.service", false)

	w := NewWatcher(tracker, WatcherConfig{
		Service:      "nlm-litestream.service",
		PollInterval: time.Hour,
		Querier:      fq,
	})
	w.poll(context.Background())

	if tracker.Status().ServiceActive {
		t.Fatal("expected ServiceActive=false after systemctl exits non-zero")
	}
}

func TestWatcher_ParseSyncLine(t *testing.T) {
	tracker := NewTracker(5 * time.Minute)
	fq := newFakeQuerier()
	fq.setActive("nlm-litestream.service", true)
	fq.setJournal("nlm-litestream.service",
		"2026-01-01T00:00:00+0000 host litestream[123]: snapshot written path=/var/lib/nlm/nlm.db\n"+
			"2026-01-01T00:00:01+0000 host litestream[123]: wal segment written\n",
	)

	w := NewWatcher(tracker, WatcherConfig{
		Service:      "nlm-litestream.service",
		PollInterval: time.Hour,
		Querier:      fq,
	})
	w.poll(context.Background())

	st := tracker.Status()
	if st.LastSyncAt.IsZero() {
		t.Fatal("expected LastSyncAt to be set after sync line")
	}
	if st.Errors != 0 {
		t.Fatalf("expected 0 errors, got %d", st.Errors)
	}
}

func TestWatcher_ParseErrorLine(t *testing.T) {
	tracker := NewTracker(5 * time.Minute)
	fq := newFakeQuerier()
	fq.setActive("nlm-litestream.service", true)
	fq.setJournal("nlm-litestream.service",
		"2026-01-01T00:00:00+0000 host litestream[123]: error replicating WAL: connection refused\n"+
			"2026-01-01T00:00:01+0000 host litestream[123]: replication failed: timeout\n",
	)

	w := NewWatcher(tracker, WatcherConfig{
		Service:      "nlm-litestream.service",
		PollInterval: time.Hour,
		Querier:      fq,
	})
	w.poll(context.Background())

	st := tracker.Status()
	if st.Errors != 2 {
		t.Fatalf("expected 2 errors, got %d", st.Errors)
	}
	if !st.LastSyncAt.IsZero() {
		t.Fatal("expected no sync recorded for error-only output")
	}
}

func TestWatcher_MixedLines(t *testing.T) {
	tracker := NewTracker(5 * time.Minute)
	fq := newFakeQuerier()
	fq.setActive("nlm-litestream.service", true)
	fq.setJournal("nlm-litestream.service",
		"2026-01-01T00:00:00+0000 host litestream[1]: snapshot written\n"+
			"2026-01-01T00:00:01+0000 host litestream[1]: error replicating WAL\n"+
			"2026-01-01T00:00:02+0000 host litestream[1]: wal segment written\n",
	)

	w := NewWatcher(tracker, WatcherConfig{
		Service:      "nlm-litestream.service",
		PollInterval: time.Hour,
		Querier:      fq,
	})
	w.poll(context.Background())

	st := tracker.Status()
	if st.Errors != 1 {
		t.Fatalf("expected 1 error, got %d", st.Errors)
	}
	if st.LastSyncAt.IsZero() {
		t.Fatal("expected at least one sync to be recorded")
	}
}

func TestWatcher_EmptyJournal(t *testing.T) {
	tracker := NewTracker(5 * time.Minute)
	fq := newFakeQuerier()
	fq.setActive("nlm-litestream.service", true)
	// No journal entries set → querier returns nil, nil.

	w := NewWatcher(tracker, WatcherConfig{
		Service:      "nlm-litestream.service",
		PollInterval: time.Hour,
		Querier:      fq,
	})
	w.poll(context.Background())

	st := tracker.Status()
	if st.Errors != 0 || !st.LastSyncAt.IsZero() {
		t.Fatal("empty journal should not change tracker state")
	}
	if !st.ServiceActive {
		t.Fatal("service should be active")
	}
}

func TestWatcher_RunCancels(t *testing.T) {
	tracker := NewTracker(5 * time.Minute)
	fq := newFakeQuerier()
	fq.setActive("nlm-litestream.service", true)

	w := NewWatcher(tracker, WatcherConfig{
		Service:      "nlm-litestream.service",
		PollInterval: 10 * time.Millisecond,
		Querier:      fq,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
		// expected
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Watcher.Run did not return after context cancellation")
	}
}

func TestWatcher_Defaults(t *testing.T) {
	tracker := NewTracker(5 * time.Minute)
	w := NewWatcher(tracker, WatcherConfig{})
	if w.ServiceName() != "nlm-litestream.service" {
		t.Fatalf("expected default service name, got %q", w.ServiceName())
	}
	if w.pollInterval != 30*time.Second {
		t.Fatalf("expected default poll interval 30s, got %v", w.pollInterval)
	}
}
