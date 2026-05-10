// Package controller implements the NLM controller HTTP API and the
// SQLite-backed registry/results store described in spec §3.2 / §3.5.
package controller

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jscobbie73/netlatencymonitor/internal/dbmigrate"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// OpenDB opens (and migrates) the controller SQLite database. Per spec §3.2
// the controller uses WAL + synchronous=FULL to maximize durability before
// Litestream replicates to S3.
func OpenDB(path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("controller: db path required")
	}
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)",
		path,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("controller: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("controller: ping: %w", err)
	}
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("controller: migrations sub: %w", err)
	}
	if err := dbmigrate.Up(db, sub, "controller"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
