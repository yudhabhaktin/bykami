// Package loyalty implements the #SobatKAMi points ledger.
//
// The design rule, stated in the architecture record and enforced here and in
// the schema: an append-only ledger, never a mutable balance column. A balance
// is SUM(points). Mutable totals drift, cannot be audited, and produce disputes
// that cannot be resolved. Here, every point is traceable to the event that
// created it, and a mistake is corrected with a compensating entry rather than
// an edit — which means the correction is auditable too.
package loyalty

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/store"
)

type Kind string

const (
	// Earn credits points. Always positive, always idempotent.
	Earn Kind = "earn"
	// Burn spends points. Always negative.
	Burn Kind = "burn"
	// Adjust is the compensating entry — the only way to correct history, and
	// the reason nothing else needs to be mutable. May go either direction.
	Adjust Kind = "adjust"
)

var (
	// ErrInsufficientPoints means the burn would overdraw the balance.
	ErrInsufficientPoints = errors.New("loyalty: insufficient points")
	// ErrNonPositive means an earn or burn was asked for with no magnitude.
	ErrNonPositive = errors.New("loyalty: points must be greater than zero")
	// ErrNoIdempotencyKey means an earn arrived without the key that makes it
	// safe to retry. Required rather than optional so a caller cannot opt out
	// of the guarantee by forgetting.
	ErrNoIdempotencyKey = errors.New("loyalty: earn requires an idempotency key")
)

type Entry struct {
	ID             string
	UserID         string
	Vertical       string
	Kind           Kind
	Points         int64
	ReferenceID    string
	IdempotencyKey string
	CreatedAt      time.Time
}

type Ledger struct{ db *sql.DB }

func New(db *sql.DB) *Ledger { return &Ledger{db: db} }

// Earn credits points for an event, exactly once per idempotency key.
//
// A retried payment webhook is the motivating case: the gateway may deliver the
// same event several times, and each delivery calls this. The second call
// returns the entry the first one wrote, with no second credit — so callers can
// retry freely without reconciling anything.
func (l *Ledger) Earn(ctx context.Context, userID, vertical string, points int64, referenceID, idempotencyKey string) (Entry, error) {
	if points <= 0 {
		return Entry{}, ErrNonPositive
	}
	if idempotencyKey == "" {
		return Entry{}, ErrNoIdempotencyKey
	}

	e, err := l.insert(ctx, userID, vertical, Earn, points, referenceID, idempotencyKey)
	if err == nil {
		return e, nil
	}

	// The unique index fired, so this exact event was already recorded. That is
	// success, not failure — return what is already there. Reading it back
	// rather than reconstructing it means the caller sees the original entry's
	// id and timestamp, which is what a reconciliation report needs.
	if store.IsConstraint(err) {
		existing, findErr := l.byIdempotencyKey(ctx, idempotencyKey)
		if findErr == nil {
			return existing, nil
		}
		return Entry{}, fmt.Errorf("loyalty: earn conflicted but original not found: %w", findErr)
	}
	return Entry{}, err
}

// Burn spends points, refusing to overdraw.
//
// The balance check and the insert share one transaction. With the store's
// single writer that makes them atomic, so two concurrent redemptions of the
// last 100 points cannot both succeed — the second sees the first's entry.
func (l *Ledger) Burn(ctx context.Context, userID, vertical string, points int64, referenceID, idempotencyKey string) (Entry, error) {
	if points <= 0 {
		return Entry{}, ErrNonPositive
	}

	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, fmt.Errorf("loyalty: begin: %w", err)
	}
	defer tx.Rollback()

	var balance int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(points), 0) FROM loyalty_entries WHERE user_id = ?`, userID,
	).Scan(&balance); err != nil {
		return Entry{}, fmt.Errorf("loyalty: balance: %w", err)
	}
	if balance < points {
		return Entry{}, fmt.Errorf("%w: have %d, need %d", ErrInsufficientPoints, balance, points)
	}

	e := Entry{
		ID:             newID(),
		UserID:         userID,
		Vertical:       vertical,
		Kind:           Burn,
		Points:         -points,
		ReferenceID:    referenceID,
		IdempotencyKey: idempotencyKey,
		CreatedAt:      time.Now().UTC().Truncate(time.Second),
	}
	if err := insertTx(ctx, tx, e); err != nil {
		return Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, fmt.Errorf("loyalty: commit: %w", err)
	}
	return e, nil
}

// Adjust writes a compensating entry. Points may be positive or negative; this
// is the documented way to fix a mistake, and the only one the schema allows.
func (l *Ledger) Adjust(ctx context.Context, userID, vertical string, points int64, reason string) (Entry, error) {
	if points == 0 {
		return Entry{}, ErrNonPositive
	}
	return l.insert(ctx, userID, vertical, Adjust, points, reason, "")
}

// Balance is SUM(points), computed every time. Never cached, never stored.
func (l *Ledger) Balance(ctx context.Context, userID string) (int64, error) {
	var balance int64
	if err := l.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(points), 0) FROM loyalty_entries WHERE user_id = ?`, userID,
	).Scan(&balance); err != nil {
		return 0, fmt.Errorf("loyalty: balance: %w", err)
	}
	return balance, nil
}

