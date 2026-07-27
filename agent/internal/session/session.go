// Package session owns the booth's lifecycle: what is open, what a photo
// belongs to, and when the customer is done.
//
// The kiosk owns an explicit lifecycle and the hot folder is a stream the agent
// attributes to it. That asymmetry is the whole design — the camera has no idea
// a session exists, so nothing about attribution can depend on the camera
// cooperating.
package session

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

// DefaultGrace is how long after [Store.Close] a late file still attaches to
// the session it plainly belongs to.
//
// Vendor software writes when it writes: a frame fired a second before the
// customer tapped "Selesai" can land afterwards. Without a grace window that
// frame becomes an orphan, which means a customer who paid for it does not
// receive it — the failure this window exists to prevent.
const DefaultGrace = 20 * time.Second

// DefaultTakeLimit is `maksimal 15x take` from the studio's price list.
//
// Enforceable at capture now that the app owns the shutter rather than the
// customer's hand. It is still not the last word: the selection step enforces
// what was actually bought, because a stray file in the folder must never
// become a free print.
const DefaultTakeLimit = 15

var (
	// ErrAlreadyOpen means a session is running. There is one camera, one screen
	// and one customer, so a second live session would make the attribution of
	// an incoming file ambiguous.
	ErrAlreadyOpen = errors.New("session: another session is live")

	ErrNoSession = errors.New("session: no session is open")
	ErrNotFound  = errors.New("session: not found")

	// ErrTakeLimit is the shutter refusing. The customer sees a count, not this.
	ErrTakeLimit = errors.New("session: take limit reached")

	// ErrNotPaid is the other way the shutter refuses. A self-service booth has
	// nobody standing between a stranger and the camera, so this is the
	// attendant.
	ErrNotPaid = errors.New("session: not paid for")
)

type State string

const (
	// AwaitingPayment is where every session starts. The QR code is on screen
	// and the shutter is locked.
	AwaitingPayment State = "awaiting_payment"

	Open   State = "open"
	Closed State = "closed"

	// Abandoned is a customer who walked away from the QR code. A state rather
	// than a deleted row: the payment attempt behind it can still settle at the
	// gateway a minute later, so what it points at has to survive.
	Abandoned State = "abandoned"
)

// Package is what the customer bought. Copied onto the session rather than
// referenced, because the catalogue is content that will change and a session
// has to still say what it was six weeks later.
type Package struct {
	ID          string
	Name        string
	PriceIDR    int64
	TemplateID  string
	PrintCopies int
	TakeLimit   int
}

type Session struct {
	ID        string
	OutletID  string
	State     State
	Package   Package
	TakeLimit int
	OpenedAt  time.Time
	PaidAt    time.Time
	ClosedAt  time.Time

	Phone            string
	ConsentVersion   string
	ConsentedAt      time.Time
	MarketingConsent bool
}

// Live reports whether this session holds the booth.
func (s Session) Live() bool { return s.State == AwaitingPayment || s.State == Open }

type Store struct {
	db    *sql.DB
	grace time.Duration

	// now is swapped in tests. Attribution is entirely about time, so a test
	// that cannot control the clock has to sleep through the grace window to
	// prove anything about it.
	now func() time.Time
}

type Option func(*Store)

func WithGrace(d time.Duration) Option    { return func(s *Store) { s.grace = d } }
func WithClock(f func() time.Time) Option { return func(s *Store) { s.now = f } }

