// Package phone normalises Indonesian mobile numbers to E.164.
//
// This matters more here than it looks. The phone number *is* the account
// identifier — there are no passwords and no email — so two spellings of one
// number are two accounts, with two loyalty balances and two booking histories.
// Merging those after the fact means reconciling a ledger, which the design
// deliberately made append-only and therefore awkward to rewrite. Normalising on
// the way in is the only cheap moment.
package phone

import (
	"errors"
	"strings"
)

// ErrInvalid covers every rejection. Callers surface one message to the user
// regardless of which rule failed: a login form that explains *why* a number
// looks wrong is also telling an attacker which numbers exist.
var ErrInvalid = errors.New("phone: not a valid Indonesian mobile number")

const (
	countryCode = "62"

	// Indonesian mobile national significant numbers start with 8 and run 9 to
	// 12 digits. The lower bound is deliberately permissive: the studio's own
	// printed number is 9 digits, which is short enough to suspect a typo in the
	// price list but is still dialable, and rejecting it here would block a real
	// customer to satisfy a guess.
	minNSN = 9
	maxNSN = 12
)

// Normalize accepts the shapes an Indonesian number is actually written in —
// "0811-3777-10", "+62 811 3777 10", "62811377710", "811377710" — and returns
// the single E.164 form "+62811377710".
//
// It is intentionally strict about the result and forgiving about the input.
// Anything a human might type as separator is discarded; anything that could
// change which handset rings is not.
func Normalize(raw string) (string, error) {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if digits == "" {
		return "", ErrInvalid
	}

	// Reduce every accepted spelling to the national significant number, then
	// validate once. Doing it in this order means a new input format costs a
	// case here rather than another copy of the length and prefix rules.
	var nsn string
	switch {
	case strings.HasPrefix(digits, "620"):
		// "+62 0811…" — a trunk prefix left in place after the country code was
		// added. Common when a number is copied between a local contact list and
		// an international field.
		nsn = digits[3:]
	case strings.HasPrefix(digits, countryCode):
		nsn = digits[2:]
	case strings.HasPrefix(digits, "0"):
		nsn = digits[1:]
	default:
		nsn = digits
	}

	if !strings.HasPrefix(nsn, "8") {
		return "", ErrInvalid
	}
	if len(nsn) < minNSN || len(nsn) > maxNSN {
		return "", ErrInvalid
	}

	return "+" + countryCode + nsn, nil
}

// WhatsApp renders a normalised number the way wa.me expects it: digits only,
// no leading plus. Kept next to Normalize so the two cannot drift.
func WhatsApp(e164 string) string {
	return strings.TrimPrefix(e164, "+")
}
