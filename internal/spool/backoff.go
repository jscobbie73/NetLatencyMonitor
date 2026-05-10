package spool

import "time"

// Backoff returns the wait window for a row that has failed `attempts` times:
// min(2^attempts * base, max). attempts==0 returns base (next attempt may
// proceed immediately because the caller compares last_attempt_at + window
// against now, and last_attempt_at is NULL on first try).
func Backoff(attempts int, base, max time.Duration) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	// 2^attempts can overflow; cap at max early.
	// Once 2^attempts * base >= max, we're at the cap.
	w := base
	for i := 0; i < attempts; i++ {
		w *= 2
		if w >= max || w <= 0 { // <=0 catches int64 overflow
			return max
		}
	}
	if w > max {
		return max
	}
	return w
}
