package store

import (
	"path/filepath"
	"testing"
)

func TestMigrationsAreIdempotent(t *testing.T) {
	// Every deploy runs migrations on a database that has already had them. If
	// the second run is not a no-op, the first restart after a deploy fails.
	path := filepath.Join(t.TempDir(), "bykami.db")

	for i := range 3 {
		db, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		var applied int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
			t.Fatalf("count migrations: %v", err)
		}
		if applied != 1 {
			t.Errorf("open %d: %d migrations recorded, want 1", i, applied)
		}
		db.Close()
	}
}

func TestPragmasAreSet(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "bykami.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var journal string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want wal — a slow write will stall reads", journal)
	}

	// Foreign keys are off by default in SQLite, and the schema leans on them:
	// this is what stops a customer with loyalty history being deleted out from
	// under their own ledger.
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Error("foreign_keys is off")
	}
}

func TestIsConstraintDistinguishesRuleViolationsFromRealFailures(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`INSERT INTO users (id, phone, created_at) VALUES ('a', '+62811', 1)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO users (id, phone, created_at) VALUES ('b', '+62811', 1)`)
	if !IsConstraint(err) {
		t.Errorf("duplicate phone not reported as a constraint violation: %v", err)
	}

	_, err = db.Exec(`SELECT * FROM a_table_that_does_not_exist`)
	if IsConstraint(err) {
		t.Errorf("a missing table was reported as a constraint violation: %v", err)
	}
}
