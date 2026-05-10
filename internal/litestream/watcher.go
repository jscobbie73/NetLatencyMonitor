package litestream

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// Querier runs a system command and returns its combined output.
// The exit code is reflected in err (non-zero exit → non-nil error).
type Querier interface {
	Query(ctx context.Context, args ...string) ([]byte, error)
}

// CommandQuerier executes real system commands via os/exec.
type CommandQuerier struct{}

// Query implements Querier.
func (c *CommandQuerier) Query(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec
	return cmd.Output()
}

// WatcherConfig holds Watcher parameters. All fields have sensible defaults.
type WatcherConfig struct {
	// Service is the systemd unit name to watch (default "nlm-litestream.service").
	Service string
	// PollInterval is how often to poll systemctl + journalctl (default 30s).
	PollInterval time.Duration
	// InitialLookback is how far back to scan the journal on the very first
	// poll. Defaults to PollInterval, but should be set to MaxLitestreamLag so
	// the tracker seeds LastSyncAt correctly after a controller restart even
	// when the most recent sync happened before the last PollInterval window.
	InitialLookback time.Duration
	// Querier is the command runner. Nil → CommandQuerier (real system calls).
	Querier Querier
}

// Watcher polls the Litestream systemd service and drives a Tracker by:
//  1. Checking `systemctl is-active` on each tick → SetServiceActive.
//  2. Scanning `journalctl` output since the last poll → RecordSync / RecordError.
type Watcher struct {
	tracker         *Tracker
	service         string
	pollInterval    time.Duration
	initialLookback time.Duration
	querier         Querier
	lastPoll        time.Time
}

// NewWatcher returns a Watcher that will drive tracker. Call Run to start it.
func NewWatcher(tracker *Tracker, cfg WatcherConfig) *Watcher {
	if cfg.Service == "" {
		cfg.Service = "nlm-litestream.service"
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.InitialLookback <= 0 {
		cfg.InitialLookback = cfg.PollInterval
	}
	q := cfg.Querier
	if q == nil {
		q = &CommandQuerier{}
	}
	return &Watcher{
		tracker:         tracker,
		service:         cfg.Service,
		pollInterval:    cfg.PollInterval,
		initialLookback: cfg.InitialLookback,
		querier:         q,
	}
}

// Run polls the Litestream service until ctx is cancelled. Designed to run
// as a goroutine alongside the controller:
//
//	go lswatch.Run(ctx)
func (w *Watcher) Run(ctx context.Context) {
	w.poll(ctx)
	tick := time.NewTicker(w.pollInterval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			w.poll(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// poll performs one check cycle: service active status + journal scan.
func (w *Watcher) poll(ctx context.Context) {
	// systemctl is-active --quiet exits 0 when active, non-zero otherwise.
	_, err := w.querier.Query(ctx, "systemctl", "is-active", "--quiet", w.service)
	w.tracker.SetServiceActive(err == nil)

	since := w.lastPoll
	w.lastPoll = time.Now()
	if since.IsZero() {
		// First poll: use initialLookback (typically MaxLitestreamLag) so the
		// tracker seeds LastSyncAt even if the most recent sync predates the
		// normal PollInterval window. This prevents a false /readyz 503 on
		// controller restart when Litestream has been replicating continuously.
		since = w.lastPoll.Add(-w.initialLookback)
	}

	// journalctl --since accepts "YYYY-MM-DD HH:MM:SS" in local time.
	sinceStr := since.Format("2006-01-02 15:04:05")
	out, err := w.querier.Query(ctx,
		"journalctl", "-u", w.service,
		"--since", sinceStr,
		"--no-pager", "-o", "short-iso",
	)
	if err != nil || len(out) == 0 {
		return
	}

	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		w.parseLine(sc.Text())
	}
}

// parseLine checks a single journal line for Litestream sync or error events.
// Litestream logs snapshot and WAL segment writes on successful replication;
// errors appear at ERROR level with keywords like "error" or "failed".
func (w *Watcher) parseLine(line string) {
	lower := strings.ToLower(line)

	isErr := strings.Contains(lower, "error") ||
		strings.Contains(lower, "failed") ||
		strings.Contains(lower, "fatal")

	isSync := !isErr && (strings.Contains(lower, "snapshot") ||
		strings.Contains(lower, "wal segment") ||
		strings.Contains(lower, "replicated") ||
		strings.Contains(lower, "synced"))

	switch {
	case isErr:
		w.tracker.RecordError()
	case isSync:
		w.tracker.RecordSync(time.Time{}) // uses tracker's internal clock
	}
}

// ServiceName returns the watched unit name (useful in tests and logging).
func (w *Watcher) ServiceName() string {
	return w.service
}

// FormatSince formats t as the string passed to journalctl --since.
// Exported for use in tests that need to match exact query arguments.
func FormatSince(t time.Time) string {
	return t.Format("2006-01-02 15:04:05")
}
