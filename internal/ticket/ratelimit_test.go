package ticket

import (
	"testing"
	"time"
)

func TestRateLimiterBlocksAfterLimit(t *testing.T) {
	r := NewRateLimiter(3, time.Minute, time.Minute)
	const ip = "10.0.0.1"

	for i := 1; i <= 2; i++ {
		if got := r.RecordFailure(ip); got {
			t.Errorf("RecordFailure(%d) returned blocked=true early", i)
		}
		if r.Blocked(ip) {
			t.Errorf("Blocked after %d failures should be false", i)
		}
	}
	if got := r.RecordFailure(ip); !got {
		t.Error("RecordFailure at limit should return blocked=true")
	}
	if !r.Blocked(ip) {
		t.Error("Blocked at limit should be true")
	}
}

func TestRateLimiterIPsIndependent(t *testing.T) {
	r := NewRateLimiter(2, time.Minute, time.Minute)
	r.RecordFailure("10.0.0.1")
	r.RecordFailure("10.0.0.1")
	if !r.Blocked("10.0.0.1") {
		t.Error("first IP should be blocked")
	}
	if r.Blocked("10.0.0.2") {
		t.Error("second IP should not be blocked")
	}
}

func TestRateLimiterWindowSlides(t *testing.T) {
	now := time.Now()
	r := NewRateLimiter(3, time.Minute, time.Minute)
	r.now = func() time.Time { return now }

	r.RecordFailure("ip")
	r.RecordFailure("ip")
	// Advance past the window.
	now = now.Add(2 * time.Minute)
	if got := r.RecordFailure("ip"); got {
		t.Error("after sliding window, single failure should not block")
	}
	if r.Blocked("ip") {
		t.Error("should not be blocked after window slid")
	}
}

func TestRateLimiterBlockExpires(t *testing.T) {
	now := time.Now()
	r := NewRateLimiter(2, time.Minute, time.Minute)
	r.now = func() time.Time { return now }

	r.RecordFailure("ip")
	r.RecordFailure("ip")
	if !r.Blocked("ip") {
		t.Fatal("should be blocked")
	}
	now = now.Add(2 * time.Minute)
	if r.Blocked("ip") {
		t.Error("block should have expired")
	}
}

func TestRateLimiterPruneExpired(t *testing.T) {
	now := time.Now()
	r := NewRateLimiter(2, time.Minute, time.Minute)
	r.now = func() time.Time { return now }
	r.RecordFailure("ip")
	now = now.Add(2 * time.Minute)
	if n := r.PruneExpired(); n != 1 {
		t.Errorf("pruned = %d, want 1", n)
	}
}
