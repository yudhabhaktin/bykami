package httpd

import (
	"errors"
	"strings"
)

// The same rules as api/internal/phone, copied rather than shared.
//
// They cannot be imported: that package is under another module's internal/,
// and the two modules are deliberately separate so the booth binary and the
// cloud can move apart. Promoting it to a shared module would couple every
// agent release to an API release for eight lines of string handling.
//
// The rules must not drift, because the phone number *is* the account: two
// spellings of one number become two accounts with two loyalty balances, and
// the ledger is append-only precisely so that history is awkward to rewrite.
// A number normalised one way here and another way in the cloud is that bug
// with extra steps.
var errInvalidPhone = errors.New("not a valid Indonesian mobile number")

const (
	countryCode = "62"

	// Indonesian mobile national significant numbers start with 8 and run 9 to
	// 12 digits.
	minNSN = 9
	maxNSN = 12
)

// normalizePhone is forgiving about input and strict about the result:
// "0811-3777-10", "+62 811 3777 10" and "811377710" all become "+62811377710".
func normalizePhone(raw string) (string, error) {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if digits == "" {
		return "", errInvalidPhone
	}

	var nsn string
	switch {
	case strings.HasPrefix(digits, "620"):
		// A trunk prefix left in place after the country code was added, which
		// happens whenever a number is copied out of a local contact list.
		nsn = digits[3:]
	case strings.HasPrefix(digits, countryCode):
		nsn = digits[2:]
	case strings.HasPrefix(digits, "0"):
		nsn = digits[1:]
	default:
		nsn = digits
	}

	if !strings.HasPrefix(nsn, "8") {
		return "", errInvalidPhone
	}
	if len(nsn) < minNSN || len(nsn) > maxNSN {
		return "", errInvalidPhone
	}
	return "+" + countryCode + nsn, nil
}
