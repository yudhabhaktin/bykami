// Package store opens the SQLite database and applies migrations.
//
// SQLite rather than Postgres because the target is a 1 vCPU / 1 GB box, where
// a separate database process and the application would compete for the same
// gigabyte. In-process costs nothing and removes a network hop.
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	// Pure-Go SQLite. Chosen over the cgo-based driver so that
	// `GOOS=linux GOARCH=amd64 go build` produces a static binary from any
	// machine — the infrastructure record requires building in CI and shipping
	// the binary, never installing a toolchain on the production host.
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open returns a database with pragmas set and every migration applied.
//
// dsn is a file path, or ":memory:" for tests.
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}

	// SQLite permits one writer at a time. An in-memory database is also torn
	// down when its last connection closes, so a pool would hand out empty
	// databases at random. One connection makes writes serialise in the pool
	// instead of racing to SQLITE_BUSY, which on a single core costs nothing.
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{
		// Readers do not block the writer and the writer does not block
		// readers. Without this a single slow write stalls every request.
		"PRAGMA journal_mode = WAL",
		// Durable across application crashes, which is the failure that
		// actually happens. Surviving a power cut without an fsync per commit
		// is not worth the write cost here.
		"PRAGMA synchronous = NORMAL",
		// Off by default in SQLite, and every foreign key in the schema is
		// load-bearing — including the one that stops a customer with loyalty
		// history being deleted out from under their ledger.
		"PRAGMA foreign_keys = ON",
		// Wait rather than fail if a write is briefly in flight.
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("store: %s: %w", pragma, err)
		}
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// migrate applies every embedded migration exactly once, in filename order.
//
// Each runs inside a transaction together with the row recording it, so a
// migration that fails halfway leaves no trace and can simply be re-run — the
// alternative is a database that is neither at the old version nor the new one,
// which on a single production box means a manual repair under pressure.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			name       TEXT    PRIMARY KEY,
			applied_at INTEGER NOT NULL
		) STRICT`,
	); err != nil {
		return fmt.Errorf("store: migrations table: %w", err)
	}

	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("store: list migrations: %w", err)
	}
	sort.Strings(names)

	for _, name := range names {
		var applied int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name,
		).Scan(&applied); err != nil {
			return fmt.Errorf("store: check %s: %w", name, err)
		}
		if applied > 0 {
			continue
		}

		body, err := migrations.ReadFile(name)
		if err != nil {
			return fmt.Errorf("store: read %s: %w", name, err)
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("store: begin %s: %w", name, err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: apply %s: %w", name, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations (name, applied_at) VALUES (?, unixepoch())`, name,
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit %s: %w", name, err)
		}
	}
	return nil
}

// IsConstraint reports whether err is SQLite rejecting a write because a
// constraint said no.
//
// Callers use this to tell "this violates a rule we deliberately encoded" from
// "the database is broken". The string match is unfortunate but the driver does
// not export its error codes; it is contained here so exactly one place has to
// change if that improves.
func IsConstraint(err error) bool {
	return err != nil && strings.Contains(err.Error(), "constraint")
}
