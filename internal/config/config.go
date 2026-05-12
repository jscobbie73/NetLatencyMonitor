// Package config loads NLM configuration from environment variables.
//
// All settings are env-var-first per spec §3.1. Operational tuning vars
// (NLM_SPOOL_*, NLM_PROBE_*, NLM_HUB_REFRESH_INTERVAL, NLM_READY_MAX_CLOCK_DRIFT_MS,
// NLM_STARTUP_JITTER) may change per environment without breaking compat.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// SpoolConfig holds the agent spool settings from spec §3.1.
type SpoolConfig struct {
	Path               string
	DrainRows          int
	MaxAgeHours        int
	MaxSizeMB          int
	BackoffBaseSeconds int
	BackoffMaxSeconds  int
}

// LoadSpool reads spool-related env vars and applies the spec defaults.
func LoadSpool() (SpoolConfig, error) {
	c := SpoolConfig{
		Path:               getString("NLM_SPOOL_PATH", "/var/lib/nlm/spool.db"),
		DrainRows:          0,
		MaxAgeHours:        0,
		MaxSizeMB:          0,
		BackoffBaseSeconds: 0,
		BackoffMaxSeconds:  0,
	}
	var err error
	if c.DrainRows, err = getInt("NLM_SPOOL_DRAIN_ROWS", 100); err != nil {
		return c, err
	}
	if c.MaxAgeHours, err = getInt("NLM_SPOOL_MAX_AGE_HOURS", 24); err != nil {
		return c, err
	}
	if c.MaxSizeMB, err = getInt("NLM_SPOOL_MAX_SIZE_MB", 100); err != nil {
		return c, err
	}
	if c.BackoffBaseSeconds, err = getInt("NLM_SPOOL_BACKOFF_BASE_SECONDS", 30); err != nil {
		return c, err
	}
	if c.BackoffMaxSeconds, err = getInt("NLM_SPOOL_BACKOFF_MAX_SECONDS", 3600); err != nil {
		return c, err
	}
	if c.DrainRows <= 0 {
		return c, fmt.Errorf("NLM_SPOOL_DRAIN_ROWS must be > 0")
	}
	if c.MaxAgeHours <= 0 {
		return c, fmt.Errorf("NLM_SPOOL_MAX_AGE_HOURS must be > 0")
	}
	if c.MaxSizeMB <= 0 {
		return c, fmt.Errorf("NLM_SPOOL_MAX_SIZE_MB must be > 0")
	}
	if c.BackoffBaseSeconds <= 0 {
		return c, fmt.Errorf("NLM_SPOOL_BACKOFF_BASE_SECONDS must be > 0")
	}
	if c.BackoffMaxSeconds < c.BackoffBaseSeconds {
		return c, fmt.Errorf("NLM_SPOOL_BACKOFF_MAX_SECONDS must be >= NLM_SPOOL_BACKOFF_BASE_SECONDS")
	}
	return c, nil
}

// MaxAge returns the maximum spool row age as a duration.
func (c SpoolConfig) MaxAge() time.Duration {
	return time.Duration(c.MaxAgeHours) * time.Hour
}

// MaxSizeBytes returns the maximum spool footprint in bytes.
func (c SpoolConfig) MaxSizeBytes() int64 {
	return int64(c.MaxSizeMB) * 1024 * 1024
}

// BackoffBase returns the base window for the exponential drain backoff.
func (c SpoolConfig) BackoffBase() time.Duration {
	return time.Duration(c.BackoffBaseSeconds) * time.Second
}

// BackoffMax returns the cap on the exponential drain backoff.
func (c SpoolConfig) BackoffMax() time.Duration {
	return time.Duration(c.BackoffMaxSeconds) * time.Second
}

// Target identifies a single probe destination.
type Target struct {
	ID      string
	Address string // host:port
}

// AgentConfig holds settings shared by spoke and hub modes.
type AgentConfig struct {
	NodeID        string
	NodeSecret    string // resolved from env or NLM_NODE_SECRET_PATH
	ControllerURL string
	Targets       []Target // overrides controller-supplied targets when set
	ProbeTimeout  time.Duration
	HTTPTimeout   time.Duration
	StartupJitter time.Duration

	// Hub-mode specific.
	HubProbeInterval   time.Duration // NLM_HUB_PROBE_INTERVAL (default 60s)
	HubRefreshInterval time.Duration // NLM_HUB_REFRESH_INTERVAL (default 5m)
}