// History returns a user's entries, newest first — the statement a customer
// support conversation is actually resolved with.
func (l *Ledger) History(ctx context.Context, userID string, limit int) ([]Entry, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT id, user_id, vertical, kind, points,
		        COALESCE(reference_id, ''), COALESCE(idempotency_key, ''), created_at
		 FROM loyalty_entries
		 WHERE user_id = ?
		 ORDER BY created_at DESC, id DESC
		 LIMIT ?`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("loyalty: history: %w", err)
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		var ts int64
		if err := rows.Scan(&e.ID, &e.UserID, &e.Vertical, &e.Kind, &e.Points,
			&e.ReferenceID, &e.IdempotencyKey, &ts); err != nil {
			return nil, fmt.Errorf("loyalty: scan: %w", err)
		}
		e.CreatedAt = time.Unix(ts, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

func (l *Ledger) insert(ctx context.Context, userID, vertical string, kind Kind, points int64, referenceID, idempotencyKey string) (Entry, error) {
	e := Entry{
		ID:             newID(),
		UserID:         userID,
		Vertical:       vertical,
		Kind:           kind,
		Points:         points,
		ReferenceID:    referenceID,
		IdempotencyKey: idempotencyKey,
		CreatedAt:      time.Now().UTC().Truncate(time.Second),
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, fmt.Errorf("loyalty: begin: %w", err)
	}
	defer tx.Rollback()
	if err := insertTx(ctx, tx, e); err != nil {
		return Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, fmt.Errorf("loyalty: commit: %w", err)
	}
	return e, nil
}

func insertTx(ctx context.Context, tx *sql.Tx, e Entry) error {
	// NULL rather than "" for the optional columns: the partial unique index on
	// idempotency_key skips NULLs, so storing an empty string would make every
	// keyless entry collide with the next one.
	_, err := tx.ExecContext(ctx,
		`INSERT INTO loyalty_entries
		   (id, user_id, vertical, kind, points, reference_id, idempotency_key, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.UserID, e.Vertical, string(e.Kind), e.Points,
		nullIfEmpty(e.ReferenceID), nullIfEmpty(e.IdempotencyKey), e.CreatedAt.Unix())
	if err != nil {
		return fmt.Errorf("loyalty: insert: %w", err)
	}
	return nil
}

func (l *Ledger) byIdempotencyKey(ctx context.Context, key string) (Entry, error) {
	var e Entry
	var ts int64
	err := l.db.QueryRowContext(ctx,
		`SELECT id, user_id, vertical, kind, points,
		        COALESCE(reference_id, ''), COALESCE(idempotency_key, ''), created_at
		 FROM loyalty_entries WHERE idempotency_key = ?`, key,
	).Scan(&e.ID, &e.UserID, &e.Vertical, &e.Kind, &e.Points,
		&e.ReferenceID, &e.IdempotencyKey, &ts)
	if err != nil {
		return Entry{}, err
	}
	e.CreatedAt = time.Unix(ts, 0).UTC()
	return e, nil
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
		// crypto/rand does not fail on any supported platform; if it does, the
		// process has no business minting identifiers.
		panic("loyalty: entropy unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
