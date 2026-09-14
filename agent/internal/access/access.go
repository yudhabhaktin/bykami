// Package access is the booth's local account system for the tunnelled test
// deployment.
//
// It is a deliberate duplicate of the PBKDF2-SHA256 scheme in
// api/internal/adminauth, because the two modules share no code on purpose.
// The booth and the cloud are separate modules joined only by go.work, and
// a shared package would become a coupling that lets a booth migration break
// the API or vice versa.
package access

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"crypto/pbkdf2"
)

const (
	passwordLength   = 24
	saltLength       = 16
	pbkdf2Iterations = 600_000
	pbkdf2KeyLength  = 32
)

// Alphabet without lookalikes: no 0/O, 1/l/I.
const passwordAlphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"

// Rate-limiting constants. Eleven failures in fifteen minutes locks the
// address for fifteen minutes. More than ten is the design's wording; the
// eleventh triggers the lock.
const (
	maxFailures     = 11
	attemptWindow   = 15 * time.Minute
	lockoutDuration = 15 * time.Minute
)

var (
	ErrNotFound    = errors.New("access: not found")
	ErrLastAccount = errors.New("access: last account")
)

// Registry holds booth access accounts.
type Registry struct {
	db         *sql.DB
	now        func() time.Time
	iterations int // override for tests; zero means pbkdf2Iterations
}

func New(db *sql.DB, now func() time.Time) *Registry {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Registry{db: db, now: now}
}

func (r *Registry) kdfIterations() int {
	if r.iterations > 0 {
		return r.iterations
	}
	return pbkdf2Iterations
}

// SetIterations lowers the KDF cost for tests. Never call in production.
func (r *Registry) SetIterations(n int) { r.iterations = n }

// Add creates an account with a generated password and returns it once.
func (r *Registry) Add(ctx context.Context, username string) (string, error) {
	username = cleanUsername(username)
	if username == "" {
		return "", errors.New("access: username is required")
	}

	pw, err := generatePassword(passwordLength)
	if err != nil {
		return "", fmt.Errorf("access: generate password: %w", err)
	}

	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("access: entropy: %w", err)
	}

	hash, err := pbkdf2.Key(sha256.New, pw, salt, r.kdfIterations(), pbkdf2KeyLength)
	if err != nil {
		return "", fmt.Errorf("access: pbkdf2: %w", err)
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO booth_access_accounts (id, username, hash, salt, iterations, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		newID(), username, hash, salt, r.kdfIterations(), r.now().Unix(),
	)
	if err != nil {
		return "", fmt.Errorf("access: insert account: %w", err)
	}
	return pw, nil
}

// Verify checks a username and password. On an unknown username it still runs
// the KDF against a dummy hash before refusing, so the response time does not
// reveal whether the username exists.
func (r *Registry) Verify(ctx context.Context, username, password string) error {
	username = cleanUsername(username)

	var hash, salt []byte
	var iterations int
	err := r.db.QueryRowContext(ctx,
		`SELECT hash, salt, iterations FROM booth_access_accounts
		 WHERE username = ? AND disabled_at IS NULL`,
		username,
	).Scan(&hash, &salt, &iterations)

	if errors.Is(err, sql.ErrNoRows) {
		// Unknown username: run a dummy KDF so the timing matches a real
		// verification. A zeroed salt and the full iteration count.
		dummySalt := make([]byte, saltLength)
		_, _ = pbkdf2.Key(sha256.New, password, dummySalt, r.kdfIterations(), pbkdf2KeyLength)
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("access: lookup: %w", err)
	}

	if iterations == 0 {
		iterations = pbkdf2Iterations
	}
	derived, err := pbkdf2.Key(sha256.New, password, salt, iterations, pbkdf2KeyLength)
	if err != nil {
		return fmt.Errorf("access: pbkdf2: %w", err)
	}
	if subtle.ConstantTimeCompare(hash, derived) != 1 {
		return ErrNotFound
	}

	_, err = r.db.ExecContext(ctx,
		`UPDATE booth_access_accounts SET last_used_at = ? WHERE username = ?`,
		r.now().Unix(), username,
	)
	if err != nil {
		return fmt.Errorf("access: update last_used_at: %w", err)
	}
	return nil
}

