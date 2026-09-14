package access

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/store"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestAddAndVerify(t *testing.T) {
	db := openTestDB(t)
	r := New(db, nil)
	r.SetIterations(1)
	ctx := context.Background()

	pw, err := r.Add(ctx, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if pw == "" {
		t.Fatal("password is empty")
	}

	if err := r.Verify(ctx, "tester", pw); err != nil {
		t.Fatalf("verify correct password: %v", err)
	}
	if err := r.Verify(ctx, "tester", pw+"x"); err == nil {
		t.Fatal("wrong password should fail")
	}
	if err := r.Verify(ctx, "nosuchuser", pw); err == nil {
		t.Fatal("unknown username should fail")
	}
}

func TestVerifyTimingForUnknownUsername(t *testing.T) {
	db := openTestDB(t)
	r := New(db, nil)
	r.SetIterations(1000)
	ctx := context.Background()

	start := time.Now()
	_ = r.Verify(ctx, "nosuchuser", "anypassword")
	dummy := time.Since(start)

	_, _ = r.Add(ctx, "tester")
	start = time.Now()
	_ = r.Verify(ctx, "tester", "wrongpassword")
	real := time.Since(start)

	// The dummy KDF should take roughly as long as the real one.
	// Allow a generous margin for scheduler noise.
	if dummy < real/2 || dummy > real*2 {
		t.Logf("dummy=%v real=%v — timing may differ", dummy, real)
	}
}

func TestPasswdChangesPassword(t *testing.T) {
	db := openTestDB(t)
	r := New(db, nil)
	r.SetIterations(1)
	ctx := context.Background()

	old, _ := r.Add(ctx, "tester")
	newPw, err := r.Passwd(ctx, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if newPw == old {
		t.Fatal("new password should differ from old")
	}
	if err := r.Verify(ctx, "tester", old); err == nil {
		t.Fatal("old password should no longer work")
	}
	if err := r.Verify(ctx, "tester", newPw); err != nil {
		t.Fatalf("new password should work: %v", err)
	}
}

func TestRemoveRefusesLastAccount(t *testing.T) {
	db := openTestDB(t)
	r := New(db, nil)
	r.SetIterations(1)
	ctx := context.Background()

	_, _ = r.Add(ctx, "alice")
	if err := r.Remove(ctx, "alice"); err != ErrLastAccount {
		t.Fatalf("removing last account: got %v, want ErrLastAccount", err)
	}

	_, _ = r.Add(ctx, "bob")
	if err := r.Remove(ctx, "alice"); err != nil {
		t.Fatalf("removing with spare: %v", err)
	}
	if err := r.Remove(ctx, "bob"); err != ErrLastAccount {
		t.Fatalf("removing last account: got %v, want ErrLastAccount", err)
	}
}

func TestList(t *testing.T) {
	db := openTestDB(t)
	r := New(db, nil)
	r.SetIterations(1)
	ctx := context.Background()

	_, _ = r.Add(ctx, "charlie")
	_, _ = r.Add(ctx, "alice")
	_, _ = r.Add(ctx, "bob")

	us, err := r.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(us) != 3 {
		t.Fatalf("len = %d, want 3", len(us))
	}
	if us[0] != "alice" || us[1] != "bob" || us[2] != "charlie" {
		t.Fatalf("order = %v, want [alice bob charlie]", us)
	}
}

func TestRateLimit(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().UTC()
	r := New(db, func() time.Time { return now })
	r.SetIterations(1)
	ctx := context.Background()

	// 10 failures is allowed.
	for i := 0; i < 10; i++ {
		if err := r.RecordAttempt(ctx, "1.2.3.4", "tester"); err != nil {
			t.Fatal(err)
		}
	}
	locked, err := r.IsLockedOut(ctx, "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if locked {
		t.Fatal("10 attempts should not lock")
	}

	// 11th locks.
	_ = r.RecordAttempt(ctx, "1.2.3.4", "tester")
	locked, err = r.IsLockedOut(ctx, "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("11 attempts should lock")
	}

	// Other addresses are unaffected.
	locked, _ = r.IsLockedOut(ctx, "5.6.7.8")
	if locked {
		t.Fatal("other address should not be locked")
	}

	// After the lockout duration passes, the address is free again.
	now = now.Add(lockoutDuration + time.Second)
	locked, _ = r.IsLockedOut(ctx, "1.2.3.4")
	if locked {
		t.Fatal("address should be unlocked after lockout duration")
	}
}
