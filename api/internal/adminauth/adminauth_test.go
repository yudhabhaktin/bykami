package adminauth

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/store"
)

func TestAddAndVerifyRoundTrip(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := New(db, nil)
	ctx := context.Background()

	pw, err := r.Add(ctx, "kasir-1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(pw) != passwordLength {
		t.Errorf("password length = %d, want %d", len(pw), passwordLength)
	}

	cred, err := r.Verify(ctx, pw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if cred.Label != "kasir-1" {
		t.Errorf("label = %q, want %q", cred.Label, "kasir-1")
	}
	if cred.LastUsedAt == nil {
		t.Error("last_used_at was not set")
	}
}

func TestVerifyReturnsNotFoundForWrongPassword(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := New(db, nil)
	ctx := context.Background()
	if _, err := r.Add(ctx, "kasir-1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	_, err = r.Verify(ctx, "wrong-password-1234567890")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("verify wrong password = %v, want ErrNotFound", err)
	}
}

func TestVerifyReturnsNotFoundForUnknownPassword(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := New(db, nil)
	ctx := context.Background()

	_, err = r.Verify(ctx, "totally-unknown-password")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("verify unknown = %v, want ErrNotFound", err)
	}
}

func TestVerifyRefusesDisabledCredential(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := New(db, nil)
	ctx := context.Background()

	pw, err := r.Add(ctx, "kasir-1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := r.Remove(ctx, "kasir-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	_, err = r.Verify(ctx, pw)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("verify disabled = %v, want ErrNotFound", err)
	}
}

func TestTwoCredentialsNeverResolveToEachOther(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := New(db, nil)
	ctx := context.Background()

	pw1, err := r.Add(ctx, "kasir-1")
	if err != nil {
		t.Fatalf("add 1: %v", err)
	}
	pw2, err := r.Add(ctx, "kasir-2")
	if err != nil {
		t.Fatalf("add 2: %v", err)
	}

	cred1, err := r.Verify(ctx, pw1)
	if err != nil {
		t.Fatalf("verify 1: %v", err)
	}
	if cred1.Label != "kasir-1" {
		t.Errorf("pw1 resolved to %q, want kasir-1", cred1.Label)
	}

	cred2, err := r.Verify(ctx, pw2)
	if err != nil {
		t.Fatalf("verify 2: %v", err)
	}
	if cred2.Label != "kasir-2" {
		t.Errorf("pw2 resolved to %q, want kasir-2", cred2.Label)
	}

	_, err = r.Verify(ctx, pw1)
	if err != nil {
		t.Fatalf("verify 1 again: %v", err)
	}
}

func TestSessionRoundTrip(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := New(db, nil)
	ctx := context.Background()

	pw, err := r.Add(ctx, "kasir-1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	cred, err := r.Verify(ctx, pw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	token, err := r.StartSession(ctx, cred.ID)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}

	got, err := r.SessionForToken(ctx, token)
	if err != nil {
		t.Fatalf("session for token: %v", err)
	}
	if got.Label != "kasir-1" {
		t.Errorf("label = %q, want kasir-1", got.Label)
	}

	if err := r.EndSession(ctx, token); err != nil {
		t.Fatalf("end session: %v", err)
	}
	_, err = r.SessionForToken(ctx, token)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("after end = %v, want ErrNotFound", err)
	}
}

func TestDisabledCredentialEndsSession(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := New(db, nil)
	ctx := context.Background()

	pw, err := r.Add(ctx, "kasir-1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	cred, err := r.Verify(ctx, pw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	token, err := r.StartSession(ctx, cred.ID)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	if _, err := r.SessionForToken(ctx, token); err != nil {
		t.Fatalf("precondition: session should work: %v", err)
	}

	if err := r.Remove(ctx, "kasir-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	_, err = r.SessionForToken(ctx, token)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("after remove = %v, want ErrNotFound", err)
	}
}

func TestListNeverIncludesSecrets(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := New(db, nil)
	ctx := context.Background()

	if _, err := r.Add(ctx, "kasir-1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	all, err := r.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len = %d, want 1", len(all))
	}
	if all[0].Label != "kasir-1" {
		t.Errorf("label = %q, want kasir-1", all[0].Label)
	}
}

func TestRateLimitLocksAfterElevenFailures(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	clk := &clock{at: time.Now()}
	r := New(db, clk.now)
	ctx := context.Background()

	if _, err := r.Add(ctx, "kasir-1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	for i := 0; i < 10; i++ {
		_, _ = r.Verify(ctx, "wrong")
		if err := r.RecordAttempt(ctx, "203.0.113.7"); err != nil {
			t.Fatalf("record attempt %d: %v", i, err)
		}
	}

	locked, err := r.IsLockedOut(ctx, "203.0.113.7")
	if err != nil {
		t.Fatalf("check lockout: %v", err)
	}
	if locked {
		t.Fatal("locked before 11th failure")
	}

	// 11th failure
	_, _ = r.Verify(ctx, "wrong")
	if err := r.RecordAttempt(ctx, "203.0.113.7"); err != nil {
		t.Fatalf("record attempt 11: %v", err)
	}

	locked, err = r.IsLockedOut(ctx, "203.0.113.7")
	if err != nil {
		t.Fatalf("check lockout: %v", err)
	}
	if !locked {
		t.Fatal("not locked after 11th failure")
	}

	// After the lockout window, the address is free again.
	clk.advance(lockoutDuration + time.Second)
	locked, err = r.IsLockedOut(ctx, "203.0.113.7")
	if err != nil {
		t.Fatalf("check lockout after window: %v", err)
	}
	if locked {
		t.Fatal("still locked after window passed")
	}
}

func TestTamperedHashIsRefusedEvenIfLookupMatches(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := New(db, nil)
	ctx := context.Background()

	pw, err := r.Add(ctx, "kasir-1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	// Tamper with the stored hash
	lookup := sha256.Sum256([]byte(pw))
	_, err = db.ExecContext(ctx,
		"UPDATE admin_credentials SET hash = X'00' WHERE lookup = ?", lookup[:])
	if err != nil {
		t.Fatalf("tamper: %v", err)
	}

	_, err = r.Verify(ctx, pw)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("verify tampered = %v, want ErrNotFound", err)
	}
}

type clock struct {
	at time.Time
}

func (c *clock) now() time.Time {
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.at = c.at.Add(d)
}