// Passwd regenerates the password for an existing account.
func (r *Registry) Passwd(ctx context.Context, username string) (string, error) {
	username = cleanUsername(username)

	pw, err := generatePassword(passwordLength)
	if err != nil {
		return "", fmt.Errorf("access: generate password: %w", err)
	}

	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("access: entropy: %w", err)
	}
	hash, err := pbkdf2.Key(sha256.New, pw, salt, r.kdfIterations(), pbkdf2KeyLength)
	if err != nil {
		return "", fmt.Errorf("access: pbkdf2: %w", err)
	}

	res, err := r.db.ExecContext(ctx,
		`UPDATE booth_access_accounts SET hash = ?, salt = ?, iterations = ?
		 WHERE username = ? AND disabled_at IS NULL`,
		hash, salt, r.kdfIterations(), username,
	)
	if err != nil {
		return "", fmt.Errorf("access: passwd: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return "", ErrNotFound
	}
	return pw, nil
}

// Remove disables an account. Refuses to remove the last one.
func (r *Registry) Remove(ctx context.Context, username string) error {
	username = cleanUsername(username)

	var count int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM booth_access_accounts WHERE disabled_at IS NULL`,
	).Scan(&count); err != nil {
		return fmt.Errorf("access: count accounts: %w", err)
	}
	if count <= 1 {
		return ErrLastAccount
	}

	res, err := r.db.ExecContext(ctx,
		`UPDATE booth_access_accounts SET disabled_at = ? WHERE username = ? AND disabled_at IS NULL`,
		r.now().Unix(), username,
	)
	if err != nil {
		return fmt.Errorf("access: remove: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns every active account username.
func (r *Registry) List(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT username FROM booth_access_accounts WHERE disabled_at IS NULL ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("access: list: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("access: scan: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Count returns how many active accounts exist.
func (r *Registry) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM booth_access_accounts WHERE disabled_at IS NULL`,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("access: count: %w", err)
	}
	return n, nil
}

// RecordAttempt records one failed login attempt.
func (r *Registry) RecordAttempt(ctx context.Context, ip, username string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO booth_login_attempts (id, ip, username, created_at) VALUES (?, ?, ?, ?)`,
		newID(), ip, username, r.now().Unix())
	if err != nil {
		return fmt.Errorf("access: record attempt: %w", err)
	}
	return nil
}

// IsLockedOut reports whether this address has exceeded the failure limit.
func (r *Registry) IsLockedOut(ctx context.Context, ip string) (bool, error) {
	since := r.now().Add(-attemptWindow).Unix()
	var recent int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM booth_login_attempts WHERE ip = ? AND created_at > ?`,
		ip, since,
	).Scan(&recent); err != nil {
		return false, fmt.Errorf("access: count attempts: %w", err)
	}
	if recent < maxFailures {
		return false, nil
	}

	var lockedSince int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT created_at FROM booth_login_attempts
		 WHERE ip = ? ORDER BY created_at DESC LIMIT 1 OFFSET ?`,
		ip, maxFailures-1,
	).Scan(&lockedSince); err != nil {
		return false, fmt.Errorf("access: lock boundary: %w", err)
	}
	return r.now().Unix()-lockedSince < int64(lockoutDuration.Seconds()), nil
}

// SweepAttempts deletes old login attempts.
func (r *Registry) SweepAttempts(ctx context.Context) error {
	cutoff := r.now().Add(-lockoutDuration).Unix()
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM booth_login_attempts WHERE created_at < ?`, cutoff)
	if err != nil {
		return fmt.Errorf("access: sweep attempts: %w", err)
	}
	return nil
}

func cleanUsername(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func generatePassword(n int) (string, error) {
	var buf [1]byte
	out := make([]byte, n)
	alphabetLen := byte(len(passwordAlphabet))
	for i := range out {
		for {
			if _, err := rand.Read(buf[:]); err != nil {
				return "", err
			}
			if buf[0] < alphabetLen*byte(256/int(alphabetLen)) {
				out[i] = passwordAlphabet[buf[0]%alphabetLen]
				break
			}
		}
	}
	return string(out), nil
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
