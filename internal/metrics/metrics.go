// Package metrics owns the controller's Prometheus exposition.
//
// Metric names mirror spec §11.1. Each Server creates its own Registry so
// tests run in isolation and the global default registry stays uncontaminated.
package metrics

import (
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics bundles every controller metric. Construct with New.
type Metrics struct {
	Reg *prometheus.Registry

	// Ingest
	IngestTotal   *prometheus.CounterVec
	IngestLatency prometheus.Histogram

	// Per-node spool (gauges keyed by node_id)
	NodeSpoolDepth         *prometheus.GaugeVec
	NodeSpoolOldestAgeSecs *prometheus.GaugeVec
	NodeSpoolDropsTotal    *prometheus.CounterVec
	NodeLastSeenSeconds    *prometheus.GaugeVec

	// Clock & replication
	ClockDriftMS                prometheus.Gauge
	LitestreamLastSyncTimestamp prometheus.Gauge
	LitestreamReplicationErrors prometheus.Counter
	LitestreamLagSeconds        prometheus.Gauge

	// Connectivity
	ActiveWebSocketConnections prometheus.Gauge
	WSTicketsIssued            prometheus.Counter
	WSTicketsUsed              prometheus.Counter
	WSTicketsRejected          *prometheus.CounterVec

	// DB / aggregate
	DBSizeBytes       prometheus.Gauge
	ProbeResultsTotal prometheus.Counter
	NodesActive       prometheus.Gauge
	NodesStale        prometheus.Gauge
	NodesOffline      prometheus.Gauge
}

// New creates and registers every metric on a fresh Registry.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{Reg: reg}

	m.IngestTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nlm_ingest_total",
		Help: "POST /api/v1/results responses by node_id and HTTP status.",
	}, []string{"node_id", "status"})

	m.IngestLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "nlm_ingest_latency_seconds",
		Help:    "Server-side handling time for POST /api/v1/results.",
		Buckets: prometheus.DefBuckets,
	})

	m.NodeSpoolDepth = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nlm_node_spool_depth",
		Help: "Latest spool_depth reported by each node.",
	}, []string{"node_id"})

	m.NodeSpoolOldestAgeSecs = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nlm_node_spool_oldest_age_seconds",
		Help: "Latest spool oldest-row age reported by each node, in seconds.",
	}, []string{"node_id"})

	m.NodeSpoolDropsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nlm_node_spool_drops_total",
		Help: "Cumulative spool drops per node, snapshotted from the agent.",
	}, []string{"node_id"})

	m.NodeLastSeenSeconds = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nlm_node_last_seen_seconds",
		Help: "Seconds since the controller last received a result from this node.",
	}, []string{"node_id"})

	m.ClockDriftMS = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nlm_clock_drift_ms",
		Help: "Absolute system-clock offset reported by chronyc, in milliseconds.",
	})

	m.LitestreamLastSyncTimestamp = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nlm_litestream_last_sync_timestamp_seconds",
		Help: "Unix timestamp of the most recent successful Litestream sync.",
	})

	m.LitestreamReplicationErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nlm_litestream_replication_errors_total",
		Help: "Cumulative Litestream replication errors observed by the controller.",
	})

	m.LitestreamLagSeconds = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nlm_litestream_lag_seconds",
		Help: "Seconds since the most recent successful Litestream sync.",
	})

	m.ActiveWebSocketConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nlm_active_websocket_connections",
		Help: "Number of currently-open authenticated WebSocket connections.",
	})

	m.WSTicketsIssued = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nlm_ws_tickets_issued_total",
		Help: "Total WebSocket tickets issued.",
	})

	m.WSTicketsUsed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nlm_ws_tickets_used_total",
		Help: "Total WebSocket tickets successfully redeemed.",
	})

	m.WSTicketsRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nlm_ws_tickets_rejected_total",
		Help: "Failed WebSocket ticket validations by reason.",
	}, []string{"reason"}) // expired | unknown | ratelimit

	m.DBSizeBytes = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nlm_db_size_bytes",
		Help: "On-disk size of the controller SQLite DB, refreshed on each scrape.",
	})

	m.ProbeResultsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nlm_probe_results_total",
		Help: "Cumulative probe_result rows ingested.",
	})

	m.NodesActive = prometheus.NewGauge(prometheus.GaugeOpts{Name: "nlm_nodes_active", Help: "Nodes seen recently (status=active)."})
	m.NodesStale = prometheus.NewGauge(prometheus.GaugeOpts{Name: "nlm_nodes_stale", Help: "Nodes whose last-seen falls into the stale window."})
	m.NodesOffline = prometheus.NewGauge(prometheus.GaugeOpts{Name: "nlm_nodes_offline", Help: "Nodes considered offline."})

	for _, c := range m.allCollectors() {
		reg.MustRegister(c)
	}
	return m
}

func (m *Metrics) allCollectors() []prometheus.Collector {
	return []prometheus.Collector{
		m.IngestTotal,
		m.IngestLatency,
		m.NodeSpoolDepth,
		m.NodeSpoolOldestAgeSecs,
		m.NodeSpoolDropsTotal,
		m.NodeLastSeenSeconds,
		m.ClockDriftMS,
		m.LitestreamLastSyncTimestamp,
		m.LitestreamReplicationErrors,
		m.LitestreamLagSeconds,
		m.ActiveWebSocketConnections,
		m.WSTicketsIssued,
		m.WSTicketsUsed,
		m.WSTicketsRejected,
		m.DBSizeBytes,
		m.ProbeResultsTotal,
		m.NodesActive,
		m.NodesStale,
		m.NodesOffline,
	}
}

// Handler returns the /metrics handler. If token is non-empty, every request
// must present `Authorization: Bearer <token>`; missing or wrong token
// yields 401 (per spec §11.1).
//
// onScrape, if non-nil, is invoked before each scrape so the server can
// refresh on-demand gauges (db size, lag) without paying for a periodic
// background sampler.
func (m *Metrics) Handler(token string, onScrape func()) http.Handler {
	base := promhttp.HandlerFor(m.Reg, promhttp.HandlerOpts{Registry: m.Reg})
	wrap := func(w http.ResponseWriter, r *http.Request) {
		if onScrape != nil {
			onScrape()
		}
		base.ServeHTTP(w, r)
	}
	if token == "" {
		return http.HandlerFunc(wrap)
	}
	expected := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !equalSecure(auth, expected) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		wrap(w, r)
	})
}

func equalSecure(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0 && strings.EqualFold(a[:len("Bearer ")], "Bearer ")
}
