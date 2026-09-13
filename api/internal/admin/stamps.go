package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/bhaktiyudha/bykami/api/internal/identity"
	"github.com/bhaktiyudha/bykami/api/internal/membership"
	"github.com/bhaktiyudha/bykami/api/internal/phone"
)

// The stamp card in the console: the page that replaces the pen.
//
// Everything here is behind staffOnly, and there is no customer-facing entry
// point beside it. Redemption is an operator action because a gift is a
// fulfilment somebody has to hand over — the worst case of the public lookup
// being enumerated is knowing that a number has seven stamps.
//
// The page's shape follows the card a member is holding: the ladder, the slots
// filled, the gifts outstanding, and what was written today. The one number the
// operator types by hand is the amount off a receipt, which is why the form
// says what a stamp costs and the page shows the stamps an amount earns before
// it is written.

func (c *Console) stamps(w http.ResponseWriter, r *http.Request, op identity.User) {
	q := strings.TrimSpace(r.URL.Query().Get("phone"))
	p := page{
		Title:      "Stempel",
		Operator:   op.Phone,
		CSRF:       csrfToken(r),
		StampQuery: q,
	}

	// Outcomes of the POSTs below, which redirect rather than render so that a
	// refresh cannot resubmit a stamp. ok is either the number of stamps just
	// written or a sentence already written for the operator.
	if ok := r.URL.Query().Get("ok"); ok != "" {
		if n, err := strconv.Atoi(ok); err == nil && n > 1 {
			p.Notice = strconv.Itoa(n) + " stempel tersimpan."
		} else if ok == "1" {
			p.Notice = "Tersimpan."
		} else {
			p.Notice = ok
		}
	}
	if msg := r.URL.Query().Get("err"); msg != "" {
		p.Error = msg
	}

	if q != "" {
		p.StampSearched = true
		card, err := c.members.StaffView(r.Context(), q)
		switch {
		case err == nil:
			p.StampCard = &card
		case errors.Is(err, membership.ErrNotFound):
			// Not an error in the operator's way — it is the invitation to
			// create the member, which is how somebody joins. The template
			// says so under the search box, and deliberately does not overwrite
			// whatever the POST above just reported: an amount below the
			// threshold and an unknown number are two facts at once, and the
			// shortfall is the one the operator has to act on.
		case errors.Is(err, phone.ErrInvalid):
			p.Error = "Nomor tidak valid."
		default:
			c.serverError(w, r, "stamp lookup", err, p)
			return
		}
	}

	// Both lists are read on every visit: a gift outstanding is the thing the
	// next customer asks about, and today's stamps are the thing the operator
	// voids when they typed the amount wrong.
	rewards, err := c.members.OpenRewards(r.Context(), 50)
	if err != nil {
		c.serverError(w, r, "open rewards", err, p)
		return
	}
	p.StampRewards = rewards

	today, err := c.members.Today(r.Context())
	if err != nil {
		c.serverError(w, r, "stamps today", err, p)
		return
	}
	p.StampToday = today

	c.render(w, r, http.StatusOK, "stamps.html", p)
}

// stampsPurchase writes a stamp from the counter.
//
// The amount is typed by hand from a receipt, which is why the page shows what
// a stamp costs and refuses a below-threshold amount with the shortfall rather
// than recording a zero-point row.
func (c *Console) stampsPurchase(w http.ResponseWriter, r *http.Request, op identity.User) {
	if !validCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}

	rawPhone := strings.TrimSpace(r.FormValue("phone"))
	amount, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("amount")), 10, 64)
	reference := strings.TrimSpace(r.FormValue("reference"))
	name := strings.TrimSpace(r.FormValue("name"))

	switch {
	case rawPhone == "":
		c.backToStamps(w, r, "", "Nomor WhatsApp wajib diisi.")
		return
	case err != nil || amount <= 0:
		c.backToStamps(w, r, rawPhone, "Nominal harus berupa angka lebih dari nol.")
		return
	}

	// outlet is empty: the console serves one studio today, and inventing an
	// outlet id here would make the settlement report that does not exist yet
	// wrong in a way nobody would notice for months.
	res, err := c.members.Purchase(r.Context(), rawPhone, name, amount, reference, op.Phone, "")
	switch {
	case err == nil:
	case errors.Is(err, membership.ErrBelowMinimum):
		// The real message, with the shortfall in it. "Below the minimum" with
		// no number leaves the operator doing arithmetic on a phone.
		c.backToStamps(w, r, rawPhone, "Nominal kurang: "+detail(err, membership.ErrBelowMinimum))
		return
	case errors.Is(err, membership.ErrNotVoidable):
		c.backToStamps(w, r, rawPhone, detail(err, membership.ErrNotVoidable))
		return
	case errors.Is(err, phone.ErrInvalid):
		c.backToStamps(w, r, rawPhone, "Nomor tidak valid.")
		return
	default:
		c.log.Error("admin: stamp purchase", "operator", op.Phone, "err", err)
		c.backToStamps(w, r, rawPhone, "Gagal menyimpan stempel.")
		return
	}

	c.log.Info("admin: stamps written", "operator", op.Phone, "phone", rawPhone,
		"amount", amount, "stamps", res.Purchase.Stamps, "replayed", res.Replayed,
		"card", res.Card.CardNo, "closed", res.CardClosed)
	if res.Replayed {
		// A double-tap, and the page should say so rather than looking as if it
		// wrote a second set.
		c.backToStamps(w, r, rawPhone, "Referensi itu sudah pernah dicatat; tidak ada stempel tambahan.")
		return
	}
	c.redirect(w, r, "/stamps?phone="+urlQueryEscape(rawPhone)+
		"&ok="+strconv.Itoa(res.Purchase.Stamps))
}

