// Package agent implements the NLM agent runtime: probe execution, the HTTP
// client used by spool drains, and the spoke/listener mode runners.
//
// Spec note: probes measure TCP-connect latency (dial → established) per
// spec §1; this is network RTT plus host kernel handshake, not pure ICMP RTT.
package agent

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/jscobbie73/netlatencymonitor/internal/config"
	"github.com/jscobbie73/netlatencymonitor/internal/controller"
)

// Probe runs one TCP-connect probe against addr and returns the round-trip
// duration on success, or a non-nil error on failure (including timeout).
//
// The dialer is a fresh net.Dialer per call so callers can pin per-probe
// timeouts via ctx without sharing state across goroutines.
func Probe(ctx context.Context, addr string, timeout time.Duration) (time.Duration, error) {
	d := net.Dialer{}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	conn, err := d.DialContext(dctx, "tcp", addr)
	if err != nil {
		return 0, err
	}
	rtt := time.Since(start)
	_ = conn.Close()
	return rtt, nil
}

// RunProbes fans out one TCP-connect probe per target concurrently and
// collects the observations in input order.
//
// Concurrent dials are bounded by len(targets); for the modest target counts
// in NLM (per-spoke target list is small) we don't need a worker pool.
func RunProbes(ctx context.Context, targets []config.Target, timeout time.Duration) []controller.ResultObservation {
	out := make([]controller.ResultObservation, len(targets))
	var wg sync.WaitGroup
	wg.Add(len(targets))
	for i, t := range targets {
		go func(i int, t config.Target) {
			defer wg.Done()
			rtt, err := Probe(ctx, t.Address, timeout)
			obs := controller.ResultObservation{TargetID: t.ID}
			if err != nil {
				obs.Error = err.Error()
			} else {
				ms := float64(rtt) / float64(time.Millisecond)
				obs.LatencyMs = &ms
			}
			out[i] = obs
		}(i, t)
	}
	wg.Wait()
	return out
}
