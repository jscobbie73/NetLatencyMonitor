package litestream

import (
	"testing"
	"time"
)

func TestTrackerHealthyRequiresActiveAndRecentSync(t *testing.T) {
	tr := NewTracker(5 * time.Minute)

	if tr.Healthy(time.Minute) {
		t.Error("freshly-built tracker should NOT be healthy")
	}

	tr.SetServiceActive(true)
	if tr.Healthy(time.Minute) {
		t.Error("active but never-synced should NOT be healthy")
	}

	tr.RecordSync(time.Now())
	if !tr.Healthy(time.Minute) {
		t.Error("active + recent sync should be healthy")
	}

	tr.SetServiceActive(false)
	if tr.Healthy(time.Minute) {
		t.Error("inactive service should NOT be healthy regardless of recency")
	}
}

func TestTrackerStaleSyncFailsGate(t *testing.T) {
	tr := NewTracker(5 * time.Minute)
	tr.SetServiceActive(true)
	tr.RecordSync(time.Now().Add(-10 * time.Minute))
	if tr.Healthy(time.Minute) {
		t.Error("stale sync should fail the gate")
	}
	if tr.Healthy(15*time.Minute) == false {
		t.Error("widening the gate should let it pass")
	}
}

func TestTrackerRecordSyncMonotonic(t *testing.T) {
	tr := NewTracker(time.Minute)
	now := time.Now()
	tr.RecordSync(now)
	tr.RecordSync(now.Add(-time.Hour)) // older sync should NOT replace newer
	if got := tr.Status().LastSyncAt; !got.Equal(now) {
		t.Errorf("LastSyncAt = %v, want %v (older sync must not overwrite)", got, now)
	}
}

func TestTrackerRecordError(t *testing.T) {
	tr := NewTracker(time.Minute)
	tr.RecordError()
	tr.RecordError()
	if tr.Status().Errors != 2 {
		t.Errorf("Errors = %d, want 2", tr.Status().Errors)
	}
}

func TestTrackerLagWithoutSyncIsHuge(t *testing.T) {
	tr := NewTracker(time.Minute)
	if lag := tr.Lag(); lag < 24*time.Hour {
		t.Errorf("never-synced Lag should be very large; got %v", lag)
	}
}
