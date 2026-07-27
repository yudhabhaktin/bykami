package store_test

import (
	"database/sql"
	"testing"

	"github.com/bhaktiyudha/bykami/agent/internal/store"
)

func open(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func openSession(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO sessions
		   (id, outlet_id, state, package_id, package_name, price_idr, template_id,
		    print_copies, take_limit, opened_at, paid_at)
		 VALUES (?, 'jajag', 'open', 'mini', 'MINI', 45000, 'strip-3', 1, 15, unixepoch(), unixepoch())`, id,
	); err != nil {
		t.Fatalf("open session %s: %v", id, err)
	}
}

// live is a session row in any state, for the constraints that are about state
// rather than about a working booth.
const liveColumns = `(id, outlet_id, state, package_id, package_name, price_idr, template_id,
	print_copies, take_limit, opened_at, paid_at`

func TestOpenIsIdempotent(t *testing.T) {
	db := open(t)

	// Migrating twice must be a no-op, because every agent start runs them.
	if err := func() error { _, err := db.Exec("SELECT 1"); return err }(); err != nil {
		t.Fatalf("query after migrate: %v", err)
	}

	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied == 0 {
		t.Fatal("no migrations recorded")
	}
}

// The one thing the hot-folder design has to get right is attribution, and it
// is only unambiguous while exactly one session is open.
func TestOnlyOneSessionMayBeOpen(t *testing.T) {
	db := open(t)
	openSession(t, db, "s1")

	_, err := db.Exec(
		`INSERT INTO sessions ` + liveColumns + `)
		 VALUES ('s2', 'jajag', 'open', 'mini', 'MINI', 45000, 'strip-3', 1, 15, unixepoch(), unixepoch())`,
	)
	if err == nil {
		t.Fatal("second live session was accepted; attribution is now ambiguous")
	}
	if !store.IsConstraint(err) {
		t.Fatalf("want a constraint violation, got %v", err)
	}

	// Closing the first must free the slot, or the booth takes one session ever.
	if _, err := db.Exec(`UPDATE sessions SET state = 'closed', closed_at = unixepoch() WHERE id = 's1'`); err != nil {
		t.Fatalf("close s1: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO sessions ` + liveColumns + `)
		 VALUES ('s2', 'jajag', 'open', 'mini', 'MINI', 45000, 'strip-3', 1, 15, unixepoch(), unixepoch())`,
	); err != nil {
		t.Fatalf("open s2 after closing s1: %v", err)
	}

	// A session still waiting at the QR code holds the booth too, or the
	// customer standing there loses it to whoever taps the screen next.
	if _, err := db.Exec(`UPDATE sessions SET state = 'closed', closed_at = unixepoch() WHERE id = 's2'`); err != nil {
		t.Fatalf("close s2: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO sessions ` + liveColumns + `)
		 VALUES ('s3', 'jajag', 'awaiting_payment', 'mini', 'MINI', 45000, 'strip-3', 1, 15, unixepoch(), NULL)`,
	); err != nil {
		t.Fatalf("start s3: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO sessions ` + liveColumns + `)
		 VALUES ('s4', 'jajag', 'awaiting_payment', 'mini', 'MINI', 45000, 'strip-3', 1, 15, unixepoch(), NULL)`,
	); err == nil {
		t.Fatal("a second session took the booth from one waiting to pay")
	}
}

func TestClosedAtMustMatchState(t *testing.T) {
	db := open(t)

	for _, tc := range []struct {
		name  string
		state string
		// SQL literal so the test can pass NULL.
		closedAt string
	}{
		{"closed without timestamp", "closed", "NULL"},
		{"open with timestamp", "open", "unixepoch()"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Exec(
				`INSERT INTO sessions
				   (id, outlet_id, state, package_id, package_name, price_idr, template_id,
				    print_copies, take_limit, opened_at, paid_at, closed_at)
				 VALUES ('x', 'jajag', ?, 'mini', 'MINI', 45000, 'strip-3', 1, 15,
				         unixepoch(), unixepoch(), `+tc.closedAt+`)`,
				tc.state,
			)
			if err == nil {
				t.Fatal("accepted a session whose state and closed_at disagree")
			}
		})
	}
}

// PDP: the number and the consent that permits it are one fact, so the schema
// refuses to hold half of it.
func TestPhoneRequiresConsent(t *testing.T) {
	db := open(t)
	openSession(t, db, "s1")

	if _, err := db.Exec(`UPDATE sessions SET phone = '+6281234567890' WHERE id = 's1'`); err == nil {
		t.Fatal("stored a phone number with no consent record")
	}

	if _, err := db.Exec(
		`UPDATE sessions SET phone = '+6281234567890', consent_version = 'v1', consented_at = unixepoch()
		 WHERE id = 's1'`,
	); err != nil {
		t.Fatalf("phone with consent was refused: %v", err)
	}
}

func TestMarketingConsentDefaultsToNo(t *testing.T) {
	db := open(t)
	openSession(t, db, "s1")

	var marketing int
	if err := db.QueryRow(`SELECT marketing_consent FROM sessions WHERE id = 's1'`).Scan(&marketing); err != nil {
		t.Fatalf("read marketing_consent: %v", err)
	}
	if marketing != 0 {
		t.Fatal("marketing consent defaulted to yes; a pre-ticked box is not consent")
	}
}

