// Package adminauth is the operator console's password credential system.
//
// One generated password per operator. The password is the identity: the
// server looks it up, finds whose it is, and attributes the write to that
// person. The label is the username and the password proves it; they are the
// same thing, not two columns.
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

// ErrLastManager is returned when an action would remove the last manager.
var ErrLastManager = errors.New("adminauth: last manager")

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
	CanManage  bool
	CreatedAt  time.Time
	LastUsedAt *time.Time
	Disabled   bool
	CreatedBy  string
	DisabledBy string
	MustChange bool
	ChangedAt  *time.Time
	ResetBy    string
	ResetAt    *time.Time
}

// Registry holds the operator credentials and their sessions.
type Registry struct {
	db         *sql.DB
	now        func() time.Time
	iterations int // override for tests; zero means pbkdf2Iterations
}

// New returns the registry. A nil clock is time.Now, which is what production
// wants and what a test that does not care about time wants.
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
func (r *Registry) SetIterations(n int) {
	r.iterations = n
}

// Add creates a credential with a generated password and returns the password
// once. It is the caller's job to show it to whoever is standing there and
// then forget it.
func (r *Registry) Add(ctx context.Context, label string) (string, error) {
	return r.AddWithCreator(ctx, label, "")
}

// AddWithCreator is Add with the acting manager recorded as created_by.
// The first credential ever created is automatically a manager, because the
// console has no way to promote anybody and a console born with no manager is
// a console nobody can administer. Two adds racing cannot both claim first:
// the can_manage value comes from a subquery that counts the table at the
// moment the INSERT runs.
func (r *Registry) AddWithCreator(ctx context.Context, label, createdBy string) (string, error) {
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
	hash, err := pbkdf2.Key(sha256.New, pw, salt, r.kdfIterations(), pbkdf2KeyLength)
	if err != nil {
		return "", fmt.Errorf("adminauth: pbkdf2: %w", err)
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO admin_credentials (id, label, lookup, hash, salt, created_at, created_by, can_manage)
		 VALUES (?, ?, ?, ?, ?, ?, ?, CASE WHEN (SELECT COUNT(*) FROM admin_credentials) = 0 THEN 1 ELSE 0 END)`,
		newID(), label, lookup[:], hash, salt, r.now().Unix(), nullIfEmpty(createdBy),
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

	c, created, lastUsed, disabled, hash, salt, err := r.scanByLookup(ctx, lookup[:])
	if err != nil {
		return Credential{}, err
	}
	if disabled.Valid {
		return Credential{}, ErrNotFound
	}

	derived, err := pbkdf2.Key(sha256.New, password, salt, r.kdfIterations(), pbkdf2KeyLength)
	if err != nil {
		return Credential{}, fmt.Errorf("adminauth: pbkdf2: %w", err)
	}
	if subtle.ConstantTimeCompare(hash, derived) != 1 {
		return Credential{}, ErrNotFound
	}

	return r.finishVerify(ctx, password, c, created, lastUsed)
}

func scanCredential(row *sql.Row) (Credential, sql.NullInt64, sql.NullInt64, sql.NullInt64, []byte, []byte, error) {
	var c Credential
	var created, lastUsed, disabled, changedAt, resetAt sql.NullInt64
	var hash, salt []byte
	var createdBy, disabledBy, resetBy sql.NullString
	err := row.Scan(&c.ID, &c.Label, &c.CanManage, &created, &lastUsed, &disabled, &createdBy, &disabledBy, &c.MustChange, &changedAt, &resetBy, &resetAt, &hash, &salt)
	c.CreatedBy = createdBy.String
	c.DisabledBy = disabledBy.String
	c.ResetBy = resetBy.String
	if changedAt.Valid {
		t := time.Unix(changedAt.Int64, 0).UTC()
		c.ChangedAt = &t
	}
	if resetAt.Valid {
		t := time.Unix(resetAt.Int64, 0).UTC()
		c.ResetAt = &t
	}
	return c, created, lastUsed, disabled, hash, salt, err
}

func (r *Registry) scanByLookup(ctx context.Context, lookup []byte) (Credential, sql.NullInt64, sql.NullInt64, sql.NullInt64, []byte, []byte, error) {
	c, created, lastUsed, disabled, hash, salt, err := scanCredential(r.db.QueryRowContext(ctx,
		`SELECT id, label, can_manage, created_at, last_used_at, disabled_at, created_by, disabled_by, must_change, changed_at, reset_by, reset_at, hash, salt
		   FROM admin_credentials WHERE lookup = ?`,
		lookup,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, created, lastUsed, disabled, nil, nil, ErrNotFound
	}
	if err != nil {
		return Credential{}, created, lastUsed, disabled, nil, nil, fmt.Errorf("adminauth: lookup: %w", err)
	}
	return c, created, lastUsed, disabled, hash, salt, nil
}

// MustChange reports whether a credential still has its generated password.
func (r *Registry) MustChange(ctx context.Context, label string) (bool, error) {
	label = cleanLabel(label)
	var mc int
	if err := r.db.QueryRowContext(ctx,
		`SELECT must_change FROM admin_credentials WHERE label = ?`, label,
	).Scan(&mc); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, fmt.Errorf("adminauth: must_change: %w", err)
	}
	return mc != 0, nil
}

// VerifyByLabel checks a username and password together. On an unknown username
// it still runs the KDF against a dummy hash before refusing, so the response
// time does not reveal whether the username exists. This looks like wasted work
// and is not: a timing oracle on username existence is a weaker but real leak.
func (r *Registry) VerifyByLabel(ctx context.Context, label, password string) (Credential, error) {
	label = cleanLabel(label)

	var c Credential
	var created, lastUsed, disabled sql.NullInt64
	var hash, salt []byte
	var createdBy, disabledBy sql.NullString
	var changedAt, resetAt sql.NullInt64
	var resetBy sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT id, label, can_manage, created_at, last_used_at, disabled_at, created_by, disabled_by, must_change, changed_at, reset_by, reset_at, hash, salt
		   FROM admin_credentials WHERE label = ?`,
		label,
	).Scan(&c.ID, &c.Label, &c.CanManage, &created, &lastUsed, &disabled, &createdBy, &disabledBy, &c.MustChange, &changedAt, &resetBy, &resetAt, &hash, &salt)
	c.CreatedBy = createdBy.String
	c.DisabledBy = disabledBy.String
	c.ResetBy = resetBy.String
	if changedAt.Valid {
		t := time.Unix(changedAt.Int64, 0).UTC()
		c.ChangedAt = &t
	}
	if resetAt.Valid {
		t := time.Unix(resetAt.Int64, 0).UTC()
		c.ResetAt = &t
	}

	// Unknown username: run a dummy KDF so the timing matches a real verification.
	if errors.Is(err, sql.ErrNoRows) {
		// A zeroed salt and a zeroed hash. The KDF still runs for the full
		// iteration count, so an observer cannot distinguish this from a real
		// credential by timing alone.
		dummySalt := make([]byte, saltLength)
		_, _ = pbkdf2.Key(sha256.New, password, dummySalt, r.kdfIterations(), pbkdf2KeyLength)
		return Credential{}, ErrNotFound
	}
	if err != nil {
		return Credential{}, fmt.Errorf("adminauth: lookup: %w", err)
	}

	if disabled.Valid {
		// Same timing defence: run the KDF even though we already know no.
		_, _ = pbkdf2.Key(sha256.New, password, salt, r.kdfIterations(), pbkdf2KeyLength)
		return Credential{}, ErrNotFound
	}

	derived, err := pbkdf2.Key(sha256.New, password, salt, r.kdfIterations(), pbkdf2KeyLength)
	if err != nil {
		return Credential{}, fmt.Errorf("adminauth: pbkdf2: %w", err)
	}
	if subtle.ConstantTimeCompare(hash, derived) != 1 {
		return Credential{}, ErrNotFound
	}

	return r.finishVerify(ctx, password, c, created, lastUsed)
}

