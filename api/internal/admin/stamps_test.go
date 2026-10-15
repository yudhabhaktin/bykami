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

	// Correcting a mistyped amount is the frequent action after a POST, and
	// every POST redirects to the top: today's list belongs above the gifts.
	todayAt := strings.Index(body, "<h2>Stempel hari ini</h2>")
	giftsAt := strings.Index(body, `<h2 id="hadiah">Hadiah menunggu</h2>`)
	if todayAt < 0 || giftsAt < 0 {
		t.Fatal("the page is missing one of the two section headings")
	}
	if todayAt > giftsAt {
		t.Error("Stempel hari ini is below Hadiah menunggu; the correction is a scroll away")
	}

	// The outstanding list has to be usable at phone width: three cells, and
	// the not-yet-due state written in words rather than a long date.
	rest := body[giftsAt:]
	tbody := strings.Index(rest, "<tbody>")
	if tbody < 0 {
		t.Fatal("the outstanding list has no tbody")
	}
	rest = rest[tbody:]
	rowEnd := strings.Index(rest, "</tr>")
	if rowEnd < 0 {
		t.Fatal("the outstanding list has no row")
	}
	row := rest[:rowEnd]
	if n := strings.Count(row, "<td"); n != 3 {
		t.Errorf("outstanding row has %d cells, want 3 at phone width: %s", n, row)
	}
	if !strings.Contains(row, "bisa ditukar besok") {
		t.Errorf("outstanding row does not say the gift is due tomorrow: %s", row)
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
	if !strings.Contains(f.get(t, redirectPath(t, w), token).Body.String(), "bukan hari ini") {
		t.Error("a same-day redemption is not explained as a tomorrow thing")
	}
	if n := countRows(t, f, `SELECT COUNT(*) FROM membership_rewards WHERE redeemed_at IS NOT NULL`); n != 0 {
		t.Errorf("%d gifts were redeemed on the day they were issued", n)
	}
}

// Tukar has to land on the member whose gift it was: the operator may have a
// different number in the search box, and posting that instead would hand the
// confirmation to the wrong person.
func TestStampsRedeemLandsOnTheGiftsOwner(t *testing.T) {
	f := newFixture(t)
	token := f.signIn(t)

	// Two purchases: four stamps mint the slot-2 and slot-4 gifts, so one stays
	// outstanding after the redeem and the unsearched page still has a name.
	for i := 0; i < 2; i++ {
		if _, err := f.stamps.Purchase(context.Background(), customerPhone, "Isyara Hadza", 90_000, "", operatorLabel, ""); err != nil {
			t.Fatalf("seed purchase: %v", err)
		}
	}
	card, err := f.stamps.StaffView(context.Background(), customerPhone)
	if err != nil {
		t.Fatalf("staff view: %v", err)
	}
	reward := card.Slots[1].Reward
	if reward == nil {
		t.Fatal("no gift on slot 2")
	}
	if _, err := f.db.Exec(`UPDATE membership_rewards SET redeem_after = 0`); err != nil {
		t.Fatalf("make gifts due: %v", err)
	}

	// A different number sits in the search box.
	csrf := csrfFrom(t, f.get(t, "/stamps", token).Body.String())
	w := f.post(t, "/stamps/redeem", url.Values{
		"csrf":   {csrf},
		"phone":  {"081200000001"},
		"reward": {reward.ID},
	}, token)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("redeem = %d, want 303: %s", w.Code, w.Body.String())
	}

	// The Location says which number the operator was sent back to. The body
	// below says what rendered there: a number that is not a member still
	// prints the name in the name field's placeholder and in the outstanding
	// list, so only the card's own "name · full number" line proves it.
	if loc := w.Header().Get("Location"); !strings.Contains(loc, url.QueryEscape("+6281298765432")) {
		t.Errorf("redirect = %q, want it to carry the gift owner's own number", loc)
	}

	back := f.get(t, redirectPath(t, w), token)
	if back.Code != http.StatusOK {
		t.Fatalf("stamps = %d, want 200", back.Code)
	}
	body := back.Body.String()
	if !strings.Contains(body, "Hadiah ditukar") {
		t.Errorf("the confirmation did not land on the gift's owner: %s", body)
	}
	// The console shows the number in full, and the template writes "+" as
	// &#43;. This line only exists when the owner's own card rendered.
	if !strings.Contains(body, "Isyara Hadza · &#43;6281298765432") {
		t.Errorf("the redirect did not render the gift owner's card: %s", body)
	}
	// With no search, the only place a name can come from is the outstanding
	// list, so this is also the list naming the member.
	if page := f.get(t, "/stamps", token).Body.String(); !strings.Contains(page, "Isyara Hadza") {
		t.Error("the outstanding list does not name the member whose gift is waiting")
	}
}

