package ticket

import (
	"sync"
	"time"
)

// DefaultBlockDuration is how long an over-limit IP is shut out. Per spec §3.3.
const DefaultBlockDuration = 60 * time.Second

// DefaultWindow is the rolling window for counting failures (spec §3.3).
const DefaultWindow = 60 * time.Second

// RateLimiter throttles failed ticket validations per source IP.
//
// Semantics per spec §3.3:
//   - Track failed validations per source IP within a rolling 60s window.
//   - Once `limit` failures accumulate, return Blocked() == true for the
//     next 60s.
//   - Successful validations don't count against the limit.
//   - State is in-memory only; resets on controller restart.
type RateLimiter struct {
	mu       sync.Mutex
	failures map[string]*ipState
	limit    int
	window   time.Duration
	block    time.Duration
	now      func() time.Time
}

type ipState struct {
	count        int
	windowStart  time.Time
	blockedUntil time.Time
}

// NewRateLimiter constructs a limiter; pass 0 for limit/window/block to use
// the spec defaults.
func NewRateLimiter(limit int, window, block time.Duration) *RateLimiter {
	if limit <= 0 {
		limit = 10
	}
	if window <= 0 {
		window = DefaultWindow
	}
	if block <= 0 {
		block = DefaultBlockDuration
	}
	return &RateLimiter{
		failures: make(map[string]*ipState),
		limit:    limit,
		window:   window,
		block:    block,
		now:      time.Now,
	}
}

// Blocked reports whether ip is currently in a block window.
func (r *RateLimiter) Blocked(ip string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.failures[ip]
	if !ok {
		return false
	}
	return r.now().Before(st.blockedUntil)
}

// RecordFailure increments the counter for ip and returns true if this
// failure put the IP into a block. A successful validation should NOT call
// this method.
func (r *RateLimiter) RecordFailure(ip string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	st, ok := r.failures[ip]
	if !ok {
		st = &ipState{windowStart: now}
		r.failures[ip] = st
	}

	// If we're already inside a block, no need to update further.
	if now.Before(st.blockedUntil) {
		return false
	}

	// Slide the window: old failures don't count.
	if now.Sub(st.windowStart) > r.window {
		st.count = 0
		st.windowStart = now
	}

	st.count++
	if st.count >= r.limit {
		st.blockedUntil = now.Add(r.block)
		return true
	}
	return false
}

// PruneExpired clears state entries whose block has long since elapsed and
// whose count window has rolled. Cheap; call from a periodic loop if desired.
func (r *RateLimiter) PruneExpired() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	n := 0
	for ip, st := range r.failures {
		if now.After(st.blockedUntil) && now.Sub(st.windowStart) > r.window {
			delete(r.failures, ip)
			n++
		}
	}
	return n
}
