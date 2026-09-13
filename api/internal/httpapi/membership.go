package httpapi

import (
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/bhaktiyudha/bykami/api/internal/membership"
	"github.com/bhaktiyudha/bykami/api/internal/phone"
)

// The customer's way to their own card, and the only public read this package
// serves that is about a person.
//
// POST /v1/membership/lookup takes a WhatsApp number and returns the card. No
// session, no cookie, no code, no provider. The number is already written on the
// front of the printed card and is already the account, so it is the key that
// needs no distribution.
//
// It is deliberately not behind the auth gate that answers 503: there is nothing
// to authenticate and no OTP to send, and putting the card behind a provider
// that does not exist would leave the paper card as the real system. It is
// behind CORS, because a browser on the brand domain reads it — see cors in
// booking.go, which is shared rather than copied.
//
// What it costs is stated plainly in the design record: anybody who types a
// number learns whether it is a member and how many stamps they hold. The
// mitigations are the rate limit and the masked name, both inside
// internal/membership, and the fact that nothing on this surface can take a
// gift.

type lookupRequest struct {
	Phone string `json:"phone"`
}

// membershipLookup answers with the member's card.
//
// The response body is membership.CardView itself, whose json tags are written
// for this route: the console's fields (the account id, the reward's issuer and
// redemption) are tagged out, so there is no second shape to keep in step with
// the first.
func (a *API) membershipLookup(w http.ResponseWriter, r *http.Request) {
	// Same shape as the booth routes: a deployment nobody wired cannot serve,
	// and a 503 says so where a nil dereference would say nothing an operator
	// could act on.
	if a.membership == nil {
		a.fail(w, http.StatusServiceUnavailable,
			"membership lookup is not configured on this deployment")
		return
	}

	var req lookupRequest
	if !a.decode(w, r, &req) {
		return
	}

	card, err := a.membership.Lookup(r.Context(), req.Phone, callerIP(r))
	switch {
	case err == nil:
		a.write(w, http.StatusOK, card)
	case errors.Is(err, phone.ErrInvalid):
		a.fail(w, http.StatusBadRequest, "not a valid Indonesian mobile number")
	case errors.Is(err, membership.ErrNotFound):
		a.fail(w, http.StatusNotFound, "no card for that number")
	case errors.Is(err, membership.ErrTooManyLookups):
		a.fail(w, http.StatusTooManyRequests,
			"too many lookups for this number or address; try again later")
	default:
		a.internal(w, "membership lookup", err)
	}
}

// callerIP is who to count the request against.
//
// The process listens on localhost and Cloudflare Tunnel dials out to it, so
// RemoteAddr is the tunnel and is the same for every caller on the internet —
// counting against it would rate-limit the whole world as one. Cloudflare sets
// CF-Connecting-IP to the true client and X-Forwarded-For carries the chain, so
// they are tried in that order. On anything else (a test, `astro dev`) the
// socket address is the answer.
//
// A caller can forge either header if it can reach this port directly, which is
// exactly why the port is bound to localhost and the tunnel is the only route
// to it. The limit is a rate limit, not an identity.
func callerIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" {
		return ip
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		// The left-most entry is the original client; everything after it is
		// proxies this request passed through.
		if first, _, ok := strings.Cut(fwd, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(fwd)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
