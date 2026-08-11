package mfa

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/store"
	"github.com/bhaktiyudha/bykami/api/internal/totp"
)

const operator = "081234567890"

// A fixed instant, so that every code in these tests is deterministic.
var at = time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)

func newRegistry(t *testing.T) *Registry {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	r := New(db)
	r.now = func() time.Time { return at }
	return r
}

// wrongCode returns six digits that are not accepted at t — including across
// the skew window, so a test that means to fail cannot accidentally succeed.
func wrongCode(t *testing.T, secret []byte, when time.Time) string {
	t.Helper()

	accepted := map[string]bool{}
	for delta := -1; delta <= 1; delta++ {
		accepted[totp.Code(secret, when.Add(time.Duration(delta)*totp.Period))] = true
	}
	for i := range 10 {
		if candidate := fmt.Sprintf("%06d", i); !accepted[candidate] {
			return candidate
		}
	}
	t.Fatal("could not find a wrong code")
	return ""
}

func TestAnEnrolledOperatorsCodeIsAccepted(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	e164, secret, err := r.Enroll(ctx, operator)
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	// Normalised on the way in, so the number typed into the console in any
	// Indonesian form lands on the same row.
	if e164 != "+6281234567890" {
		t.Errorf("enrolled as %q, want +6281234567890", e164)
	}

	if err := r.Verify(ctx, "0812-3456-7890", totp.Code(secret, at)); err != nil {
		t.Errorf("verify: %v", err)
	}
}

// The guard the last_step column exists for. A code stays valid for the rest of
// its period, so without this one read over a shoulder is good for another
// minute and a half.
func TestACodeCannotBeUsedTwice(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	_, secret, err := r.Enroll(ctx, operator)
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	code := totp.Code(secret, at)

	if err := r.Verify(ctx, operator, code); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if err := r.Verify(ctx, operator, code); !errors.Is(err, ErrBadCode) {
		t.Errorf("second use = %v, want ErrBadCode", err)
	}

	// The next period's code still works, so the guard spends one step rather
	// than locking the operator out of their own authenticator.
	later := at.Add(totp.Period)
	r.now = func() time.Time { return later }
	if err := r.Verify(ctx, operator, totp.Code(secret, later)); err != nil {
		t.Errorf("the next code was refused: %v", err)
	}
}

func TestAWrongCodeIsRefused(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	_, secret, err := r.Enroll(ctx, operator)
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}

	if err := r.Verify(ctx, operator, wrongCode(t, secret, at)); !errors.Is(err, ErrBadCode) {
		t.Errorf("verify = %v, want ErrBadCode", err)
	}
}

func TestAnUnenrolledNumberIsRefused(t *testing.T) {
	r := newRegistry(t)

	err := r.Verify(context.Background(), "081298765432", "123456")
	if !errors.Is(err, ErrNotEnrolled) {
		t.Errorf("verify = %v, want ErrNotEnrolled", err)
	}
}

// Guessing has to be slow. Six digits across three steps is one in three
// hundred thousand, which is only safe while the attempts are counted.
func TestFailuresLockTheEnrolment(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	_, secret, err := r.Enroll(ctx, operator)
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	wrong := wrongCode(t, secret, at)

	for i := range maxFailures {
		if err := r.Verify(ctx, operator, wrong); !errors.Is(err, ErrBadCode) {
			t.Fatalf("attempt %d = %v, want ErrBadCode", i+1, err)
		}
	}

	// The right code, refused — which is the whole point: a lockout that let a
	// correct guess through would not be one.
	if err := r.Verify(ctx, operator, totp.Code(secret, at)); !errors.Is(err, ErrLockedOut) {
		t.Errorf("after %d failures = %v, want ErrLockedOut", maxFailures, err)
	}

	// And it lifts on its own.
	after := at.Add(lockout + time.Minute)
	r.now = func() time.Time { return after }
	if err := r.Verify(ctx, operator, totp.Code(secret, after)); err != nil {
		t.Errorf("still locked after the lockout expired: %v", err)
	}
}