// A member with no name still gets a readable row: a bare dash, not an empty
// cell the operator has to interpret.
func TestStampsOutstandingListMarksAMissingName(t *testing.T) {
	f := newFixture(t)
	token := f.signIn(t)

	if _, err := f.stamps.Purchase(context.Background(), "081200000002", "", 90_000, "", operatorLabel, ""); err != nil {
		t.Fatalf("seed purchase: %v", err)
	}

	body := f.get(t, "/stamps", token).Body.String()
	// A plain check for the dash would pass anyway: the page's help text uses
	// one. The cell itself is the assertion.
	if !strings.Contains(body, "<td>—<br>") {
		t.Errorf("a nameless member's outstanding row has no dash: %s", body)
	}

	// The card they are sent to after Tukar has the same hole, and it is the
	// one place the operator looks for whose card it is.
	card := f.get(t, "/stamps?phone=081200000002", token).Body.String()
	if !strings.Contains(card, "— · &#43;6281200000002") {
		t.Errorf("the card of a nameless member does not start with a dash: %s", card)
	}
}

// A ready gift can be handed over from the card's own slot, without hunting the
// outstanding list for the row. The number posted is the card's own, so every
// failure comes back to this member.
func TestStampsCardSlotRedeemsAnAvailableGift(t *testing.T) {
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
	if _, err := f.db.Exec(`UPDATE membership_rewards SET redeem_after = 0`); err != nil {
		t.Fatalf("make gifts due: %v", err)
	}

	page := f.get(t, "/stamps?phone="+customerPhone, token)
	if page.Code != http.StatusOK {
		t.Fatalf("stamps = %d, want 200: %s", page.Code, page.Body.String())
	}
	grid := sliceBetween(t, page.Body.String(), `<div class="stampgrid">`, `<h2>Tulis stempel</h2>`)
	for _, want := range []string{
		`action="/stamps/redeem"`,
		`name="reward" value="` + reward.ID + `"`,
		`name="phone" value="&#43;6281298765432"`,
	} {
		if !strings.Contains(grid, want) {
			t.Errorf("the card grid is missing %q: %s", want, grid)
		}
	}

	csrf := csrfFrom(t, page.Body.String())
	form := url.Values{
		"csrf":   {csrf},
		"phone":  {"+6281298765432"},
		"reward": {reward.ID},
	}
	w := f.post(t, "/stamps/redeem", form, token)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("redeem = %d, want 303: %s", w.Code, w.Body.String())
	}
	done := f.get(t, redirectPath(t, w), token)
	if done.Code != http.StatusOK {
		t.Fatalf("stamps = %d, want 200", done.Code)
	}
	body := done.Body.String()
	grid = sliceBetween(t, body, `<div class="stampgrid">`, `<h2>Tulis stempel</h2>`)
	if !strings.Contains(grid, "sudah ditukar") {
		t.Errorf("the redeemed slot does not say sudah ditukar: %s", grid)
	}
	if strings.Contains(grid, `action="/stamps/redeem"`) {
		t.Errorf("the redeemed slot still offers Tukar: %s", grid)
	}
	if strings.Contains(body, reward.ID) {
		t.Errorf("the redeemed reward's id is still on the page: %s", body)
	}

	// A double tap: the second POST reports the first and lands back on the
	// same member's card.
	twice := f.post(t, "/stamps/redeem", form, token)
	if twice.Code != http.StatusSeeOther {
		t.Fatalf("second redeem = %d, want 303", twice.Code)
	}
	back := f.get(t, redirectPath(t, twice), token).Body.String()
	if !strings.Contains(back, "Hadiah itu sudah ditukar oleh "+operatorLabel) {
		t.Errorf("a double tap does not name who took the gift: %s", back)
	}
	if !strings.Contains(back, "Isyara Hadza · &#43;6281298765432") {
		t.Errorf("the double tap did not land on the member's own card: %s", back)
	}
}

// Only a gift that can actually be handed over gets a button. A slot already
// redeemed or not yet due stays text.
func TestStampsCardSlotOffersNoTukarForRedeemedOrNotYetDue(t *testing.T) {
	f := newFixture(t)
	token := f.signIn(t)

	for i := 0; i < 2; i++ {
		if _, err := f.stamps.Purchase(context.Background(), customerPhone, "Isyara Hadza", 90_000, "", operatorLabel, ""); err != nil {
			t.Fatalf("seed purchase %d: %v", i, err)
		}
	}
	card, err := f.stamps.StaffView(context.Background(), customerPhone)
	if err != nil {
		t.Fatalf("staff view: %v", err)
	}
	due := card.Slots[1].Reward
	waiting := card.Slots[3].Reward
	if due == nil || waiting == nil {
		t.Fatal("two purchases did not mint gifts on slots 2 and 4")
	}
	// Make only slot 2 due, then hand it over through the service.
	if _, err := f.db.Exec(`UPDATE membership_rewards SET redeem_after = 0 WHERE id = ?`, due.ID); err != nil {
		t.Fatalf("make slot 2 due: %v", err)
	}
	if _, err := f.stamps.Redeem(context.Background(), due.ID, operatorLabel); err != nil {
		t.Fatalf("redeem slot 2: %v", err)
	}

	grid := sliceBetween(t, f.get(t, "/stamps?phone="+customerPhone, token).Body.String(),
		`<div class="stampgrid">`, `<h2>Tulis stempel</h2>`)
	if !strings.Contains(grid, "sudah ditukar") {
		t.Errorf("the redeemed slot does not say sudah ditukar: %s", grid)
	}
	if !strings.Contains(grid, "bisa ditukar besok") {
		t.Errorf("the not-yet-due slot does not say bisa ditukar besok: %s", grid)
	}
	if strings.Contains(grid, `action="/stamps/redeem"`) {
		t.Errorf("a slot that cannot be handed over still offers Tukar: %s", grid)
	}
}

