// Package ui holds templ components and view-model types for the NLM web UI.
package ui

import "time"

// NodeInfo is the view-model for one node row in the admin UI.
type NodeInfo struct {
	ID         string
	Role       string
	Address    string
	Disabled   bool
	SpoolDepth int
	LastSeen   time.Time
}

// MatrixEntry holds the aggregated latency between one (source, target) pair.
type MatrixEntry struct {
	SourceID string
	TargetID string
	AvgLatMS float64
	MinLatMS float64
	MaxLatMS float64
	Samples  int
	HasError bool
	LastSeen time.Time
}

// LatencyMatrix is the full N×N probe-result grid.
type LatencyMatrix struct {
	Nodes   []string      // ordered node IDs (hub nodes only)
	Entries []MatrixEntry // all observed (source, target) pairs
}

// Get returns the entry for (src, dst), or nil when no data exists yet.
func (m LatencyMatrix) Get(src, dst string) *MatrixEntry {
	for i := range m.Entries {
		e := &m.Entries[i]
		if e.SourceID == src && e.TargetID == dst {
			return e
		}
	}
	return nil
}

// cellClass maps an average latency (ms) to a CSS class name.
func cellClass(ms float64) string {
	switch {
	case ms < 10:
		return "cell-good"
	case ms < 50:
		return "cell-warn"
	default:
		return "cell-bad"
	}
}
