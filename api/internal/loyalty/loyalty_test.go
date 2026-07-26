package loyalty

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/bhaktiyudha/bykami/api/internal/store"
)

func newLedger(t *testing.T) (*Ledger, *sql.DB) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(
		`INSERT INTO users (id, phone, created_at) VALUES ('u1', '+6281234567890', unixepoch())`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return New(db), db
}

func TestBalanceIsTheSumOfEntries(t *testing.T) {
	l, _ := newLedger(t)
	ctx := context.Background()

	if _, err := l.Earn(ctx, "u1", "studio", 500, "booking-1", "idem-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Earn(ctx, "u1", "booth", 300, "booking-2", "idem-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Burn(ctx, "u1", "dimsamcong", 200, "order-1", "idem-3"); err != nil {
		t.Fatal(err)
	}

	got, err := l.Balance(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(600); got != want {
		t.Errorf("balance = %d, want %d", got, want)
	}
}

func TestEarnIsIdempotent(t *testing.T) {
	l, db := newLedger(t)
	ctx := context.Background()

	// The motivating case: a payment gateway delivers the same webhook twice.
	first, err := l.Earn(ctx, "u1", "studio", 500, "payment-abc", "webhook-abc")
	if err != nil {
		t.Fatal(err)
	}
	second, err := l.Earn(ctx, "u1", "studio", 500, "payment-abc", "webhook-abc")
	if err != nil {
		t.Fatalf("retry should succeed, got %v", err)
	}

	if first.ID != second.ID {
		t.Errorf("retry created a new entry: %s then %s", first.ID, second.ID)
	}
	if balance, _ := l.Balance(ctx, "u1"); balance != 500 {
		t.Errorf("balance = %d, want 500 — the retry credited twice", balance)
	}

	var rows int
	db.QueryRow(`SELECT COUNT(*) FROM loyalty_entries`).Scan(&rows)
	if rows != 1 {
		t.Errorf("%d entries written, want 1", rows)
	}
}

func TestConcurrentEarnWithOneKeyCreditsOnce(t *testing.T) {
	l, db := newLedger(t)
	ctx := context.Background()

	// Idempotency that only holds when calls are sequential is not idempotency.
	// A gateway retrying while the first delivery is still in flight is exactly
	// how double credits happen in production.
	const goroutines = 16
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := range goroutines {
		wg.Go(func() {
			_, errs[i] = l.Earn(ctx, "u1", "studio", 500, "payment-abc", "same-key")
		})
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d failed: %v", i, err)
		}
	}
	if balance, _ := l.Balance(ctx, "u1"); balance != 500 {
		t.Errorf("balance = %d, want 500", balance)
	}
	var rows int
	db.QueryRow(`SELECT COUNT(*) FROM loyalty_entries`).Scan(&rows)
	if rows != 1 {
		t.Errorf("%d entries written, want 1", rows)
	}
}

func TestBurnWillNotOverdraw(t *testing.T) {
	l, _ := newLedger(t)
	ctx := context.Background()

	if _, err := l.Earn(ctx, "u1", "studio", 100, "b1", "k1"); err != nil {
		t.Fatal(err)
	}
	_, err := l.Burn(ctx, "u1", "dimsamcong", 500, "order-1", "k2")
	if !errors.Is(err, ErrInsufficientPoints) {
		t.Fatalf("got %v, want ErrInsufficientPoints", err)
	}
	if balance, _ := l.Balance(ctx, "u1"); balance != 100 {
		t.Errorf("balance = %d, want 100 — a refused burn still moved points", balance)
	}
}

func TestConcurrentBurnsCannotBothSpendTheLastPoints(t *testing.T) {
	l, _ := newLedger(t)
	ctx := context.Background()

	if _, err := l.Earn(ctx, "u1", "studio", 100, "b1", "k1"); err != nil {
		t.Fatal(err)
	}

	const goroutines = 8
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := range goroutines {
		wg.Go(func() {
			_, errs[i] = l.Burn(ctx, "u1", "dimsamcong", 100, "order", "")
		})
	}
	wg.Wait()

	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrInsufficientPoints) {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Errorf("%d burns succeeded, want exactly 1", succeeded)
	}
	if balance, _ := l.Balance(ctx, "u1"); balance != 0 {
		t.Errorf("balance = %d, want 0", balance)
	}
}

