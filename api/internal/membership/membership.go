// Package membership maps the studio's paper loyalty card onto the loyalty
// ledger.
//
// The design rule, stated in design/membership.md and enforced here and in the
// schema: a stamp is the studio's unit *in the existing ledger*. One stamp per
// multiple of Rp 45.000, floored per transaction, so Rp 44.000 twice is zero
// stamps and Rp 90.000 is two. The remainder is dropped on purpose — the card
// advertises it — and a balance cannot express a per-transaction floor, which
// is why the stamps a card has filled are grouped into membership_cards rather
// than summed out of loyalty_entries.
//
// The second rule is that a gift earned today is redeemed on a later visit,
// never the same day. It is a column (redeem_after) and a condition inside the
// reward's own UPDATE, not a check in a handler, because it is the card's
// printed fine print.
//
// Corrections never edit. Voiding a purchase writes a compensating adjust entry
// and marks the purchase, its stamps and the gifts it minted as voided, with a
// reason naming the operator. That is the same shape loyalty argues for, and it
// is why nothing here is mutable.
//
// Every rule below is decided in Go only to produce a good error message — the
// database has the only vote.
package membership

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/identity"
	"github.com/bhaktiyudha/bykami/api/internal/loyalty"
	"github.com/bhaktiyudha/bykami/api/internal/phone"
	"github.com/bhaktiyudha/bykami/api/internal/store"
)

// WIB is the only timezone the studio operates in, as a fixed offset rather
// than a named zone.
//
// Indonesia has no daylight saving, so +07:00 is exact all year, and a fixed
// zone needs no tzdata on the host. time.LoadLocation would drag a zone
// database into a cross-compiled static binary for a value that has been
// constant since 1964 — and a container that shipped without tzdata would
// silently shift a redeem_after by hours, which is a customer being told their
// gift is ready on the wrong day.
var WIB = time.FixedZone("WIB", 7*60*60)

// The rate limit, per phone and per IP, over a trailing hour. Two limits rather
// than one because they catch different walks: a single number guessed from
// many places, and a single place walking a list of numbers. The numbers match
// identity's challenge limit in spirit — strict per subject, generous enough
// that a real customer refreshing a page is never refused.
const (
	maxLookupsPerPhone = 20
	maxLookupsPerIP    = 60
	lookupWindow       = time.Hour
	// Interesting to a rate limit, worthless as a record: swept on the way past
	// so the table does not grow with the platform.
	lookupRetention = 24 * time.Hour
)

// openRewardsLimit bounds the console's outstanding list when the caller has no
// opinion, so nothing asks the server to walk the whole table.
const openRewardsLimit = 50

// voidWindow is how long a purchase may be undone. Past a day the receipt has
// been reconciled and the stamp has been counted; the way back then is an
// adjustment by hand, which is visible.
const voidWindow = 24 * time.Hour

var (
	// ErrNotFound covers a number that is not a member, a reward or purchase id
	// that does not exist, and every other "there is nothing here" answer.
	ErrNotFound = errors.New("membership: not found")
	// ErrBelowMinimum means the amount did not reach one stamp. The message
	// states the shortfall in rupiah, because "below minimum" without the
	// number is a message an operator cannot act on.
	ErrBelowMinimum = errors.New("membership: amount is below the minimum")
	// ErrTooManyLookups means this phone or this address has asked too often.
	ErrTooManyLookups = errors.New("membership: too many lookups")
	// ErrAlreadyRedeemed means the gift was handed over already.
	ErrAlreadyRedeemed = errors.New("membership: reward already redeemed")
	// ErrNotYetRedeemable means the same-day rule is still running.
	ErrNotYetRedeemable = errors.New("membership: reward is not redeemable yet")
	// ErrRewardVoided means the gift was cancelled with the purchase that
	// minted it.
	ErrRewardVoided = errors.New("membership: reward was voided")
	// ErrNotVoidable is every reason a void is refused. One sentinel with a
	// specific message per case: the operator reads the message, and the caller
	// only ever needs to know that a rule said no.
	ErrNotVoidable = errors.New("membership: purchase cannot be voided")
)

// Users is the part of identity this package needs: the account a phone number
// belongs to, created on the way past if it does not exist. The counter is how
// somebody joins — no OTP, no account form, no app — so this is EnsureUser and
// not StartSession.
type Users interface {
	EnsureUser(ctx context.Context, rawPhone, name, email string) (identity.User, error)
}

// CardView is the member's card as a page renders it: the slots, the gifts
// sitting on them, and how full it is.
//
// UserID is carried for the console and deliberately not serialised — an id is
// a way to address the account, and the public lookup exists to hand a caller
// their own card back, not to teach them how accounts are addressed.
type CardView struct {
	UserID string `json:"-"`
	// Name is masked on the public route and in full in the console.
	Name              string `json:"name"`
	Phone             string `json:"phone"`
	CardNo            int    `json:"card_no"`
	Filled            int    `json:"filled"`
	StampsPerCard     int    `json:"stamps_per_card"`
	AmountPerStampIDR int64  `json:"amount_per_stamp_idr"`
	Slots             []Slot `json:"slots"`
}

// Slot is one of the card's printed slots. Label is the gift the slot carries,
// filled or not, so a page can show the ladder before it is walked.
type Slot struct {
	No     int     `json:"no"`
	Label  string  `json:"label"`
	Filled bool    `json:"filled"`
	Reward *Reward `json:"reward"`
}

// Reward is a gift, issued as a row with a state rather than as a balance.
//
// Available is derived, never stored: it is the same condition the redeem
// UPDATE is written against, so the button a page draws and the write that
// answers it agree by construction.
type Reward struct {
	ID string `json:"id"`
	// StampNo is not serialised: the slot it sits on already carries the
	// number.
	StampNo     int        `json:"-"`
	Kind        string     `json:"kind"`
	Label       string     `json:"label"`
	IssuedAt    time.Time  `json:"-"`
	RedeemAfter time.Time  `json:"redeem_after"`
	RedeemedAt  *time.Time `json:"redeemed_at"`
	RedeemedBy  string     `json:"-"`
	Available   bool       `json:"available"`
	// Phone is whose gift it is. Not serialised — the customer knows their own
	// number — and the slots on their own page do not need it. The console's
	// outstanding list is the reason it is here at all: a gift is a fulfilment
	// somebody has to hand to a named person, and a list of labels with no
	// names is a list nobody can act on.
	Phone string `json:"-"`
}

