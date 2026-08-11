// Package identity implements phone-first, password-free accounts.
//
// Phone-first matches Indonesian norms; email-first does not. There are no
// passwords at all — one-time codes only. That is fewer support requests, no
// credential-stuffing surface to defend, and it matches what this market
// already expects from every other app it uses.
//
// A session is a bearer token and nothing more. This package deliberately does
// not decide how it travels — httpapi carries it in an Authorization header,
// because the first consumer is the kiosk at http://localhost, an origin that
// could not send a bykami.id cookie at all.
//
// Cookie-borne SSO across studio, booth, and dimsamcong is still the plan and
// is still what the subdomain layout is for; it becomes a decision about which
// surface sets a Domain=.bykami.id cookie over these same tokens. The jar has
// no opt-out, so that decision is made per surface — see
// design/platform-architecture.md — and app.bykami.id is explicitly excluded
// from it.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/phone"
)

var (
	// ErrInvalidCode covers a wrong code, an expired one, one already used, and
	// one for a number with no challenge outstanding. Deliberately one error:
	// distinguishing them tells an attacker which numbers have accounts.
	ErrInvalidCode = errors.New("identity: invalid or expired code")
	// ErrTooManyRequests means this number asked for codes too quickly.
	ErrTooManyRequests = errors.New("identity: too many code requests")
	// ErrNoSession means the token was unknown or expired.
	ErrNoSession = errors.New("identity: no active session")
)

const (
	codeTTL     = 5 * time.Minute
	sessionTTL  = 30 * 24 * time.Hour
	maxAttempts = 5

	// A number may request this many codes per window. Set against SMS cost as
	// much as security: each send is billed, so an unthrottled endpoint is a
	// way to spend someone else's money.
	maxSendsPerWindow = 3
	sendWindow        = 15 * time.Minute
)

// Sender delivers a code. WhatsApp is primary and SMS is the fallback, but the
// service does not care which — that choice belongs to the implementation, and
// keeping it behind this interface is what lets identity be built and tested
// before a provider account exists.
type Sender interface {
	Send(ctx context.Context, e164, code string) error
}

type User struct {
	ID        string
	Phone     string
	Name      string
	Email     string
	CreatedAt time.Time
}

type Service struct {
	db     *sql.DB
	sender Sender
	now    func() time.Time
}

func New(db *sql.DB, sender Sender) *Service {
	return &Service{db: db, sender: sender, now: func() time.Time { return time.Now().UTC() }}
}

