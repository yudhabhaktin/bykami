package adminauth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/store"
)

func newTestRegistry(db *sql.DB, now func() time.Time) *Registry {
	r := New(db, now)
	r.SetIterations(1000)
	return r
}

func TestAddAndVerifyRoundTrip(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
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
	if cred.CanManage {
		t.Error("new credential should not be a manager by default")
	}
}

func TestVerifyReturnsNotFoundForWrongPassword(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
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

	r := newTestRegistry(db, nil)
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

	r := newTestRegistry(db, nil)
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

	r := newTestRegistry(db, nil)
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

	r := newTestRegistry(db, nil)
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

	r := newTestRegistry(db, nil)
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

	r := newTestRegistry(db, nil)
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
	r := newTestRegistry(db, clk.now)
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

	r := newTestRegistry(db, nil)
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

// --- VerifyByLabel ---

func TestVerifyByLabelRoundTrip(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
	ctx := context.Background()

	pw, err := r.Add(ctx, "kasir-1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	cred, err := r.VerifyByLabel(ctx, "kasir-1", pw)
	if err != nil {
		t.Fatalf("verify by label: %v", err)
	}
	if cred.Label != "kasir-1" {
		t.Errorf("label = %q, want kasir-1", cred.Label)
	}
	if cred.LastUsedAt == nil {
		t.Error("last_used_at was not set")
	}
}

func TestVerifyByLabelUnknownUsername(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
	ctx := context.Background()

	_, err = r.VerifyByLabel(ctx, "nobody", "any-password")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown username = %v, want ErrNotFound", err)
	}
}

func TestVerifyByLabelWrongPassword(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
	ctx := context.Background()
	if _, err := r.Add(ctx, "kasir-1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	_, err = r.VerifyByLabel(ctx, "kasir-1", "wrong-password-1234567890")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("wrong password = %v, want ErrNotFound", err)
	}
}

func TestVerifyByLabelDisabledCredential(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
	ctx := context.Background()

	pw, err := r.Add(ctx, "kasir-1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := r.Remove(ctx, "kasir-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	_, err = r.VerifyByLabel(ctx, "kasir-1", pw)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("verify disabled = %v, want ErrNotFound", err)
	}
}

func TestVerifyByLabelUpdatesLastUsed(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	clk := &clock{at: time.Now()}
	r := newTestRegistry(db, clk.now)
	ctx := context.Background()

	pw, err := r.Add(ctx, "kasir-1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	cred, err := r.VerifyByLabel(ctx, "kasir-1", pw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	first := *cred.LastUsedAt

	clk.advance(time.Second)
	cred, err = r.VerifyByLabel(ctx, "kasir-1", pw)
	if err != nil {
		t.Fatalf("verify again: %v", err)
	}
	if !cred.LastUsedAt.After(first) {
		t.Error("last_used_at was not updated on second verify")
	}
}

// --- Operator management ---

func TestAddWithCreatorRecordsIt(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
	ctx := context.Background()

	pw, err := r.AddWithCreator(ctx, "kasir-1", "manager-1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	_ = pw

	c, err := r.CredentialByLabel(ctx, "kasir-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.CreatedBy != "manager-1" {
		t.Errorf("created_by = %q, want manager-1", c.CreatedBy)
	}
}

func TestRemoveWithActorRecordsIt(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
	ctx := context.Background()

	if _, err := r.Add(ctx, "kasir-1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := r.RemoveWithActor(ctx, "kasir-1", "manager-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	c, err := r.CredentialByLabel(ctx, "kasir-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !c.Disabled {
		t.Error("expected disabled")
	}
	if c.DisabledBy != "manager-1" {
		t.Errorf("disabled_by = %q, want manager-1", c.DisabledBy)
	}
}

func TestSetManageAndUnsetManage(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
	ctx := context.Background()

	for _, l := range []string{"kasir-1", "manager-2"} {
		if _, err := r.Add(ctx, l); err != nil {
			t.Fatalf("add %s: %v", l, err)
		}
		if err := r.SetManage(ctx, l); err != nil {
			t.Fatalf("set manage %s: %v", l, err)
		}
	}

	if err := r.UnsetManage(ctx, "kasir-1"); err != nil {
		t.Fatalf("unset manage: %v", err)
	}
	c, err := r.CredentialByLabel(ctx, "kasir-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.CanManage {
		t.Error("expected not can_manage after UnsetManage")
	}
}

func TestUnsetManageRefusesLastManager(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
	ctx := context.Background()

	if _, err := r.Add(ctx, "manager-1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := r.SetManage(ctx, "manager-1"); err != nil {
		t.Fatalf("set manage: %v", err)
	}

	err = r.UnsetManage(ctx, "manager-1")
	if !errors.Is(err, ErrLastManager) {
		t.Errorf("unset last manager = %v, want ErrLastManager", err)
	}
}

func TestUnsetManageAllowsWithSpareManager(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
	ctx := context.Background()

	for _, l := range []string{"manager-1", "manager-2"} {
		if _, err := r.Add(ctx, l); err != nil {
			t.Fatalf("add %s: %v", l, err)
		}
		if err := r.SetManage(ctx, l); err != nil {
			t.Fatalf("set manage %s: %v", l, err)
		}
	}

	if err := r.UnsetManage(ctx, "manager-2"); err != nil {
		t.Fatalf("unset spare manager: %v", err)
	}
	c, err := r.CredentialByLabel(ctx, "manager-2")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.CanManage {
		t.Error("expected can_manage revoked")
	}
}

func TestManagerCount(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
	ctx := context.Background()

	if _, err := r.Add(ctx, "m1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := r.Add(ctx, "m2"); err != nil {
		t.Fatalf("add: %v", err)
	}

	if err := r.SetManage(ctx, "m1"); err != nil {
		t.Fatalf("set manage: %v", err)
	}

	n, err := r.ManagerCount(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("manager count = %d, want 1", n)
	}
}

func TestCredentialByLabel(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
	ctx := context.Background()

	if _, err := r.Add(ctx, "kasir-1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	c, err := r.CredentialByLabel(ctx, "kasir-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Label != "kasir-1" {
		t.Errorf("label = %q, want kasir-1", c.Label)
	}

	_, err = r.CredentialByLabel(ctx, "nobody")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown = %v, want ErrNotFound", err)
	}
}

func TestSessionForTokenIncludesCanManage(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
	ctx := context.Background()

	if _, err := r.Add(ctx, "manager-1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := r.SetManage(ctx, "manager-1"); err != nil {
		t.Fatalf("set manage: %v", err)
	}

	pw, err := r.Add(ctx, "kasir-1")
	if err != nil {
		t.Fatalf("add kasir: %v", err)
	}
	cred, err := r.Verify(ctx, pw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	token, err := r.StartSession(ctx, cred.ID)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	s, err := r.SessionForToken(ctx, token)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if s.CanManage {
		t.Error("kasir should not have CanManage")
	}
}

func TestListIncludesCanManage(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
	ctx := context.Background()

	if _, err := r.Add(ctx, "kasir-1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := r.Add(ctx, "manager-1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := r.SetManage(ctx, "manager-1"); err != nil {
		t.Fatalf("set manage: %v", err)
	}

	all, err := r.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, c := range all {
		switch c.Label {
		case "kasir-1":
			if c.CanManage {
				t.Error("kasir-1 should not have CanManage")
			}
		case "manager-1":
			if !c.CanManage {
				t.Error("manager-1 should have CanManage")
			}
		}
	}
}

func TestSetPasswordReplacesHashAndClearsMustChange(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
	ctx := context.Background()

	if _, err := r.Add(ctx, "kasir-1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := r.SetPassword(ctx, "kasir-1", "new-password-123"); err != nil {
		t.Fatalf("set password: %v", err)
	}

	c, err := r.CredentialByLabel(ctx, "kasir-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.MustChange {
		t.Error("must_change should be cleared")
	}
	if c.ChangedAt == nil {
		t.Error("changed_at should be set")
	}

	// Old generated password no longer works
	_, err = r.VerifyByLabel(ctx, "kasir-1", "old-password-would-not-work")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("old password = %v, want ErrNotFound", err)
	}

	// New password works
	_, err = r.VerifyByLabel(ctx, "kasir-1", "new-password-123")
	if err != nil {
		t.Errorf("new password = %v, want nil", err)
	}
}

func TestSetPasswordRefusesShortPassword(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
	ctx := context.Background()

	if _, err := r.Add(ctx, "kasir-1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := r.SetPassword(ctx, "kasir-1", "short"); err == nil {
		t.Error("short password should be refused")
	}
}

func TestResetPasswordSetsMustChangeAndRecordsActor(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	r := newTestRegistry(db, nil)
	ctx := context.Background()

	if _, err := r.Add(ctx, "kasir-1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := r.SetPassword(ctx, "kasir-1", "first-password-123"); err != nil {
		t.Fatalf("set password: %v", err)
	}

	pw, err := r.ResetPassword(ctx, "kasir-1", "manager-1")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if pw == "" {
		t.Error("empty reset password")
	}

	c, err := r.CredentialByLabel(ctx, "kasir-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !c.MustChange {
		t.Error("must_change should be set after reset")
	}
	if c.ResetBy != "manager-1" {
		t.Errorf("reset_by = %q, want manager-1", c.ResetBy)
	}
	if c.ResetAt == nil {
		t.Error("reset_at should be set")
	}

	// The reset password works
	_, err = r.VerifyByLabel(ctx, "kasir-1", pw)
	if err != nil {
		t.Errorf("verify reset password = %v, want nil", err)
	}

	// The previous password no longer works
	_, err = r.VerifyByLabel(ctx, "kasir-1", "first-password-123")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("old password after reset = %v, want ErrNotFound", err)
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