// A run of failures that stops short of the limit must not accumulate across a
// successful sign-in, or an operator who mistypes twice a week eventually locks
// themselves out for no reason.
func TestASuccessClearsTheFailureCount(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	_, secret, err := r.Enroll(ctx, operator)
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	wrong := wrongCode(t, secret, at)

	for range maxFailures - 1 {
		if err := r.Verify(ctx, operator, wrong); !errors.Is(err, ErrBadCode) {
			t.Fatalf("setup: %v", err)
		}
	}
	if err := r.Verify(ctx, operator, totp.Code(secret, at)); err != nil {
		t.Fatalf("correct code: %v", err)
	}

	// A fresh run of failures, one short of the limit again. If the counter had
	// survived, this would lock.
	next := at.Add(totp.Period)
	r.now = func() time.Time { return next }
	wrong = wrongCode(t, secret, next)
	for range maxFailures - 1 {
		if err := r.Verify(ctx, operator, wrong); !errors.Is(err, ErrBadCode) {
			t.Fatalf("second run: %v", err)
		}
	}
	if err := r.Verify(ctx, operator, totp.Code(secret, next)); err != nil {
		t.Errorf("locked out by failures either side of a success: %v", err)
	}
}

func TestUnlockLiftsALockoutEarly(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	_, secret, err := r.Enroll(ctx, operator)
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	wrong := wrongCode(t, secret, at)
	for range maxFailures {
		r.Verify(ctx, operator, wrong)
	}

	if err := r.Unlock(ctx, operator); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if err := r.Verify(ctx, operator, totp.Code(secret, at)); err != nil {
		t.Errorf("still locked after unlock: %v", err)
	}
}

// Refused rather than silently replaced. Overwriting would invalidate whatever
// is already on the operator's phone, and the person running the command would
// not find out until that operator next tried to sign in.
func TestEnrollingTwiceIsRefused(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	if _, _, err := r.Enroll(ctx, operator); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if _, _, err := r.Enroll(ctx, operator); !errors.Is(err, ErrAlreadyEnrolled) {
		t.Errorf("second enrolment = %v, want ErrAlreadyEnrolled", err)
	}
}

func TestRevokeEndsAccessImmediately(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	_, secret, err := r.Enroll(ctx, operator)
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if err := r.Revoke(ctx, operator); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if err := r.Verify(ctx, operator, totp.Code(secret, at)); !errors.Is(err, ErrNotEnrolled) {
		t.Errorf("a revoked authenticator still worked: %v", err)
	}
	if err := r.Revoke(ctx, operator); !errors.Is(err, ErrNotEnrolled) {
		t.Errorf("revoking twice = %v, want ErrNotEnrolled", err)
	}
}

func TestAnEnrolmentIsNotAnotherOperatorsEnrolment(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	_, first, err := r.Enroll(ctx, operator)
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if _, _, err := r.Enroll(ctx, "081298765432"); err != nil {
		t.Fatalf("enrol second: %v", err)
	}

	if err := r.Verify(ctx, "081298765432", totp.Code(first, at)); !errors.Is(err, ErrBadCode) {
		t.Errorf("one operator's code was accepted for another: %v", err)
	}
}

func TestListAndCountReportWhatIsEnrolled(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	if n, err := r.Count(ctx); err != nil || n != 0 {
		t.Fatalf("count on an empty registry = %d, %v", n, err)
	}

	_, secret, err := r.Enroll(ctx, operator)
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}

	n, err := r.Count(ctx)
	if err != nil || n != 1 {
		t.Fatalf("count = %d, %v", n, err)
	}

	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Phone != "+6281234567890" {
		t.Fatalf("list = %+v", list)
	}
	// Never used yet, so no last-used instant to report.
	if !list[0].LastUsed.IsZero() {
		t.Errorf("LastUsed = %v on an authenticator nobody has used", list[0].LastUsed)
	}

	if err := r.Verify(ctx, operator, totp.Code(secret, at)); err != nil {
		t.Fatalf("verify: %v", err)
	}
	list, err = r.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Recovered from the spent step, which is the instant that step began — so
	// it lands within one period of the sign-in rather than exactly on it.
	if gap := at.Sub(list[0].LastUsed); gap < 0 || gap >= totp.Period {
		t.Errorf("LastUsed = %v, want within one period before %v", list[0].LastUsed, at)
	}
}

func TestAMalformedNumberIsRejectedEverywhere(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	if _, _, err := r.Enroll(ctx, "not-a-phone"); err == nil {
		t.Error("Enroll accepted a number that is not one")
	}
	if err := r.Verify(ctx, "not-a-phone", "123456"); err == nil {
		t.Error("Verify accepted a number that is not one")
	}
	if err := r.Revoke(ctx, "not-a-phone"); err == nil {
		t.Error("Revoke accepted a number that is not one")
	}
}
