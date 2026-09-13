package membership

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/identity"
	"github.com/bhaktiyudha/bykami/api/internal/loyalty"
	"github.com/bhaktiyudha/bykami/api/internal/phone"
	"github.com/bhaktiyudha/bykami/api/internal/store"
)

// noopSender stands in for WhatsApp. Nothing in this package sends anything —
// the counter is how somebody joins — but identity insists on a sender.
type noopSender struct{}

func (noopSender) Send(context.Context, string, string) error { return nil }

// clock is the injected time. The same-day rule is a day long, so a test that
// cannot move the clock is a test that sleeps for one.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = t
}

func (c *clock) advance(d time.Duration) {
	c.set(c.now().Add(d))
}

// testNow is the instant every test sits at: Sunday 13 September 2026, 10:00
// WIB. Chosen so that the WIB day and the UTC day differ — the bug this guards
// against is a day boundary that follows the server rather than the studio.
var testNow = time.Date(2026, 9, 13, 10, 0, 0, 0, WIB)

const (
	operatorPhone = "081234567890"
	memberPhone   = "081298765432"
)

type fixture struct {
	svc    *Service
	ledger *loyalty.Ledger
	ident  *identity.Service
	db     *sql.DB
	clock  *clock
}

func newFixture(t *testing.T) fixture {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	clk := &clock{at: testNow}
	ident := identity.New(db, noopSender{})
	ledger := loyalty.New(db)

	return fixture{
		svc:    New(db, ledger, ident, clk.now),
		ledger: ledger,
		ident:  ident,
		db:     db,
		clock:  clk,
	}
}

// member creates the account a purchase will be attached to and returns its id.
func (f fixture) member(t *testing.T) identity.User {
	t.Helper()
	u, err := f.ident.EnsureUser(context.Background(), memberPhone, "Isyara Hadza", "")
	if err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	return u
}

func (f fixture) balance(t *testing.T, userID string) int64 {
	t.Helper()
	b, err := f.ledger.Balance(context.Background(), userID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	return b
}

func (f fixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// buy is the ordinary sale: an amount in rupiah, no reference, so the service
// mints one.
func (f fixture) buy(t *testing.T, amount int64) PurchaseResult {
	t.Helper()
	res, err := f.svc.Purchase(context.Background(), memberPhone, "Isyara Hadza", amount, "", operatorPhone, "")
	if err != nil {
		t.Fatalf("purchase %d: %v", amount, err)
	}
	return res
}

func TestLookupReturnsTheCardWithAMaskedName(t *testing.T) {
	f := newFixture(t)
	f.member(t)
	f.buy(t, 45_000)

	card, err := f.svc.Lookup(context.Background(), memberPhone, "203.0.113.7")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}

	// The name is the thing a caller could not supply, so it is the thing an
	// enumeration would harvest.
	if card.Name != "Isyara H." {
		t.Errorf("name = %q, want %q", card.Name, "Isyara H.")
	}
	// The number comes back in full: the caller typed it, so masking it would
	// hide it from the only person who already knows it.
	e164, err := phone.Normalize(memberPhone)
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}
	if card.Phone != e164 {
		t.Errorf("phone = %q, want %q", card.Phone, e164)
	}
	if card.CardNo != 1 || card.Filled != 1 || card.StampsPerCard != 9 {
		t.Errorf("card = no %d filled %d of %d, want no 1 filled 1 of 9",
			card.CardNo, card.Filled, card.StampsPerCard)
	}
	if card.AmountPerStampIDR != 45_000 {
		t.Errorf("amount per stamp = %d, want 45000", card.AmountPerStampIDR)
	}
	if len(card.Slots) != 9 {
		t.Fatalf("%d slots, want 9", len(card.Slots))
	}
	if !card.Slots[0].Filled {
		t.Error("slot 1 is empty after one purchase")
	}
	// The ladder is visible before it is walked.
	if card.Slots[1].Label != "+5 menit" || card.Slots[8].Label != "Free photobox 1 orang" {
		t.Errorf("slot labels = %q, %q", card.Slots[1].Label, card.Slots[8].Label)
	}
}

