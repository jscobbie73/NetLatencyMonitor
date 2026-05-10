// Package spool implements the agent-side write-ahead spool described in
// NLM spec §3.1.
//
// One row = one probe cycle = one HTTP POST. The drain step honors an
// exponential backoff per row, retention bounds count the SQLite footprint
// inclusive of WAL/SHM, and a persisted drops counter survives restarts.
package spool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/jscobbie73/netlatencymonitor/internal/dbmigrate"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// SendOutcome is the agent's interpretation of a controller response for
// one spool row. The drain loop translates HTTP status codes into one of
// these per spec §3.1 / §3.4.
type SendOutcome int

const (
	// OutcomeAccepted means the row is durably persisted on the controller
	// (201 or 409). Caller MUST delete the spool row.
	OutcomeAccepted SendOutcome = iota
	// OutcomePermanentFail means the payload is malformed (e.g. 422). Bump
	// attempts and log; do NOT delete. Operator intervention needed.
	OutcomePermanentFail
	// OutcomeTransientFail means 5xx, network error, or timeout. Bump
	// attempts; the row will be retried after backoff.
	OutcomeTransientFail
)

// Sender ships one spooled payload to the controller and returns the outcome.
// Implementations live outside this package (in the agent's HTTP client); the
// spool only knows about the contract.
type Sender interface {
	Send(ctx context.Context, probeRunID string, payload []byte) (SendOutcome, error)
}

// Config is the runtime tuning for Drain. These come from internal/config.
type Config struct {
	DrainRows   int
	MaxAge      time.Duration
	MaxSize     int64 // bytes; sums db + wal + shm
	BackoffBase time.Duration
	BackoffMax  time.Duration
}

// Spool is the persistent FIFO of pending probe results.
type Spool struct {
	db   *sql.DB
	path string
	cfg  Config
	now  func() time.Time
}

// Open opens (creating if needed) the spool DB at path, applies migrations,
// and sets the SQLite pragmas required by spec §3.1 (WAL + synchronous=NORMAL).
func Open(path string, cfg Config) (*Spool, error) {
	if path == "" {
		return nil, errors.New("spool: path required")
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("spool: open db: %w", err)
	}
	// modernc.org/sqlite multiplexes a single file; one writer at a time.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("spool: ping: %w", err)
	}
	if err := runMigrations(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Spool{db: db, path: path, cfg: cfg, now: time.Now}, nil
}

// Close closes the underlying DB.
func (s *Spool) Close() error { return s.db.Close() }

func runMigrations(db *sql.DB) error {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("spool: migrations sub: %w", err)
	}
	return dbmigrate.Up(db, sub, "spool")
}

// ErrDuplicate is returned by Enqueue when probe_run_id already exists.
// Callers should treat this as a soft success per spec §3.1 Case C.
var ErrDuplicate = errors.New("spool: duplicate probe_run_id")

// Enqueue inserts one row. payload is the JSON body that will be POSTed
// to /api/v1/results. Returns ErrDuplicate if probeRunID is already spooled.
func (s *Spool) Enqueue(ctx context.Context, probeRunID string, payload []byte) error {
	if probeRunID == "" {
		return errors.New("spool: probe_run_id required")
	}
	if len(payload) == 0 {
		return errors.New("spool: payload required")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO spool(probe_run_id, payload) VALUES (?, ?)`,
		probeRunID, string(payload))
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		return fmt.Errorf("spool: enqueue: %w", err)
	}
	return nil
}

// Depth returns the number of rows currently in the spool.
func (s *Spool) Depth(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM spool`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("spool: depth: %w", err)
	}
	return n, nil
}

// OldestAge returns the age of the oldest spooled row, or 0 if empty.
func (s *Spool) OldestAge(ctx context.Context) (time.Duration, error) {
	var ts sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT MIN(enqueued_at) FROM spool`).Scan(&ts)
	if err != nil {
		return 0, fmt.Errorf("spool: oldest age: %w", err)
	}
	if !ts.Valid {
		return 0, nil
	}
	t, err := parseSQLiteTime(ts.String)
	if err != nil {
		return 0, fmt.Errorf("spool: parse oldest: %w", err)
	}
	return s.now().Sub(t), nil
}

// DropsTotal returns the persisted count of rows dropped by retention.
func (s *Spool) DropsTotal(ctx context.Context) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM spool_meta WHERE key = 'drops_total'`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("spool: drops_total: %w", err)
	}
	return v, nil
}

// FootprintBytes returns the on-disk size of the spool DB inclusive of WAL
// and SHM files. Used to enforce NLM_SPOOL_MAX_SIZE_MB per spec §3.1.
func (s *Spool) FootprintBytes() (int64, error) {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		fi, err := os.Stat(s.path + suffix)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return 0, fmt.Errorf("spool: stat %s: %w", s.path+suffix, err)
		}
		total += fi.Size()
	}
	return total, nil
}

// DrainStats reports what one Drain call did.
type DrainStats struct {
	Considered int // rows fetched from DB
	Skipped    int // rows skipped due to backoff window
	Accepted   int // rows deleted because controller accepted (201/409)
	Permanent  int // rows that returned a permanent failure (e.g. 422)
	Transient  int // rows that returned a transient failure (5xx/network)
	Dropped    int // rows deleted by retention enforcement this cycle
}

