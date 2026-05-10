package controller

import (
	"context"
	"sort"

	"github.com/jscobbie73/netlatencymonitor/internal/ui"
)

// queryMatrix returns the latency matrix for all hub nodes, using probe
// results from the last 5 minutes.
func (s *Server) queryMatrix(ctx context.Context) (ui.LatencyMatrix, error) {
	// Collect hub node IDs for the matrix axes.
	nodes, err := s.nodes.List(ctx)
	if err != nil {
		return ui.LatencyMatrix{}, err
	}
	var hubIDs []string
	for _, n := range nodes {
		if n.Role == RoleHub && !n.Disabled {
			hubIDs = append(hubIDs, n.ID)
		}
	}
	sort.Strings(hubIDs)

	if len(hubIDs) == 0 {
		return ui.LatencyMatrix{Nodes: hubIDs}, nil
	}

	const q = `
SELECT
    source_id,
    target_id,
    AVG(latency_ms)  AS avg_ms,
    MIN(latency_ms)  AS min_ms,
    MAX(latency_ms)  AS max_ms,
    COUNT(*)         AS samples,
    MAX(observed_at) AS last_seen,
    SUM(CASE WHEN error IS NOT NULL AND error != '' THEN 1 ELSE 0 END) AS errors
FROM probe_results
WHERE observed_at > datetime('now', '-5 minutes')
GROUP BY source_id, target_id
`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return ui.LatencyMatrix{}, err
	}
	defer func() { _ = rows.Close() }()

	var entries []ui.MatrixEntry
	for rows.Next() {
		var e ui.MatrixEntry
		var errCount int
		if err := rows.Scan(
			&e.SourceID, &e.TargetID,
			&e.AvgLatMS, &e.MinLatMS, &e.MaxLatMS,
			&e.Samples, &e.LastSeen,
			&errCount,
		); err != nil {
			return ui.LatencyMatrix{}, err
		}
		e.HasError = errCount > 0 && e.Samples == errCount
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return ui.LatencyMatrix{}, err
	}

	return ui.LatencyMatrix{Nodes: hubIDs, Entries: entries}, nil
}
