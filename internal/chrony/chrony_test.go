package chrony

import (
	"context"
	"strings"
	"testing"
)

func TestParseTrackingSlow(t *testing.T) {
	out := strings.Join([]string{
		"Reference ID    : 7F7F0101 (localhost)",
		"Stratum         : 3",
		"Ref time (UTC)  : Sun Apr 06 12:34:56 2025",
		"System time     : 0.000123456 seconds slow of NTP time",
		"Last offset     : -0.000234567 seconds",
	}, "\n")
	got, err := ParseTracking(out)
	if err != nil {
		t.Fatalf("ParseTracking: %v", err)
	}
	want := DriftMS(0.123456)
	if got < want-0.001 || got > want+0.001 {
		t.Errorf("drift = %v ms, want %v", got, want)
	}
}

func TestParseTrackingFast(t *testing.T) {
	out := "System time     : 0.045 seconds fast of NTP time"
	got, err := ParseTracking(out)
	if err != nil {
		t.Fatalf("ParseTracking: %v", err)
	}
	if got < 44 || got > 46 {
		t.Errorf("drift = %v ms, want ~45", got)
	}
}

func TestParseTrackingNegativeOffsetCollapsedToMagnitude(t *testing.T) {
	// chronyc usually prints magnitude + direction word, but be robust
	// to a parser that hands us a signed number directly.
	out := "System time     : -0.010 seconds slow of NTP time"
	got, err := ParseTracking(out)
	if err != nil {
		t.Fatalf("ParseTracking: %v", err)
	}
	if got < 9.5 || got > 10.5 {
		t.Errorf("drift = %v ms, want ~10", got)
	}
}

func TestParseTrackingNoSystemTime(t *testing.T) {
	out := "Reference ID    : 7F7F0101 (localhost)\nStratum         : 3\n"
	if _, err := ParseTracking(out); err == nil {
		t.Error("expected error when System time line is missing")
	}
}

func TestParseTrackingMalformed(t *testing.T) {
	out := "System time     : not-a-number seconds slow of NTP time"
	if _, err := ParseTracking(out); err == nil {
		t.Error("expected parse error on garbage offset")
	}
}

func TestFixedQuerier(t *testing.T) {
	q := FixedQuerier{Value: 42}
	v, err := q.Drift(context.Background())
	if err != nil || v != 42 {
		t.Errorf("FixedQuerier: %v, %v", v, err)
	}
}

func TestCommandQuerierBadBinary(t *testing.T) {
	c := &CommandQuerier{Binary: "/definitely/does/not/exist/chronyc"}
	if _, err := c.Drift(context.Background()); err == nil {
		t.Error("expected error from missing binary")
	}
}