// LoadAgent reads agent env vars. Used by spoke and hub modes (Phases 3 / 5).
//
//	NLM_NODE_ID            agent identifier; must match nodes.id on controller
//	NLM_NODE_SECRET        bearer secret; takes precedence over the file
//	NLM_NODE_SECRET_PATH   file containing the bearer secret (default
//	                       /var/lib/nlm/node.secret)
//	NLM_CONTROLLER_URL     base URL, e.g. https://controller.example.com
//	NLM_PROBE_TARGETS      comma-separated id=host:port pairs
//	NLM_PROBE_TIMEOUT      per-target TCP-connect timeout (default 5s)
//	NLM_HTTP_TIMEOUT       controller request timeout (default 10s)
//	NLM_STARTUP_JITTER     max random delay before the first cycle (default 10s)
func LoadAgent() (AgentConfig, error) {
	c := AgentConfig{
		NodeID:        getString("NLM_NODE_ID", ""),
		ControllerURL: getString("NLM_CONTROLLER_URL", ""),
	}
	if c.NodeID == "" {
		return c, fmt.Errorf("NLM_NODE_ID must be set")
	}
	if c.ControllerURL == "" {
		return c, fmt.Errorf("NLM_CONTROLLER_URL must be set")
	}

	secret, err := loadAgentSecret()
	if err != nil {
		return c, err
	}
	c.NodeSecret = secret

	rawTargets := getString("NLM_PROBE_TARGETS", "")
	c.Targets, err = parseTargets(rawTargets)
	if err != nil {
		return c, err
	}

	probeSecs, err := getInt("NLM_PROBE_TIMEOUT", 5)
	if err != nil {
		return c, err
	}
	if probeSecs <= 0 {
		return c, fmt.Errorf("NLM_PROBE_TIMEOUT must be > 0")
	}
	c.ProbeTimeout = time.Duration(probeSecs) * time.Second

	httpSecs, err := getInt("NLM_HTTP_TIMEOUT", 10)
	if err != nil {
		return c, err
	}
	if httpSecs <= 0 {
		return c, fmt.Errorf("NLM_HTTP_TIMEOUT must be > 0")
	}
	c.HTTPTimeout = time.Duration(httpSecs) * time.Second

	jitterSecs, err := getInt("NLM_STARTUP_JITTER", 10)
	if err != nil {
		return c, err
	}
	if jitterSecs < 0 {
		return c, fmt.Errorf("NLM_STARTUP_JITTER must be >= 0")
	}
	c.StartupJitter = time.Duration(jitterSecs) * time.Second

	hubProbeSecs, err := getInt("NLM_HUB_PROBE_INTERVAL", 60)
	if err != nil {
		return c, err
	}
	if hubProbeSecs <= 0 {
		return c, fmt.Errorf("NLM_HUB_PROBE_INTERVAL must be > 0")
	}
	c.HubProbeInterval = time.Duration(hubProbeSecs) * time.Second

	hubRefreshSecs, err := getInt("NLM_HUB_REFRESH_INTERVAL", 300)
	if err != nil {
		return c, err
	}
	if hubRefreshSecs <= 0 {
		return c, fmt.Errorf("NLM_HUB_REFRESH_INTERVAL must be > 0")
	}
	c.HubRefreshInterval = time.Duration(hubRefreshSecs) * time.Second

	return c, nil
}

func loadAgentSecret() (string, error) {
	if v := os.Getenv("NLM_NODE_SECRET"); v != "" {
		return strings.TrimSpace(v), nil
	}
	path := getString("NLM_NODE_SECRET_PATH", "/var/lib/nlm/node.secret")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read NLM_NODE_SECRET_PATH (%s): %w", path, err)
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", fmt.Errorf("NLM_NODE_SECRET_PATH (%s) is empty", path)
	}
	return secret, nil
}

