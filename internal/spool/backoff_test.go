package spool

import (
	"testing"
	"time"
)

func TestBackoffProgression(t *testing.T) {
	base := 30 * time.Second
	max := time.Hour

	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{0, 30 * time.Second},
		{1, 60 * time.Second},
		{2, 120 * time.Second},
		{3, 240 * time.Second},
		{4, 480 * time.Second},
		{5, 960 * time.Second},
		{6, 1920 * time.Second},
		{7, max}, // 30s * 128 = 3840s > 3600s cap
		{20, max},
		{1000, max},
	}
	for _, tc := range cases {
		got := Backoff(tc.attempts, base, max)
		if got != tc.want {
			t.Errorf("Backoff(%d) = %v, want %v", tc.attempts, got, tc.want)
		}
	}
}

func TestBackoffNegativeAttempts(t *testing.T) {
	base := 10 * time.Second
	if got := Backoff(-5, base, time.Hour); got != base {
		t.Errorf("Backoff(-5) = %v, want %v", got, base)
	}
}

func TestBackoffOverflowSafe(t *testing.T) {
	// Make sure huge attempts counts don't panic or return negative durations.
	if got := Backoff(1<<30, time.Second, time.Hour); got != time.Hour {
		t.Errorf("Backoff(2^30) = %v, want %v", got, time.Hour)
	}
}
