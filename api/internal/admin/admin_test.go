package admin_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/admin"
	"github.com/bhaktiyudha/bykami/api/internal/booking"
	"github.com/bhaktiyudha/bykami/api/internal/frames"
	"github.com/bhaktiyudha/bykami/api/internal/gcal"
	"github.com/bhaktiyudha/bykami/api/internal/identity"
	"github.com/bhaktiyudha/bykami/api/internal/loyalty"
	"github.com/bhaktiyudha/bykami/api/internal/mfa"
	"github.com/bhaktiyudha/bykami/api/internal/phone"
	"github.com/bhaktiyudha/bykami/api/internal/store"
	"github.com/bhaktiyudha/bykami/api/internal/totp"
)

const (
	operatorPhone = "081234567890"
	customerPhone = "081298765432"
	cookieName    = "__Host-bykami-admin"
)

type capturingSender struct {
	mu   sync.Mutex
	code string
	sent int
}

func (s *capturingSender) Send(_ context.Context, _, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.code, s.sent = code, s.sent+1
	return nil
}

func (s *capturingSender) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.code
}

type fixture struct {
	h      http.Handler
	sender *capturingSender
	ident  *identity.Service
	ledger *loyalty.Ledger
	auth   *mfa.Registry
	db     *sql.DB

	// The enrolled secrets, by normalised number, so that a test can produce
	// the code an operator's phone would be showing.
	secrets map[string][]byte
}

// newFixture builds a console whose staff are all enrolled and can sign in.
// That is the ordinary state and what nearly every test wants; the ones about
// enrolment itself use newFixtureCal and enrol by hand.
func newFixture(t *testing.T, staff ...string) fixture {
	t.Helper()
	return newFixtureCal(t, nil, staff...)
}

// newFixtureCal is newFixture with a calendar attached, for the settings page.
// A nil calendar is the ordinary case and the deployed one: no Google credential,
// so admin.New receives a nil worker and the page says so.
func newFixtureCal(t *testing.T, cal booking.Calendar, staff ...string) fixture {
	t.Helper()
	return newFixtureConnect(t, cal, nil, staff...)
}

// newFixtureConnect is newFixtureCal with the Google consent flow wired up. A
// nil connect is the deployed default and every other fixture's case: the
// console then offers the paste-a-calendar-id form and nothing else.
func newFixtureConnect(t *testing.T, cal booking.Calendar, connect *gcal.Connect, staff ...string) fixture {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	sender := &capturingSender{}
	ident := identity.New(db, sender)
	ledger := loyalty.New(db)
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	desk := booking.New(db, 0)
	auth := mfa.New(db)

	// NewWorker returns nil for a nil calendar, which is what puts the console on
	// its "no credential" path rather than a typed nil that would panic.
	worker := booking.NewWorker(desk, cal, log, time.Minute, "Jajag")

	c, err := admin.New(ident, ledger, frames.New(db), desk, worker, auth, connect, log, staff)
	if err != nil {
		t.Fatalf("new console: %v", err)
	}

	f := fixture{
		h: c.Handler(), sender: sender, ident: ident, ledger: ledger,
		auth: auth, db: db, secrets: map[string][]byte{},
	}
	for _, s := range staff {
		f.enrol(t, s)
	}
	return f
}

// enrol gives one number an authenticator and remembers its secret.
func (f fixture) enrol(t *testing.T, rawPhone string) {
	t.Helper()

	e164, secret, err := f.auth.Enroll(context.Background(), rawPhone)
	if err != nil {
		t.Fatalf("enrol %s: %v", rawPhone, err)
	}
	f.secrets[e164] = secret
}

// code is what the authenticator for a number would be showing now.
func (f fixture) code(t *testing.T, rawPhone string, at time.Time) string {
	t.Helper()

	e164, err := phone.Normalize(rawPhone)
	if err != nil {
		t.Fatalf("normalise %s: %v", rawPhone, err)
	}
	secret, ok := f.secrets[e164]
	if !ok {
		t.Fatalf("%s has no enrolled authenticator", e164)
	}
	return totp.Code(secret, at)
}

