// Package mfa keeps the operator authenticators and checks their codes.
//
// It is the stateful half of internal/totp: the arithmetic there is pure, and
// everything that has to be remembered between two sign-ins is here — which
// secret belongs to which number, which time step has already been spent, and
// how many wrong guesses have arrived lately.
//
// It knows nothing about who is allowed into the console. That is the
// allow-list in internal/admin, checked separately on every request, and the
// separation is deliberate: enrolling somebody here grants no access, so
// enrolment can be an ordinary shell command instead of a privileged one. The
// two questions are "is this really that number" and "does that number matter",
// and keeping them apart is what stops the second one drifting into a table
// somebody can write to.
package mfa

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/phone"
	"github.com/bhaktiyudha/bykami/api/internal/totp"
)

const (
	// Consecutive failures before the enrolment is locked, and for how long.
	//
	// Six digits across a three-step window is roughly one guess in three
	// hundred thousand. Five tries a quarter of an hour puts a determined
	// guesser at years of continuous attempts, while an operator who fat-fingers
	// a code twice notices nothing.
	maxFailures = 5
	lockout     = 15 * time.Minute
)

var (
	// ErrNotEnrolled means that number has no authenticator.
	ErrNotEnrolled = errors.New("mfa: no authenticator is enrolled for that number")
	// ErrAlreadyEnrolled means one is already there. Enrolling again would
	// silently invalidate whatever is on the operator's phone, so it is refused
	// rather than overwritten — revoke first, deliberately.
	ErrAlreadyEnrolled = errors.New("mfa: an authenticator is already enrolled for that number")
	// ErrLockedOut means too many wrong codes arrived recently.
	ErrLockedOut = errors.New("mfa: too many failed attempts")
	// ErrBadCode covers a wrong code and a reused one. One error on purpose,
	// matching identity.ErrInvalidCode: telling the two apart tells an attacker
	// whether the number they guessed is enrolled.
	ErrBadCode = errors.New("mfa: wrong or already-used code")
)

// Registry is the set of enrolled authenticators.
type Registry struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Registry {
	return &Registry{db: db, now: func() time.Time { return time.Now().UTC() }}
}

// Enrolment is one operator's authenticator, without its secret. Nothing
// returns the secret after enrolment: it exists to be shown once, at the
// moment it is created, and a listing that carried it would put every
// operator's credential on the screen of whoever ran the command.
type Enrolment struct {
	Phone       string
	CreatedAt   time.Time
	LastUsed    time.Time // zero until the first successful sign-in
	LockedUntil time.Time // zero when not locked
}

// Locked reports whether this enrolment is refusing codes as at t.
func (e Enrolment) Locked(t time.Time) bool {
	return !e.LockedUntil.IsZero() && e.LockedUntil.After(t)
}

// Enroll generates a secret for a number and returns it with the normalised
// number. The secret is returned exactly once, here; it is stored but never
// read back out.
func (r *Registry) Enroll(ctx context.Context, rawPhone string) (string, []byte, error) {
	e164, err := phone.Normalize(rawPhone)
	if err != nil {
		return "", nil, err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, fmt.Errorf("mfa: begin: %w", err)
	}
	defer tx.Rollback()

	// Checked rather than left to the primary key, so that a genuine conflict
	// is distinguishable from a CHECK constraint firing on a bug.
	var exists int
	switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM admin_totp WHERE phone = ?`, e164).Scan(&exists); {
	case err == nil:
		return "", nil, ErrAlreadyEnrolled
	case !errors.Is(err, sql.ErrNoRows):
		return "", nil, fmt.Errorf("mfa: check enrolment: %w", err)
	}

	secret := totp.NewSecret()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO admin_totp (phone, secret, created_at) VALUES (?, ?, ?)`,
		e164, secret, r.now().Unix(),
	); err != nil {
		return "", nil, fmt.Errorf("mfa: enrol: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", nil, fmt.Errorf("mfa: commit: %w", err)
	}
	return e164, secret, nil
}

// Revoke removes an authenticator. This is how a lost phone is handled, and it
// is immediate: the codes that phone shows stop working on the next request.
func (r *Registry) Revoke(ctx context.Context, rawPhone string) error {
	e164, err := phone.Normalize(rawPhone)
	if err != nil {
		return err
	}

	res, err := r.db.ExecContext(ctx, `DELETE FROM admin_totp WHERE phone = ?`, e164)
	if err != nil {
		return fmt.Errorf("mfa: revoke: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mfa: revoke: %w", err)
	}
	if n == 0 {
		return ErrNotEnrolled
	}
	return nil
}

