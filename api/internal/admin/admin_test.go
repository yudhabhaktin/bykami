package admin_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/bhaktiyudha/bykami/api/internal/admin"
	"github.com/bhaktiyudha/bykami/api/internal/frames"
	"github.com/bhaktiyudha/bykami/api/internal/identity"
	"github.com/bhaktiyudha/bykami/api/internal/loyalty"
	"github.com/bhaktiyudha/bykami/api/internal/store"
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
	db     *sql.DB
}

func newFixture(t *testing.T, authEnabled bool, staff ...string) fixture {
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

	c, err := admin.New(ident, ledger, frames.New(db), log, staff, authEnabled)
	if err != nil {
		t.Fatalf("new console: %v", err)
	}
	return fixture{h: c.Handler(), sender: sender, ident: ident, ledger: ledger, db: db}
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

	if w := f.post(t, "/login", url.Values{"phone": {phone}}, ""); w.Code != http.StatusOK {
		t.Fatalf("login = %d", w.Code)
	}
	w := f.post(t, "/verify", url.Values{"phone": {phone}, "code": {f.sender.last()}}, "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("verify = %d, want 303: %s", w.Code, w.Body.String())
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
	f := newFixture(t, true, operatorPhone)

	w := f.get(t, "/", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	body := w.Body.String()
	for _, want := range []string{"Masuk", "Nomor WhatsApp operator", `action="/login"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// An operator console on a public hostname must never be indexed.
	if !strings.Contains(body, `name="robots" content="noindex, nofollow"`) {
		t.Error("login page is missing the noindex directive")
	}
}

// The deployed state: the form says why it cannot be used rather than failing
// at the submit.
func TestLoginPageDeclaresTheClosedGate(t *testing.T) {
	f := newFixture(t, false, operatorPhone)

	body := f.get(t, "/", "").Body.String()

	if !strings.Contains(body, "OTP belum aktif") {
		t.Error("closed gate is not explained on the page")
	}
	if !strings.Contains(body, "disabled") {
		t.Error("submit is not disabled while delivery is off")
	}
}

func TestLoginRefusedWhileDeliveryIsOff(t *testing.T) {
	f := newFixture(t, false, operatorPhone)

	w := f.post(t, "/login", url.Values{"phone": {operatorPhone}}, "")

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	if f.sender.sent != 0 {
		t.Errorf("sender called %d times behind a closed gate", f.sender.sent)
	}
}

// A stranger must not be able to use this form to discover who is an operator,
// and must not be able to make it spend money on an SMS.
func TestNonOperatorGetsIdenticalResponseAndNoCode(t *testing.T) {
	f := newFixture(t, true, operatorPhone)

	operator := f.post(t, "/login", url.Values{"phone": {operatorPhone}}, "")
	sentAfterOperator := f.sender.sent

	stranger := f.post(t, "/login", url.Values{"phone": {customerPhone}}, "")

	if operator.Code != stranger.Code {
		t.Errorf("status differs: operator %d, stranger %d", operator.Code, stranger.Code)
	}
	// The pages echo back the number that was typed, so compare with that one
	// varying part removed. What must not differ is anything else — the notice,
	// the form state, or whether a code-entry field appeared.
	a := strings.ReplaceAll(operator.Body.String(), operatorPhone, "PHONE")
	b := strings.ReplaceAll(stranger.Body.String(), customerPhone, "PHONE")
	if a != b {
		t.Error("response differs between an operator and a stranger, which reveals who is staff")
	}
	if f.sender.sent != sentAfterOperator {
		t.Error("a code was sent to a non-operator")
	}
}

// The privilege boundary. A customer can hold a completely valid session — the
// login flow is the same one they use — and must still not reach the console.
func TestValidNonOperatorLoginIsRefusedAndItsSessionDestroyed(t *testing.T) {
	f := newFixture(t, true, operatorPhone)

	// Mint a genuine session for a customer, straight through identity so the
	// allow-list check in /login cannot be what stops it.
	if err := f.ident.RequestCode(context.Background(), customerPhone); err != nil {
		t.Fatalf("request code: %v", err)
	}
	w := f.post(t, "/verify", url.Values{"phone": {customerPhone}, "code": {f.sender.last()}}, "")

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	for _, ck := range w.Result().Cookies() {
		if ck.Name == cookieName && ck.Value != "" {
			t.Fatal("a session cookie was issued to a non-operator")
		}
	}
}

func TestOperatorSignsInAndReachesTheConsole(t *testing.T) {
	f := newFixture(t, true, operatorPhone)

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
	f := newFixture(t, true, operatorPhone)

	f.post(t, "/login", url.Values{"phone": {operatorPhone}}, "")
	w := f.post(t, "/verify", url.Values{"phone": {operatorPhone}, "code": {f.sender.last()}}, "")

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
	f := newFixture(t, true, operatorPhone)

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
	f := newFixture(t, true, operatorPhone)
	token := f.signIn(t, operatorPhone)

	if w := f.get(t, "/customers", token); w.Code != http.StatusOK {
		t.Fatalf("precondition: customers = %d", w.Code)
	}

	// Same identity service and the same live session, a console that no longer
	// lists that number.
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	revoked, err := admin.New(f.ident, f.ledger, frames.New(f.db), log, nil, true)
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
	f := newFixture(t, true, operatorPhone)
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
	f := newFixture(t, true, operatorPhone)
	token := f.signIn(t, operatorPhone)

	body := f.get(t, "/customers?phone="+customerPhone, token).Body.String()

	if !strings.Contains(body, "Tidak ada akun") {
		t.Error("an unknown number did not report itself as unknown")
	}
}

func TestAdjustWritesACompensatingEntry(t *testing.T) {
	f := newFixture(t, true, operatorPhone)
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
	f := newFixture(t, true, operatorPhone)
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
	f := newFixture(t, true, operatorPhone)
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
	f := newFixture(t, true, operatorPhone)
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
	f := newFixture(t, true, "+6281234567890")

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

	_, err = admin.New(identity.New(db, &capturingSender{}), loyalty.New(db), frames.New(db), log,
		[]string{"not-a-phone-number"}, true)
	if err == nil {
		t.Fatal("an unparseable operator number was accepted")
	}
}

func TestConsolePagesAreNotStorable(t *testing.T) {
	f := newFixture(t, true, operatorPhone)
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