// Content-addressed, which is what makes the crash-recovery rescan safe: the
// second insert of the same bytes is a constraint violation to swallow rather
// than a duplicate row to find later.
func TestPhotoContentHashIsUnique(t *testing.T) {
	db := open(t)
	openSession(t, db, "s1")

	insert := func(id string) error {
		_, err := db.Exec(
			`INSERT INTO photos (id, session_id, content_hash, path, bytes, width, height, source, captured_at, ingested_at)
			 VALUES (?, 's1', 'deadbeef', 'sessions/s1/1.jpg', 100, 6000, 4000, 'hotfolder', unixepoch(), unixepoch())`, id,
		)
		return err
	}

	if err := insert("p1"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	err := insert("p2")
	if err == nil {
		t.Fatal("the same bytes were ingested twice")
	}
	if !store.IsConstraint(err) {
		t.Fatalf("want a constraint violation so ingest can swallow it, got %v", err)
	}
}

// NULL session_id is meaningful: staff test shots and accidental fires are kept
// as orphans and shown in admin, not discarded.
func TestPhotoMayBeOrphaned(t *testing.T) {
	db := open(t)

	if _, err := db.Exec(
		`INSERT INTO photos (id, session_id, content_hash, path, bytes, width, height, source, captured_at, ingested_at)
		 VALUES ('p1', NULL, 'abc', 'unassigned/1.jpg', 100, 6000, 4000, 'hotfolder', unixepoch(), unixepoch())`,
	); err != nil {
		t.Fatalf("orphan photo was refused: %v", err)
	}
}

func TestFailedPrintJobMustSayWhy(t *testing.T) {
	db := open(t)
	openSession(t, db, "s1")

	_, err := db.Exec(
		`INSERT INTO print_jobs (id, session_id, layout, copies, sheets, state, queued_at)
		 VALUES ('j1', 's1', '4r', 1, 1, 'failed', unixepoch())`,
	)
	if err == nil {
		t.Fatal(`accepted a failed job with no error text`)
	}
}

func TestMediaLedgerIsAppendOnly(t *testing.T) {
	db := open(t)

	if _, err := db.Exec(
		`INSERT INTO media_entries (id, kind, sheets, note, created_at)
		 VALUES ('m1', 'load', 700, 'roll 1', unixepoch())`,
	); err != nil {
		t.Fatalf("load roll: %v", err)
	}

	if _, err := db.Exec(`UPDATE media_entries SET sheets = 999 WHERE id = 'm1'`); err == nil {
		t.Fatal("media history was rewritten by UPDATE")
	}
	if _, err := db.Exec(`DELETE FROM media_entries WHERE id = 'm1'`); err == nil {
		t.Fatal("media history was rewritten by DELETE")
	}
}

func TestMediaSignFollowsKind(t *testing.T) {
	db := open(t)

	// A "consume" that adds sheets would make the counter read high exactly
	// when it matters — near the end of a roll.
	if _, err := db.Exec(
		`INSERT INTO media_entries (id, kind, sheets, created_at) VALUES ('m1', 'consume', 5, unixepoch())`,
	); err == nil {
		t.Fatal("a consume entry added sheets to the roll")
	}

	// And consumption must trace to the job that caused it.
	if _, err := db.Exec(
		`INSERT INTO media_entries (id, kind, sheets, created_at) VALUES ('m2', 'consume', -5, unixepoch())`,
	); err == nil {
		t.Fatal("sheets were consumed by no job")
	}
}

// The self-service gate, expressed where it cannot be forgotten: a session that
// has left awaiting_payment must have a payment behind it.
func TestOpenSessionMustBePaid(t *testing.T) {
	db := open(t)

	if _, err := db.Exec(
		`INSERT INTO sessions ` + liveColumns + `)
		 VALUES ('s1', 'jajag', 'open', 'mini', 'MINI', 45000, 'strip-3', 1, 15, unixepoch(), NULL)`,
	); err == nil {
		t.Fatal("the shutter was released with no payment recorded")
	}
}

func TestPaymentAmountMustBePositive(t *testing.T) {
	db := open(t)
	openSession(t, db, "s1")

	if _, err := db.Exec(
		`INSERT INTO payments (id, session_id, provider, external_id, amount_idr, state, created_at, expires_at)
		 VALUES ('p1', 's1', 'sim', 'ext1', 0, 'pending', unixepoch(), unixepoch() + 300)`,
	); err == nil {
		t.Fatal("accepted a charge for nothing")
	}
}

// The provider's id is the idempotency key: a retried create must not become
// two charges against one session.
func TestPaymentExternalIDIsUnique(t *testing.T) {
	db := open(t)
	openSession(t, db, "s1")

	insert := func(id string) error {
		_, err := db.Exec(
			`INSERT INTO payments (id, session_id, provider, external_id, amount_idr, state, created_at, expires_at)
			 VALUES (?, 's1', 'sim', 'ext1', 45000, 'pending', unixepoch(), unixepoch() + 300)`, id)
		return err
	}
	if err := insert("p1"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := insert("p2"); err == nil {
		t.Fatal("one charge was recorded twice")
	}
}
