// Package litestream tracks Litestream replication health for use by the
// controller's /readyz check and Prometheus metrics.
//
// The data model is intentionally narrow: an external producer (typically a
// goroutine tailing the Litestream service's journald output) reports
// successful syncs and errors via Tracker; consumers query Status.
package litestream

import (
	"sync"
	"time"
)

// Status is the snapshot consumed by /readyz and /metrics.
type Status struct {
	// LastSyncAt is the wall-clock time of the most recent successful sync.
	// Zero when no sync has been observed yet.
	LastSyncAt time.Time
	// ServiceActive mirrors `systemctl is-active nlm-litestream.service`
	// (true when the unit is running). Defaults to false until set.
	ServiceActive bool
	// Errors counts replication errors since process start.
	Errors uint64
	// LagMax is the configured max lag; helpful for /metrics labels but not
	// authoritative for the gate (the controller passes maxLag explicitly
	// when calling Healthy).
	LagMax time.Duration
}

// Tracker is a tiny thread-safe holder. Producers write; consumers read.
type Tracker struct {
	mu     sync.RWMutex
	status Status
	now    func() time.Time
}

// NewTracker returns an empty tracker. maxLag is informational only.
func NewTracker(maxLag time.Duration) *Tracker {
	return &Tracker{
		status: Status{LagMax: maxLag},
		now:    time.Now,
	}
}

// RecordSync notes a successful sync at t (or "now" if zero).
func (t *Tracker) RecordSync(at time.Time) {
	if at.IsZero() {
		at = t.now()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if at.After(t.status.LastSyncAt) {
		t.status.LastSyncAt = at
	}
}

// RecordError increments the error counter.
func (t *Tracker) RecordError() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status.Errors++
}

// SetServiceActive updates the systemd-active flag.
func (t *Tracker) SetServiceActive(active bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status.ServiceActive = active
}

// Status returns the current snapshot.
func (t *Tracker) Status() Status {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.status
}

// Lag returns the time since the last successful sync, or a very large
// value when no sync has been observed (so an unstarted controller fails
// the gate).
func (t *Tracker) Lag() time.Duration {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.status.LastSyncAt.IsZero() {
		return time.Duration(1<<62 - 1)
	}
	return t.now().Sub(t.status.LastSyncAt)
}

// Healthy returns true iff:
//   - the systemd unit is reported active, and
//   - the last successful sync happened within maxLag
//
// Both conditions per spec §3.2.
func (t *Tracker) Healthy(maxLag time.Duration) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.status.ServiceActive {
		return false
	}
	if t.status.LastSyncAt.IsZero() {
		return false
	}
	return t.now().Sub(t.status.LastSyncAt) <= maxLag
}