// RequestCode normalises the number, mints a one-time code, and sends it.
//
// The code is returned to nobody — only its hash is stored, so a database read
// cannot be replayed into a login, and a leaked backup is not a set of live
// credentials.
func (s *Service) RequestCode(ctx context.Context, rawPhone string) error {
	e164, err := phone.Normalize(rawPhone)
	if err != nil {
		return err
	}

	var recent int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM otp_challenges WHERE phone = ? AND created_at > ?`,
		e164, s.now().Add(-sendWindow).Unix(),
	).Scan(&recent); err != nil {
		return fmt.Errorf("identity: rate check: %w", err)
	}
	if recent >= maxSendsPerWindow {
		return ErrTooManyRequests
	}

	code := newCode()
	sum := sha256.Sum256([]byte(e164 + ":" + code))

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO otp_challenges (id, phone, code_hash, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		newID(), e164, sum[:], s.now().Add(codeTTL).Unix(), s.now().Unix(),
	); err != nil {
		return fmt.Errorf("identity: store challenge: %w", err)
	}

	return s.sender.Send(ctx, e164, code)
}

// VerifyCode checks a code and, on success, returns the user and a session
// token. The account is created on first successful verification — there is no
// separate registration step, because a phone number that can receive a code is
// all the account ever needed.
//
// The returned token is the only time its plaintext exists; the database keeps
// a hash.
func (s *Service) VerifyCode(ctx context.Context, rawPhone, code string) (User, string, error) {
	e164, err := phone.Normalize(rawPhone)
	if err != nil {
		return User{}, "", ErrInvalidCode
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", fmt.Errorf("identity: begin: %w", err)
	}
	defer tx.Rollback()

	var (
		id       string
		hash     []byte
		expires  int64
		attempts int
	)
	err = tx.QueryRowContext(ctx,
		`SELECT id, code_hash, expires_at, attempts
		 FROM otp_challenges
		 WHERE phone = ? AND consumed_at IS NULL
		 ORDER BY created_at DESC
		 LIMIT 1`, e164,
	).Scan(&id, &hash, &expires, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", ErrInvalidCode
	}
	if err != nil {
		return User{}, "", fmt.Errorf("identity: load challenge: %w", err)
	}

	if s.now().Unix() > expires || attempts >= maxAttempts {
		return User{}, "", ErrInvalidCode
	}

	// Count the attempt before checking it. A guess that is not recorded until
	// it succeeds is not rate limited at all.
	if _, err := tx.ExecContext(ctx,
		`UPDATE otp_challenges SET attempts = attempts + 1 WHERE id = ?`, id,
	); err != nil {
		return User{}, "", fmt.Errorf("identity: record attempt: %w", err)
	}

	sum := sha256.Sum256([]byte(e164 + ":" + code))
	if subtle.ConstantTimeCompare(sum[:], hash) != 1 {
		// Commit so the incremented attempt count survives the failure.
		if err := tx.Commit(); err != nil {
			return User{}, "", fmt.Errorf("identity: commit attempt: %w", err)
		}
		return User{}, "", ErrInvalidCode
	}

	// Single use. Without this the code stays valid for the rest of its TTL,
	// and a code read over someone's shoulder is good for another five minutes.
	if _, err := tx.ExecContext(ctx,
		`UPDATE otp_challenges SET consumed_at = ? WHERE id = ?`, s.now().Unix(), id,
	); err != nil {
		return User{}, "", fmt.Errorf("identity: consume challenge: %w", err)
	}

	user, err := upsertUser(ctx, tx, e164, s.now())
	if err != nil {
		return User{}, "", err
	}

	token := newToken()
	tokenHash := sha256.Sum256([]byte(token))
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		tokenHash[:], user.ID, s.now().Add(sessionTTL).Unix(), s.now().Unix(),
	); err != nil {
		return User{}, "", fmt.Errorf("identity: create session: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return User{}, "", fmt.Errorf("identity: commit: %w", err)
	}
	return user, token, nil
}

// UserForSession resolves a session token to its user.
func (s *Service) UserForSession(ctx context.Context, token string) (User, error) {
	sum := sha256.Sum256([]byte(token))
	var u User
	var created int64
	err := s.db.QueryRowContext(ctx,
		`SELECT u.id, u.phone, COALESCE(u.name, ''), COALESCE(u.email, ''), u.created_at
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = ? AND s.expires_at > ?`,
		sum[:], s.now().Unix(),
	).Scan(&u.ID, &u.Phone, &u.Name, &u.Email, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNoSession
	}
	if err != nil {
		return User{}, fmt.Errorf("identity: resolve session: %w", err)
	}
	u.CreatedAt = time.Unix(created, 0).UTC()
	return u, nil
}

// ErrNoUser means no account exists for that number.
var ErrNoUser = errors.New("identity: no such user")

// UserByPhone finds an account by number, normalising first so that an operator
// can paste whatever form the customer gave them — 0812…, +62812…, or with
// spaces and dashes — and still land on the same record.
//
// Unlike the login path, this one distinguishes "no account" from "bad number".
// The reasoning that collapses them there does not apply: the caller is a
// logged-in operator looking someone up, not an anonymous guesser probing which
// numbers are registered.
func (s *Service) UserByPhone(ctx context.Context, rawPhone string) (User, error) {
	e164, err := phone.Normalize(rawPhone)
	if err != nil {
		return User{}, err
	}

	var u User
	var created int64
	err = s.db.QueryRowContext(ctx,
		`SELECT id, phone, COALESCE(name, ''), COALESCE(email, ''), created_at
		 FROM users WHERE phone = ?`, e164,
	).Scan(&u.ID, &u.Phone, &u.Name, &u.Email, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNoUser
	}
	if err != nil {
		return User{}, fmt.Errorf("identity: load user by phone: %w", err)
	}
	u.CreatedAt = time.Unix(created, 0).UTC()
	return u, nil
}

// EnsureUser finds or creates the account a phone number belongs to, filling in
// a name and email if the account has none yet.
//
// This is booking's way in, and the architecture record is why it exists rather
// than booking keeping its own customer table: a vertical with its own users
// means a migration and forcing every customer to re-register the day loyalty
// launches. A booking collects a name, a number and an email, which is the whole
// of a phone-first account, so there is nothing left to migrate — the booking is
// attached to the account that will hold the points.
//
// No code is sent and no session is issued. Booking has never asked anyone to log
// in and this does not start: the number is the account, and possession of it gets
// proven whenever the customer first signs in. What this creates is a row, not a
// login.
//
// A name or email already on the account is left alone. Somebody who has
// corrected the spelling of their own name should not have it undone by a stale
// autofill in a booking form.
func (s *Service) EnsureUser(ctx context.Context, rawPhone, name, email string) (User, error) {
	e164, err := phone.Normalize(rawPhone)
	if err != nil {
		return User{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, fmt.Errorf("identity: begin: %w", err)
	}
	defer tx.Rollback()

	u, err := upsertUser(ctx, tx, e164, s.now())
	if err != nil {
		return User{}, err
	}

	// COALESCE on the stored side rather than a read-then-write: the column is
	// nullable and also carries '' from older rows, and both mean "not set".
	if _, err := tx.ExecContext(ctx,
		`UPDATE users
		    SET name  = CASE WHEN COALESCE(name, '')  = '' THEN ? ELSE name  END,
		        email = CASE WHEN COALESCE(email, '') = '' THEN ? ELSE email END
		  WHERE id = ?`,
		nullIfEmpty(name), nullIfEmpty(email), u.ID,
	); err != nil {
		return User{}, fmt.Errorf("identity: fill user: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return User{}, fmt.Errorf("identity: commit: %w", err)
	}

	if u.Name == "" {
		u.Name = name
	}
	if u.Email == "" {
		u.Email = email
	}
	return u, nil
}

// StartSession issues a session for a number whose owner has already been
// proven to be who they say, by something other than a one-time code.
//
// The only caller is the operator console, which proves it with an
// authenticator code — see internal/mfa and internal/totp. Nothing on the
// customer path calls this: there, VerifyCode is the way in, and it mints its
// own session once it has checked the code, so this cannot be reached without a
// second authentication step of the caller's own.
//
// This is a privileged primitive and should be read as one. It takes a phone
// number and hands back a live session for it, with nothing in between —
// whatever calls it *is* the authentication, and adding a second caller means
// adding a second way to log in as anybody.
func (s *Service) StartSession(ctx context.Context, rawPhone string) (User, string, error) {
	e164, err := phone.Normalize(rawPhone)
	if err != nil {
		return User{}, "", err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", fmt.Errorf("identity: begin: %w", err)
	}
	defer tx.Rollback()

	// The account is created here if it does not exist, exactly as a first
	// verified login would. An operator who has never been a customer has no
	// users row until the first time they sign in to the console.
	user, err := upsertUser(ctx, tx, e164, s.now())
	if err != nil {
		return User{}, "", err
	}

	token := newToken()
	tokenHash := sha256.Sum256([]byte(token))
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		tokenHash[:], user.ID, s.now().Add(sessionTTL).Unix(), s.now().Unix(),
	); err != nil {
		return User{}, "", fmt.Errorf("identity: create session: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return User{}, "", fmt.Errorf("identity: commit: %w", err)
	}
	return user, token, nil
}

// EndSession logs one device out. Idempotent.
func (s *Service) EndSession(ctx context.Context, token string) error {
	sum := sha256.Sum256([]byte(token))
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, sum[:]); err != nil {
		return fmt.Errorf("identity: end session: %w", err)
	}
	return nil
}

func upsertUser(ctx context.Context, tx *sql.Tx, e164 string, now time.Time) (User, error) {
	var u User
	var created int64
	err := tx.QueryRowContext(ctx,
		`SELECT id, phone, COALESCE(name, ''), COALESCE(email, ''), created_at FROM users WHERE phone = ?`,
		e164,
	).Scan(&u.ID, &u.Phone, &u.Name, &u.Email, &created)
	if err == nil {
		u.CreatedAt = time.Unix(created, 0).UTC()
		return u, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return User{}, fmt.Errorf("identity: load user: %w", err)
	}

	u = User{ID: newID(), Phone: e164, CreatedAt: now.Truncate(time.Second)}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO users (id, phone, created_at) VALUES (?, ?, ?)`,
		u.ID, u.Phone, u.CreatedAt.Unix(),
	); err != nil {
		return User{}, fmt.Errorf("identity: create user: %w", err)
	}
	return u, nil
}

// newCode returns a 6-digit code drawn from crypto/rand.
//
// Rejection sampling rather than modulo: 256 is not a multiple of 10, so
// `b % 10` would make the low digits measurably likelier. With a 5-minute TTL
// and 5 attempts that bias is not the weakest link, but the fix is three lines
// and the reasoning does not have to be revisited later.
func newCode() string {
	const digits = "0123456789"
	out := make([]byte, 6)
	var buf [1]byte
	for i := range out {
		for {
			if _, err := rand.Read(buf[:]); err != nil {
				panic("identity: entropy unavailable: " + err.Error())
			}
			if buf[0] < 250 { // 250 = 25 * 10, the largest unbiased range
				out[i] = digits[buf[0]%10]
				break
			}
		}
	}
	return string(out)
}

func newToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("identity: entropy unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("identity: entropy unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// nullIfEmpty keeps "not given" as NULL rather than ”. users.name and
// users.email are optional by design, and two spellings of absent is one more
// than any reader of this table should have to handle.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
