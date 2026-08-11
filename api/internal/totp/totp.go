// Package totp implements the time-based one-time passwords in RFC 6238, the
// six digits an authenticator app shows.
//
// This is the operator console's login, and it exists because the console did
// not have one. Customer accounts are phone-first and password-free, verified
// by a code sent over WhatsApp — and WhatsApp needs a provider account that
// does not exist yet, so on the deployed box nobody could sign in at all. An
// authenticator needs no provider, no delivery, and nothing to pay for: the
// secret is agreed once, and after that both ends can compute the same number
// from the clock.
//
// It is deliberately not offered to customers. Asking somebody who came in for
// photographs to install an authenticator app is a worse experience than the
// code they already expect, and the reasoning for phone-first accounts in
// internal/identity has not changed. This is for the handful of people who
// operate the place.
//
// # What it is not
//
// A second factor. There is one factor here and it is the authenticator; the
// phone number names which operator is signing in but proves nothing, and the
// allow-list in internal/admin is authorisation rather than authentication.
// Calling it MFA would suggest a defence in depth that is not there. What it
// does replace is a delivery channel that can be intercepted at the network,
// with a secret that never travels after enrolment.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// Period, Digits and SHA-1 are RFC 6238's defaults, and every authenticator
	// assumes them when the URI does not say otherwise. Changing any of them
	// means every app that silently ignores the parameter shows wrong codes
	// forever, so they are constants rather than configuration.
	Period = 30 * time.Second
	Digits = 6

	// How many steps either side of now are accepted. One is the RFC's own
	// suggestion: it covers a phone whose clock is half a minute out and the
	// operator who starts typing at 29 seconds past, at the cost of widening
	// the guessable set from one code to three.
	skew = 1

	// 160 bits, matching SHA-1's output. RFC 4226 requires at least 128 and
	// recommends this.
	secretBytes = 20
)

// ErrBadSecret means the stored or supplied secret was not the base32 an
// authenticator was given.
var ErrBadSecret = errors.New("totp: malformed secret")

// base32Encoding is unpadded uppercase, which is what every authenticator app
// expects and what a person retyping a secret by hand can actually read. The
// "=" padding of the standard encoding is legal in the URI but several apps
// reject it.
var base32Encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a fresh shared secret.
//
// It panics if the system has no entropy, which matches internal/identity: a
// box that cannot produce random bytes must not carry on and issue a
// predictable credential.
func NewSecret() []byte {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		panic("totp: entropy unavailable: " + err.Error())
	}
	return b
}

// Encode renders a secret the way an authenticator expects to be given one,
// which is also the form typed in by hand when a camera will not cooperate.
func Encode(secret []byte) string {
	return base32Encoding.EncodeToString(secret)
}

// Decode reads a secret back. Spaces and lowercase are accepted because both
// turn up whenever a human has been involved.
func Decode(s string) ([]byte, error) {
	s = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	b, err := base32Encoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadSecret, err)
	}
	if len(b) == 0 {
		return nil, ErrBadSecret
	}
	return b, nil
}

// Code returns the digits for the step containing t.
func Code(secret []byte, t time.Time) string {
	return codeAt(secret, step(t))
}

// Verify reports whether code is valid at t, and returns the time step it
// matched.
//
// The step is returned rather than kept private because the caller has to
// record it: a code stays valid for the rest of its period, so without
// remembering which step was spent, one shoulder-surfed number can be used
// again by somebody else within the minute. That guard lives in internal/mfa,
// which is the only thing with somewhere to write it down.
func Verify(secret []byte, code string, t time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != Digits {
		return 0, false
	}

	now := step(t)
	for delta := int64(-skew); delta <= skew; delta++ {
		at := now + delta
		// Constant time, because a comparison that stops at the first wrong
		// digit tells an attacker how much of a guess was right.
		if subtle.ConstantTimeCompare([]byte(codeAt(secret, at)), []byte(code)) == 1 {
			return at, true
		}
	}
	return 0, false
}

// URI is the otpauth:// address an authenticator reads out of a QR code.
//
// issuer names the business and account names the operator, and both appear in
// the app's list. issuer is repeated as a parameter as well as in the label:
// the label prefix is the older convention and the parameter the newer one, and
// apps in the wild read one or the other.
// The secret needs no escaping: base32 is letters and digits.
func URI(issuer, account string, secret []byte) string {
	return "otpauth://totp/" + escapeLabel(issuer) + ":" + escapeLabel(account) +
		"?secret=" + Encode(secret) + "&issuer=" + escapeParam(issuer)
}

// escapeLabel percent-encodes one half of the label.
//
// url.PathEscape is left to do the work except for "+", which it deliberately
// leaves alone because a plus is a legal path character. An authenticator that
// reuses its query-string parser reads that plus as a space, and an operator's
// +62 number then shows up in the app as a leading blank. Encoding it costs two
// characters and removes the ambiguity.
func escapeLabel(s string) string {
	return strings.ReplaceAll(url.PathEscape(s), "+", "%2B")
}

// escapeParam percent-encodes a query value, spelling a space as %20 rather
// than the "+" that url.QueryEscape produces — the same ambiguity as above, in
// the other direction. QueryEscape emits a bare "+" only ever for a space, so
// swapping them back is safe; a literal plus has already become %2B.
func escapeParam(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// step is the counter both ends derive from the clock, and the whole basis of
// this working without a network: two machines that agree on the time agree on
// the number.
func step(t time.Time) int64 {
	return t.Unix() / int64(Period/time.Second)
}

// codeAt is HOTP from RFC 4226 — an HMAC of the counter, from which four bytes
// are picked at an offset the hash itself names, and the low digits kept.
func codeAt(secret []byte, counter int64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(counter))

	mac := hmac.New(sha1.New, secret)
	mac.Write(buf[:])
	sum := mac.Sum(nil)

	// Dynamic truncation: the last nibble chooses where to read, so the digits
	// do not always come from the same part of the hash.
	offset := int(sum[len(sum)-1] & 0x0F)
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7FFFFFFF

	mod := uint32(1)
	for range Digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", Digits, value%mod)
}
