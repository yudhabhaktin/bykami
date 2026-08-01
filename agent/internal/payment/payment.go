// Package payment is the booth's shutter lock: QRIS at the screen, before the
// camera will fire.
//
// # Why this exists, when the design record said it would not
//
// design/kiosk.md dropped per-session payment on the grounds that nobody pays
// at this studio — `BOOKING TANPA DP`, the customer pays a human at the
// counter. That reasoning holds only for an attended studio. A self-service
// booth has nobody standing between a stranger and the camera, so the payment
// *is* the attendant, and dropping it means anyone who walks up gets a free
// session and a free print. The decision is reversed there; this package is the
// consequence.
//
// # The state machine kiosk.md warned about
//
// The dropped design was "a slot held while a QRIS code races a webhook against
// a timeout", which is where most self-built booking systems carry their worst
// bugs. That shape is avoided here, and the difference is worth stating because
// it looks superficially identical:
//
//   - Nothing is reserved. There is no slot, no inventory and no hold to expire
//     wrongly — only this booth, whose single-live-session index already says
//     one customer at a time.
//   - Settlement is pulled, never pushed. The booth is at http://localhost with
//     no inbound path, so a gateway webhook cannot reach it. It polls, which
//     also means a lost callback is a slow answer rather than a stuck screen.
//   - Money is authoritative in the cloud. This table records what the booth
//     was told, so that the shutter keeps working when the network does not.
//
// # There is no real provider yet
//
// QRIS means Xendit, which is blocked on a business entity, NPWP and a bank
// account — days to weeks, entirely outside the build. Until that exists the
// only implementation is [Simulated], which cmd/bykami-agent requires an
// explicit flag to select and warns about at startup, in the same way api/
// requires -otp-delivery=log for its placeholder sender.
package payment

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/store"
)

// DefaultTTL is how long a QR code is good for.
//
// Long enough to find the app, fumble the PIN and try again; short enough that
// a customer who wandered off does not hold the booth. The screen shows the
// countdown, because an unexplained expiry looks like a broken machine.
const DefaultTTL = 5 * time.Minute

type State string

const (
	Pending State = "pending"
	Settled State = "settled"
	Expired State = "expired"
	Failed  State = "failed"
)

// Kind is what a payment buys.
//
// One session has exactly one [Session] payment and any number of [Reprint]
// ones. They are separated because they unlock different things: the session
// payment releases the shutter, a reprint payment adds a single sheet to what
// the customer may take away, and a booth that confused the two would either
// hand out free prints or lock the camera after the first reprint expired.
type Kind string

const (
	SessionKind Kind = "session"
	Reprint     Kind = "reprint"
)

var (
	ErrNotFound = errors.New("payment: not found")

	// ErrNotConfigured is what every route answers when no provider is
	// selected. The booth is then a screen that says "pay at the counter",
	// which is a working studio rather than a broken booth.
	ErrNotConfigured = errors.New("payment: no provider is configured")
)

type Payment struct {
	ID         string
	SessionID  string
	Provider   string
	ExternalID string
	AmountIDR  int64
	QRPayload  string
	Kind       Kind
	State      State
	CreatedAt  time.Time
	ExpiresAt  time.Time
	SettledAt  time.Time
}

// Charge is what a provider returns when a QR code has been minted.
type Charge struct {
	// ExternalID is the provider's id, and this package's idempotency key.
	ExternalID string
	// QRPayload is the QRIS string the customer's banking app scans. The kiosk
	// renders it; nothing here draws pixels.
	QRPayload string
	ExpiresAt time.Time
}

// Provider is a payment gateway. Two methods, and the second one is a poll
// rather than a callback because the booth has no inbound path.
type Provider interface {
	Name() string
	Create(ctx context.Context, sessionID string, amountIDR int64) (Charge, error)
	// Status is asked repeatedly while a customer stands at the screen, so it
	// must be cheap and it must be safe to call after settlement.
	Status(ctx context.Context, externalID string) (State, error)
}

type Store struct {
	db       *sql.DB
	provider Provider
	ttl      time.Duration
	now      func() time.Time
}

type Option func(*Store)

func WithTTL(d time.Duration) Option      { return func(s *Store) { s.ttl = d } }
func WithClock(f func() time.Time) Option { return func(s *Store) { s.now = f } }