// wrongCode returns six digits this operator's authenticator is not showing,
// and would not accept one step either side.
//
// Searched rather than hardcoded. A literal "000000" is the right code about
// one run in three hundred thousand, and a test that fails that rarely is one
// nobody can reproduce and everybody learns to re-run.
func (f fixture) wrongCode(t *testing.T, rawPhone string, at time.Time) string {
	t.Helper()

	accepted := map[string]bool{}
	for delta := -1; delta <= 1; delta++ {
		accepted[f.code(t, rawPhone, at.Add(time.Duration(delta)*totp.Period))] = true
	}
	for i := range 10 {
		if candidate := fmt.Sprintf("%06d", i); !accepted[candidate] {
			return candidate
		}
	}
	t.Fatal("could not find a wrong code")
	return ""
}

func (f fixture) get(t *testing.T, path, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f fixture) post(t *testing.T, path string, form url.Values, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

// signIn drives the real login flow and returns the session cookie value.
func (f fixture) signIn(t *testing.T, phone string) string {
	t.Helper()

	now := time.Now()
	w := f.post(t, "/login", url.Values{
		"phone": {phone}, "code": {f.code(t, phone, now)},
	}, "")

	// A second sign-in inside the same half-minute would present a code whose
	// step has already been spent, and the replay guard would refuse it. The
	// next step's code is inside the skew window and strictly later, so it is
	// accepted — which keeps a test that signs in twice from failing for a
	// reason that has nothing to do with what it is testing.
	if w.Code == http.StatusUnauthorized {
		w = f.post(t, "/login", url.Values{
			"phone": {phone}, "code": {f.code(t, phone, now.Add(totp.Period))},
		}, "")
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login = %d, want 303: %s", w.Code, w.Body.String())
	}

	for _, ck := range w.Result().Cookies() {
		if ck.Name == cookieName {
			return ck.Value
		}
	}
	t.Fatal("no session cookie set")
	return ""
}

func csrfFrom(t *testing.T, body string) string {
	t.Helper()
	const marker = `name="csrf" value="`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatal("no csrf token in page")
	}
	rest := body[i+len(marker):]
	return rest[:strings.Index(rest, `"`)]
}

