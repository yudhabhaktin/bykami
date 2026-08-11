package totp

import (
	"strings"
	"testing"
	"time"
)

// The secret RFC 6238 publishes its test vectors against.
var rfcSecret = []byte("12345678901234567890")

// The published vectors, which are the only reason to believe this file
// computes the same numbers as the app on somebody's phone.
//
// The RFC tabulates eight digits and this package produces six. That is not an
// adaptation of the test: truncation is a remainder modulo a power of ten, so
// the six-digit code is the last six digits of the eight-digit one, exactly.
func TestMatchesTheRFCVectors(t *testing.T) {
	tests := []struct {
		unix int64
		want string // the last six digits of the RFC's eight
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	}

	for _, tc := range tests {
		at := time.Unix(tc.unix, 0).UTC()
		if got := Code(rfcSecret, at); got != tc.want {
			t.Errorf("Code(T=%d) = %s, want %s", tc.unix, got, tc.want)
		}
	}
}

// One step either side, and no further. The window is what lets an operator
// whose phone clock has drifted sign in; making it wider would multiply the
// codes a guesser can hit for no gain a correct clock does not already give.
func TestTheSkewWindowIsOneStepEitherSide(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()

	tests := []struct {
		steps int64
		want  bool
	}{
		{-2, false},
		{-1, true},
		{0, true},
		{1, true},
		{2, false},
	}

	for _, tc := range tests {
		at := now.Add(time.Duration(tc.steps) * Period)
		code := Code(rfcSecret, at)

		got, ok := Verify(rfcSecret, code, now)
		if ok != tc.want {
			t.Errorf("a code %d steps away: accepted = %v, want %v", tc.steps, ok, tc.want)
		}
		// The step is what the replay guard writes down, so it has to name the
		// code that actually matched rather than whatever the clock says now.
		if ok {
			if want := step(now) + tc.steps; got != want {
				t.Errorf("a code %d steps away matched step %d, want %d", tc.steps, got, want)
			}
		}
	}
}

func TestAWrongCodeIsRejected(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	right := Code(rfcSecret, now)

	tests := []struct {
		name string
		code string
	}{
		{"empty", ""},
		{"too short", right[:5]},
		{"too long", right + "0"},
		{"not digits", "abcdef"},
		{"another secret's code", Code([]byte("09876543210987654321"), now)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := Verify(rfcSecret, tc.code, now); ok {
				t.Errorf("Verify(%q) accepted it", tc.code)
			}
		})
	}
}

// Whitespace turns up whenever a person has retyped what their phone showed,
// and several apps display the code as two groups of three.
func TestACodeSurvivesBeingRetyped(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	code := Code(rfcSecret, now)

	for _, typed := range []string{code, " " + code + " ", code[:3] + " " + code[3:]} {
		if _, ok := Verify(rfcSecret, typed, now); !ok {
			t.Errorf("Verify(%q) rejected a correct code", typed)
		}
	}
}

func TestASecretSurvivesTheRoundTrip(t *testing.T) {
	secret := NewSecret()
	if len(secret) != secretBytes {
		t.Fatalf("secret is %d bytes, want %d", len(secret), secretBytes)
	}

	encoded := Encode(secret)
	if strings.Contains(encoded, "=") {
		t.Errorf("encoded secret is padded, which some authenticators reject: %q", encoded)
	}

	// Lowercase and spaces, as a person would type it off a screen.
	spaced := strings.ToLower(encoded[:4] + " " + encoded[4:])
	back, err := Decode(spaced)
	if err != nil {
		t.Fatalf("decode %q: %v", spaced, err)
	}
	if string(back) != string(secret) {
		t.Error("the secret did not survive being written down and read back")
	}
}

func TestTwoSecretsDiffer(t *testing.T) {
	// Not a test of the generator's quality, only that it is called: an
	// enrolment that handed every operator the same secret would work
	// perfectly, right up until any of them could sign in as any other.
	if string(NewSecret()) == string(NewSecret()) {
		t.Fatal("two secrets came back identical")
	}
}

func TestMalformedSecretsAreRefused(t *testing.T) {
	for _, s := range []string{"", "  ", "not!base32"} {
		if _, err := Decode(s); err == nil {
			t.Errorf("Decode(%q) was accepted", s)
		}
	}
}

// The URI is the whole of what the QR code carries, so an authenticator that
// cannot parse it is an operator who cannot enrol.
func TestURIIsWhatAnAuthenticatorExpects(t *testing.T) {
	secret := []byte("12345678901234567890")
	got := URI("bykami", "+6281234567890", secret)

	const want = "otpauth://totp/bykami:%2B6281234567890" +
		"?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ&issuer=bykami"
	if got != want {
		t.Errorf("URI =\n %s\nwant\n %s", got, want)
	}
}

// A plus in the label must not arrive as a space, and a space must not arrive
// as a plus. Both are the same confusion between path and query escaping, and
// the symptom is an operator's number displayed wrongly in the app.
func TestSpacesAndPlusesSurviveTheLabel(t *testing.T) {
	got := URI("studio by KAMI", "+6281234567890", []byte("12345678901234567890"))

	for _, want := range []string{
		"otpauth://totp/studio%20by%20KAMI:%2B6281234567890",
		"issuer=studio%20by%20KAMI",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("URI %q is missing %q", got, want)
		}
	}
	// A bare plus anywhere would be read as a space by a query-string parser.
	if strings.Contains(got, "+") {
		t.Errorf("URI carries an unescaped plus: %s", got)
	}
}