func New(db *sql.DB, opts ...Option) *Store {
	s := &Store{db: db, grace: DefaultGrace, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Start creates a session awaiting payment. The shutter stays locked until
// [Store.MarkPaid].
//
// Fails while another session is live rather than replacing it: taking the
// booth from a customer who is mid-payment is worse than telling the next one
// to wait a moment.
func (s *Store) Start(ctx context.Context, outletID string, pkg Package) (Session, error) {
	takeLimit := pkg.TakeLimit
	if takeLimit <= 0 {
		takeLimit = DefaultTakeLimit
	}
	now := s.now()
	sess := Session{
		ID:        newID(),
		OutletID:  outletID,
		State:     AwaitingPayment,
		Package:   pkg,
		TakeLimit: takeLimit,
		OpenedAt:  now,
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions
		   (id, outlet_id, state, package_id, package_name, price_idr, template_id, print_copies, take_limit, opened_at)
		 VALUES (?, ?, 'awaiting_payment', ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, outletID, pkg.ID, pkg.Name, pkg.PriceIDR, pkg.TemplateID, pkg.PrintCopies,
		takeLimit, now.Unix(),
	)
	switch {
	case store.IsConstraint(err):
		// The partial unique index, not a race we lost. Either way the answer
		// for the caller is the same.
		return Session{}, ErrAlreadyOpen
	case err != nil:
		return Session{}, fmt.Errorf("session: start: %w", err)
	}
	return sess, nil
}

// MarkPaid releases the shutter. Idempotent: the kiosk polls for settlement,
// so the same answer arrives more than once by design.
func (s *Store) MarkPaid(ctx context.Context, id string) (Session, error) {
	now := s.now()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET state = 'open', paid_at = ? WHERE id = ? AND state = 'awaiting_payment'`,
		now.Unix(), id,
	); err != nil {
		return Session{}, fmt.Errorf("session: mark paid: %w", err)
	}
	return s.Get(ctx, id)
}

// Abandon releases the booth from a session that was never paid for.
//
// The row stays. A paid session is closed, never abandoned — the money moved
// and the record has to say so — and an unpaid one still has a charge behind it
// that the gateway could settle after the customer has gone.
func (s *Store) Abandon(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET state = 'abandoned', closed_at = ?
		  WHERE id = ? AND state = 'awaiting_payment'`,
		s.now().Unix(), id)
	if err != nil {
		return fmt.Errorf("session: abandon: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Close ends the session. Stragglers still attach for the grace window.
func (s *Store) Close(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET state = 'closed', closed_at = ? WHERE id = ? AND state = 'open'`,
		s.now().Unix(), id,
	)
	if err != nil {
		return fmt.Errorf("session: close: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("session: close: %w", err)
	}
	if n == 0 {
		// Already closed, or never existed. Idempotent on purpose: the kiosk can
		// send "Selesai" twice, and a customer walking away mid-flow means a
		// timeout will close it from the other side.
		return nil
	}
	return nil
}

// Current returns the session holding the booth, paid for or not. The kiosk
// needs the unpaid one too — that is the screen showing the QR code.
func (s *Store) Current(ctx context.Context) (Session, bool, error) {
	sess, err := s.scanOne(ctx,
		`SELECT `+columns+` FROM sessions WHERE state IN ('awaiting_payment', 'open')`)
	switch {
	case errors.Is(err, ErrNotFound):
		return Session{}, false, nil
	case err != nil:
		return Session{}, false, err
	}
	return sess, true, nil
}

func (s *Store) Get(ctx context.Context, id string) (Session, error) {
	return s.scanOne(ctx, `SELECT `+columns+` FROM sessions WHERE id = ?`, id)
}

// Recent returns the newest sessions, for the operator view.
func (s *Store) Recent(ctx context.Context, limit int) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+columns+` FROM sessions ORDER BY opened_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("session: recent: %w", err)
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		sess, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("session: recent: %w", err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// Attribute decides which session a file captured at t belongs to, and reports
// false for an orphan.
//
// t is the filesystem mtime, never EXIF. Camera clocks drift and nobody resets
// them after a battery change, so EXIF is metadata and this is the truth.
//
// Three outcomes, in the order they are checked:
//
//   - the open session, if the file was written after it opened;
//   - the most recently closed session, if the file landed inside its grace
//     window — a straggler;
//   - nothing. Staff test shots and accidental fires between sessions are
//     orphans, kept and shown in admin rather than discarded, because a
//     customer's frame that missed by a second looks exactly the same until a
//     human decides.
func (s *Store) Attribute(ctx context.Context, t time.Time) (string, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM sessions
		  WHERE state = 'open' AND opened_at <= ?
		  UNION ALL
		  SELECT id FROM (
		    SELECT id, closed_at FROM sessions
		     WHERE state = 'closed' AND opened_at <= ? AND ? <= closed_at + ?
		     ORDER BY closed_at DESC LIMIT 1
		  )
		  LIMIT 1`,
		t.Unix(), t.Unix(), t.Unix(), int64(s.grace.Seconds()),
	).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("session: attribute: %w", err)
	}
	return id, true, nil
}

// Takes counts the frames attributed to a session so far.
func (s *Store) Takes(ctx context.Context, id string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM photos WHERE session_id = ?`, id,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("session: takes: %w", err)
	}
	return n, nil
}

// MayFire reports whether the shutter may be released, and returns the open
// session so the caller does not have to look it up again.
//
// Counts landed frames, not fired shutters. The difference is one in-flight
// frame during the second or two a file takes to arrive, which is why the
// capture coordinator serialises fires — and why kiosk.md keeps the selection
// step as the backstop for what was actually bought.
func (s *Store) MayFire(ctx context.Context) (Session, error) {
	sess, ok, err := s.Current(ctx)
	if err != nil {
		return Session{}, err
	}
	if !ok {
		return Session{}, ErrNoSession
	}
	// The payment is the attendant. Checked here rather than only in the UI,
	// because this is the function the shutter actually goes through.
	if sess.State != Open {
		return sess, ErrNotPaid
	}
	n, err := s.Takes(ctx, sess.ID)
	if err != nil {
		return Session{}, err
	}
	if n >= sess.TakeLimit {
		return sess, ErrTakeLimit
	}
	return sess, nil
}

// RecordDelivery stores the phone number the customer gave for their files,
// together with the consent that permits holding it.
//
// The number is stored unverified and never earns loyalty on its own — the
// cloud credits only once it has been verified through the OTP flow, which is
// what keeps the append-only ledger clean given the number is the account.
//
// The two consents are separate arguments because they are separate purposes.
// Bundling them is the most common PDP mistake, and a single boolean here would
// be that mistake encoded.
func (s *Store) RecordDelivery(ctx context.Context, id, e164, consentVersion string, marketing bool) error {
	if e164 == "" || consentVersion == "" {
		// The schema refuses this too. Saying it here gives the caller a usable
		// message instead of a constraint name.
		return errors.New("session: delivery needs a number and a consent version")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE sessions
		    SET phone = ?, consent_version = ?, consented_at = ?, marketing_consent = ?
		  WHERE id = ?`,
		e164, consentVersion, s.now().Unix(), boolToInt(marketing), id,
	)
	if err != nil {
		return fmt.Errorf("session: record delivery: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

const columns = `id, outlet_id, state, package_id, package_name, price_idr, template_id,
	print_copies, take_limit, opened_at, paid_at, closed_at,
	phone, consent_version, consented_at, marketing_consent`

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Session, error) {
	var (
		s                             Session
		paidAt, closedAt, consentedAt sql.NullInt64
		phone, consentVersion         sql.NullString
		openedAt, marketing           int64
	)
	if err := row.Scan(
		&s.ID, &s.OutletID, &s.State,
		&s.Package.ID, &s.Package.Name, &s.Package.PriceIDR, &s.Package.TemplateID, &s.Package.PrintCopies,
		&s.TakeLimit, &openedAt, &paidAt, &closedAt,
		&phone, &consentVersion, &consentedAt, &marketing,
	); err != nil {
		return Session{}, err
	}
	s.Package.TakeLimit = s.TakeLimit
	s.OpenedAt = time.Unix(openedAt, 0)
	if paidAt.Valid {
		s.PaidAt = time.Unix(paidAt.Int64, 0)
	}
	if closedAt.Valid {
		s.ClosedAt = time.Unix(closedAt.Int64, 0)
	}
	if consentedAt.Valid {
		s.ConsentedAt = time.Unix(consentedAt.Int64, 0)
	}
	s.Phone = phone.String
	s.ConsentVersion = consentVersion.String
	s.MarketingConsent = marketing == 1
	return s, nil
}

func (s *Store) scanOne(ctx context.Context, query string, args ...any) (Session, error) {
	sess, err := scan(s.db.QueryRowContext(ctx, query, args...))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Session{}, ErrNotFound
	case err != nil:
		return Session{}, fmt.Errorf("session: query: %w", err)
	}
	return sess, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any supported platform; if it does, the
		// process has no business minting identifiers.
		panic("session: entropy unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
