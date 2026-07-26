package identity

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/store"
)

// capture stands in for WhatsApp. Keeping the real sender behind an interface is
// what lets identity be finished and proven before a provider account exists.
type capture struct {
	code string
	to   string
	sent int
	err  error
}

func (c *capture) Send(_ context.Context, e164, code string) error {
	if c.err != nil {
		return c.err
	}
	c.to, c.code, c.sent = e164, code, c.sent+1
	return nil
}

func newService(t *testing.T) (*Service, *capture, *sql.DB) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	c := &capture{}
	return New(db, c), c, db
}

func TestFirstVerificationCreatesTheAccount(t *testing.T) {
	s, sender, _ := newService(t)
	ctx := context.Background()

	// There is no registration step. A number that can receive a code is the
	// whole account.
	if err := s.RequestCode(ctx, "0812-3456-7890"); err != nil {
		t.Fatal(err)
	}
	user, token, err := s.VerifyCode(ctx, "0812-3456-7890", sender.code)
	if err != nil {
		t.Fatal(err)
	}
	if user.Phone != "+6281234567890" {
		t.Errorf("phone = %q, want normalised E.164", user.Phone)
	}
	if token == "" {
		t.Error("no session token issued")
	}

	got, err := s.UserForSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != user.ID {
		t.Errorf("session resolved to %s, want %s", got.ID, user.ID)
	}
}

func TestOneNumberWrittenTwoWaysIsOneAccount(t *testing.T) {
	s, sender, db := newService(t)
	ctx := context.Background()

	// The failure this prevents is two accounts with two loyalty balances for
	// one person, which is only fixable by rewriting an append-only ledger.
	for _, spelling := range []string{"0812-3456-7890", "+62 812 3456 7890"} {
		if err := s.RequestCode(ctx, spelling); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.VerifyCode(ctx, spelling, sender.code); err != nil {
			t.Fatal(err)
		}
	}

	var users int
	db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users)
	if users != 1 {
		t.Errorf("%d users, want 1", users)
	}
}

func TestTheCodeIsNeverStored(t *testing.T) {
	s, sender, db := newService(t)
	if err := s.RequestCode(context.Background(), "081234567890"); err != nil {
		t.Fatal(err)
	}

	// A database read must not be replayable into a login.
	var stored []byte
	if err := db.QueryRow(`SELECT code_hash FROM otp_challenges`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) == sender.code {
		t.Error("the plaintext code is in the database")
	}
	var matches int
	db.QueryRow(`SELECT COUNT(*) FROM otp_challenges WHERE CAST(code_hash AS TEXT) = ?`, sender.code).Scan(&matches)
	if matches != 0 {
		t.Error("the plaintext code is recoverable from the database")
	}
}

func TestAWrongCodeIsRejected(t *testing.T) {
	s, sender, _ := newService(t)
	ctx := context.Background()
	if err := s.RequestCode(ctx, "081234567890"); err != nil {
		t.Fatal(err)
	}
	wrong := "000000"
	if sender.code == wrong {
		wrong = "111111"
	}
	if _, _, err := s.VerifyCode(ctx, "081234567890", wrong); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("got %v, want ErrInvalidCode", err)
	}
}

func TestACodeWorksOnlyOnce(t *testing.T) {
	s, sender, _ := newService(t)
	ctx := context.Background()
	if err := s.RequestCode(ctx, "081234567890"); err != nil {
		t.Fatal(err)
	}
	code := sender.code

	if _, _, err := s.VerifyCode(ctx, "081234567890", code); err != nil {
		t.Fatal(err)
	}
	// Otherwise a code glimpsed over a shoulder stays good for the rest of its
	// five minutes.
	if _, _, err := s.VerifyCode(ctx, "081234567890", code); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("code was reusable: got %v, want ErrInvalidCode", err)
	}
}

func TestAnExpiredCodeIsRejected(t *testing.T) {
	s, sender, _ := newService(t)
	ctx := context.Background()
	if err := s.RequestCode(ctx, "081234567890"); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Now().UTC().Add(codeTTL + time.Minute) }
	if _, _, err := s.VerifyCode(ctx, "081234567890", sender.code); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("got %v, want ErrInvalidCode", err)
	}
}