// A gift left on a closed earlier card has no slot to sit in, so the card must
// say it exists and point at the list rather than letting the operator read the
// missing photobox as not earned.
func TestStampsCardPointsAtEarlierCardsGifts(t *testing.T) {
	f := newFixture(t)
	token := f.signIn(t)

	// Card 1 closes with gifts on 2, 4, 6, 8 and 9; card 2 carries slot 2.
	for i := 0; i < 6; i++ {
		if _, err := f.stamps.Purchase(context.Background(), customerPhone, "Isyara Hadza", 90_000, "", operatorLabel, ""); err != nil {
			t.Fatalf("seed purchase %d: %v", i, err)
		}
	}

	card := sliceBetween(t, f.get(t, "/stamps?phone="+customerPhone, token).Body.String(),
		"<h2>Kartu", "<h2>Tulis stempel</h2>")
	if !strings.Contains(card, "5 hadiah dari kartu sebelumnya belum ditukar.") {
		t.Errorf("the card does not say how many gifts sit on an earlier card: %s", card)
	}
	if !strings.Contains(card, `href="#hadiah"`) {
		t.Errorf("the earlier-gift line does not link to the outstanding list: %s", card)
	}

	// All gifts on the open card: the line must not appear.
	if _, err := f.stamps.Purchase(context.Background(), "081200000004", "Bima", 90_000, "", operatorLabel, ""); err != nil {
		t.Fatalf("seed other member: %v", err)
	}
	own := sliceBetween(t, f.get(t, "/stamps?phone=081200000004", token).Body.String(),
		"<h2>Kartu", "<h2>Tulis stempel</h2>")
	if strings.Contains(own, "kartu sebelumnya") {
		t.Error("a member whose gifts are all on the open card is told about an earlier card")
	}
}

// Searching a number narrows the outstanding list to that member, so a gift
// left on a closed card is still reachable even though the card view shows
// only the member's latest card.
func TestStampsListNarrowsToTheSearchedMember(t *testing.T) {
	f := newFixture(t)
	token := f.signIn(t)

	// Six 90.000 purchases earn twelve stamps: card 1 closes with gifts on
	// slots 2, 4, 6, 8 and 9, and card 2 carries a fresh slot-2 gift.
	for i := 0; i < 6; i++ {
		if _, err := f.stamps.Purchase(context.Background(), customerPhone, "Isyara Hadza", 90_000, "", operatorLabel, ""); err != nil {
			t.Fatalf("seed purchase %d: %v", i, err)
		}
	}
	if _, err := f.stamps.Purchase(context.Background(), "081200000003", "Dewi Lestari", 90_000, "", operatorLabel, ""); err != nil {
		t.Fatalf("seed other member: %v", err)
	}

	searched := f.get(t, "/stamps?phone="+customerPhone, token).Body.String()
	if !strings.Contains(searched, "Kartu 2") {
		t.Fatal("the searched page is not showing card 2; the setup did not close card 1")
	}
	if strings.Contains(searched, "Dewi Lestari") {
		t.Error("the searched page lists another member's gift")
	}
	// Card 2's grid prints the same slot-9 label, so slice from the heading to
	// assert on the list itself.
	gifts := sliceBetween(t, searched, `<h2 id="hadiah">Hadiah menunggu</h2>`, "</main>")
	if !strings.Contains(gifts, "Free photobox 1 orang") {
		t.Errorf("the narrowed list dropped the gift left on the closed card: %s", gifts)
	}

	all := f.get(t, "/stamps", token).Body.String()
	if !strings.Contains(all, "Dewi Lestari") {
		t.Error("the unsearched page does not list every outstanding gift")
	}
}

// sliceBetween is the page from the first occurrence of from to the next
// occurrence of to. Tests assert on a section through it so the same words
// elsewhere on the page cannot pass for the section under test.
func sliceBetween(t *testing.T, body, from, to string) string {
	t.Helper()
	i := strings.Index(body, from)
	if i < 0 {
		t.Fatalf("page missing %q", from)
	}
	rest := body[i:]
	j := strings.Index(rest, to)
	if j < 0 {
		t.Fatalf("page missing %q after %q", to, from)
	}
	return rest[:j]
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