func TestRootServesTheLoginPage(t *testing.T) {
	f := newFixture(t, operatorPhone)

	w := f.get(t, "/", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	body := w.Body.String()
	// Both fields on the one form: the number and the code its authenticator is
	// showing. There is no page in between, because nothing is sent anywhere.
	for _, want := range []string{"Masuk", "Nomor operator", "autentikator", `action="/login"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// An operator console on a public hostname must never be indexed.
	if !strings.Contains(body, `name="robots" content="noindex, nofollow"`) {
		t.Error("login page is missing the noindex directive")
	}
}

// A console nobody has enrolled against cannot be signed in to by anyone, and
// says which command fixes that — rather than refusing every correct code with
// a message about the code being wrong.
func TestLoginPageSaysWhenNobodyIsEnrolled(t *testing.T) {
	f := newFixture(t, operatorPhone)
	// Listed as staff, but with no authenticator — which is the state of a box
	// the moment this lands, before anybody has run the enrol command.
	if err := f.auth.Revoke(context.Background(), operatorPhone); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	body := f.get(t, "/", "").Body.String()

	if !strings.Contains(body, "Belum ada operator yang terdaftar") {
		t.Error("an empty registry is not explained on the page")
	}
	if !strings.Contains(body, "admin enroll") {
		t.Error("the page does not say how to enrol somebody")
	}
}

// A stranger must not be able to use this form to discover who is an operator.
// With one form and one message the property is easier to hold than it was
// across two steps, but it is the same property and worth the same test.
func TestEveryRefusalLooksTheSame(t *testing.T) {
	f := newFixture(t, operatorPhone)

	// An operator with the wrong code, and a stranger with the same wrong code.
	wrong := f.wrongCode(t, operatorPhone, time.Now())
	operator := f.post(t, "/login", url.Values{
		"phone": {operatorPhone}, "code": {wrong},
	}, "")
	stranger := f.post(t, "/login", url.Values{
		"phone": {customerPhone}, "code": {wrong},
	}, "")

	if operator.Code != stranger.Code {
		t.Errorf("status differs: operator %d, stranger %d", operator.Code, stranger.Code)
	}
	// The page echoes back the number that was typed, so compare with that one
	// varying part removed. Nothing else may differ.
	a := strings.ReplaceAll(operator.Body.String(), operatorPhone, "PHONE")
	b := strings.ReplaceAll(stranger.Body.String(), customerPhone, "PHONE")
	if a != b {
		t.Error("response differs between an operator and a stranger, which reveals who is staff")
	}
}

// The privilege boundary, and the reason enrolment can be an ordinary shell
// command: a correct code from a number nobody put on the allow-list opens
// nothing at all.
func TestACorrectCodeFromANonOperatorIsRefused(t *testing.T) {
	f := newFixture(t, operatorPhone)
	f.enrol(t, customerPhone) // a real authenticator, just not an operator's

	w := f.post(t, "/login", url.Values{
		"phone": {customerPhone}, "code": {f.code(t, customerPhone, time.Now())},
	}, "")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	for _, ck := range w.Result().Cookies() {
		if ck.Name == cookieName && ck.Value != "" {
			t.Fatal("a session cookie was issued to a non-operator")
		}
	}
}

// A stranger who guesses an operator's number must not be able to spend that
// operator's failed attempts and lock them out of their own console. The
// allow-list is checked first, so the registry never sees the attempt.
func TestAGuessAtANumberCannotLockTheOperatorOut(t *testing.T) {
	f := newFixture(t, operatorPhone)

	for range 10 {
		f.post(t, "/login", url.Values{
			"phone": {customerPhone}, "code": {"000000"},
		}, "")
	}

	if token := f.signIn(t, operatorPhone); token == "" {
		t.Error("the operator was locked out by guesses at somebody else's number")
	}
}

// The guard the spent-step record exists for. A code stays valid for the rest
// of its period, so one read over a shoulder must not be worth a second login.
func TestACodeCannotBeUsedTwice(t *testing.T) {
	f := newFixture(t, operatorPhone)
	code := f.code(t, operatorPhone, time.Now())

	if w := f.post(t, "/login", url.Values{"phone": {operatorPhone}, "code": {code}}, ""); w.Code != http.StatusSeeOther {
		t.Fatalf("first use = %d, want 303", w.Code)
	}
	w := f.post(t, "/login", url.Values{"phone": {operatorPhone}, "code": {code}}, "")

	if w.Code != http.StatusUnauthorized {
		t.Errorf("second use = %d, want 401", w.Code)
	}
	for _, ck := range w.Result().Cookies() {
		if ck.Name == cookieName && ck.Value != "" {
			t.Error("a replayed code was issued a session")
		}
	}
}

func TestOperatorSignsInAndReachesTheConsole(t *testing.T) {
	f := newFixture(t, operatorPhone)

	token := f.signIn(t, operatorPhone)

	w := f.get(t, "/customers", token)
	if w.Code != http.StatusOK {
		t.Fatalf("customers = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Cari pelanggan") {
		t.Error("console did not render the search page")
	}
}

// The cookie's scoping is the rule platform-architecture.md sets for this
// hostname: it must never enter the .bykami.id jar.
func TestSessionCookieIsHostOnlyAndLocked(t *testing.T) {
	f := newFixture(t, operatorPhone)

	w := f.post(t, "/login", url.Values{
		"phone": {operatorPhone}, "code": {f.code(t, operatorPhone, time.Now())},
	}, "")

	var ck *http.Cookie
	for _, got := range w.Result().Cookies() {
		if got.Name == cookieName {
			ck = got
		}
	}
	if ck == nil {
		t.Fatal("no session cookie")
	}
	if ck.Domain != "" {
		t.Errorf("Domain = %q, want empty so the cookie stays host-only", ck.Domain)
	}
	if !ck.HttpOnly {
		t.Error("cookie is not HttpOnly, so script could read the session")
	}
	if !ck.Secure {
		t.Error("cookie is not Secure")
	}
	if ck.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", ck.SameSite)
	}
	if ck.Path != "/" {
		t.Errorf("Path = %q, want /", ck.Path)
	}
	// The __Host- prefix is what makes a browser enforce all of the above.
	if !strings.HasPrefix(ck.Name, "__Host-") {
		t.Errorf("cookie name %q lacks the __Host- prefix", ck.Name)
	}
}

func TestConsoleRequiresASession(t *testing.T) {
	f := newFixture(t, operatorPhone)

	for _, tc := range []struct{ name, cookie string }{
		{"no cookie", ""},
		{"garbage cookie", "not-a-real-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := f.get(t, "/customers", tc.cookie)
			if w.Code != http.StatusSeeOther {
				t.Errorf("status = %d, want 303 to the login page", w.Code)
			}
		})
	}
}

// Privilege is derived from the phone on every request, never stored in the
// session — so revoking an operator takes effect immediately rather than
// whenever their session happens to expire.
func TestRevokingAnOperatorEndsAccessImmediately(t *testing.T) {
	f := newFixture(t, operatorPhone)
	token := f.signIn(t, operatorPhone)

	if w := f.get(t, "/customers", token); w.Code != http.StatusOK {
		t.Fatalf("precondition: customers = %d", w.Code)
	}

	// Same identity service and the same live session, a console that no longer
	// lists that number.
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	revoked, err := admin.New(f.ident, f.ledger, frames.New(f.db), booking.New(f.db, 0), nil, f.auth, nil, log, nil)
	if err != nil {
		t.Fatalf("new console: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/customers", nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	w := httptest.NewRecorder()
	revoked.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303 — a revoked operator still had access", w.Code)
	}
}

func TestCustomerLookupShowsBalanceAndHistory(t *testing.T) {
	f := newFixture(t, operatorPhone)
	token := f.signIn(t, operatorPhone)

	// Give the customer an account and some history.
	if err := f.ident.RequestCode(context.Background(), customerPhone); err != nil {
		t.Fatalf("request code: %v", err)
	}
	user, _, err := f.ident.VerifyCode(context.Background(), customerPhone, f.sender.last())
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := f.ledger.Earn(context.Background(), user.ID, "studio", 250, "visit-1", "key-1"); err != nil {
		t.Fatalf("earn: %v", err)
	}

	body := f.get(t, "/customers?phone="+customerPhone, token).Body.String()

	// html/template escapes "+" to &#43; in text context, so match the digits.
	// A browser renders it as +6281298765432 either way.
	for _, want := range []string{"6281298765432", "250", "studio", "visit-1"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestLookupOfAnUnknownNumber(t *testing.T) {
	f := newFixture(t, operatorPhone)
	token := f.signIn(t, operatorPhone)

	body := f.get(t, "/customers?phone="+customerPhone, token).Body.String()

	if !strings.Contains(body, "Tidak ada akun") {
		t.Error("an unknown number did not report itself as unknown")
	}
}

func TestAdjustWritesACompensatingEntry(t *testing.T) {
	f := newFixture(t, operatorPhone)
	token := f.signIn(t, operatorPhone)

	if err := f.ident.RequestCode(context.Background(), customerPhone); err != nil {
		t.Fatalf("request code: %v", err)
	}
	user, _, err := f.ident.VerifyCode(context.Background(), customerPhone, f.sender.last())
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	page := f.get(t, "/customers?phone="+customerPhone, token)
	csrf := csrfFrom(t, page.Body.String())

	w := f.post(t, "/customers/"+user.ID+"/adjust", url.Values{
		"csrf":     {csrf},
		"phone":    {customerPhone},
		"points":   {"75"},
		"vertical": {"studio"},
		"reason":   {"kompensasi cetak gagal"},
	}, token)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("adjust = %d, want 303: %s", w.Code, w.Body.String())
	}

	balance, err := f.ledger.Balance(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 75 {
		t.Errorf("balance = %d, want 75", balance)
	}

	// The operator is recorded, because the ledger has no actor column and an
	// anonymous adjustment cannot be defended later.
	entries, err := f.ledger.History(context.Background(), user.ID, 10)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(entries) != 1 || !strings.Contains(entries[0].ReferenceID, "+6281234567890") {
		t.Errorf("entry does not name the operator: %+v", entries)
	}
}

func TestAdjustRequiresCSRF(t *testing.T) {
	f := newFixture(t, operatorPhone)
	token := f.signIn(t, operatorPhone)

	for _, tc := range []struct{ name, csrf string }{
		{"missing", ""},
		{"wrong", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := f.post(t, "/customers/whoever/adjust", url.Values{
				"csrf": {tc.csrf}, "points": {"10"},
				"vertical": {"studio"}, "reason": {"x"},
			}, token)
			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", w.Code)
			}
		})
	}
}

func TestAdjustValidation(t *testing.T) {
	f := newFixture(t, operatorPhone)
	token := f.signIn(t, operatorPhone)

	if err := f.ident.RequestCode(context.Background(), customerPhone); err != nil {
		t.Fatalf("request code: %v", err)
	}
	user, _, err := f.ident.VerifyCode(context.Background(), customerPhone, f.sender.last())
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	csrf := csrfFrom(t, f.get(t, "/customers?phone="+customerPhone, token).Body.String())

	tests := []struct{ name, points, vertical, reason string }{
		{"zero points", "0", "studio", "x"},
		{"not a number", "abc", "studio", "x"},
		{"unknown vertical", "10", "nope", "x"},
		{"no reason", "10", "studio", "  "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f.post(t, "/customers/"+user.ID+"/adjust", url.Values{
				"csrf": {csrf}, "phone": {customerPhone},
				"points": {tc.points}, "vertical": {tc.vertical}, "reason": {tc.reason},
			}, token)

			balance, err := f.ledger.Balance(context.Background(), user.ID)
			if err != nil {
				t.Fatalf("balance: %v", err)
			}
			if balance != 0 {
				t.Errorf("balance = %d, want 0 — an invalid adjustment was written", balance)
			}
		})
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	f := newFixture(t, operatorPhone)
	token := f.signIn(t, operatorPhone)

	if w := f.post(t, "/logout", nil, token); w.Code != http.StatusSeeOther {
		t.Fatalf("logout = %d, want 303", w.Code)
	}

	// Server-side, not merely cleared in the browser.
	if w := f.get(t, "/customers", token); w.Code != http.StatusSeeOther {
		t.Errorf("session still worked after logout: %d", w.Code)
	}
}

// The allow-list is normalised, so an operator configured as +62… is the same
// person as one who types 0812… into the form.
func TestStaffListIsNormalised(t *testing.T) {
	f := newFixture(t, "+6281234567890")

	if token := f.signIn(t, "0812-3456-7890"); token == "" {
		t.Error("a differently-formatted operator number was not recognised")
	}
}

// A mistyped allow-list must stop startup. One that matches nobody looks
// exactly like a working one until someone tries to log in.
func TestUnparseableStaffNumberIsAStartupError(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	_, err = admin.New(identity.New(db, &capturingSender{}), loyalty.New(db), frames.New(db),
		booking.New(db, 0), nil, mfa.New(db), nil, log, []string{"not-a-phone-number"})
	if err == nil {
		t.Fatal("an unparseable operator number was accepted")
	}
}

func TestConsolePagesAreNotStorable(t *testing.T) {
	f := newFixture(t, operatorPhone)
	token := f.signIn(t, operatorPhone)

	for _, path := range []string{"/", "/customers"} {
		w := f.get(t, path, token)
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s Cache-Control = %q, want no-store", path, got)
		}
		if got := w.Header().Get("Content-Security-Policy"); got == "" && w.Code == http.StatusOK {
			t.Errorf("%s has no CSP", path)
		}
	}
}