// A gift earned today is redeemed on a later visit, never the same day, and the
// rule is a stored instant rather than a handler's opinion.
func TestAGiftIsNotRedeemableUntilTheNextDay(t *testing.T) {
	f := newFixture(t)
	f.member(t)
	f.buy(t, 90_000) // two stamps, so the slot-2 gift is issued

	card, err := f.svc.Lookup(context.Background(), memberPhone, "")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	reward := card.Slots[1].Reward
	if reward == nil {
		t.Fatal("no gift on slot 2 after two stamps")
	}
	want := time.Date(2026, 9, 14, 0, 0, 0, 0, WIB)
	if !reward.RedeemAfter.Equal(want) {
		t.Errorf("redeem_after = %s, want %s", reward.RedeemAfter, want)
	}
	if reward.Available {
		t.Error("a gift issued today is already redeemable")
	}

	if _, err := f.svc.Redeem(context.Background(), reward.ID, operatorPhone); !errors.Is(err, ErrNotYetRedeemable) {
		t.Errorf("same-day redeem = %v, want ErrNotYetRedeemable", err)
	}

	// The day after. Driven through the clock, not a sleep.
	f.clock.set(want)
	got, err := f.svc.Redeem(context.Background(), reward.ID, operatorPhone)
	if err != nil {
		t.Fatalf("redeem the next day: %v", err)
	}
	if got.RedeemedAt == nil || got.RedeemedBy != operatorPhone {
		t.Errorf("redeemed = %+v, want it marked and named", got)
	}
}

func TestLookupOfAStrangerIsNotFound(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.Lookup(context.Background(), "081200000001", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("lookup = %v, want ErrNotFound", err)
	}
	// The attempt is still recorded: a walk through numbers that are not
	// members is exactly the pattern the table exists to make visible.
	if n := f.count(t, `SELECT COUNT(*) FROM membership_lookups WHERE found = 0`); n != 1 {
		t.Errorf("%d unfound lookups recorded, want 1", n)
	}
}

func TestLookupRejectsAnUnparseableNumber(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.Lookup(context.Background(), "not a number", ""); !errors.Is(err, phone.ErrInvalid) {
		t.Errorf("lookup = %v, want phone.ErrInvalid", err)
	}
}

func TestLookupIsRateLimitedPerPhone(t *testing.T) {
	f := newFixture(t)
	f.member(t)

	for i := range maxLookupsPerPhone {
		if _, err := f.svc.Lookup(context.Background(), memberPhone, "203.0.113.7"); err != nil {
			t.Fatalf("lookup %d: %v", i+1, err)
		}
	}
	if _, err := f.svc.Lookup(context.Background(), memberPhone, "203.0.113.7"); !errors.Is(err, ErrTooManyLookups) {
		t.Errorf("the %dth lookup = %v, want ErrTooManyLookups", maxLookupsPerPhone+1, err)
	}
}

// Two staff members looking at the same gift must not both hand it over. The
// conditional UPDATE's row count is the whole answer, so exactly one wins.
func TestTwoConcurrentRedeemsYieldExactlyOneSuccess(t *testing.T) {
	f := newFixture(t)
	f.member(t)
	f.buy(t, 90_000)
	f.clock.set(time.Date(2026, 9, 14, 9, 0, 0, 0, WIB))

	card, err := f.svc.Lookup(context.Background(), memberPhone, "")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	rewardID := card.Slots[1].Reward.ID

	const goroutines = 8
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := range goroutines {
		wg.Go(func() {
			_, errs[i] = f.svc.Redeem(context.Background(), rewardID, operatorPhone)
		})
	}
	wg.Wait()

	succeeded := 0
	for i, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrAlreadyRedeemed):
		default:
			t.Errorf("goroutine %d: unexpected error %v", i, err)
		}
	}
	if succeeded != 1 {
		t.Errorf("%d redeems succeeded, want exactly 1", succeeded)
	}
}

