package admin_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// /stamps is the page that replaces the pen. It is still an operator page, and
// the two things worth pinning are that a stranger cannot read it and that an
// amount below a stamp is refused with the shortfall rather than a 500.

func TestStampsRequiresAnOperator(t *testing.T) {
	f := newFixture(t)

	for _, tc := range []struct{ name, cookie string }{
		{"no cookie", ""},
		{"garbage cookie", "not-a-real-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := f.get(t, "/stamps", tc.cookie)
			if w.Code != http.StatusSeeOther {
				t.Errorf("status = %d, want 303 to the login page", w.Code)
			}
			if w.Code == http.StatusOK && strings.Contains(w.Body.String(), "Stempel") {
				t.Error("the stamp page rendered for somebody who is not an operator")
			}
		})
	}
}

func TestStampsPageShowsTheCard(t *testing.T) {
	f := newFixture(t)
	token := f.signIn(t)

	if _, err := f.stamps.Purchase(context.Background(), customerPhone, "Isyara Hadza", 90_000, "", operatorLabel, ""); err != nil {
		t.Fatalf("seed purchase: %v", err)
	}

	w := f.get(t, "/stamps?phone="+customerPhone, token)
	if w.Code != http.StatusOK {
		t.Fatalf("stamps = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// The console shows the name in full: the operator is checking it against
	// the card in front of them, not harvesting it.
	for _, want := range []string{
		"Kartu 1", "Isyara Hadza", customerPhone,
		// html/template escapes "+" to &#43; in text context, which a browser
		// renders as a plus either way.
		"5 menit", "Free photobox 1 orang",
		"Tulis stempel", "Hadiah menunggu", "Stempel hari ini",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// The same-day rule has to be visible, not sprung on the customer.
	if !strings.Contains(body, "bisa ditukar") {
		t.Error("the page does not say when an un-redeemable gift becomes redeemable")
	}
}

// A handwritten amount off a receipt is where a factor of ten gets typed, so a
// below-threshold amount is refused with the shortfall named.
func TestStampsPurchaseRendersTheShortfall(t *testing.T) {
	f := newFixture(t)
	token := f.signIn(t)

	page := f.get(t, "/stamps?phone="+customerPhone, token)
	csrf := csrfFrom(t, page.Body.String())

	w := f.post(t, "/stamps/purchase", url.Values{
		"csrf":   {csrf},
		"phone":  {customerPhone},
		"amount": {"40000"},
		"name":   {"Isyara Hadza"},
	}, token)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("purchase = %d, want 303: %s", w.Code, w.Body.String())
	}

	back := f.get(t, redirectPath(t, w), token)
	if back.Code != http.StatusOK {
		t.Fatalf("stamps = %d, want 200", back.Code)
	}
	if !strings.Contains(back.Body.String(), "kurang Rp 5.000") {
		t.Errorf("the refusal does not name the shortfall: %s", back.Body.String())
	}

	// Nothing was written: not a zero-point row, not a card.
	if n := countRows(t, f, `SELECT COUNT(*) FROM membership_purchases`); n != 0 {
		t.Errorf("%d purchases written for a below-minimum amount, want 0", n)
	}
}

func TestStampsPurchaseWritesTheStamps(t *testing.T) {
	f := newFixture(t)
	token := f.signIn(t)

	csrf := csrfFrom(t, f.get(t, "/stamps?phone="+customerPhone, token).Body.String())
	w := f.post(t, "/stamps/purchase", url.Values{
		"csrf":   {csrf},
		"phone":  {customerPhone},
		"amount": {"90000"},
		"name":   {"Isyara Hadza"},
	}, token)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("purchase = %d, want 303: %s", w.Code, w.Body.String())
	}

	if n := countRows(t, f, `SELECT COUNT(*) FROM membership_stamps`); n != 2 {
		t.Errorf("%d stamps written for Rp 90.000, want 2", n)
	}
	body := f.get(t, redirectPath(t, w), token).Body.String()
	if !strings.Contains(body, "2 stempel tersimpan") {
		t.Errorf("the page does not confirm what was written: %s", body)
	}
}

// The write path is a POST from a page, so it carries the same CSRF rule as
// every other form in the console.
func TestStampsPurchaseRequiresCSRF(t *testing.T) {
	f := newFixture(t)
	token := f.signIn(t)

	for _, tc := range []struct{ name, csrf string }{
		{"missing", ""},
		{"wrong", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := f.post(t, "/stamps/purchase", url.Values{
				"csrf": {tc.csrf}, "phone": {customerPhone},
				"amount": {"90000"}, "name": {"Isyara Hadza"},
			}, token)
			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", w.Code)
			}
		})
	}
	if n := countRows(t, f, `SELECT COUNT(*) FROM membership_purchases`); n != 0 {
		t.Errorf("%d purchases written behind a failed CSRF check, want 0", n)
	}
}

// Redeeming is an operator action, and the same-day rule is enforced by the
// database rather than by the page.
func TestStampsRedeemRefusesAGiftIssuedToday(t *testing.T) {
	f := newFixture(t)
	token := f.signIn(t)

	if _, err := f.stamps.Purchase(context.Background(), customerPhone, "Isyara Hadza", 90_000, "", operatorLabel, ""); err != nil {
		t.Fatalf("seed purchase: %v", err)
	}
	card, err := f.stamps.StaffView(context.Background(), customerPhone)
	if err != nil {
		t.Fatalf("staff view: %v", err)
	}
	reward := card.Slots[1].Reward
	if reward == nil {
		t.Fatal("no gift on slot 2")
	}

	csrf := csrfFrom(t, f.get(t, "/stamps?phone="+customerPhone, token).Body.String())
	w := f.post(t, "/stamps/redeem", url.Values{
		"csrf":   {csrf},
		"phone":  {customerPhone},
		"reward": {reward.ID},
	}, token)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("redeem = %d, want 303", w.Code)
	}
	if !strings.Contains(f.get(t, redirectPath(t, w), token).Body.String(), "besok") {
		t.Error("a same-day redemption is not explained as a tomorrow thing")
	}
	if n := countRows(t, f, `SELECT COUNT(*) FROM membership_rewards WHERE redeemed_at IS NOT NULL`); n != 0 {
		t.Errorf("%d gifts were redeemed on the day they were issued", n)
	}
}

// redirectPath is the Location a POST sent the operator to, as a request path.
func redirectPath(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	loc := w.Header().Get("Location")
	if loc == "" {
		t.Fatal("no Location header on the redirect")
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location %q: %v", loc, err)
	}
	return u.Path + "?" + u.RawQuery
}