// New returns the store. provider may be nil, which is the deployed default and
// makes every operation answer ErrNotConfigured.
func New(db *sql.DB, provider Provider, opts ...Option) *Store {
	s := &Store{db: db, provider: provider, ttl: DefaultTTL, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Enabled reports whether payment can be taken at all. The kiosk asks so it can
// say why the screen looks different, rather than failing at the tap.
func (s *Store) Enabled() bool { return s.provider != nil }

// Create mints a QR code for a session and records it as pending.
func (s *Store) Create(ctx context.Context, sessionID string, amountIDR int64, kind Kind) (Payment, error) {
	if s.provider == nil {
		return Payment{}, ErrNotConfigured
	}
	if amountIDR <= 0 {
		return Payment{}, errors.New("payment: amount must be positive")
	}
	if kind != SessionKind && kind != Reprint {
		return Payment{}, fmt.Errorf("payment: unknown kind %q", kind)
	}

	charge, err := s.provider.Create(ctx, sessionID, amountIDR)
	if err != nil {
		return Payment{}, fmt.Errorf("payment: create: %w", err)
	}
	if charge.ExternalID == "" {
		return Payment{}, errors.New("payment: provider returned no id")
	}

	now := s.now()
	expires := charge.ExpiresAt
	if expires.IsZero() {
		expires = now.Add(s.ttl)
	}

	p := Payment{
		ID:         newID(),
		SessionID:  sessionID,
		Provider:   s.provider.Name(),
		ExternalID: charge.ExternalID,
		AmountIDR:  amountIDR,
		QRPayload:  charge.QRPayload,
		Kind:       kind,
		State:      Pending,
		CreatedAt:  now,
		ExpiresAt:  expires,
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO payments (id, session_id, provider, external_id, amount_idr, qr_payload, kind, state, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		p.ID, p.SessionID, p.Provider, p.ExternalID, p.AmountIDR, nullIfEmpty(p.QRPayload),
		string(p.Kind), p.CreatedAt.Unix(), p.ExpiresAt.Unix(),
	)
	switch {
	case store.IsConstraint(err):
		// The provider handed back an id we already hold, which means the
		// charge exists. Returning it is the idempotent answer.
		return s.ByExternalID(ctx, charge.ExternalID)
	case err != nil:
		return Payment{}, fmt.Errorf("payment: record: %w", err)
	}
	return p, nil
}

// Refresh polls the provider for a pending payment and stores what it says.
//
// Expiry is decided locally as well as remotely: a provider that is slow or
// unreachable must not leave a customer staring at a dead QR code with no way
// forward.
func (s *Store) Refresh(ctx context.Context, id string) (Payment, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return Payment{}, err
	}
	if p.State != Pending {
		return p, nil
	}
	if s.provider == nil {
		return Payment{}, ErrNotConfigured
	}

	state, err := s.provider.Status(ctx, p.ExternalID)
	if err != nil {
		// The customer is standing at the screen. A failed poll is not a failed
		// payment, so the row is left pending and the kiosk asks again.
		return p, fmt.Errorf("payment: status: %w", err)
	}

	if state == Pending && !s.now().Before(p.ExpiresAt) {
		state = Expired
	}
	if state == Pending {
		return p, nil
	}
	return s.setState(ctx, p, state)
}

// MarkSettled records a settled payment. Separate from Refresh so that the
// simulated provider's "the customer paid" button has one path in.
func (s *Store) MarkSettled(ctx context.Context, id string) (Payment, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return Payment{}, err
	}
	if p.State == Settled {
		return p, nil
	}
	return s.setState(ctx, p, Settled)
}

func (s *Store) setState(ctx context.Context, p Payment, state State) (Payment, error) {
	var settled any
	if state == Settled {
		settled = s.now().Unix()
	}
	// Guarded on the current state so that two concurrent pollers cannot both
	// settle a payment, and so a settled payment cannot be expired afterwards
	// by a slow poll that started earlier.
	res, err := s.db.ExecContext(ctx,
		`UPDATE payments SET state = ?, settled_at = ? WHERE id = ? AND state = 'pending'`,
		string(state), settled, p.ID)
	if err != nil {
		return Payment{}, fmt.Errorf("payment: set state: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Someone else moved it first. Their answer is the real one.
		return s.Get(ctx, p.ID)
	}
	return s.Get(ctx, p.ID)
}

func (s *Store) Get(ctx context.Context, id string) (Payment, error) {
	return s.one(ctx, `SELECT `+columns+` FROM payments WHERE id = ?`, id)
}

func (s *Store) ByExternalID(ctx context.Context, externalID string) (Payment, error) {
	return s.one(ctx, `SELECT `+columns+` FROM payments WHERE external_id = ?`, externalID)
}

// Latest returns the most recent payment for a session, which is the one the
// screen is showing.
//
// Ties break on rowid, which is insertion order. created_at has one-second
// resolution and a customer buying a reprint produces a second payment against
// a session that already has one — comfortably inside the same second when the
// booth is quick. The previous tiebreaker was the id, which is random hex, so
// whichever charge won was a coin toss: the screen would poll the session's
// old settled payment, see "settled" immediately, and hand out a sheet nobody
// had scanned a QR code for.
func (s *Store) Latest(ctx context.Context, sessionID string) (Payment, error) {
	return s.one(ctx,
		`SELECT `+columns+` FROM payments WHERE session_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`,
		sessionID)
}

// SettledReprints counts the extra prints a session has actually paid for.
//
// Settled only. A pending reprint is a QR code on screen that nobody has
// scanned yet, and treating it as bought would let a customer take a sheet by
// opening the dialog and walking away.
func (s *Store) SettledReprints(ctx context.Context, sessionID string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM payments WHERE session_id = ? AND kind = 'reprint' AND state = 'settled'`,
		sessionID,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("payment: settled reprints: %w", err)
	}
	return n, nil
}

const columns = `id, session_id, provider, external_id, amount_idr, qr_payload, kind, state,
	created_at, expires_at, settled_at`

func (s *Store) one(ctx context.Context, query string, args ...any) (Payment, error) {
	var (
		p         Payment
		qr        sql.NullString
		settledAt sql.NullInt64
		created   int64
		expires   int64
		kind      string
		state     string
	)
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&p.ID, &p.SessionID, &p.Provider, &p.ExternalID, &p.AmountIDR, &qr, &kind, &state,
		&created, &expires, &settledAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Payment{}, ErrNotFound
	case err != nil:
		return Payment{}, fmt.Errorf("payment: query: %w", err)
	}
	p.QRPayload, p.Kind, p.State = qr.String, Kind(kind), State(state)
	p.CreatedAt, p.ExpiresAt = time.Unix(created, 0), time.Unix(expires, 0)
	if settledAt.Valid {
		p.SettledAt = time.Unix(settledAt.Int64, 0)
	}
	return p, nil
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
		panic("payment: entropy unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