// The double-tap. A gateway retry, an operator pressing twice, a browser
// resubmitting — one set of stamps, and the second attempt reports the first.
func TestTwoConcurrentPurchasesWithOneReferenceCreditOnce(t *testing.T) {
	f := newFixture(t)
	u := f.member(t)

	const goroutines = 8
	var wg sync.WaitGroup
	results := make([]PurchaseResult, goroutines)
	errs := make([]error, goroutines)
	for i := range goroutines {
		wg.Go(func() {
			results[i], errs[i] = f.svc.Purchase(
				context.Background(), memberPhone, "Isyara Hadza", 45_000, "receipt-1", operatorPhone, "")
		})
	}
	wg.Wait()

	replayed := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if results[i].Replayed {
			replayed++
		}
	}
	if replayed != goroutines-1 {
		t.Errorf("%d of %d calls reported a replay, want %d", replayed, goroutines, goroutines-1)
	}
	if got := f.balance(t, u.ID); got != 1 {
		t.Errorf("balance = %d, want 1 — a repeated reference credited twice", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM membership_purchases`); n != 1 {
		t.Errorf("%d purchases written, want 1", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM membership_stamps`); n != 1 {
		t.Errorf("%d stamps written, want 1", n)
	}
}

// The remainder is dropped, and dropping it is the card's advertised rule
// rather than a rounding error: Rp 44.000 twice is zero stamps, not one.
func TestBelowMinimumWritesNothing(t *testing.T) {
	f := newFixture(t)
	u := f.member(t)

	_, err := f.svc.Purchase(context.Background(), memberPhone, "Isyara Hadza", 44_000, "", operatorPhone, "")
	if !errors.Is(err, ErrBelowMinimum) {
		t.Fatalf("purchase = %v, want ErrBelowMinimum", err)
	}
	if !strings.Contains(err.Error(), "kurang Rp 1.000") {
		t.Errorf("message = %q, want it to state the shortfall", err)
	}

	if got := f.balance(t, u.ID); got != 0 {
		t.Errorf("balance = %d, want 0", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM loyalty_entries`); n != 0 {
		t.Errorf("%d ledger entries written, want 0", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM membership_purchases`); n != 0 {
		t.Errorf("%d purchases written, want 0", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM membership_cards`); n != 0 {
		t.Errorf("%d cards opened, want 0", n)
	}
}

// One stamp per multiple, floored per transaction, and the whole multiple is
// the unit: Rp 90.000 is two.
func TestNinetyThousandEarnsTwoStamps(t *testing.T) {
	f := newFixture(t)
	u := f.member(t)

	res := f.buy(t, 90_000)

	if res.Purchase.Stamps != 2 {
		t.Errorf("stamps = %d, want 2", res.Purchase.Stamps)
	}
	if res.Card.Filled != 2 {
		t.Errorf("card filled = %d, want 2", res.Card.Filled)
	}
	if got := f.balance(t, u.ID); got != 2 {
		t.Errorf("balance = %d, want 2", got)
	}
}

func TestTheNinthStampClosesTheCardAndTheNextPurchaseOpensANewOne(t *testing.T) {
	f := newFixture(t)
	f.member(t)

	last := f.buy(t, 9*45_000)
	if !last.CardClosed {
		t.Error("a full card did not close")
	}
	if last.Card.CardNo != 1 {
		t.Errorf("card = %d, want 1", last.Card.CardNo)
	}
	var closed sql.NullInt64
	if err := f.db.QueryRow(`SELECT closed_at FROM membership_cards WHERE card_no = 1`).Scan(&closed); err != nil {
		t.Fatalf("load card: %v", err)
	}
	if !closed.Valid {
		t.Error("closed_at was not written")
	}

	next := f.buy(t, 45_000)
	if next.Card.CardNo != 2 {
		t.Errorf("next purchase landed on card %d, want 2", next.Card.CardNo)
	}
	if next.Card.Filled != 1 {
		t.Errorf("new card filled = %d, want 1", next.Card.Filled)
	}
	// Stamps never migrate between cards.
	if n := f.count(t, `SELECT COUNT(*) FROM membership_stamps WHERE card_id = (SELECT id FROM membership_cards WHERE card_no = 2)`); n != 1 {
		t.Errorf("%d stamps on card 2, want 1", n)
	}
}

// Corrections never edit: a void writes a compensating entry, and the sums say
// the balance is where it was.
func TestVoidFreesTheStampAndCompensates(t *testing.T) {
	f := newFixture(t)
	u := f.member(t)
	before := f.balance(t, u.ID)

	res := f.buy(t, 45_000)
	if f.balance(t, u.ID) != before+1 {
		t.Fatalf("precondition: balance = %d, want %d", f.balance(t, u.ID), before+1)
	}

	voided, err := f.svc.Void(context.Background(), res.Purchase.ID, "salah ketik nominal", operatorPhone)
	if err != nil {
		t.Fatalf("void: %v", err)
	}
	if !voided.Voided || voided.VoidReason != "salah ketik nominal" {
		t.Errorf("purchase = %+v, want it marked with its reason", voided)
	}
	if got := f.balance(t, u.ID); got != before {
		t.Errorf("balance = %d, want %d — the compensation is missing", got, before)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM loyalty_entries WHERE kind = 'adjust'`); n != 1 {
		t.Errorf("%d adjust entries, want 1", n)
	}
	// The compensation names the operator and the purchase, because the ledger
	// has no actor column.
	var note string
	if err := f.db.QueryRow(`SELECT reference_id FROM loyalty_entries WHERE kind = 'adjust'`).Scan(&note); err != nil {
		t.Fatalf("load adjust: %v", err)
	}
	for _, want := range []string{res.Purchase.ID, operatorPhone, "salah ketik nominal"} {
		if !strings.Contains(note, want) {
			t.Errorf("adjust reason %q does not name %q", note, want)
		}
	}

	// The freed stamp is available again: stamp_no is live+1.
	card, err := f.svc.StaffView(context.Background(), memberPhone)
	if err != nil {
		t.Fatalf("staff view: %v", err)
	}
	if card.Filled != 0 {
		t.Errorf("card filled = %d, want 0 after the void", card.Filled)
	}
	next := f.buy(t, 45_000)
	if next.Card.Filled != 1 {
		t.Errorf("card filled = %d after re-stamping, want 1", next.Card.Filled)
	}
}

func TestVoidOfAnOlderPurchaseIsRefused(t *testing.T) {
	f := newFixture(t)
	f.member(t)

	first := f.buy(t, 45_000)
	// An hour later, a second purchase. The first is no longer the tail, and
	// voiding it would leave a hole in the middle of the card.
	f.clock.advance(time.Hour)
	f.buy(t, 45_000)

	if _, err := f.svc.Void(context.Background(), first.Purchase.ID, "salah", operatorPhone); !errors.Is(err, ErrNotVoidable) {
		t.Errorf("void of a covered purchase = %v, want ErrNotVoidable", err)
	}
}

func TestVoidIsRefusedAfterADay(t *testing.T) {
	f := newFixture(t)
	f.member(t)

	res := f.buy(t, 45_000)
	f.clock.advance(25 * time.Hour)

	_, err := f.svc.Void(context.Background(), res.Purchase.ID, "salah", operatorPhone)
	if !errors.Is(err, ErrNotVoidable) {
		t.Errorf("void after a day = %v, want ErrNotVoidable", err)
	}
	if !strings.Contains(err.Error(), "24 jam") {
		t.Errorf("message = %q, want it to name the window", err)
	}
}

// The one consequence a compensating entry cannot undo: the customer walked
// away with the gift.
func TestVoidIsRefusedWhenTheGiftWasHandedOver(t *testing.T) {
	f := newFixture(t)
	f.member(t)

	res := f.buy(t, 90_000)
	f.clock.set(time.Date(2026, 9, 14, 9, 0, 0, 0, WIB))

	card, err := f.svc.StaffView(context.Background(), memberPhone)
	if err != nil {
		t.Fatalf("staff view: %v", err)
	}
	if _, err := f.svc.Redeem(context.Background(), card.Slots[1].Reward.ID, operatorPhone); err != nil {
		t.Fatalf("redeem: %v", err)
	}

	if _, err := f.svc.Void(context.Background(), res.Purchase.ID, "salah", operatorPhone); !errors.Is(err, ErrNotVoidable) {
		t.Errorf("void after the gift was given = %v, want ErrNotVoidable", err)
	}
}

func TestVoidRequiresAReason(t *testing.T) {
	f := newFixture(t)
	f.member(t)
	res := f.buy(t, 45_000)

	if _, err := f.svc.Void(context.Background(), res.Purchase.ID, "  ", operatorPhone); !errors.Is(err, ErrNotVoidable) {
		t.Errorf("void with no reason = %v, want ErrNotVoidable", err)
	}
}

// A voided purchase's slot is filled by the next purchase, so the card still
// reaches nine live stamps and closes.
func TestVoidedSlotIsFilledByNextPurchaseAndCardReachesNine(t *testing.T) {
	f := newFixture(t)
	f.member(t)

	// Eight stamps, not closed.
	for i := 0; i < 8; i++ {
		f.buy(t, 45_000)
	}

	// The ninth stamp closes the card.
	ninth := f.buy(t, 45_000)
	if !ninth.CardClosed {
		t.Fatal("expected card to close on 9th stamp")
	}

	// Void the ninth stamp — the card reopens.
	if _, err := f.svc.Void(context.Background(), ninth.Purchase.ID, "salah", operatorPhone); err != nil {
		t.Fatalf("void: %v", err)
	}

	card, err := f.svc.StaffView(context.Background(), memberPhone)
	if err != nil {
		t.Fatalf("staff view: %v", err)
	}
	if card.Filled != 8 {
		t.Errorf("card filled = %d, want 8 after void", card.Filled)
	}
	if card.Slots[8].Filled {
		t.Error("slot 9 is still filled after void")
	}

	// The next purchase fills slot 9 and closes the card again.
	next := f.buy(t, 45_000)
	if !next.CardClosed {
		t.Error("expected card to close again after re-stamping slot 9")
	}
	if next.Card.Filled != 9 {
		t.Errorf("card filled = %d, want 9", next.Card.Filled)
	}
}

func TestTodayIsTheStudioDay(t *testing.T) {
	f := newFixture(t)
	f.member(t)

	// 00:30 WIB on the 14th, which is still the 13th in UTC.
	f.clock.set(time.Date(2026, 9, 14, 0, 30, 0, 0, WIB))
	res := f.buy(t, 45_000)

	today, err := f.svc.Today(context.Background())
	if err != nil {
		t.Fatalf("today: %v", err)
	}
	if len(today) != 1 || today[0].ID != res.Purchase.ID {
		t.Fatalf("today = %+v, want the purchase just made", today)
	}
	if today[0].Phone == "" {
		t.Error("today's list does not say whose purchase it is")
	}

	// The same purchase is yesterday's from the studio's next day.
	f.clock.advance(24 * time.Hour)
	today, err = f.svc.Today(context.Background())
	if err != nil {
		t.Fatalf("today: %v", err)
	}
	if len(today) != 0 {
		t.Errorf("today = %+v, want nothing a day later", today)
	}
}

func TestOpenRewardsListsTheGiftsStillToHandOver(t *testing.T) {
	f := newFixture(t)
	f.member(t)
	f.buy(t, 90_000)

	open, err := f.svc.OpenRewards(context.Background(), 0)
	if err != nil {
		t.Fatalf("open rewards: %v", err)
	}
	if len(open) != 1 || open[0].Kind != "extra_minutes_5" {
		t.Fatalf("open = %+v, want the slot-2 gift", open)
	}
	// Not yet redeemable is still listed, so the console can say so rather than
	// hiding a gift the customer can see on their own card.
	if open[0].Available {
		t.Error("a gift issued today is listed as available")
	}
	if open[0].Phone == "" {
		t.Error("the list does not say whose gift it is")
	}
}

// The import's whole point: the cards in customers' hands become rows without
// anybody re-registering, and the gifts already ticked are not handed over
// twice.
func TestImportTwiceIsSafeAndReissuesNothing(t *testing.T) {
	f := newFixture(t)
	rows := []ImportRow{
		{Name: "Isyara Hadza", Phone: memberPhone, Barcode: "CARD-001", Stamps: 3},
	}

	first, err := f.svc.Import(context.Background(), rows, operatorPhone, "")
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if first.Imported != 1 || first.Skipped != 0 {
		t.Fatalf("first = %+v, want one imported", first)
	}
	if len(first.Cards) != 1 || first.Cards[0].Filled != 3 {
		t.Fatalf("cards = %+v, want one card with three stamps", first.Cards)
	}
	u, err := f.ident.UserByPhone(context.Background(), memberPhone)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	if got := f.balance(t, u.ID); got != 3 {
		t.Errorf("balance = %d, want 3", got)
	}
	// The slot-2 gift is minted and marked collected in the same step: the
	// customer already took it on paper.
	if n := f.count(t, `SELECT COUNT(*) FROM membership_rewards`); n != 1 {
		t.Errorf("%d gifts after importing three stamps, want 1 (slot 2)", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM membership_rewards WHERE redeemed_at IS NULL`); n != 0 {
		t.Errorf("%d gifts left uncollected, want 0 — the paper card already had them", n)
	}

	second, err := f.svc.Import(context.Background(), rows, operatorPhone, "")
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if second.Imported != 0 || second.Skipped != 1 {
		t.Errorf("second = %+v, want one skipped", second)
	}
	if got := f.balance(t, u.ID); got != 3 {
		t.Errorf("balance = %d, want 3 — the re-run credited again", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM membership_rewards`); n != 1 {
		t.Errorf("%d gifts after the re-run, want 1", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM membership_purchases`); n != 0 {
		t.Errorf("%d purchases after the re-run, want 0", n)
	}
}

// Somebody bringing in a card with eight ticks has collected the gifts for 2, 4,
// 6 and 8 on paper. The import mints them and marks them redeemed.
func TestImportedGiftsAreMarkedAsAlreadyCollected(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.Import(context.Background(),
		[]ImportRow{{Name: "Isyara Hadza", Phone: memberPhone, Barcode: "CARD-002", Stamps: 8}},
		operatorPhone, ""); err != nil {
		t.Fatalf("import: %v", err)
	}

	rows, err := f.db.Query(
		`SELECT stamp_no, redeemed_at, COALESCE(redeemed_by, '') FROM membership_rewards ORDER BY stamp_no`)
	if err != nil {
		t.Fatalf("rewards: %v", err)
	}
	defer rows.Close()

	got := map[int]bool{}
	for rows.Next() {
		var (
			no       int
			at       sql.NullInt64
			redeemer string
		)
		if err := rows.Scan(&no, &at, &redeemer); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if !at.Valid {
			t.Errorf("gift on slot %d was not marked collected", no)
		}
		if redeemer != operatorPhone {
			t.Errorf("gift on slot %d names %q, want the operator", no, redeemer)
		}
		got[no] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	for _, no := range []int{2, 4, 6, 8} {
		if !got[no] {
			t.Errorf("no gift minted for slot %d", no)
		}
	}
	if got[9] {
		t.Error("the ninth-slot gift was minted for an eight-stamp card")
	}
}

// Fewer stamps than the first milestone still opens the card: the paper card
// exists, and the row is what carries it in.
func TestImportWithOneStampStillCreatesTheCard(t *testing.T) {
	f := newFixture(t)

	res, err := f.svc.Import(context.Background(),
		[]ImportRow{{Name: "Isyara Hadza", Phone: memberPhone, Barcode: "CARD-003", Stamps: 1}},
		operatorPhone, "")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Cards) != 1 || res.Cards[0].Filled != 1 || res.Cards[0].CardNo != 1 {
		t.Fatalf("cards = %+v, want one opened card with one stamp", res.Cards)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM membership_rewards`); n != 0 {
		t.Errorf("%d gifts minted for a one-stamp card, want 0", n)
	}
}

// A full card in hand does not overflow into the next one: the row's stamps go
// on a newly opened card.
func TestImportOpensANewCardWhenTheOneInHandIsTooFull(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.Import(context.Background(),
		[]ImportRow{{Name: "Isyara Hadza", Phone: memberPhone, Barcode: "CARD-004", Stamps: 7}},
		operatorPhone, ""); err != nil {
		t.Fatalf("first import: %v", err)
	}
	res, err := f.svc.Import(context.Background(),
		[]ImportRow{{Name: "Isyara Hadza", Phone: memberPhone, Barcode: "CARD-005", Stamps: 4}},
		operatorPhone, "")
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if len(res.Cards) != 1 || res.Cards[0].CardNo != 2 || res.Cards[0].Filled != 4 {
		t.Fatalf("card = %+v, want card 2 with four stamps", res.Cards)
	}
	var closed sql.NullInt64
	if err := f.db.QueryRow(`SELECT closed_at FROM membership_cards WHERE card_no = 1`).Scan(&closed); err != nil {
		t.Fatalf("load card: %v", err)
	}
	if !closed.Valid {
		t.Error("the card in hand was not closed when the row moved to a new one")
	}
}

func TestMaskName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Isyara Hadza", "Isyara H."},
		{"Isyara Hadza Ramadhan", "Isyara H. R."},
		{"Isyara", "Isyara"},
		{"", ""},
		{"  Isyara   Hadza  ", "Isyara H."},
	}
	for _, tc := range tests {
		if got := maskName(tc.in); got != tc.want {
			t.Errorf("maskName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
