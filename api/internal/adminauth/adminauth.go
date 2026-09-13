// Package adminauth is the operator console's password credential system.
//
// One generated password per operator. The password is the identity: the
// server looks it up, finds whose it is, and attributes the write to that
// person. No username, no phone, no code.
//
// The password is 24 characters from an alphabet without lookalikes, drawn
// from crypto/rand. That is ~120 bits, which is why a fast hash (SHA-256)
// is acceptable as the lookup index and why the KDF's iteration count is
// insurance rather than the main defence — there is no dictionary to attack.
//
// Both lookup and hash are checked, and that ordering is the point: a lookup
// that merely found a row would make a database read a login, because the
// reader could add their own row and sign in. Verifying the KDF afterwards
// means the index is useful for finding a candidate and useless as a credential.
package adminauth

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
	"strings"
	"time"

	"crypto/pbkdf2"
)

// Alphabet without lookalikes: no 0/O, 1/l/I.
const passwordAlphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"

const (
	passwordLength   = 24
	saltLength       = 16
	pbkdf2Iterations = 600_000
	pbkdf2KeyLength  = 32
)

// ErrNotFound is every authentication failure: unknown password, wrong
// password, disabled credential, or locked-out address. One error so that
// the page never tells them apart.
var ErrNotFound = errors.New("adminauth: not found")

// rateLimit is how many failures in how long lock an address.
const (
	maxFailures     = 11
	attemptWindow   = 15 * time.Minute
	lockoutDuration = 15 * time.Minute
)

// Credential is one operator, as the console sees them.
type Credential struct {
	ID         string
	Label      string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	Disabled   bool
}

// Registry holds the operator credentials and their sessions.
type Registry struct {
	db  *sql.DB
	now func() time.Time
}

// New returns the registry. A nil clock is time.Now, which is what production
// wants and what a test that does not care about time wants.
func New(db *sql.DB, now func() time.Time) *Registry {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Registry{db: db, now: now}
}

// Add creates a credential with a generated password and returns the password
// once. It is the caller's job to show it to whoever is standing there and
// then forget it.
func (r *Registry) Add(ctx context.Context, label string) (string, error) {
	label = cleanLabel(label)
	if label == "" {
		return "", errors.New("adminauth: label is required")
	}

	pw, err := generatePassword(passwordLength)
	if err != nil {
		return "", fmt.Errorf("adminauth: generate password: %w", err)
	}

	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("adminauth: entropy: %w", err)
	}

	lookup := sha256.Sum256([]byte(pw))
	hash, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iterations, pbkdf2KeyLength)
	if err != nil {
		return "", fmt.Errorf("adminauth: pbkdf2: %w", err)
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO admin_credentials (id, label, lookup, hash, salt, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		newID(), label, lookup[:], hash, salt, r.now().Unix(),
	)
	if err != nil {
		return "", fmt.Errorf("adminauth: insert credential: %w", err)
	}
	return pw, nil
}

// Verify checks a password and, on success, returns the credential it belongs
// to. The same error is returned for every kind of failure.
func (r *Registry) Verify(ctx context.Context, password string) (Credential, error) {
	lookup := sha256.Sum256([]byte(password))

	var (
		id       string
		label    string
		created  int64
		lastUsed sql.NullInt64
		disabled sql.NullInt64
		hash     []byte
		salt     []byte
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT id, label, created_at, last_used_at, disabled_at, hash, salt
		   FROM admin_credentials WHERE lookup = ?`,
		lookup[:],
	).Scan(&id, &label, &created, &lastUsed, &disabled, &hash, &salt)
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	if err != nil {
		return Credential{}, fmt.Errorf("adminauth: lookup: %w", err)
	}

	if disabled.Valid {
		return Credential{}, ErrNotFound
	}

	derived, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, pbkdf2KeyLength)
	if err != nil {
		return Credential{}, fmt.Errorf("adminauth: pbkdf2: %w", err)
	}
	if subtle.ConstantTimeCompare(hash, derived) != 1 {
		return Credential{}, ErrNotFound
	}

	now := r.now()
	if _, err := r.db.ExecContext(ctx,
		`UPDATE admin_credentials SET last_used_at = ? WHERE id = ?`,
		now.Unix(), id,
	); err != nil {
		return Credential{}, fmt.Errorf("adminauth: update last_used_at: %w", err)
	}

	c := Credential{ID: id, Label: label, CreatedAt: time.Unix(created, 0).UTC(), LastUsedAt: &now}
	return c, nil
}

// List returns every credential, ordered by label. Secrets are never included.
func (r *Registry) List(ctx context.Context) ([]Credential, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, label, created_at, last_used_at, disabled_at
		   FROM admin_credentials ORDER BY label`)
	if err != nil {
		return nil, fmt.Errorf("adminauth: list: %w", err)
	}
	defer rows.Close()

	var out []Credential
	for rows.Next() {
		var c Credential
		var created int64
		var lastUsed, disabled sql.NullInt64
		if err := rows.Scan(&c.ID, &c.Label, &created, &lastUsed, &disabled); err != nil {
			return nil, fmt.Errorf("adminauth: scan credential: %w", err)
		}
		c.CreatedAt = time.Unix(created, 0).UTC()
		if lastUsed.Valid {
			t := time.Unix(lastUsed.Int64, 0).UTC()
			c.LastUsedAt = &t
		}
		c.Disabled = disabled.Valid
		out = append(out, c)
	}
	return out, rows.Err()
}