// Purchase is one stamping event: the amount, the stamps it earned, and who
// wrote them.
type Purchase struct {
	ID          string
	CardID      string
	AmountIDR   int64
	Stamps      int
	ReferenceID string
	Operator    string
	Outlet      string
	CreatedAt   time.Time
	Voided      bool
	VoidReason  string
	// Phone is whose purchase it is. The console's "today" list and its void
	// button are the reason it is carried: voiding a purchase by an opaque id
	// is not something an operator can check before pressing.
	Phone string
}

// PurchaseResult is what a purchase did: the row it wrote, the card as it now
// stands, and whether the card closed. Replayed means this reference had
// already been stamped and nothing was credited twice.
type PurchaseResult struct {
	Purchase   Purchase
	Card       CardView
	CardClosed bool
	Replayed   bool
}

// ImportRow is one paper card being brought in: the name and number written on
// the front, the stamps already ticked, and the barcode if the card has one.
type ImportRow struct {
	Name    string
	Phone   string
	Barcode string
	Stamps  int
}

// ImportResult summarizes a run: how many rows were written, how many were
// already in the database, and the cards as they stand afterwards.
type ImportResult struct {
	Imported int
	Skipped  int
	Cards    []CardView
}

// Service is the card over the ledger.
type Service struct {
	db     *sql.DB
	ledger *loyalty.Ledger
	users  Users
	// now is injected so that the same-day rule can be driven in a test
	// without sleeping for a day, and so that every timestamp this package
	// writes comes from one clock.
	now func() time.Time
}

// New returns the service. A nil clock is time.Now, which is what production
// wants and what a test that does not care about time wants.
func New(db *sql.DB, ledger *loyalty.Ledger, users Users, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{db: db, ledger: ledger, users: users, now: now}
}

// queryer is the shape both *sql.DB and *sql.Tx satisfy, so a card view can be
// built inside the transaction that just changed it — which is what makes the
// answer the write's own, not a later read of somebody else's.
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// program is the card's configuration: what a stamp costs and how many fill the
// card.
type program struct {
	id             string
	vertical       string
	amountPerStamp int64
	stampsPerCard  int
}

// Lookup is the public read: a phone number in, the card out.
//
// No session, no cookie, no code, no provider. The number is already written on
// the front of the card and is already the account, so it is the key that needs
// no distribution. What that costs is stated in the design record: anybody who
// types a number learns whether it is a member and how many stamps they hold.
// The mitigations are the rate limit below and the masked name — the name is
// the thing the caller could not supply, and therefore the thing an enumeration
// would harvest. The number comes back in full because the caller typed it, so
// masking it would hide it from the only person who already knows it.
func (s *Service) Lookup(ctx context.Context, rawPhone, ip string) (CardView, error) {
	e164, err := phone.Normalize(rawPhone)
	if err != nil {
		return CardView{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CardView{}, fmt.Errorf("membership: begin lookup: %w", err)
	}
	defer tx.Rollback()

	now := s.now()
	// Counted before anything is looked up, so a walk-through spends its budget
	// whether or not it finds members.
	if err := s.checkRate(ctx, tx, e164, ip, now); err != nil {
		return CardView{}, err
	}

	view, found, err := s.viewFor(ctx, tx, e164)
	if err != nil {
		return CardView{}, err
	}

	// Recorded either way: a walk through numbers that are not members is
	// exactly the pattern these rows exist to make visible.
	if err := s.recordLookup(ctx, tx, e164, ip, found, now); err != nil {
		return CardView{}, err
	}
	if err := tx.Commit(); err != nil {
		return CardView{}, fmt.Errorf("membership: commit lookup: %w", err)
	}

	if !found {
		return CardView{}, ErrNotFound
	}
	view.Name = maskName(view.Name)
	return view, nil
}

// StaffView is the console's read. No mask and no rate limit: the caller is an
// authenticated operator standing in front of the customer, and the name is not
// a secret they are trying to extract — it is the thing they are checking.
func (s *Service) StaffView(ctx context.Context, rawPhone string) (CardView, error) {
	e164, err := phone.Normalize(rawPhone)
	if err != nil {
		return CardView{}, err
	}
	view, found, err := s.viewFor(ctx, s.db, e164)
	if err != nil {
		return CardView{}, err
	}
	if !found {
		return CardView{}, ErrNotFound
	}
	return view, nil
}

// viewFor builds the member's card, or reports that no account holds the
// number. Unmasked: masking is the public route's business and happens there.
func (s *Service) viewFor(ctx context.Context, q queryer, e164 string) (CardView, bool, error) {
	var (
		userID, name string
	)
	err := q.QueryRowContext(ctx,
		`SELECT id, COALESCE(name, '') FROM users WHERE phone = ?`, e164,
	).Scan(&userID, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return CardView{}, false, nil
	}
	if err != nil {
		return CardView{}, false, fmt.Errorf("membership: load user: %w", err)
	}

	prog, err := loadProgram(ctx, q)
	if err != nil {
		return CardView{}, false, err
	}
	view, err := s.cardView(ctx, q, prog, userID, name, e164)
	if err != nil {
		return CardView{}, false, err
	}
	return view, true, nil
}

// checkRate refuses when this phone or this address has been asking too often.
// Zero means allowed; a refusal is the answer, not an error to be logged.
func (s *Service) checkRate(ctx context.Context, q queryer, e164, ip string, now time.Time) error {
	since := now.Add(-lookupWindow).Unix()

	var phoneCount int
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM membership_lookups WHERE phone = ? AND created_at > ?`,
		e164, since,
	).Scan(&phoneCount); err != nil {
		return fmt.Errorf("membership: lookup rate (phone): %w", err)
	}
	if phoneCount >= maxLookupsPerPhone {
		return ErrTooManyLookups
	}

	// An empty address is not counted: a caller that did not come over the wire
	// cannot be attributed one, and counting every such call against the same
	// empty string would let one caller exhaust it for all of them.
	if ip == "" {
		return nil
	}
	var ipCount int
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM membership_lookups WHERE ip = ? AND created_at > ?`,
		ip, since,
	).Scan(&ipCount); err != nil {
		return fmt.Errorf("membership: lookup rate (ip): %w", err)
	}
	if ipCount >= maxLookupsPerIP {
		return ErrTooManyLookups
	}
	return nil
}

