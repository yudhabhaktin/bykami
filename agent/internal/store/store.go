// Package store opens the booth's SQLite database and applies migrations.
//
// Deliberately the same shape as api/internal/store rather than shared code.
// The two databases hold different things and will diverge — this one holds
// photos, print jobs and media, and none of that belongs in the cloud schema —
// so a shared package would become a place where a booth migration could break
// the API. What is worth copying is the pattern, not the file.
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	// Pure-Go SQLite, and here the reason is stronger than it is in the cloud:
	// the booth binary is cross-compiled from macOS with GOOS=windows, which cgo
	// would make impossible without a Windows toolchain.
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

	// One connection. SQLite permits a single writer, and an in-memory database
	// is torn down when its last connection closes — so a pool would hand out
	// empty databases at random in tests.
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		// Every foreign key here is load-bearing, including the one that stops a
		// session being deleted out from under the photos attributed to it.
		"PRAGMA foreign_keys = ON",
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
// migration that fails halfway leaves no trace and can be re-run. On a booth PC
// in a shop that matters more than it does on a server: the person who would
// otherwise repair it by hand is the owner, mid-session.
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
// The ingest path depends on this: re-ingesting a file the database already
// has must be a no-op to swallow, not an error to surface. The string match is
// unfortunate but the driver does not export its codes; it is contained here so
// one place changes if that improves.
func IsConstraint(err error) bool {
	return err != nil && strings.Contains(err.Error(), "constraint")
}