// Remove disables a credential. The row is not deleted so that it can still be
// audited. Sessions belonging to it stop working on the next request because
// SessionForToken joins against admin_credentials and checks disabled_at.
func (r *Registry) Remove(ctx context.Context, label string) error {
	label = cleanLabel(label)
	res, err := r.db.ExecContext(ctx,
		`UPDATE admin_credentials SET disabled_at = ? WHERE label = ? AND disabled_at IS NULL`,
		r.now().Unix(), label,
	)
	if err != nil {
		return fmt.Errorf("adminauth: remove: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Count returns how many credentials exist (including disabled ones).
func (r *Registry) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM admin_credentials`).Scan(&n); err != nil {
		return 0, fmt.Errorf("adminauth: count: %w", err)
	}
	return n, nil
}

// StartSession mints a session for a credential and returns the plaintext
// token. The database keeps only its hash.
func (r *Registry) StartSession(ctx context.Context, credentialID string) (string, error) {
	token := newToken()
	tokenHash := sha256.Sum256([]byte(token))
	now := r.now()
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO admin_sessions (token_hash, credential_id, expires_at, created_at)
		 VALUES (?, ?, ?, ?)`,
		tokenHash[:], credentialID, now.Add(30*24*time.Hour).Unix(), now.Unix(),
	)
	if err != nil {
		return "", fmt.Errorf("adminauth: start session: %w", err)
	}
	return token, nil
}

// SessionForToken resolves a session token to its credential. Disabled
// credentials are refused, which is what makes Remove take effect immediately.
func (r *Registry) SessionForToken(ctx context.Context, token string) (Credential, error) {
	tokenHash := sha256.Sum256([]byte(token))
	var c Credential
	var created int64
	var lastUsed sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT c.id, c.label, c.created_at, c.last_used_at
		   FROM admin_sessions s
		   JOIN admin_credentials c ON c.id = s.credential_id
		  WHERE s.token_hash = ? AND s.expires_at > ? AND c.disabled_at IS NULL`,
		tokenHash[:], r.now().Unix(),
	).Scan(&c.ID, &c.Label, &created, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	if err != nil {
		return Credential{}, fmt.Errorf("adminauth: resolve session: %w", err)
	}
	c.CreatedAt = time.Unix(created, 0).UTC()
	if lastUsed.Valid {
		t := time.Unix(lastUsed.Int64, 0).UTC()
		c.LastUsedAt = &t
	}
	return c, nil
}

// EndSession deletes one session. Idempotent.
func (r *Registry) EndSession(ctx context.Context, token string) error {
	tokenHash := sha256.Sum256([]byte(token))
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM admin_sessions WHERE token_hash = ?`, tokenHash[:])
	if err != nil {
		return fmt.Errorf("adminauth: end session: %w", err)
	}
	return nil
}

// RecordAttempt records one login attempt from an address.
func (r *Registry) RecordAttempt(ctx context.Context, ip string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO admin_login_attempts (id, ip, created_at) VALUES (?, ?, ?)`,
		newID(), ip, r.now().Unix())
	if err != nil {
		return fmt.Errorf("adminauth: record attempt: %w", err)
	}
	return nil
}

// IsLockedOut reports whether this address has exceeded the failure limit.
func (r *Registry) IsLockedOut(ctx context.Context, ip string) (bool, error) {
	since := r.now().Add(-attemptWindow).Unix()
	var recent int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM admin_login_attempts WHERE ip = ? AND created_at > ?`,
		ip, since,
	).Scan(&recent); err != nil {
		return false, fmt.Errorf("adminauth: count attempts: %w", err)
	}
	if recent < maxFailures {
		return false, nil
	}

	// Locked: find the creation time of the Nth most recent attempt.
	// The address stays locked until that attempt is older than the lockout.
	var lockedSince int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT created_at FROM admin_login_attempts
		  WHERE ip = ? ORDER BY created_at DESC LIMIT 1 OFFSET ?`,
		ip, maxFailures-1,
	).Scan(&lockedSince); err != nil {
		return false, fmt.Errorf("adminauth: lock boundary: %w", err)
	}
	return r.now().Unix()-lockedSince < int64(lockoutDuration.Seconds()), nil
}

// SweepAttempts deletes login attempts older than the retention window.
func (r *Registry) SweepAttempts(ctx context.Context) error {
	cutoff := r.now().Add(-lockoutDuration).Unix()
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM admin_login_attempts WHERE created_at < ?`, cutoff)
	if err != nil {
		return fmt.Errorf("adminauth: sweep attempts: %w", err)
	}
	return nil
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
			// Rejection sampling: find the largest multiple of alphabetLen
			// that fits in a byte, and reject anything above it.
			max := 256 - (256 % int(alphabetLen))
			if int(buf[0]) < max {
				out[i] = passwordAlphabet[int(buf[0])%int(alphabetLen)]
				break
			}
		}
	}
	return string(out), nil
}

func cleanLabel(s string) string {
	return strings.TrimSpace(s)
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("adminauth: entropy unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

func newToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("adminauth: entropy unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