// recordLookup writes one row and sweeps the ones that have aged out, in the
// same batch as the attempt it describes.
func (s *Service) recordLookup(ctx context.Context, q queryer, e164, ip string, found bool, now time.Time) error {
	bit := 0
	if found {
		bit = 1
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO membership_lookups (id, phone, ip, found, created_at) VALUES (?, ?, ?, ?, ?)`,
		newID(), e164, ip, bit, now.Unix()); err != nil {
		return fmt.Errorf("membership: record lookup: %w", err)
	}
	if _, err := q.ExecContext(ctx,
		`DELETE FROM membership_lookups WHERE created_at < ?`,
		now.Add(-lookupRetention).Unix()); err != nil {
		return fmt.Errorf("membership: sweep lookups: %w", err)
	}
	return nil
}

// Purchase writes one stamping event: the ledger entry, the purchase row, the
// stamps, and the gifts the stamps reached.
//
// The amount floors to whole stamps per transaction and nothing carries over.
// Below one stamp the request is refused with the shortfall and nothing at all
// is written — not a zero-point row, because a zero-point row is a purchase
// that earned nothing, which is a different claim.
//
// The reference is the idempotency key's other half: a double-tap writes one
// set of stamps and the second attempt reports the first one.
func (s *Service) Purchase(ctx context.Context, rawPhone, name string, amountIDR int64, reference, operator, outlet string) (PurchaseResult, error) {
	e164, err := phone.Normalize(rawPhone)
	if err != nil {
		return PurchaseResult{}, err
	}
	prog, err := loadProgram(ctx, s.db)
	if err != nil {
		return PurchaseResult{}, err
	}

	stamps := int(amountIDR / prog.amountPerStamp)
	if stamps <= 0 {
		// The message states the shortfall in rupiah. "Below the minimum" with
		// no number leaves the operator doing arithmetic on a phone.
		shortfall := prog.amountPerStamp - amountIDR
		return PurchaseResult{}, fmt.Errorf("%w: kurang %s dari %s",
			ErrBelowMinimum, rupiah(shortfall), rupiah(prog.amountPerStamp))
	}

	user, err := s.users.EnsureUser(ctx, e164, name, "")
	if err != nil {
		return PurchaseResult{}, err
	}

	// Generated only when the operator left it blank. A counter sale has no
	// receipt number to type, and an empty key would make every such sale
	// idempotent with the next one.
	if reference = strings.TrimSpace(reference); reference == "" {
		reference = newReference()
	}
	idempotencyKey := "member:" + user.ID + ":" + reference

	// Idempotent on the key: a retry returns the entry the first attempt wrote
	// and credits nothing a second time. Called before the card transaction
	// because the ledger owns its own and cannot join ours — the store hands out
	// exactly one connection, so a nested transaction would deadlock on itself.
	entry, err := s.ledger.Earn(ctx, user.ID, prog.vertical, int64(stamps), reference, idempotencyKey)
	if err != nil {
		return PurchaseResult{}, fmt.Errorf("membership: earn: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PurchaseResult{}, fmt.Errorf("membership: begin purchase: %w", err)
	}
	defer tx.Rollback()

	// A reference already used by a live purchase is this exact sale arriving
	// twice. Read back and reported, not re-credited — the same answer the
	// unique index would give, arrived at before the write rather than after.
	// The index below is still the authority: two calls that get past this check
	// together collide there instead.
	existing, err := s.purchaseByReference(ctx, tx, user.ID, reference)
	switch {
	case err == nil:
		if existing.Voided {
			// The reference was freed by a void, but the ledger entry it
			// created was not: the earn above returned the original, so
			// stamping again would put stamps on a card with fewer points to
			// back them. Refused rather than silently inconsistent.
			return PurchaseResult{}, fmt.Errorf(
				"%w: referensi %q berasal dari pembelian yang sudah dibatalkan",
				ErrNotVoidable, reference)
		}
		view, err := s.cardView(ctx, tx, prog, user.ID, user.Name, e164)
		if err != nil {
			return PurchaseResult{}, err
		}
		return PurchaseResult{Purchase: existing, Card: view, Replayed: true,
			CardClosed: cardClosed(view)}, nil
	case !errors.Is(err, sql.ErrNoRows):
		return PurchaseResult{}, err
	}

	cardID, live, err := s.openCard(ctx, tx, user.ID, prog)
	if err != nil {
		return PurchaseResult{}, err
	}

	p := Purchase{
		ID: newID(), CardID: cardID, AmountIDR: amountIDR, Stamps: stamps,
		ReferenceID: reference, Operator: operator, Outlet: outlet,
		CreatedAt: s.now().Truncate(time.Second), Phone: e164,
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO membership_purchases
		   (id, user_id, card_id, entry_id, amount_idr, stamps, reference_id, outlet_id, operator, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, user.ID, cardID, entry.ID, p.AmountIDR, p.Stamps, reference, outlet,
		operator, p.CreatedAt.Unix(),
	); err != nil {
		// The unique index fired, so a concurrent call wrote this exact
		// purchase between the check above and here. Roll back and report the
		// row that exists rather than a second one.
		if store.IsConstraint(err) {
			tx.Rollback()
			return s.replay(ctx, prog, user.ID, user.Name, e164, reference)
		}
		return PurchaseResult{}, fmt.Errorf("membership: insert purchase: %w", err)
	}

	for i := range stamps {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO membership_stamps (card_id, stamp_no, purchase_id, created_at)
			 VALUES (?, ?, ?, ?)`,
			cardID, live+1+i, p.ID, p.CreatedAt.Unix(),
		); err != nil {
			return PurchaseResult{}, fmt.Errorf("membership: insert stamp: %w", err)
		}
	}

	live += stamps
	closed := false
	if live >= prog.stampsPerCard {
		// Nothing expires here. An unredeemed gift survives the card closing,
		// which is the friendlier reading of a card that says nothing about
		// expiry, and is easier to tighten later than to loosen.
		if _, err := tx.ExecContext(ctx,
			`UPDATE membership_cards SET closed_at = ? WHERE id = ? AND closed_at IS NULL`,
			p.CreatedAt.Unix(), cardID,
		); err != nil {
			return PurchaseResult{}, fmt.Errorf("membership: close card: %w", err)
		}
		closed = true
	}

	if err := s.mint(ctx, tx, prog, user.ID, cardID, s.now()); err != nil {
		return PurchaseResult{}, err
	}

	view, err := s.cardView(ctx, tx, prog, user.ID, user.Name, e164)
	if err != nil {
		return PurchaseResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return PurchaseResult{}, fmt.Errorf("membership: commit purchase: %w", err)
	}
	return PurchaseResult{Purchase: p, Card: view, CardClosed: closed}, nil
}

