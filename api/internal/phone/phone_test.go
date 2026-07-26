package phone

import "testing"

func TestNormalizeAcceptsEverySpellingOfOneNumber(t *testing.T) {
	// The point of the table is the last column being identical throughout: all
	// of these are one handset, and therefore must be one account.
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"local trunk prefix", "081234567890", "+6281234567890"},
		{"local with dashes", "0812-3456-7890", "+6281234567890"},
		{"local with spaces", "0812 3456 7890", "+6281234567890"},
		{"international plus", "+6281234567890", "+6281234567890"},
		{"international spaced", "+62 812 3456 7890", "+6281234567890"},
		{"international no plus", "6281234567890", "+6281234567890"},
		{"bare national", "81234567890", "+6281234567890"},
		{"country code then trunk", "62081234567890", "+6281234567890"},
		{"parenthesised", "(0812) 3456-7890", "+6281234567890"},
		{"tel uri leftovers", "tel:+62-812-3456-7890", "+6281234567890"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Normalize(c.in)
			if err != nil {
				t.Fatalf("Normalize(%q) returned error: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestNormalizeAcceptsTheStudiosOwnPrintedNumber(t *testing.T) {
	// From the studio price list, printed as 0811-3777-10. Short enough to
	// suspect the PDF clipped a digit, but it is dialable and a real customer
	// could type it. The catalogue keeps its own "unverified" flag on this; the
	// normaliser must not double as a second, silent opinion.
	got, err := Normalize("0811-3777-10")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "+62811377710"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeRejectsWhatIsNotAnIndonesianMobile(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"separators only", "-- () --"},
		{"landline, not mobile", "0318765432"},   // Surabaya area code, starts 3
		{"jakarta landline", "0211234567"},       // starts 2
		{"too short", "08123456"},                // 7-digit NSN
		{"too long", "0812345678901234"},         // 15-digit NSN
		{"foreign number", "+14155552671"},       // US
		{"letters only", "not a phone"},          //
		{"malaysian mobile", "+60123456789"},     // +60, not +62
		{"country code but landline", "62211234567"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Normalize(c.in)
			if err == nil {
				t.Fatalf("Normalize(%q) = %q, want error", c.in, got)
			}
		})
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	// Normalising a stored value must be a no-op, or a re-save silently forks
	// the account.
	once, err := Normalize("0812-3456-7890")
	if err != nil {
		t.Fatal(err)
	}
	twice, err := Normalize(once)
	if err != nil {
		t.Fatal(err)
	}
	if once != twice {
		t.Errorf("not idempotent: %q then %q", once, twice)
	}
}

func TestWhatsAppDropsThePlus(t *testing.T) {
	if got, want := WhatsApp("+6281234567890"), "6281234567890"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