// Unlock clears a lockout early, for the operator who is locked out ten minutes
// before an event. It does not touch the secret, so it is not a way in: whoever
// runs it still needs the authenticator to sign in afterwards.
func (r *Registry) Unlock(ctx context.Context, rawPhone string) error {
	e164, err := phone.Normalize(rawPhone)
	if err != nil {
		return err
	}

	res, err := r.db.ExecContext(ctx,
		`UPDATE admin_totp SET fail_count = 0, locked_until = NULL WHERE phone = ?`, e164)
	if err != nil {
		return fmt.Errorf("mfa: unlock: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mfa: unlock: %w", err)
	}
	if n == 0 {
		return ErrNotEnrolled
	}
	return nil
}

// List returns every enrolment, oldest first.
func (r *Registry) List(ctx context.Context) ([]Enrolment, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT phone, created_at, last_step, locked_until
		   FROM admin_totp
		  ORDER BY created_at, phone`)
	if err != nil {
		return nil, fmt.Errorf("mfa: list: %w", err)
	}
	defer rows.Close()

	var out []Enrolment
	for rows.Next() {
		var (
			e       Enrolment
			created int64
			step    sql.NullInt64
			locked  sql.NullInt64
		)
		if err := rows.Scan(&e.Phone, &created, &step, &locked); err != nil {
			return nil, fmt.Errorf("mfa: list: %w", err)
		}
		e.CreatedAt = time.Unix(created, 0).UTC()
		if step.Valid {
			// A step is unix seconds divided by the period, so this recovers
			// the instant it began — which is the last time this authenticator
			// was used, to within half a minute.
			e.LastUsed = time.Unix(step.Int64*int64(totp.Period/time.Second), 0).UTC()
		}
		if locked.Valid {
			e.LockedUntil = time.Unix(locked.Int64, 0).UTC()
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Count is how many operators can sign in. The login page asks, so that a
// console nobody has enrolled against says so instead of refusing every
// correct code with no explanation.
func (r *Registry) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_totp`).Scan(&n); err != nil {
		return 0, fmt.Errorf("mfa: count: %w", err)
	}
	return n, nil
}

// Verify checks one code and, on success, spends the time step it belongs to.
//
// Everything happens in one transaction, which is what makes the replay guard
// real: two requests arriving with the same code in the same second must not
// both find an unspent step.
func (r *Registry) Verify(ctx context.Context, rawPhone, code string) error {
	e164, err := phone.Normalize(rawPhone)
	if err != nil {
		return err
	}
	now := r.now()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mfa: begin: %w", err)
	}
	defer tx.Rollback()

	var (
		secret   []byte
		lastStep sql.NullInt64
		failures int
		locked   sql.NullInt64
	)
	err = tx.QueryRowContext(ctx,
		`SELECT secret, last_step, fail_count, locked_until FROM admin_totp WHERE phone = ?`, e164,
	).Scan(&secret, &lastStep, &failures, &locked)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotEnrolled
	}
	if err != nil {
		return fmt.Errorf("mfa: load enrolment: %w", err)
	}

	// Checked before the code, so that a locked-out enrolment costs a guesser
	// an answer rather than an attempt.
	if locked.Valid && now.Unix() < locked.Int64 {
		return ErrLockedOut
	}

	step, ok := totp.Verify(secret, code, now)

	// A code from a step that has already been spent is refused, and counts as
	// a failure like any other. The operator whose second sign-in lands inside
	// the same half-minute waits for the next code; an attacker replaying one
	// they read over a shoulder gets nothing and burns an attempt.
	if ok && lastStep.Valid && step <= lastStep.Int64 {
		ok = false
	}

	if !ok {
		if err := recordFailure(ctx, tx, e164, failures, now); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("mfa: commit failure: %w", err)
		}
		return ErrBadCode
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE admin_totp SET last_step = ?, fail_count = 0, locked_until = NULL WHERE phone = ?`,
		step, e164,
	); err != nil {
		return fmt.Errorf("mfa: spend step: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mfa: commit: %w", err)
	}
	return nil
}

// recordFailure counts a wrong code, and locks the enrolment once they run out.
// The counter resets when the lock is set rather than accumulating, so a lock
// is served once per burst instead of every subsequent attempt extending it.
func recordFailure(ctx context.Context, tx *sql.Tx, e164 string, failures int, now time.Time) error {
	if failures+1 < maxFailures {
		if _, err := tx.ExecContext(ctx,
			`UPDATE admin_totp SET fail_count = fail_count + 1 WHERE phone = ?`, e164,
		); err != nil {
			return fmt.Errorf("mfa: record failure: %w", err)
		}
		return nil
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE admin_totp SET fail_count = 0, locked_until = ? WHERE phone = ?`,
		now.Add(lockout).Unix(), e164,
	); err != nil {
		return fmt.Errorf("mfa: lock out: %w", err)
	}
	return nil
}