// replay reports the purchase a repeated reference already wrote.
func (s *Service) replay(ctx context.Context, prog program, userID, name, display, reference string) (PurchaseResult, error) {
	p, err := s.purchaseByReference(ctx, s.db, userID, reference)
	if err != nil {
		return PurchaseResult{}, fmt.Errorf("membership: replay: %w", err)
	}
	view, err := s.cardView(ctx, s.db, prog, userID, name, display)
	if err != nil {
		return PurchaseResult{}, err
	}
	return PurchaseResult{Purchase: p, Card: view, Replayed: true,
		CardClosed: cardClosed(view)}, nil
}

// Redeem hands over a gift.
//
// One conditional UPDATE, and its row count is the answer: one row changed
// means it happened, zero means it was already taken, voided, or too early.
// Nothing is read before the write — a read-then-write is two staff members
// each reading a redeemable row and each writing to it. Reading afterwards is
// only to say which of the three zeroes this was.
func (s *Service) Redeem(ctx context.Context, rewardID, operator string) (Reward, error) {
	if strings.TrimSpace(rewardID) == "" {
		return Reward{}, ErrNotFound
	}
	now := s.now()

	res, err := s.db.ExecContext(ctx,
		`UPDATE membership_rewards
		    SET redeemed_at = ?, redeemed_by = ?
		  WHERE id = ? AND redeemed_at IS NULL AND redeem_after <= ?`,
		now.Unix(), operator, rewardID, now.Unix())
	if err != nil {
		return Reward{}, fmt.Errorf("membership: redeem: %w", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return Reward{}, fmt.Errorf("membership: redeem: %w", err)
	}
	if changed == 1 {
		return s.rewardByID(ctx, rewardID)
	}

	// Zero rows. Say which of the two it was — the message the operator sees
	// is the whole reason this read exists.
	r, err := s.rewardByID(ctx, rewardID)
	if errors.Is(err, sql.ErrNoRows) {
		return Reward{}, ErrNotFound
	}
	if err != nil {
		return Reward{}, err
	}
	switch {
	case r.RedeemedAt != nil:
		return Reward{}, ErrAlreadyRedeemed
	case r.RedeemAfter.After(now):
		return Reward{}, ErrNotYetRedeemable
	}
	return Reward{}, ErrNotFound
}

// Void undoes a purchase that has not settled into the card.
//
// Refused unless every one of these holds, and each refusal names the reason:
// the purchase is not already voided; its stamps are the most recent live ones
// on the card, so voiding them cannot leave a hole in the middle; the card has
// not closed since; the purchase is less than 24 hours old; and no gift minted
// from it has been handed over. A gift already given cannot be un-given — it
// can only be compensated, which is what the adjust entry is for.
//
// The marks are written first, in one transaction, with a conditional UPDATE as
// the gate: between two concurrent voids exactly one changes a row, so exactly
// one compensating entry can be written. The ledger's own Adjust is a separate
// transaction — the store hands out a single connection, so it cannot join this
// one without deadlocking on itself — and it runs after the gate, which is why
// the gate comes first.
func (s *Service) Void(ctx context.Context, purchaseID, reason, operator string) (Purchase, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Purchase{}, fmt.Errorf("%w: alasan wajib diisi", ErrNotVoidable)
	}
	if strings.TrimSpace(purchaseID) == "" {
		return Purchase{}, ErrNotFound
	}
	prog, err := loadProgram(ctx, s.db)
	if err != nil {
		return Purchase{}, err
	}

	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Purchase{}, fmt.Errorf("membership: begin void: %w", err)
	}
	defer tx.Rollback()

	p, err := s.purchaseByID(ctx, tx, purchaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return Purchase{}, ErrNotFound
	}
	if err != nil {
		return Purchase{}, err
	}
	if p.Voided {
		return Purchase{}, fmt.Errorf("%w: pembelian sudah dibatalkan", ErrNotVoidable)
	}
	if now.Sub(p.CreatedAt) >= voidWindow {
		return Purchase{}, fmt.Errorf("%w: pembelian sudah lebih dari 24 jam", ErrNotVoidable)
	}

	var (
		userID string
		closed sql.NullInt64
	)
	if err := tx.QueryRowContext(ctx,
		`SELECT user_id, closed_at FROM membership_cards WHERE id = ?`, p.CardID,
	).Scan(&userID, &closed); err != nil {
		return Purchase{}, fmt.Errorf("membership: load card: %w", err)
	}
	if closed.Valid && closed.Int64 > p.CreatedAt.Unix() {
		return Purchase{}, fmt.Errorf("%w: kartu sudah penuh dan ditutup", ErrNotVoidable)
	}

	// The stamps are contiguous, so the live ones of this purchase are the tail
	// if and only if the first of them sits immediately after everybody else's.
	// Any other answer means a hole would be left in the middle of the card.
	var first, live int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MIN(stamp_no), 0), COUNT(*)
		   FROM membership_stamps WHERE purchase_id = ?`,
		p.ID,
	).Scan(&first, &live); err != nil {
		return Purchase{}, fmt.Errorf("membership: load stamps: %w", err)
	}
	if live == 0 {
		return Purchase{}, fmt.Errorf("%w: tidak ada stempel aktif", ErrNotVoidable)
	}
	var others int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM membership_stamps
		  WHERE card_id = ? AND purchase_id <> ?`,
		p.CardID, p.ID,
	).Scan(&others); err != nil {
		return Purchase{}, fmt.Errorf("membership: count other stamps: %w", err)
	}
	if first != others+1 {
		return Purchase{}, fmt.Errorf(
			"%w: stempel ini bukan yang terakhir di kartu", ErrNotVoidable)
	}

	// A gift already handed over blocks the void. It is the one consequence a
	// compensating entry cannot undo: the customer walked away with it.
	var given int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM membership_rewards
		  WHERE card_id = ? AND stamp_no BETWEEN ? AND ? AND redeemed_at IS NOT NULL`,
		p.CardID, first, first+live-1,
	).Scan(&given); err != nil {
		return Purchase{}, fmt.Errorf("membership: count redeemed rewards: %w", err)
	}
	if given > 0 {
		return Purchase{}, fmt.Errorf(
			"%w: hadiah dari stempel ini sudah ditukar", ErrNotVoidable)
	}

	// The gate. One live row changed means this call owns the void; zero means
	// somebody else got there first, and no compensation is owed twice.
	res, err := tx.ExecContext(ctx,
		`UPDATE membership_purchases SET voided_at = ?, void_reason = ?
		  WHERE id = ? AND voided_at IS NULL`, now.Unix(), reason, p.ID)
	if err != nil {
		return Purchase{}, fmt.Errorf("membership: void purchase: %w", err)
	}
	if changed, err := res.RowsAffected(); err != nil {
		return Purchase{}, fmt.Errorf("membership: void purchase: %w", err)
	} else if changed != 1 {
		return Purchase{}, fmt.Errorf("%w: pembelian sudah dibatalkan", ErrNotVoidable)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM membership_stamps WHERE purchase_id = ?`, p.ID,
	); err != nil {
		return Purchase{}, fmt.Errorf("membership: void stamps: %w", err)
	}
	// The gifts those stamps minted go with them. A redeemed one cannot reach
	// here — the check above refuses first — so this only ever withdraws gifts
	// the customer has not collected.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM membership_rewards
		  WHERE card_id = ? AND stamp_no BETWEEN ? AND ? AND redeemed_at IS NULL`,
		p.CardID, first, first+live-1,
	); err != nil {
		return Purchase{}, fmt.Errorf("membership: void rewards: %w", err)
	}

	// If this purchase closed the card, reopen it so the slot is free again.
	if closed.Valid && closed.Int64 == p.CreatedAt.Unix() {
		if _, err := tx.ExecContext(ctx,
			`UPDATE membership_cards SET closed_at = NULL WHERE id = ?`, p.CardID,
		); err != nil {
			return Purchase{}, fmt.Errorf("membership: reopen card: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Purchase{}, fmt.Errorf("membership: commit void: %w", err)
	}

	// The ledger is corrected with an entry, never by editing: the mistake stays
	// visible and the correction is auditable, which is the property that makes
	// a dispute resolvable months later. The reason names the operator and the
	// purchase, because the ledger has no actor column.
	note := fmt.Sprintf("void %s oleh %s: %s", p.ID, operator, reason)
	if _, err := s.ledger.Adjust(ctx, userID, prog.vertical, -int64(live), note); err != nil {
		return Purchase{}, fmt.Errorf("membership: adjust after void: %w", err)
	}

	p.Voided = true
	p.VoidReason = reason
	return p, nil
}

// Today is the console's list: purchases made today, newest first, in WIB. The
// studio's day is a local fact, and a list that rolled over at UTC midnight
// would show yesterday's stamps as today's for the first seven hours of every
// morning.
func (s *Service) Today(ctx context.Context) ([]Purchase, error) {
	local := s.now().In(WIB)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, WIB)
	return s.purchases(ctx,
		`WHERE p.created_at >= ? AND p.created_at < ? ORDER BY p.created_at DESC, p.id DESC`,
		start.Unix(), start.AddDate(0, 0, 1).Unix())
}

// OpenRewards lists the gifts still to be handed over, oldest first. Included
// are the ones not yet redeemable, so the console can say "bisa ditukar besok"
// rather than hiding a gift the customer can see on their own card.
func (s *Service) OpenRewards(ctx context.Context, limit int) ([]Reward, error) {
	if limit <= 0 {
		limit = openRewardsLimit
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+rewardColumns+`
		   FROM membership_rewards r JOIN users u ON u.id = r.user_id
		  WHERE r.redeemed_at IS NULL
		  ORDER BY r.issued_at ASC, r.id ASC
		  LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("membership: open rewards: %w", err)
	}
	defer rows.Close()

	var out []Reward
	for rows.Next() {
		r, err := s.scanReward(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Import brings paper cards in.
//
// Somebody bringing in a card with eight ticks has collected the gifts for 2,
// 4, 6 and 8 on paper, so the milestones are minted and immediately marked
// redeemed, naming the operator. Without that step every member with a
// nearly-full card collects four free gifts in the same week, and it is the
// kind of mistake that is invisible until it is expensive.
//
// Re-running is safe: the barcode is the idempotency key, so a second run
// credits nothing and re-issues nothing.
func (s *Service) Import(ctx context.Context, rows []ImportRow, operator, outlet string) (ImportResult, error) {
	if operator = strings.TrimSpace(operator); operator == "" {
		// Provenance is the point of importing at all, so a run with nobody
		// named against it is refused rather than written as anonymous.
		return ImportResult{}, errors.New("membership: import needs an operator")
	}
	prog, err := loadProgram(ctx, s.db)
	if err != nil {
		return ImportResult{}, err
	}

	now := s.now()
	res := ImportResult{Cards: []CardView{}}

	for i, row := range rows {
		if row.Stamps <= 0 {
			return ImportResult{}, fmt.Errorf(
				"membership: import row %d: stamps must be greater than zero", i+1)
		}
		if row.Stamps > prog.stampsPerCard {
			return ImportResult{}, fmt.Errorf(
				"membership: import row %d: %d stempel melebihi kapasitas kartu (%d)",
				i+1, row.Stamps, prog.stampsPerCard)
		}

		key := "paper:" + strings.TrimSpace(row.Barcode)

		// Already imported. The ledger's unique index would refuse it anyway,
		// but the point is to report it as skipped rather than to write half a
		// card and then fail.
		var exists int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM loyalty_entries WHERE idempotency_key = ?`, key,
		).Scan(&exists); err != nil {
			return ImportResult{}, fmt.Errorf("membership: import check: %w", err)
		}
		if exists > 0 {
			res.Skipped++
			continue
		}

		user, err := s.users.EnsureUser(ctx, row.Phone, row.Name, "")
		if err != nil {
			return ImportResult{}, err
		}

		entry, err := s.ledger.Earn(ctx, user.ID, prog.vertical, int64(row.Stamps), key, key)
		if err != nil {
			return ImportResult{}, fmt.Errorf("membership: import earn: %w", err)
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return ImportResult{}, fmt.Errorf("membership: begin import: %w", err)
		}
		if _, err := s.importRow(ctx, tx, prog, user, row, entry.ID, key, operator, outlet, now); err != nil {
			tx.Rollback()
			return ImportResult{}, err
		}
		view, err := s.cardView(ctx, tx, prog, user.ID, user.Name, user.Phone)
		if err != nil {
			tx.Rollback()
			return ImportResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return ImportResult{}, fmt.Errorf("membership: commit import: %w", err)
		}

		res.Imported++
		res.Cards = append(res.Cards, view)
	}
	return res, nil
}

// importRow places one paper card: open the card the stamps belong on, write the
// purchase and its stamps, close the card if it fills, and mint the gifts that
// were already collected on paper.
// importRow places one paper card: open the card the stamps belong on, write the
// stamps with no purchase behind them, close the card if it fills, and mint the
// gifts that were already collected on paper.
func (s *Service) importRow(ctx context.Context, tx *sql.Tx, prog program, user identity.User, row ImportRow, entryID, reference, operator, outlet string, now time.Time) (string, error) {
	cardID, live, err := s.openCard(ctx, tx, user.ID, prog)
	if err != nil {
		return "", err
	}
	if live+row.Stamps > prog.stampsPerCard {
		// The card in hand has no room left for this row, so it is closed as it
		// stands and a new one is opened. A paper card does not overflow into
		// the next one and stamps never migrate between cards.
		if _, err := tx.ExecContext(ctx,
			`UPDATE membership_cards SET closed_at = ? WHERE id = ? AND closed_at IS NULL`,
			now.Unix(), cardID,
		); err != nil {
			return "", fmt.Errorf("membership: close full card: %w", err)
		}
		cardID, _, err = s.openNewCard(ctx, tx, user.ID, prog)
		if err != nil {
			return "", err
		}
		live = 0
	}

	for i := range row.Stamps {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO membership_stamps (card_id, stamp_no, purchase_id, created_at)
			 VALUES (?, ?, ?, ?)`,
			cardID, live+1+i, nil, now.Unix(),
		); err != nil {
			return "", fmt.Errorf("membership: import stamp: %w", err)
		}
	}
	live += row.Stamps

	if live >= prog.stampsPerCard {
		if _, err := tx.ExecContext(ctx,
			`UPDATE membership_cards SET closed_at = ? WHERE id = ? AND closed_at IS NULL`,
			now.Unix(), cardID,
		); err != nil {
			return "", fmt.Errorf("membership: import close card: %w", err)
		}
	}

	// Minted and handed over in one step: the customer already collected these
	// on paper, so the row records that they were, naming this run as the
	// operator who gave them out.
	if err := s.mintCollected(ctx, tx, prog, user.ID, cardID, operator, now); err != nil {
		return "", err
	}
	return cardID, nil
}

// openCard finds the member's open card or opens the first one, and reports how
// many live stamps it carries. stamp_no is always 1..live, so the next stamp is
// live+1.
func (s *Service) openCard(ctx context.Context, q queryer, userID string, prog program) (id string, live int, err error) {
	err = q.QueryRowContext(ctx,
		`SELECT id FROM membership_cards
		  WHERE user_id = ? AND program_id = ? AND closed_at IS NULL`, userID, prog.id,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		id, _, err = s.openNewCard(ctx, q, userID, prog)
		return id, 0, err
	}
	if err != nil {
		return "", 0, fmt.Errorf("membership: open card: %w", err)
	}
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM membership_stamps WHERE card_id = ?`, id,
	).Scan(&live); err != nil {
		return "", 0, fmt.Errorf("membership: count stamps: %w", err)
	}
	return id, live, nil
}

// openNewCard opens the next card for the member. card_no is max+1 rather than
// a count, because a voided purchase leaves its card number taken and reissuing
// it would make "card 3" mean two different pieces of card.
func (s *Service) openNewCard(ctx context.Context, q queryer, userID string, prog program) (id string, maxNo int, err error) {
	var cardNo int
	if err := q.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(card_no), 0) + 1 FROM membership_cards WHERE user_id = ? AND program_id = ?`,
		userID, prog.id,
	).Scan(&cardNo); err != nil {
		return "", 0, fmt.Errorf("membership: next card number: %w", err)
	}
	id = newID()
	if _, err := q.ExecContext(ctx,
		`INSERT INTO membership_cards (id, user_id, program_id, card_no, opened_at) VALUES (?, ?, ?, ?, ?)`,
		id, userID, prog.id, cardNo, s.now().Truncate(time.Second).Unix()); err != nil {
		return "", 0, fmt.Errorf("membership: open card: %w", err)
	}
	return id, 0, nil
}