func TestGuessingIsCappedEvenWithTheRightCode(t *testing.T) {
	s, sender, _ := newService(t)
	ctx := context.Background()
	if err := s.RequestCode(ctx, "081234567890"); err != nil {
		t.Fatal(err)
	}
	wrong := "000000"
	if sender.code == wrong {
		wrong = "111111"
	}
	for range maxAttempts {
		s.VerifyCode(ctx, "081234567890", wrong)
	}
	// The correct code must not rescue a challenge that has been exhausted, or
	// the limit only slows an attacker down rather than stopping them.
	if _, _, err := s.VerifyCode(ctx, "081234567890", sender.code); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("exhausted challenge still accepted the right code: %v", err)
	}
}

func TestCodeRequestsAreRateLimited(t *testing.T) {
	s, sender, _ := newService(t)
	ctx := context.Background()

	// Each send is billed, so an unthrottled endpoint is a way to spend the
	// owner's money as much as it is a security hole.
	for i := range maxSendsPerWindow {
		if err := s.RequestCode(ctx, "081234567890"); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if err := s.RequestCode(ctx, "081234567890"); !errors.Is(err, ErrTooManyRequests) {
		t.Errorf("got %v, want ErrTooManyRequests", err)
	}
	if sender.sent != maxSendsPerWindow {
		t.Errorf("%d messages sent, want %d", sender.sent, maxSendsPerWindow)
	}
}

func TestRateLimitIsPerNumber(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()
	for range maxSendsPerWindow {
		if err := s.RequestCode(ctx, "081234567890"); err != nil {
			t.Fatal(err)
		}
	}
	// One person hitting the limit must not lock out everyone else.
	if err := s.RequestCode(ctx, "081298765432"); err != nil {
		t.Errorf("a different number was rate limited: %v", err)
	}
}

func TestRequestCodeRejectsAnUnusableNumber(t *testing.T) {
	s, sender, _ := newService(t)
	if err := s.RequestCode(context.Background(), "not a phone"); err == nil {
		t.Error("accepted an invalid number")
	}
	if sender.sent != 0 {
		t.Error("sent a message to an invalid number")
	}
}

func TestUnknownAndExpiredSessionsAreRejected(t *testing.T) {
	s, sender, _ := newService(t)
	ctx := context.Background()

	if _, err := s.UserForSession(ctx, "not-a-real-token"); !errors.Is(err, ErrNoSession) {
		t.Errorf("got %v, want ErrNoSession", err)
	}

	if err := s.RequestCode(ctx, "081234567890"); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.VerifyCode(ctx, "081234567890", sender.code)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Now().UTC().Add(sessionTTL + time.Hour) }
	if _, err := s.UserForSession(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("expired session still resolved: %v", err)
	}
}

func TestEndSessionLogsOutOneDevice(t *testing.T) {
	s, sender, _ := newService(t)
	ctx := context.Background()

	tokens := make([]string, 2)
	for i := range tokens {
		if err := s.RequestCode(ctx, "081234567890"); err != nil {
			t.Fatal(err)
		}
		_, tok, err := s.VerifyCode(ctx, "081234567890", sender.code)
		if err != nil {
			t.Fatal(err)
		}
		tokens[i] = tok
	}

	if err := s.EndSession(ctx, tokens[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserForSession(ctx, tokens[0]); !errors.Is(err, ErrNoSession) {
		t.Errorf("logged-out session still resolved: %v", err)
	}
	// Logging out of a phone must not log out the tablet.
	if _, err := s.UserForSession(ctx, tokens[1]); err != nil {
		t.Errorf("the other device was logged out too: %v", err)
	}
}

func TestGeneratedCodesAreSixDigitsAndVary(t *testing.T) {
	seen := map[string]int{}
	for range 500 {
		c := newCode()
		if len(c) != 6 {
			t.Fatalf("code %q is not 6 digits", c)
		}
		for _, r := range c {
			if r < '0' || r > '9' {
				t.Fatalf("code %q has a non-digit", c)
			}
		}
		seen[c]++
	}
	// A generator stuck on one value would pass every other test here.
	if len(seen) < 400 {
		t.Errorf("only %d distinct codes in 500 draws", len(seen))
	}
}
