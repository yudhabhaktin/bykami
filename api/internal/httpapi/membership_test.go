package httpapi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/booking"
	"github.com/bhaktiyudha/bykami/api/internal/frames"
	"github.com/bhaktiyudha/bykami/api/internal/httpapi"
	"github.com/bhaktiyudha/bykami/api/internal/identity"
	"github.com/bhaktiyudha/bykami/api/internal/instagram"
	"github.com/bhaktiyudha/bykami/api/internal/loyalty"
	"github.com/bhaktiyudha/bykami/api/internal/membership"
	"github.com/bhaktiyudha/bykami/api/internal/phone"
	"github.com/bhaktiyudha/bykami/api/internal/store"
)

// The public card lookup. It is the one route here that is about a person and
// needs no session, so what it does and does not say is the whole test.
type memFixture struct {
	h     http.Handler
	svc   *membership.Service
	ident *identity.Service
	db    *sql.DB
}

func newMemAPI(t *testing.T, origins ...string) memFixture {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ident := identity.New(db, &capturingSender{})
	ledger := loyalty.New(db)
	svc := membership.New(db, ledger, ident, nil)
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))

	h := httpapi.New(httpapi.Config{
		Identity: ident, Loyalty: ledger, Frames: frames.New(db),
		Booking: booking.New(db, 0), Instagram: instagram.New(db),
		Membership: svc,
		Health:     func(ctx context.Context) error { return db.PingContext(ctx) },
		Log:        log,
		// The deployed state, and the point: the card needs no OTP, so this
		// route must not answer 503 with the auth surface closed.
		AuthEnabled:    false,
		BookingOrigins: origins,
	})
	return memFixture{h: h, svc: svc, ident: ident, db: db}
}

// memberCard builds a member with two stamps, so the card has a gift on slot 2
// and something to report.
func (f memFixture) memberCard(t *testing.T, raw string) {
	t.Helper()
	if _, err := f.svc.Purchase(context.Background(), raw, "Isyara Hadza", 90_000, "", "081234567890", ""); err != nil {
		t.Fatalf("purchase: %v", err)
	}
}

func lookup(t *testing.T, h http.Handler, raw string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"phone": raw})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/membership/lookup", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

type cardJSON struct {
	Name              string `json:"name"`
	Phone             string `json:"phone"`
	CardNo            int    `json:"card_no"`
	StampsPerCard     int    `json:"stamps_per_card"`
	Filled            int    `json:"filled"`
	AmountPerStampIDR int64  `json:"amount_per_stamp_idr"`
	Slots             []struct {
		No     int    `json:"no"`
		Label  string `json:"label"`
		Filled bool   `json:"filled"`
		Reward *struct {
			ID          string  `json:"id"`
			Label       string  `json:"label"`
			Kind        string  `json:"kind"`
			Available   bool    `json:"available"`
			RedeemedAt  *string `json:"redeemed_at"`
			RedeemAfter string  `json:"redeem_after"`
		} `json:"reward"`
	} `json:"slots"`
}

const memberRaw = "081298765432"

