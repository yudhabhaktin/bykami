package store

import (
	"io/fs"
	"path/filepath"
	"testing"
)

func TestMigrationsAreIdempotent(t *testing.T) {
	// Every deploy runs migrations on a database that has already had them. If
	// the second run is not a no-op, the first restart after a deploy fails.
	path := filepath.Join(t.TempDir(), "bykami.db")

	// Counted from the embedded files rather than written down, so that adding
	// a migration does not require editing this test — which is an edit that
	// would be made without thinking and would hide a real regression.
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	want := len(files)
	if want == 0 {
		t.Fatal("no migrations are embedded")
	}

	for i := range 3 {
		db, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		var applied int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
			t.Fatalf("count migrations: %v", err)
		}
		if applied != want {
			t.Errorf("open %d: %d migrations recorded, want %d", i, applied, want)
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

// The photobox split only fires on a database that still has the old single
// resource, so a freshly opened one runs it as a no-op and proves nothing. The
// old shape is rebuilt here by hand and the migration applied to it directly,
// which is the path a real deployment actually takes.
func TestPhotoboxSplitCarriesEverythingWithIt(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// One resource holding all three booths, a live booking on Vintage, the slot
	// that booking occupies, a closure, and a cached calendar answer.
	for _, stmt := range []string{
		`INSERT INTO booking_resources (id, name, google_calendar_id, created_at)
		 VALUES ('photobox', 'Photobox', 'booth@group.calendar.google.com', 1)`,
		`INSERT INTO booking_services
		   (id, resource_id, name, service_line, price_idr, duration_minutes, created_at)
		 VALUES ('photobox-y2k', 'photobox', 'Y2K', 'photobox', 30000, 10, 1),
		        ('photobox-vintage', 'photobox', 'Vintage', 'photobox', 25000, 10, 1),
		        ('photobox-maroon', 'photobox', 'Maroon', 'photobox', 25000, 10, 1)`,
		`INSERT INTO bookings
		   (id, resource_id, service_id, starts_at, ends_at, headcount, name, phone, created_at)
		 VALUES ('bk1', 'photobox', 'photobox-vintage', 100, 700, 2, 'Sari', '+628110000', 1)`,
		`INSERT INTO booking_slots (resource_id, starts_at, booking_id)
		 VALUES ('photobox', 100, 'bk1')`,
		`INSERT INTO booking_blackouts (id, resource_id, starts_at, ends_at, reason, created_at)
		 VALUES ('bo1', 'photobox', 200, 300, 'Idulfitri', 1)`,
		`INSERT INTO booking_calendar_busy (resource_id, starts_at, ends_at)
		 VALUES ('photobox', 400, 500)`,
		`INSERT INTO booking_calendar_sync (resource_id, fetched_at, window_from, window_to, error)
		 VALUES ('photobox', 9, 0, 0, '')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("set up the old shape: %v", err)
		}
	}

	body, err := migrations.ReadFile("migrations/0006_photobox_per_booth.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatalf("apply the split: %v", err)
	}

	// The pool is gone and three booths stand in its place.
	var left int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM booking_resources WHERE id = 'photobox'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Error("the shared photobox resource survived the split")
	}

	// Only Y2K keeps the calendar: giving it to all three would rebuild the
	// shared pool through the busy ranges.
	for _, want := range []struct{ id, name, calendar string }{
		{"photobox-y2k", "Photobox Y2K", "booth@group.calendar.google.com"},
		{"photobox-vintage", "Photobox Vintage", ""},
		{"photobox-maroon", "Photobox Maroon", ""},
	} {
		var name, calendar string
		if err := db.QueryRow(
			`SELECT name, google_calendar_id FROM booking_resources WHERE id = ?`,
			want.id).Scan(&name, &calendar); err != nil {
			t.Errorf("%s: %v", want.id, err)
			continue
		}
		if name != want.name || calendar != want.calendar {
			t.Errorf("%s = (%q, %q), want (%q, %q)",
				want.id, name, calendar, want.name, want.calendar)
		}
	}

	// Each service moved to the booth of the same name.
	for _, id := range []string{"photobox-y2k", "photobox-vintage", "photobox-maroon"} {
		var resource string
		if err := db.QueryRow(
			`SELECT resource_id FROM booking_services WHERE id = ?`, id).Scan(&resource); err != nil {
			t.Fatal(err)
		}
		if resource != id {
			t.Errorf("service %s sits on resource %q, want %q", id, resource, id)
		}
	}

	// The booking and the slot holding it followed the service, or the booth
	// would be free to sell a time it has already sold.
	var bookingResource, slotResource string
	if err := db.QueryRow(
		`SELECT resource_id FROM bookings WHERE id = 'bk1'`).Scan(&bookingResource); err != nil {
		t.Fatal(err)
	}
	if bookingResource != "photobox-vintage" {
		t.Errorf("booking moved to %q, want photobox-vintage", bookingResource)
	}
	if err := db.QueryRow(
		`SELECT resource_id FROM booking_slots WHERE booking_id = 'bk1'`).Scan(&slotResource); err != nil {
		t.Fatal(err)
	}
	if slotResource != "photobox-vintage" {
		t.Errorf("slot moved to %q, want photobox-vintage", slotResource)
	}

	// A closure that shut the photobox shuts all three booths.
	rows, err := db.Query(
		`SELECT resource_id FROM booking_blackouts ORDER BY resource_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var closed []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		closed = append(closed, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"photobox-maroon", "photobox-vintage", "photobox-y2k"}
	if len(closed) != len(want) {
		t.Fatalf("closures on %v, want one per booth: %v", closed, want)
	}
	for i := range want {
		if closed[i] != want[i] {
			t.Errorf("closures on %v, want %v", closed, want)
			break
		}
	}

	// The cached answer belonged to a calendar mapping that no longer exists.
	var cached int
	if err := db.QueryRow(
		`SELECT (SELECT COUNT(*) FROM booking_calendar_busy)
		      + (SELECT COUNT(*) FROM booking_calendar_sync)`).Scan(&cached); err != nil {
		t.Fatal(err)
	}
	if cached != 0 {
		t.Errorf("%d stale calendar rows survived, want 0", cached)
	}

	// Nothing was left pointing at a resource that no longer exists.
	violations, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer violations.Close()
	if violations.Next() {
		t.Error("the split left a dangling reference")
	}
}

// Migration 0012 added can_manage with DEFAULT 0, so a credential enrolled
// from the shell was not a manager. 0013 repairs that by promoting the
// earliest-created non-disabled credential when no manager exists.
func TestAdminManagerRepairPromotesEarliestCredential(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Seed one credential in the post-0012 shape with can_manage = 0.
	if _, err := db.Exec(
		`INSERT INTO admin_credentials (id, label, lookup, hash, salt, created_at, can_manage)
		 VALUES ('cred-1', 'yudha', X'00', X'00', X'00', 1, 0)`); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	body, err := migrations.ReadFile("migrations/0013_admin_manager.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatalf("apply 0013: %v", err)
	}

	var canManage int
	if err := db.QueryRow(`SELECT can_manage FROM admin_credentials WHERE label = 'yudha'`).Scan(&canManage); err != nil {
		t.Fatal(err)
	}
	if canManage != 1 {
		t.Errorf("can_manage = %d, want 1", canManage)
	}

	// Running it again must be a no-op.
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatalf("re-apply 0013: %v", err)
	}
	if err := db.QueryRow(`SELECT can_manage FROM admin_credentials WHERE label = 'yudha'`).Scan(&canManage); err != nil {
		t.Fatal(err)
	}
	if canManage != 1 {
		t.Errorf("can_manage after re-run = %d, want 1", canManage)
	}
}

// 0013 must be a no-op when the table already has a manager.
func TestAdminManagerRepairIsNoOpWhenManagerExists(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(
		`INSERT INTO admin_credentials (id, label, lookup, hash, salt, created_at, can_manage)
		 VALUES ('mgr-1', 'boss',  X'01', X'00', X'00', 1, 1),
		        ('k-1',   'kasir', X'02', X'00', X'00', 2, 0)`); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}

	body, err := migrations.ReadFile("migrations/0013_admin_manager.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatalf("apply 0013: %v", err)
	}

	var kasirManage int
	if err := db.QueryRow(`SELECT can_manage FROM admin_credentials WHERE label = 'kasir'`).Scan(&kasirManage); err != nil {
		t.Fatal(err)
	}
	if kasirManage != 0 {
		t.Errorf("kasir can_manage = %d, want 0", kasirManage)
	}
}

// 0013 must be a no-op on an empty table.
func TestAdminManagerRepairIsNoOpOnEmptyTable(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	body, err := migrations.ReadFile("migrations/0013_admin_manager.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatalf("apply 0013 on empty table: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM admin_credentials`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
}