func (r *Registry) finishVerify(ctx context.Context, password string, c Credential, created, lastUsed sql.NullInt64) (Credential, error) {
	now := r.now()
	if _, err := r.db.ExecContext(ctx,
		`UPDATE admin_credentials SET last_used_at = ? WHERE id = ?`,
		now.Unix(), c.ID,
	); err != nil {
		return Credential{}, fmt.Errorf("adminauth: update last_used_at: %w", err)
	}

	c.CreatedAt = time.Unix(created.Int64, 0).UTC()
	c.LastUsedAt = &now
	return c, nil
}

// CredentialByLabel returns one credential by its label, or ErrNotFound.
func (r *Registry) CredentialByLabel(ctx context.Context, label string) (Credential, error) {
	label = cleanLabel(label)
	var c Credential
	var created, lastUsed, disabled, changedAt, resetAt sql.NullInt64
	var createdBy, disabledBy, resetBy sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT id, label, can_manage, created_at, last_used_at, disabled_at, created_by, disabled_by, must_change, changed_at, reset_by, reset_at
		   FROM admin_credentials WHERE label = ?`,
		label,
	).Scan(&c.ID, &c.Label, &c.CanManage, &created, &lastUsed, &disabled, &createdBy, &disabledBy, &c.MustChange, &changedAt, &resetBy, &resetAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	if err != nil {
		return Credential{}, fmt.Errorf("adminauth: load credential: %w", err)
	}
	c.CreatedAt = time.Unix(created.Int64, 0).UTC()
	if lastUsed.Valid {
		t := time.Unix(lastUsed.Int64, 0).UTC()
		c.LastUsedAt = &t
	}
	c.Disabled = disabled.Valid
	c.CreatedBy = createdBy.String
	c.DisabledBy = disabledBy.String
	c.ResetBy = resetBy.String
	if changedAt.Valid {
		t := time.Unix(changedAt.Int64, 0).UTC()
		c.ChangedAt = &t
	}
	if resetAt.Valid {
		t := time.Unix(resetAt.Int64, 0).UTC()
		c.ResetAt = &t
	}
	return c, nil
}

// List returns every credential, ordered by label. Secrets are never included.
func (r *Registry) List(ctx context.Context) ([]Credential, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, label, can_manage, created_at, last_used_at, disabled_at, created_by, disabled_by, must_change, changed_at, reset_by, reset_at
		   FROM admin_credentials ORDER BY label`)
	if err != nil {
		return nil, fmt.Errorf("adminauth: list: %w", err)
	}
	defer rows.Close()

	var out []Credential
	for rows.Next() {
		var c Credential
		var created int64
		var lastUsed, disabled, changedAt, resetAt sql.NullInt64
		var createdBy, disabledBy, resetBy sql.NullString
		if err := rows.Scan(&c.ID, &c.Label, &c.CanManage, &created, &lastUsed, &disabled, &createdBy, &disabledBy, &c.MustChange, &changedAt, &resetBy, &resetAt); err != nil {
			return nil, fmt.Errorf("adminauth: scan credential: %w", err)
		}
		c.CreatedAt = time.Unix(created, 0).UTC()
		if lastUsed.Valid {
			t := time.Unix(lastUsed.Int64, 0).UTC()
			c.LastUsedAt = &t
		}
		c.Disabled = disabled.Valid
		c.CreatedBy = createdBy.String
		c.DisabledBy = disabledBy.String
		c.ResetBy = resetBy.String
		if changedAt.Valid {
			t := time.Unix(changedAt.Int64, 0).UTC()
			c.ChangedAt = &t
		}
		if resetAt.Valid {
			t := time.Unix(resetAt.Int64, 0).UTC()
			c.ResetAt = &t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Remove disables a credential. The row is not deleted so that it can still be
// audited. Sessions belonging to it stop working on the next request because
// SessionForToken joins against admin_credentials and checks disabled_at.
func (r *Registry) Remove(ctx context.Context, label string) error {
	return r.RemoveWithActor(ctx, label, "")
}

// RemoveWithActor disables a credential and records who did it.
// Refuses to disable the last non-disabled manager, because somebody has to
// be able to get back in.
func (r *Registry) RemoveWithActor(ctx context.Context, label, actor string) error {
	label = cleanLabel(label)

	// Load the credential first to know whether it is a manager.
	cred, err := r.CredentialByLabel(ctx, label)
	if err != nil {
		return err
	}
	if cred.Disabled {
		return ErrNotFound
	}

	// If it is a manager, count how many non-disabled managers remain.
	if cred.CanManage {
		var managerCount int
		if err := r.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM admin_credentials WHERE can_manage = 1 AND disabled_at IS NULL`,
		).Scan(&managerCount); err != nil {
			return fmt.Errorf("adminauth: count managers: %w", err)
		}
		if managerCount <= 1 {
			return ErrLastManager
		}
	}

	res, err := r.db.ExecContext(ctx,
		`UPDATE admin_credentials SET disabled_at = ?, disabled_by = ? WHERE label = ? AND disabled_at IS NULL`,
		r.now().Unix(), nullIfEmpty(actor), label,
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

// SetManage grants can_manage to a credential.
func (r *Registry) SetManage(ctx context.Context, label string) error {
	label = cleanLabel(label)
	res, err := r.db.ExecContext(ctx,
		`UPDATE admin_credentials SET can_manage = 1 WHERE label = ? AND disabled_at IS NULL`,
		label)
	if err != nil {
		return fmt.Errorf("adminauth: set manage: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UnsetManage revokes can_manage from a credential.
func (r *Registry) UnsetManage(ctx context.Context, label string) error {
	label = cleanLabel(label)

	// Refuse to remove the last manager. Somebody has to be able to get in.
	var managerCount int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM admin_credentials WHERE can_manage = 1 AND disabled_at IS NULL`,
	).Scan(&managerCount); err != nil {
		return fmt.Errorf("adminauth: count managers: %w", err)
	}
	if managerCount <= 1 {
		return ErrLastManager
	}

	res, err := r.db.ExecContext(ctx,
		`UPDATE admin_credentials SET can_manage = 0 WHERE label = ? AND disabled_at IS NULL`,
		label)
	if err != nil {
		return fmt.Errorf("adminauth: unset manage: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ManagerCount returns how many non-disabled credentials have can_manage.
func (r *Registry) ManagerCount(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM admin_credentials WHERE can_manage = 1 AND disabled_at IS NULL`,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("adminauth: manager count: %w", err)
	}
	return n, nil
}

// SetPassword replaces the credential's password with a chosen one, clears
// must_change, and records changed_at. The old password stops working
// immediately because lookup, salt and hash are all replaced.
func (r *Registry) SetPassword(ctx context.Context, label, password string) error {
	label = cleanLabel(label)
	if len(password) < 12 {
		return errors.New("adminauth: password too short")
	}

	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("adminauth: entropy: %w", err)
	}
	lookup := sha256.Sum256([]byte(password))
	hash, err := pbkdf2.Key(sha256.New, password, salt, r.kdfIterations(), pbkdf2KeyLength)
	if err != nil {
		return fmt.Errorf("adminauth: pbkdf2: %w", err)
	}

	res, err := r.db.ExecContext(ctx,
		`UPDATE admin_credentials SET lookup = ?, hash = ?, salt = ?, must_change = 0, changed_at = ?
		 WHERE label = ? AND disabled_at IS NULL`,
		lookup[:], hash, salt, r.now().Unix(), label,
	)
	if err != nil {
		return fmt.Errorf("adminauth: set password: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResetPassword generates a new one-time password for a credential, sets
// must_change, and records who did it. Returns the new password once.
func (r *Registry) ResetPassword(ctx context.Context, label, actor string) (string, error) {
	label = cleanLabel(label)

	pw, err := generatePassword(passwordLength)
	if err != nil {
		return "", fmt.Errorf("adminauth: generate password: %w", err)
	}

	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("adminauth: entropy: %w", err)
	}
	lookup := sha256.Sum256([]byte(pw))
	hash, err := pbkdf2.Key(sha256.New, pw, salt, r.kdfIterations(), pbkdf2KeyLength)
	if err != nil {
		return "", fmt.Errorf("adminauth: pbkdf2: %w", err)
	}

	res, err := r.db.ExecContext(ctx,
		`UPDATE admin_credentials SET lookup = ?, hash = ?, salt = ?, must_change = 1, reset_by = ?, reset_at = ?
		 WHERE label = ? AND disabled_at IS NULL`,
		lookup[:], hash, salt, nullIfEmpty(actor), r.now().Unix(), label,
	)
	if err != nil {
		return "", fmt.Errorf("adminauth: reset password: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return "", ErrNotFound
	}
	return pw, nil
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
		`SELECT c.id, c.label, c.can_manage, c.created_at, c.last_used_at, c.must_change
		   FROM admin_sessions s
		   JOIN admin_credentials c ON c.id = s.credential_id
		  WHERE s.token_hash = ? AND s.expires_at > ? AND c.disabled_at IS NULL`,
		tokenHash[:], r.now().Unix(),
	).Scan(&c.ID, &c.Label, &c.CanManage, &created, &lastUsed, &c.MustChange)
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

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
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