func TestCardLookupReturnsTheMaskedCard(t *testing.T) {
	f := newMemAPI(t)
	f.memberCard(t, memberRaw)

	w := lookup(t, f.h, memberRaw)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", w.Code, w.Body.String())
	}

	card := decodeBody[cardJSON](t, w)
	if card.Name != "Isyara H." {
		t.Errorf("name = %q, want the masked first name and an initial", card.Name)
	}
	e164, err := phone.Normalize(memberRaw)
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}
	if card.Phone != e164 {
		t.Errorf("phone = %q, want %q in full — the caller typed it", card.Phone, e164)
	}
	if card.CardNo != 1 || card.StampsPerCard != 9 || card.Filled != 2 {
		t.Errorf("card = no %d, %d of %d, want no 1, 2 of 9",
			card.CardNo, card.Filled, card.StampsPerCard)
	}
	if card.AmountPerStampIDR != 45_000 {
		t.Errorf("amount_per_stamp_idr = %d, want 45000", card.AmountPerStampIDR)
	}
	if len(card.Slots) != 9 {
		t.Fatalf("%d slots, want 9", len(card.Slots))
	}

	first, second := card.Slots[0], card.Slots[1]
	if first.No != 1 || first.Label != "" || !first.Filled || first.Reward != nil {
		t.Errorf("slot 1 = %+v, want filled with no gift", first)
	}
	if second.Label != "+5 menit" || !second.Filled || second.Reward == nil {
		t.Fatalf("slot 2 = %+v, want the +5 menit gift", second)
	}
	// The same-day rule is stored, so it is reportable rather than a surprise
	// when the operator presses Tukar.
	if second.Reward.Kind != "extra_minutes_5" || second.Reward.Available {
		t.Errorf("reward = %+v, want a +5 menit gift that is not yet redeemable", second.Reward)
	}
	if second.Reward.RedeemedAt != nil {
		t.Errorf("redeemed_at = %v, want null", *second.Reward.RedeemedAt)
	}
	after, err := time.Parse(time.RFC3339, second.Reward.RedeemAfter)
	if err != nil {
		t.Fatalf("redeem_after %q is not RFC3339: %v", second.Reward.RedeemAfter, err)
	}
	if !strings.HasSuffix(second.Reward.RedeemAfter, "+07:00") {
		t.Errorf("redeem_after = %q, want a WIB offset", second.Reward.RedeemAfter)
	}
	if after.Hour() != 0 || after.Minute() != 0 {
		t.Errorf("redeem_after = %s, want midnight WIB", after)
	}
	if got := after.Sub(time.Now()); got <= 0 || got > 48*time.Hour {
		t.Errorf("redeem_after is %s away, want the next studio day", got)
	}
}

// The console's fields must not leak onto a surface anyone can call: an account
// id is a way to address the account, and the reward's issuer is bookkeeping.
func TestCardLookupSerialisesNothingInternal(t *testing.T) {
	f := newMemAPI(t)
	f.memberCard(t, memberRaw)

	body := lookup(t, f.h, memberRaw).Body.String()
	for _, leak := range []string{"user_id", "issued_at", "redeemed_by", "stamp_no", "idempotency"} {
		if strings.Contains(body, leak) {
			t.Errorf("response carries %q: %s", leak, body)
		}
	}
}

func TestCardLookupOfAStrangerIsNotFound(t *testing.T) {
	f := newMemAPI(t)

	if w := lookup(t, f.h, "081200000001"); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestCardLookupRejectsGarbage(t *testing.T) {
	f := newMemAPI(t)

	if w := lookup(t, f.h, "12345"); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// A list of numbers can be walked through, so the walk is what costs. 429 is
// the answer rather than a 500, and the limit is per phone as well as per IP.
func TestCardLookupIsRateLimited(t *testing.T) {
	f := newMemAPI(t)
	f.memberCard(t, memberRaw)

	sawOK, sawLimit := 0, false
	for range 100 {
		switch w := lookup(t, f.h, memberRaw); w.Code {
		case http.StatusOK:
			sawOK++
		case http.StatusTooManyRequests:
			sawLimit = true
		default:
			t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
		}
		if sawLimit {
			break
		}
	}
	if !sawLimit {
		t.Fatal("the lookup was never rate-limited")
	}
	if sawOK == 0 {
		t.Error("the first lookup was refused")
	}
}

// It is read by a browser on the brand domain, so it needs CORS — the same
// allow-list the booking routes use, not a wildcard.
func TestCardLookupIsBehindTheOriginAllowList(t *testing.T) {
	f := newMemAPI(t)
	f.memberCard(t, memberRaw)

	allowed := httptest.NewRequest(http.MethodOptions, "/v1/membership/lookup", nil)
	allowed.Header.Set("Origin", "https://studio.bykami.id")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, allowed)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://studio.bykami.id" {
		t.Errorf("Allow-Origin = %q, want the calling site", got)
	}

	stranger := httptest.NewRequest(http.MethodOptions, "/v1/membership/lookup", nil)
	stranger.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	f.h.ServeHTTP(w, stranger)
	if w.Code == http.StatusNoContent {
		t.Error("preflight from an unknown origin was answered with CORS headers")
	}
}

// The card needs no OTP, so the auth gate that answers 503 must not stand in
// front of it — otherwise nothing can use the card until a provider exists,
// which is exactly the state this change replaces.
func TestCardLookupIsNotBehindTheAuthGate(t *testing.T) {
	f := newMemAPI(t)
	f.memberCard(t, memberRaw)

	if w := lookup(t, f.h, memberRaw); w.Code == http.StatusServiceUnavailable {
		t.Fatalf("lookup = 503, want the card: %s", w.Body.String())
	}
}