// Drain runs one drain pass per spec §3.1. It selects up to DrainRows rows
// ordered by enqueued_at, skips any whose backoff window has not elapsed,
// sends the rest via send, applies outcomes, then enforces retention.
func (s *Spool) Drain(ctx context.Context, send Sender) (DrainStats, error) {
	var stats DrainStats

	type candidate struct {
		id            int64
		probeRunID    string
		payload       string
		attempts      int
		lastAttemptAt sql.NullString
	}
	var batch []candidate

	if err := func() error {
		rows, err := s.db.QueryContext(ctx,
			`SELECT id, probe_run_id, payload, attempts, last_attempt_at
			   FROM spool
			   ORDER BY enqueued_at ASC
			   LIMIT ?`, s.cfg.DrainRows)
		if err != nil {
			return fmt.Errorf("spool: drain select: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.probeRunID, &c.payload, &c.attempts, &c.lastAttemptAt); err != nil {
				return fmt.Errorf("spool: drain scan: %w", err)
			}
			batch = append(batch, c)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("spool: drain rows: %w", err)
		}
		return nil
	}(); err != nil {
		return stats, err
	}
	stats.Considered = len(batch)

	now := s.now()
	for _, c := range batch {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		if c.lastAttemptAt.Valid {
			last, err := parseSQLiteTime(c.lastAttemptAt.String)
			if err != nil {
				return stats, fmt.Errorf("spool: parse last_attempt_at: %w", err)
			}
			window := Backoff(c.attempts, s.cfg.BackoffBase, s.cfg.BackoffMax)
			if last.Add(window).After(now) {
				stats.Skipped++
				continue
			}
		}

		outcome, sendErr := send.Send(ctx, c.probeRunID, []byte(c.payload))
		switch outcome {
		case OutcomeAccepted:
			if _, err := s.db.ExecContext(ctx, `DELETE FROM spool WHERE id = ?`, c.id); err != nil {
				return stats, fmt.Errorf("spool: drain delete: %w", err)
			}
			stats.Accepted++
		case OutcomePermanentFail:
			if err := s.markFailure(ctx, c.id, sendErr); err != nil {
				return stats, err
			}
			stats.Permanent++
		case OutcomeTransientFail:
			if err := s.markFailure(ctx, c.id, sendErr); err != nil {
				return stats, err
			}
			stats.Transient++
		default:
			return stats, fmt.Errorf("spool: unknown outcome %d", outcome)
		}
	}

	dropped, err := s.enforceRetention(ctx)
	if err != nil {
		return stats, err
	}
	stats.Dropped = dropped
	return stats, nil
}

func (s *Spool) markFailure(ctx context.Context, id int64, sendErr error) error {
	msg := ""
	if sendErr != nil {
		msg = sendErr.Error()
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE spool
		   SET attempts = attempts + 1,
		       last_attempt_at = CURRENT_TIMESTAMP,
		       last_error = ?
		 WHERE id = ?`, msg, id)
	if err != nil {
		return fmt.Errorf("spool: mark failure: %w", err)
	}
	return nil
}

// enforceRetention deletes oldest rows until BOTH age and size bounds are
// satisfied (spec §3.1) and increments drops_total once per deleted row.
// Returns the number of rows dropped this cycle.
func (s *Spool) enforceRetention(ctx context.Context) (int, error) {
	dropped := 0
	for {
		// Re-evaluate bounds each iteration: footprint shrinks as we delete,
		// and we must stop as soon as both are satisfied.
		size, err := s.FootprintBytes()
		if err != nil {
			return dropped, err
		}
		age, err := s.OldestAge(ctx)
		if err != nil {
			return dropped, err
		}
		var depth int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM spool`).Scan(&depth); err != nil {
			return dropped, fmt.Errorf("spool: retention depth: %w", err)
		}
		if depth == 0 {
			return dropped, nil
		}
		if size <= s.cfg.MaxSize && age <= s.cfg.MaxAge {
			return dropped, nil
		}

		// Delete the oldest row. Iterating one at a time keeps the inclusive
		// stop condition exact and the drops counter accurate.
		res, err := s.db.ExecContext(ctx,
			`DELETE FROM spool
			  WHERE id = (SELECT id FROM spool ORDER BY enqueued_at ASC LIMIT 1)`)
		if err != nil {
			return dropped, fmt.Errorf("spool: retention delete: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return dropped, fmt.Errorf("spool: retention rows affected: %w", err)
		}
		if n == 0 {
			return dropped, nil
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE spool_meta SET value = value + 1 WHERE key = 'drops_total'`); err != nil {
			return dropped, fmt.Errorf("spool: retention bump drops: %w", err)
		}
		dropped++
	}
}

// parseSQLiteTime parses the format SQLite emits for CURRENT_TIMESTAMP /
// DEFAULT CURRENT_TIMESTAMP, which is "2006-01-02 15:04:05" in UTC.
func parseSQLiteTime(s string) (time.Time, error) {
	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02 15:04:05.999999999",
		time.RFC3339Nano,
		time.RFC3339,
	}
	var lastErr error
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
			return t, nil
		} else {
			lastErr = err
		}
	}
	return time.Time{}, lastErr
}

func isUniqueViolation(err error) bool {
	// modernc.org/sqlite returns errors whose Error() text contains
	// "UNIQUE constraint failed". Match on that to avoid depending on
	// driver-internal error types.
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}
