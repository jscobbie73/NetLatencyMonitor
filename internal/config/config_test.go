package config

import (
	"testing"
	"time"
)

func TestLoadSpoolDefaults(t *testing.T) {
	t.Setenv("NLM_SPOOL_PATH", "")
	t.Setenv("NLM_SPOOL_DRAIN_ROWS", "")
	t.Setenv("NLM_SPOOL_MAX_AGE_HOURS", "")
	t.Setenv("NLM_SPOOL_MAX_SIZE_MB", "")
	t.Setenv("NLM_SPOOL_BACKOFF_BASE_SECONDS", "")
	t.Setenv("NLM_SPOOL_BACKOFF_MAX_SECONDS", "")

	c, err := LoadSpool()
	if err != nil {
		t.Fatalf("LoadSpool: %v", err)
	}
	if c.Path != "/var/lib/nlm/spool.db" {
		t.Errorf("Path default = %q", c.Path)
	}
	if c.DrainRows != 100 {
		t.Errorf("DrainRows default = %d", c.DrainRows)
	}
	if c.MaxAge() != 24*time.Hour {
		t.Errorf("MaxAge default = %v", c.MaxAge())
	}
	if c.MaxSizeBytes() != 100*1024*1024 {
		t.Errorf("MaxSizeBytes default = %d", c.MaxSizeBytes())
	}
	if c.BackoffBase() != 30*time.Second {
		t.Errorf("BackoffBase default = %v", c.BackoffBase())
	}
	if c.BackoffMax() != time.Hour {
		t.Errorf("BackoffMax default = %v", c.BackoffMax())
	}
}

func TestLoadSpoolValidation(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"drain_rows_zero", map[string]string{"NLM_SPOOL_DRAIN_ROWS": "0"}},
		{"max_age_zero", map[string]string{"NLM_SPOOL_MAX_AGE_HOURS": "0"}},
		{"max_size_zero", map[string]string{"NLM_SPOOL_MAX_SIZE_MB": "0"}},
		{"backoff_base_zero", map[string]string{"NLM_SPOOL_BACKOFF_BASE_SECONDS": "0"}},
		{"backoff_max_lt_base", map[string]string{
			"NLM_SPOOL_BACKOFF_BASE_SECONDS": "60",
			"NLM_SPOOL_BACKOFF_MAX_SECONDS":  "30",
		}},
		{"drain_rows_garbage", map[string]string{"NLM_SPOOL_DRAIN_ROWS": "abc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := LoadSpool(); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}