func TestLedgerIsAppendOnly(t *testing.T) {
	l, db := newLedger(t)
	ctx := context.Background()

	e, err := l.Earn(ctx, "u1", "studio", 500, "b1", "k1")
	if err != nil {
		t.Fatal(err)
	}

	// The most likely author of an UPDATE here is a well-meaning fix for a
	// support ticket. The database has to be the thing that says no, because a
	// convention does not survive a hurried evening.
	if _, err := db.Exec(`UPDATE loyalty_entries SET points = 999 WHERE id = ?`, e.ID); err == nil {
		t.Error("UPDATE succeeded; the ledger is not append-only")
	}
	if _, err := db.Exec(`DELETE FROM loyalty_entries WHERE id = ?`, e.ID); err == nil {
		t.Error("DELETE succeeded; the ledger is not append-only")
	}
	if balance, _ := l.Balance(ctx, "u1"); balance != 500 {
		t.Errorf("balance = %d, want 500", balance)
	}
}

func TestMistakesAreCorrectedWithACompensatingEntry(t *testing.T) {
	l, _ := newLedger(t)
	ctx := context.Background()

	// 5000 credited where 500 was meant. The fix is another entry, and the
	// mistake stays visible — which is the property that makes a dispute
	// resolvable months later.
	if _, err := l.Earn(ctx, "u1", "studio", 5000, "b1", "k1"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Adjust(ctx, "u1", "studio", -4500, "typo in manual credit"); err != nil {
		t.Fatal(err)
	}

	if balance, _ := l.Balance(ctx, "u1"); balance != 500 {
		t.Errorf("balance = %d, want 500", balance)
	}
	history, err := l.History(ctx, "u1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("%d entries, want 2 — the original must survive the correction", len(history))
	}
}

func TestEarnRequiresAnIdempotencyKey(t *testing.T) {
	l, _ := newLedger(t)
	if _, err := l.Earn(context.Background(), "u1", "studio", 500, "b1", ""); !errors.Is(err, ErrNoIdempotencyKey) {
		t.Errorf("got %v, want ErrNoIdempotencyKey", err)
	}
}

func TestEarnAndBurnRejectNonPositiveAmounts(t *testing.T) {
	l, _ := newLedger(t)
	ctx := context.Background()
	for _, points := range []int64{0, -100} {
		if _, err := l.Earn(ctx, "u1", "studio", points, "b", "k"); !errors.Is(err, ErrNonPositive) {
			t.Errorf("Earn(%d): got %v, want ErrNonPositive", points, err)
		}
		if _, err := l.Burn(ctx, "u1", "studio", points, "b", "k"); !errors.Is(err, ErrNonPositive) {
			t.Errorf("Burn(%d): got %v, want ErrNonPositive", points, err)
		}
	}
}

func TestSchemaRejectsAnEarnThatSubtracts(t *testing.T) {
	_, db := newLedger(t)

	// Bypasses the package entirely. The vocabulary of the ledger has to hold
	// even against a direct write, or "earn" stops meaning anything to whoever
	// reads the table in two years.
	_, err := db.Exec(
		`INSERT INTO loyalty_entries (id, user_id, vertical, kind, points, idempotency_key, created_at)
		 VALUES ('x', 'u1', 'studio', 'earn', -500, 'k', unixepoch())`)
	if err == nil {
		t.Error("schema accepted an earn with negative points")
	}
}

func TestSchemaRejectsAnEarnWithoutAnIdempotencyKey(t *testing.T) {
	_, db := newLedger(t)
	_, err := db.Exec(
		`INSERT INTO loyalty_entries (id, user_id, vertical, kind, points, created_at)
		 VALUES ('x', 'u1', 'studio', 'earn', 500, unixepoch())`)
	if err == nil {
		t.Error("schema accepted an earn with no idempotency key")
	}
}

func TestPointsCrossVerticals(t *testing.T) {
	l, _ := newLedger(t)
	ctx := context.Background()

	// Earn on a photo session, spend on dimsum. This is the point of one
	// platform rather than three businesses that share a logo.
	if _, err := l.Earn(ctx, "u1", "studio", 500, "session-1", "k1"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Burn(ctx, "u1", "dimsamcong", 300, "order-1", "k2"); err != nil {
		t.Fatal(err)
	}
	if balance, _ := l.Balance(ctx, "u1"); balance != 200 {
		t.Errorf("balance = %d, want 200", balance)
	}
}

func TestCustomerWithHistoryCannotBeDeleted(t *testing.T) {
	l, db := newLedger(t)
	if _, err := l.Earn(context.Background(), "u1", "studio", 500, "b1", "k1"); err != nil {
		t.Fatal(err)
	}
	// Erasure is anonymising the user row, not deleting the entries that
	// reference it — otherwise the ledger stops adding up.
	if _, err := db.Exec(`DELETE FROM users WHERE id = 'u1'`); err == nil {
		t.Error("deleted a user who still has loyalty history")
	}
}