func parseTargets(raw string) ([]Target, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]Target, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		eq := strings.IndexByte(p, '=')
		if eq <= 0 || eq == len(p)-1 {
			return nil, fmt.Errorf("NLM_PROBE_TARGETS[%d]: expected id=host:port, got %q", i, p)
		}
		t := Target{
			ID:      strings.TrimSpace(p[:eq]),
			Address: strings.TrimSpace(p[eq+1:]),
		}
		if t.ID == "" || t.Address == "" {
			return nil, fmt.Errorf("NLM_PROBE_TARGETS[%d]: empty id or address in %q", i, p)
		}
		if _, dup := seen[t.ID]; dup {
			return nil, fmt.Errorf("NLM_PROBE_TARGETS: duplicate target id %q", t.ID)
		}
		seen[t.ID] = struct{}{}
		out = append(out, t)
	}
	return out, nil
}

// ControllerConfig holds the controller HTTP + readiness settings.
type ControllerConfig struct {
	DBPath string
	Listen string // host:port

	// Readiness gates per spec §3.2.
	MaxClockDriftMS      float64
	MaxLitestreamLagSecs int
	WSTicketRateLimit    int
	MetricsToken         string
	ChronycBinary        string
	AdminToken           string
	WSAllowedOrigins     []string // NLM_WS_ALLOWED_ORIGINS (comma-separated)
}

// LoadController reads controller env vars. Defaults are dev-friendly; in
// production these come from /etc/nlm/controller.env.
//
// Readiness env vars per spec §3.2 / §3.3:
//
//	NLM_READY_MAX_CLOCK_DRIFT_MS    chrony drift gate (default 100)
//	NLM_LITESTREAM_MAX_LAG_SECONDS  Litestream lag gate (default 300)
//	NLM_WS_TICKET_RATE_LIMIT        per-IP ticket-failure cap (default 10)
//	NLM_METRICS_TOKEN               optional bearer for /metrics
//	NLM_CHRONYC_BINARY              chronyc executable path (default "chronyc")
func LoadController() (ControllerConfig, error) {
	c := ControllerConfig{
		DBPath:        getString("NLM_CONTROLLER_DB_PATH", "/var/lib/nlm/nlm.db"),
		Listen:        getString("NLM_CONTROLLER_LISTEN", ":8080"),
		MetricsToken:  getString("NLM_METRICS_TOKEN", ""),
		ChronycBinary: getString("NLM_CHRONYC_BINARY", "chronyc"),
		AdminToken:    getString("NLM_ADMIN_TOKEN", ""),
	}

	if raw := getString("NLM_WS_ALLOWED_ORIGINS", ""); raw != "" {
		for _, o := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(o); trimmed != "" {
				c.WSAllowedOrigins = append(c.WSAllowedOrigins, trimmed)
			}
		}
	}
	if c.DBPath == "" {
		return c, fmt.Errorf("NLM_CONTROLLER_DB_PATH must not be empty")
	}
	if c.Listen == "" {
		return c, fmt.Errorf("NLM_CONTROLLER_LISTEN must not be empty")
	}

	driftMS, err := getInt("NLM_READY_MAX_CLOCK_DRIFT_MS", 100)
	if err != nil {
		return c, err
	}
	if driftMS < 0 {
		return c, fmt.Errorf("NLM_READY_MAX_CLOCK_DRIFT_MS must be >= 0")
	}
	c.MaxClockDriftMS = float64(driftMS)

	lagSecs, err := getInt("NLM_LITESTREAM_MAX_LAG_SECONDS", 300)
	if err != nil {
		return c, err
	}
	if lagSecs <= 0 {
		return c, fmt.Errorf("NLM_LITESTREAM_MAX_LAG_SECONDS must be > 0")
	}
	c.MaxLitestreamLagSecs = lagSecs

	rl, err := getInt("NLM_WS_TICKET_RATE_LIMIT", 10)
	if err != nil {
		return c, err
	}
	if rl <= 0 {
		return c, fmt.Errorf("NLM_WS_TICKET_RATE_LIMIT must be > 0")
	}
	c.WSTicketRateLimit = rl

	return c, nil
}

// MaxLitestreamLag returns the gate as a duration.
func (c ControllerConfig) MaxLitestreamLag() time.Duration {
	return time.Duration(c.MaxLitestreamLagSecs) * time.Second
}

func getString(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getInt(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid integer %q: %w", key, v, err)
	}
	return n, nil
}
