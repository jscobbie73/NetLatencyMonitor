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

// ControllerConfig holds the controller HTTP settings.
type ControllerConfig struct {
	DBPath  string
	Listen  string // host:port
	BaseURL string // optional, used by future ticket flow / UI links
}

// LoadController reads controller env vars. Defaults are dev-friendly; in
// production these come from /etc/nlm/controller.env.
func LoadController() (ControllerConfig, error) {
	c := ControllerConfig{
		DBPath:  getString("NLM_CONTROLLER_DB_PATH", "/var/lib/nlm/nlm.db"),
		Listen:  getString("NLM_CONTROLLER_LISTEN", ":8080"),
		BaseURL: getString("NLM_CONTROLLER_BASE_URL", ""),
	}
	if c.DBPath == "" {
		return c, fmt.Errorf("NLM_CONTROLLER_DB_PATH must not be empty")
	}
	if c.Listen == "" {
		return c, fmt.Errorf("NLM_CONTROLLER_LISTEN must not be empty")
	}
	return c, nil
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