// mint issues the gifts the stamps just filled, at most once each: issued_at is
// now and redeem_after is the start of the next WIB day, which is the same-day
// rule as a stored instant. The (card_id, stamp_no) unique key makes it
// idempotent, so a retried purchase cannot issue the same gift twice.
func (s *Service) mint(ctx context.Context, q queryer, prog program, userID, cardID string, now time.Time) error {
	return s.mintRewards(ctx, q, prog, userID, cardID, "", time.Time{}, now)
}

// mintCollected is mint for an import: the gifts were handed over on paper
// before the row existed, so they are recorded as already redeemed rather than
// left for the customer to collect a second time.
func (s *Service) mintCollected(ctx context.Context, q queryer, prog program, userID, cardID, operator string, now time.Time) error {
	return s.mintRewards(ctx, q, prog, userID, cardID, operator, now, now)
}

// A gift is issued for every milestone sitting on a live stamp of this card.
// Driven by the stamps rather than by their count, because after a void the two
// differ: the card keeps its numbering, so slot 2 can be filled while only one
// stamp is live.
func (s *Service) mintRewards(ctx context.Context, q queryer, prog program, userID, cardID, collectedBy string, collectedAt, now time.Time) error {
	type milestone struct {
		no    int
		kind  string
		label string
	}

	rows, err := q.QueryContext(ctx,
		`SELECT stamp_no, reward_kind, label FROM membership_milestones
		  WHERE program_id = ? ORDER BY stamp_no`, prog.id)
	if err != nil {
		return fmt.Errorf("membership: milestones: %w", err)
	}
	reached := map[int]milestone{}
	for rows.Next() {
		var m milestone
		if err := rows.Scan(&m.no, &m.kind, &m.label); err != nil {
			rows.Close()
			return fmt.Errorf("membership: scan milestone: %w", err)
		}
		reached[m.no] = m
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	live, err := liveStamps(ctx, q, cardID)
	if err != nil {
		return err
	}

	var (
		redeemedAt any
		redeemedBy any
	)
	if collectedBy != "" {
		redeemedAt, redeemedBy = collectedAt.Unix(), collectedBy
	}

	for no := range live {
		m, ok := reached[no]
		if !ok {
			continue
		}
		if _, err := q.ExecContext(ctx,
			`INSERT INTO membership_rewards
			   (id, user_id, card_id, stamp_no, reward_kind, label, issued_at, redeem_after, redeemed_at, redeemed_by)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (card_id, stamp_no) DO NOTHING`,
			newID(), userID, cardID, m.no, m.kind, m.label,
			now.Unix(), nextWIBDay(now).Unix(), redeemedAt, redeemedBy,
		); err != nil {
			return fmt.Errorf("membership: mint reward: %w", err)
		}
	}
	return nil
}

// liveStamps is the set of slot numbers currently filled on a card.
func liveStamps(ctx context.Context, q queryer, cardID string) (map[int]bool, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT stamp_no FROM membership_stamps WHERE card_id = ?`, cardID)
	if err != nil {
		return nil, fmt.Errorf("membership: live stamps: %w", err)
	}
	defer rows.Close()

	out := map[int]bool{}
	for rows.Next() {
		var no int
		if err := rows.Scan(&no); err != nil {
			return nil, fmt.Errorf("membership: scan stamp: %w", err)
		}
		out[no] = true
	}
	return out, rows.Err()
}

// cardView renders the member's card: the slots, the gifts on them, and the
// card number. With no card yet it renders the one the next purchase would
// open, which is the card the operator is about to fill.
func (s *Service) cardView(ctx context.Context, q queryer, prog program, userID, name, display string) (CardView, error) {
	labels, err := milestoneLabels(ctx, q, prog.id)
	if err != nil {
		return CardView{}, err
	}

	view := CardView{
		UserID: userID, Name: name, Phone: display,
		StampsPerCard: prog.stampsPerCard, AmountPerStampIDR: prog.amountPerStamp,
		Slots: make([]Slot, 0, prog.stampsPerCard),
	}

	cardID, cardNo, err := latestCard(ctx, q, userID, prog.id)
	if err != nil {
		return CardView{}, err
	}
	if cardID == "" {
		view.CardNo = 1
		for n := 1; n <= prog.stampsPerCard; n++ {
			view.Slots = append(view.Slots, Slot{No: n, Label: labels[n]})
		}
		return view, nil
	}
	view.CardNo = cardNo

	filled := map[int]bool{}
	rows, err := q.QueryContext(ctx,
		`SELECT stamp_no FROM membership_stamps WHERE card_id = ?`, cardID)
	if err != nil {
		return CardView{}, fmt.Errorf("membership: load stamps: %w", err)
	}
	for rows.Next() {
		var no int
		if err := rows.Scan(&no); err != nil {
			rows.Close()
			return CardView{}, fmt.Errorf("membership: scan stamp: %w", err)
		}
		filled[no] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return CardView{}, err
	}
	rows.Close()

	rewards, err := s.cardRewards(ctx, q, cardID)
	if err != nil {
		return CardView{}, err
	}

	for n := 1; n <= prog.stampsPerCard; n++ {
		slot := Slot{No: n, Label: labels[n], Filled: filled[n]}
		if r, ok := rewards[n]; ok {
			slot.Reward = &r
		}
		if slot.Filled {
			view.Filled++
		}
		view.Slots = append(view.Slots, slot)
	}
	return view, nil
}

// latestCard is the card in the member's hand: the open one, or — between cards
// — the one that just filled, so its last gift is still visible rather than
// vanishing the moment the ninth stamp lands.
func latestCard(ctx context.Context, q queryer, userID, programID string) (id string, cardNo int, err error) {
	err = q.QueryRowContext(ctx,
		`SELECT id, card_no FROM membership_cards
		  WHERE user_id = ? AND program_id = ?
		  ORDER BY (closed_at IS NULL) DESC, card_no DESC
		  LIMIT 1`, userID, programID,
	).Scan(&id, &cardNo)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("membership: latest card: %w", err)
	}
	return id, cardNo, nil
}

func milestoneLabels(ctx context.Context, q queryer, programID string) (map[int]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT stamp_no, label FROM membership_milestones WHERE program_id = ?`, programID)
	if err != nil {
		return nil, fmt.Errorf("membership: milestones: %w", err)
	}
	defer rows.Close()

	out := map[int]string{}
	for rows.Next() {
		var (
			no    int
			label string
		)
		if err := rows.Scan(&no, &label); err != nil {
			return nil, fmt.Errorf("membership: scan milestone: %w", err)
		}
		out[no] = label
	}
	return out, rows.Err()
}

