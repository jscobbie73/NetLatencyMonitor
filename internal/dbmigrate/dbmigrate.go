// Package dbmigrate runs golang-migrate migrations from an embedded FS against
// an already-open *sql.DB. Used by both the agent spool and the controller DB.
package dbmigrate

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// Up applies every pending migration in fsys (which must contain files like
// 0001_init.up.sql / 0001_init.down.sql) to db. dbName is a label used by
// golang-migrate's bookkeeping table; pass a stable name per logical database.
func Up(db *sql.DB, fsys fs.FS, dbName string) error {
	src, err := iofs.New(fsys, ".")
	if err != nil {
		return fmt.Errorf("dbmigrate: open source: %w", err)
	}
	driver, err := sqlite.WithInstance(db, &sqlite.Config{})
	if err != nil {
		return fmt.Errorf("dbmigrate: driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, dbName, driver)
	if err != nil {
		return fmt.Errorf("dbmigrate: new: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("dbmigrate: up: %w", err)
	}
	return nil
}