// stampsRedeem hands a gift over. The write is one conditional UPDATE, so two
// staff members pressing at once produce one success and one message.
func (c *Console) stampsRedeem(w http.ResponseWriter, r *http.Request, op identity.User) {
	if !validCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}

	phoneQuery := strings.TrimSpace(r.FormValue("phone"))
	id := strings.TrimSpace(r.FormValue("reward"))

	reward, err := c.members.Redeem(r.Context(), id, op.Phone)
	switch {
	case err == nil:
	case errors.Is(err, membership.ErrAlreadyRedeemed):
		c.backToStamps(w, r, phoneQuery, "Hadiah itu sudah ditukar.")
		return
	case errors.Is(err, membership.ErrNotYetRedeemable):
		c.backToStamps(w, r, phoneQuery, "Hadiah itu bisa ditukar besok, bukan hari ini.")
		return
	case errors.Is(err, membership.ErrNotFound):
		c.backToStamps(w, r, phoneQuery, "Hadiah tidak ditemukan.")
		return
	default:
		c.log.Error("admin: redeem reward", "operator", op.Phone, "reward", id, "err", err)
		c.backToStamps(w, r, phoneQuery, "Gagal menukar hadiah.")
		return
	}

	c.log.Info("admin: reward redeemed", "operator", op.Phone, "reward", reward.ID,
		"phone", reward.Phone, "label", reward.Label)
	c.redirect(w, r, "/stamps?phone="+urlQueryEscape(phoneQuery)+
		"&ok="+urlQueryEscape("Hadiah ditukar: "+reward.Label))
}

// stampsVoid undoes a purchase. Corrections never edit: the purchase and its
// stamps are marked and a compensating ledger entry is written, so the mistake
// stays visible.
func (c *Console) stampsVoid(w http.ResponseWriter, r *http.Request, op identity.User) {
	if !validCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}

	phoneQuery := strings.TrimSpace(r.FormValue("phone"))
	id := strings.TrimSpace(r.FormValue("purchase"))
	reason := strings.TrimSpace(r.FormValue("reason"))

	p, err := c.members.Void(r.Context(), id, reason, op.Phone)
	switch {
	case err == nil:
	case errors.Is(err, membership.ErrNotVoidable):
		// One sentinel with a specific message per case: the operator reads the
		// message, and the page has no business restating the rules.
		c.backToStamps(w, r, phoneQuery, detail(err, membership.ErrNotVoidable))
		return
	case errors.Is(err, membership.ErrNotFound):
		c.backToStamps(w, r, phoneQuery, "Pembelian tidak ditemukan.")
		return
	default:
		c.log.Error("admin: void purchase", "operator", op.Phone, "purchase", id, "err", err)
		c.backToStamps(w, r, phoneQuery, "Gagal membatalkan pembelian.")
		return
	}

	c.log.Info("admin: purchase voided", "operator", op.Phone, "purchase", p.ID,
		"phone", p.Phone, "stamps", p.Stamps, "reason", reason)
	c.backToStamps(w, r, phoneQuery, "")
}

// backToStamps returns to the page an operator was looking at, carrying the
// message. Redirect rather than render, like every other POST here, so that a
// refresh cannot repeat the write.
func (c *Console) backToStamps(w http.ResponseWriter, r *http.Request, phoneQuery, msg string) {
	to := "/stamps"
	if phoneQuery != "" {
		to += "?phone=" + urlQueryEscape(phoneQuery)
		if msg != "" {
			to += "&err=" + urlQueryEscape(msg)
		}
	} else if msg != "" {
		to += "?err=" + urlQueryEscape(msg)
	}
	// A void that worked says so; a refusal says why.
	if msg == "" {
		to = "/stamps"
		if phoneQuery != "" {
			to += "?phone=" + urlQueryEscape(phoneQuery) + "&ok=1"
		} else {
			to += "?ok=1"
		}
	}
	c.redirect(w, r, to)
}

// detail strips a sentinel's prefix from an error message, leaving the part
// written for the operator. One message per case, produced where the rule
// lives, rather than a second copy of the rules in the handler.
func detail(err, sentinel error) string {
	msg := err.Error()
	if rest, ok := strings.CutPrefix(msg, sentinel.Error()+": "); ok {
		return rest
	}
	return msg
}

// formatRupiah renders an amount the way the price list does: Rp 45.000.
func formatRupiah(n int64) string {
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
