// Package chrony queries chronyd's tracking output to obtain the system's
// estimated clock offset. Per spec §3.2, the controller's /readyz fails
// closed when this query fails or the offset exceeds the configured gate.
package chrony

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DriftMS is the absolute, current estimated system-clock offset in
// milliseconds. We only care about magnitude for the gate.
type DriftMS float64

// Querier returns the current absolute drift. Implementations may shell out
// to chronyc, parse a unix socket, or return a fixed value in tests.
type Querier interface {
	Drift(ctx context.Context) (DriftMS, error)
}

// CommandQuerier shells out to `chronyc tracking` and parses the
// "System time" line. Acceptable for the small scrape rate /readyz uses.
type CommandQuerier struct {
	// Binary is the chronyc executable to invoke; default "chronyc".
	Binary string
	// Timeout caps the subprocess wall clock; default 2s.
	Timeout time.Duration
}

// Drift implements Querier.
func (c *CommandQuerier) Drift(ctx context.Context) (DriftMS, error) {
	bin := c.Binary
	if bin == "" {
		bin = "chronyc"
	}
	to := c.Timeout
	if to <= 0 {
		to = 2 * time.Second
	}
	subCtx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	cmd := exec.CommandContext(subCtx, bin, "tracking")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("chrony: %s tracking: %w", bin, err)
	}
	return ParseTracking(out.String())
}

// ParseTracking pulls the System time offset out of a `chronyc tracking`
// dump and returns it as absolute milliseconds.
//
// The relevant line looks like:
//
//	System time     : 0.000123456 seconds slow of NTP time
//	System time     : 0.000234567 seconds fast of NTP time
//
// "slow" / "fast" is direction; we collapse to magnitude.
func ParseTracking(out string) (DriftMS, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "System time") {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		if len(fields) < 2 {
			return 0, fmt.Errorf("chrony: unexpected System time line: %q", line)
		}
		secs, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return 0, fmt.Errorf("chrony: parse offset %q: %w", fields[0], err)
		}
		if secs < 0 {
			secs = -secs
		}
		return DriftMS(secs * 1000), nil
	}
	return 0, errors.New("chrony: no System time line in output")
}

// FixedQuerier returns the same value every call. Used by tests.
type FixedQuerier struct {
	Value DriftMS
	Err   error
}

// Drift implements Querier.
func (f FixedQuerier) Drift(_ context.Context) (DriftMS, error) {
	return f.Value, f.Err
}