func (s *Service) cardRewards(ctx context.Context, q queryer, cardID string) (map[int]Reward, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT `+rewardColumns+`
		   FROM membership_rewards r JOIN users u ON u.id = r.user_id
		  WHERE r.card_id = ?`, cardID)
	if err != nil {
		return nil, fmt.Errorf("membership: card rewards: %w", err)
	}
	defer rows.Close()

	out := map[int]Reward{}
	for rows.Next() {
		r, err := s.scanReward(rows)
		if err != nil {
			return nil, err
		}
		out[r.StampNo] = r
	}
	return out, rows.Err()
}

// rewardColumns is the select list every reward read shares, with the phone
// joined on. One list rather than three, because three copies drift.
const rewardColumns = `r.id, r.stamp_no, r.reward_kind, r.label, r.issued_at, r.redeem_after,
	r.redeemed_at, COALESCE(r.redeemed_by, ''), COALESCE(u.phone, '')`

func (s *Service) scanReward(sc scanner) (Reward, error) {
	var (
		r             Reward
		issued, after int64
		redeemed      sql.NullInt64
	)
	if err := sc.Scan(&r.ID, &r.StampNo, &r.Kind, &r.Label, &issued, &after,
		&redeemed, &r.RedeemedBy, &r.Phone); err != nil {
		return Reward{}, fmt.Errorf("membership: scan reward: %w", err)
	}
	r.IssuedAt = time.Unix(issued, 0).UTC()
	// In WIB, which is the zone that defines it: the value is a studio midnight,
	// and a page that renders it (or a JSON body somebody reads) should not have
	// to know to convert "17:00Z" back into the start of a day.
	r.RedeemAfter = time.Unix(after, 0).In(WIB)
	if redeemed.Valid {
		t := time.Unix(redeemed.Int64, 0).UTC()
		r.RedeemedAt = &t
	}
	r.Available = r.RedeemedAt == nil && !r.RedeemAfter.After(s.now())
	return r, nil
}

func (s *Service) rewardByID(ctx context.Context, id string) (Reward, error) {
	return s.scanReward(s.db.QueryRowContext(ctx,
		`SELECT `+rewardColumns+`
		   FROM membership_rewards r JOIN users u ON u.id = r.user_id
		  WHERE r.id = ?`, id))
}

const purchaseColumns = `p.id, p.card_id, p.amount_idr, p.stamps, COALESCE(p.reference_id, ''),
	p.operator, p.outlet_id, p.created_at, p.voided_at, COALESCE(p.void_reason, ''), COALESCE(u.phone, '')`

func (s *Service) purchases(ctx context.Context, clause string, args ...any) ([]Purchase, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+purchaseColumns+`
		   FROM membership_purchases p JOIN users u ON u.id = p.user_id `+clause, args...)
	if err != nil {
		return nil, fmt.Errorf("membership: purchases: %w", err)
	}
	defer rows.Close()

	out := []Purchase{}
	for rows.Next() {
		p, err := scanPurchase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Service) purchaseByID(ctx context.Context, q queryer, id string) (Purchase, error) {
	return scanPurchase(q.QueryRowContext(ctx,
		`SELECT `+purchaseColumns+`
		   FROM membership_purchases p JOIN users u ON u.id = p.user_id
		  WHERE p.id = ?`, id))
}

func (s *Service) purchaseByReference(ctx context.Context, q queryer, userID, reference string) (Purchase, error) {
	return scanPurchase(q.QueryRowContext(ctx,
		`SELECT `+purchaseColumns+`
		   FROM membership_purchases p JOIN users u ON u.id = p.user_id
		  WHERE p.user_id = ? AND p.reference_id = ? AND p.voided_at IS NULL
		  ORDER BY p.created_at DESC, p.id DESC
		  LIMIT 1`, userID, reference))
}

type scanner interface{ Scan(dest ...any) error }

func scanPurchase(sc scanner) (Purchase, error) {
	var (
		p        Purchase
		created  int64
		voidedAt sql.NullInt64
	)
	if err := sc.Scan(&p.ID, &p.CardID, &p.AmountIDR, &p.Stamps, &p.ReferenceID,
		&p.Operator, &p.Outlet, &created, &voidedAt, &p.VoidReason, &p.Phone); err != nil {
		return Purchase{}, fmt.Errorf("membership: scan purchase: %w", err)
	}
	p.CreatedAt = time.Unix(created, 0).UTC()
	p.Voided = voidedAt.Valid
	return p, nil
}

// loadProgram reads the card's configuration. One active program is the
// deployed state; the ordering makes a second one a data decision rather than a
// change here.
func loadProgram(ctx context.Context, q queryer) (program, error) {
	var p program
	err := q.QueryRowContext(ctx,
		`SELECT id, vertical, amount_per_stamp_idr, stamps_per_card
		   FROM membership_programs WHERE active = 1 ORDER BY id LIMIT 1`,
	).Scan(&p.id, &p.vertical, &p.amountPerStamp, &p.stampsPerCard)
	if errors.Is(err, sql.ErrNoRows) {
		return program{}, fmt.Errorf("membership: no active program: %w", ErrNotFound)
	}
	if err != nil {
		return program{}, fmt.Errorf("membership: load program: %w", err)
	}
	return p, nil
}

// nextWIBDay is the start of the next WIB calendar day: issued on the 13th,
// redeemable from the 14th at midnight. Not "now + 24h", because the card says
// a later visit and a visit is a day.
func nextWIBDay(t time.Time) time.Time {
	l := t.In(WIB)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, WIB).AddDate(0, 0, 1)
}

// maskName keeps the first name and an initial for the rest: "Isyara Hadza"
// becomes "Isyara H.". The name is the thing a caller could not supply and
// therefore the thing an enumeration would harvest. A single-token name is left
// alone — there is nothing to redact, and turning it into an initial would make
// the page useless to its owner.
func maskName(name string) string {
	fields := strings.Fields(name)
	if len(fields) <= 1 {
		return name
	}
	out := []string{fields[0]}
	for _, f := range fields[1:] {
		r := []rune(f)
		if len(r) == 0 {
			continue
		}
		out = append(out, string(r[0])+".")
	}
	return strings.Join(out, " ")
}

// cardClosed reports whether the view is of a card that has filled. The card
// view does not carry the flag, and re-reading it for a replayed purchase would
// be a second query for a fact already in hand.
func cardClosed(view CardView) bool {
	return view.StampsPerCard > 0 && view.Filled >= view.StampsPerCard
}

// rupiah renders an amount the way the price list does: "Rp 45.000".
func rupiah(n int64) string {
	sign := ""
	if n < 0 {
		sign, n = "-", -n
	}
	digits := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	return sign + "Rp " + b.String()
}

// newReference names a counter sale that arrived without a receipt number. It
// is the idempotency key's other half, so it has to be unique per purchase —
// which is why the caller must generate it once and resubmit the same value,
// rather than this being called again on every retry.
func newReference() string {
	return "counter-" + newID()[:12]
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any supported platform; if it does, the
		// process has no business minting identifiers.
		panic("membership: entropy unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
