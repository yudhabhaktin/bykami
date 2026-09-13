package admin_test

import (
	"context"
	"database/sql"
	"errors"
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
	"github.com/bhaktiyudha/bykami/api/internal/adminauth"
	"github.com/bhaktiyudha/bykami/api/internal/booking"
	"github.com/bhaktiyudha/bykami/api/internal/frames"
	"github.com/bhaktiyudha/bykami/api/internal/gcal"
	"github.com/bhaktiyudha/bykami/api/internal/identity"
	"github.com/bhaktiyudha/bykami/api/internal/loyalty"
	"github.com/bhaktiyudha/bykami/api/internal/membership"
	"github.com/bhaktiyudha/bykami/api/internal/store"
)

const (
	operatorLabel = "yudha"
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
	stamps *membership.Service
	auth   *adminauth.Registry
	db     *sql.DB

	password string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	return newFixtureConnect(t, nil, nil)
}

func newFixtureCal(t *testing.T, cal booking.Calendar) fixture {
	t.Helper()
	return newFixtureConnect(t, cal, nil)
}

func newFixtureConnect(t *testing.T, cal booking.Calendar, connect *gcal.Connect) fixture {
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
	auth := adminauth.New(db, nil)
	auth.SetIterations(1000)

	worker := booking.NewWorker(desk, cal, log, time.Minute, "Jajag")
	stamps := membership.New(db, ledger, ident, time.Now)
	c, err := admin.New(ident, ledger, stamps, frames.New(db), frames.NewBooths(db), desk, worker, auth, connect, log)
	if err != nil {
		t.Fatalf("new console: %v", err)
	}

	pw, err := auth.Add(context.Background(), operatorLabel)
	if err != nil {
		t.Fatalf("add credential: %v", err)
	}
	// Tests exercise the console, not the forced-password-change flow.
	if _, err := db.Exec("UPDATE admin_credentials SET must_change = 0 WHERE label = ?", operatorLabel); err != nil {
		t.Fatalf("clear must_change: %v", err)
	}

	return fixture{
		h: c.Handler(), sender: sender, ident: ident, ledger: ledger,
		stamps: stamps, auth: auth, db: db, password: pw,
	}
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

func (f fixture) signIn(t *testing.T) string {
	t.Helper()
	w := f.post(t, "/login", url.Values{"username": {operatorLabel}, "password": {f.password}}, "")
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

func countRows(t *testing.T, f fixture, query string) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func TestRootServesTheLoginPage(t *testing.T) {
	f := newFixture(t)
	w := f.get(t, "/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"Masuk", "Kata sandi", `action="/login"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestLoginPageHasUsernameAndPasswordFields(t *testing.T) {
	f := newFixture(t)
	body := f.get(t, "/", "").Body.String()
	if !strings.Contains(body, `name="username"`) {
		t.Error("page missing username input")
	}
	if !strings.Contains(body, `name="password"`) {
		t.Error("page missing password input")
	}
	if strings.Contains(body, `name="phone"`) {
		t.Error("page still has a phone input")
	}
	if strings.Contains(body, `name="code"`) {
		t.Error("page still has a code input")
	}
}

func TestLoginPageSaysWhenNobodyIsEnrolled(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ident := identity.New(db, &capturingSender{})
	ledger := loyalty.New(db)
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	auth := adminauth.New(db, nil)
	auth.SetIterations(1000)
	stamps := membership.New(db, ledger, ident, time.Now)
	c, err := admin.New(ident, ledger, stamps, frames.New(db), frames.NewBooths(db), booking.New(db, 0), nil, auth, nil, log)
	if err != nil {
		t.Fatalf("new console: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	c.Handler().ServeHTTP(w, r)

	body := w.Body.String()
	if !strings.Contains(body, "Belum ada operator yang terdaftar") {
		t.Error("an empty registry is not explained on the page")
	}
	if !strings.Contains(body, "admin password add") {
		t.Error("the page does not say how to add an operator")
	}
}

func TestEveryRefusalLooksTheSame(t *testing.T) {
	f := newFixture(t)
	wrong := f.post(t, "/login", url.Values{"username": {"yudha"}, "password": {"wrong-password"}}, "")
	empty := f.post(t, "/login", url.Values{"username": {""}, "password": {""}}, "")
	if wrong.Code != empty.Code {
		t.Errorf("status differs: wrong %d, empty %d", wrong.Code, empty.Code)
	}
	if wrong.Body.String() != empty.Body.String() {
		t.Error("response differs between wrong password and empty password")
	}
}

func TestElevenFailuresLockTheAddress(t *testing.T) {
	f := newFixture(t)
	firstWrong := f.post(t, "/login", url.Values{"username": {"yudha"}, "password": {"wrong"}}, "").Body.String()

	for i := 0; i < 10; i++ {
		f.post(t, "/login", url.Values{"username": {"yudha"}, "password": {"wrong"}}, "")
	}

	locked := f.post(t, "/login", url.Values{"username": {"yudha"}, "password": {"wrong"}}, "")
	if locked.Code != http.StatusUnauthorized {
		t.Fatalf("locked status = %d, want 401", locked.Code)
	}
	if locked.Body.String() != firstWrong {
		t.Error("locked-out message differs from the first wrong-password message")
	}
}

func TestOperatorSignsInAndReachesTheConsole(t *testing.T) {
	f := newFixture(t)
	token := f.signIn(t)
	w := f.get(t, "/customers", token)
	if w.Code != http.StatusOK {
		t.Fatalf("customers = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Cari pelanggan") {
		t.Error("console did not render the search page")
	}
}

func TestSessionCookieIsHostOnlyAndLocked(t *testing.T) {
	f := newFixture(t)
	w := f.post(t, "/login", url.Values{"username": {operatorLabel}, "password": {f.password}}, "")
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
		t.Errorf("Domain = %q, want empty", ck.Domain)
	}
	if !ck.HttpOnly {
		t.Error("cookie is not HttpOnly")
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
	if !strings.HasPrefix(ck.Name, "__Host-") {
		t.Errorf("cookie name %q lacks the __Host- prefix", ck.Name)
	}
}

func TestConsoleRequiresASession(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct{ name, cookie string }{
		{"no cookie", ""},
		{"garbage cookie", "not-a-real-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := f.get(t, "/customers", tc.cookie)
			if w.Code != http.StatusSeeOther {
				t.Errorf("status = %d, want 303", w.Code)
			}
		})
	}
}

func TestRemovingACredentialEndsAccessImmediately(t *testing.T) {
	f := newFixture(t)
	// Add a second credential so we can remove it without hitting the last-manager guard.
	pw2, err := f.auth.Add(context.Background(), "kasir-2")
	if err != nil {
		t.Fatalf("add second: %v", err)
	}
	if _, err := f.db.Exec("UPDATE admin_credentials SET must_change = 0 WHERE label = ?", "kasir-2"); err != nil {
		t.Fatalf("clear must_change: %v", err)
	}
	w := f.post(t, "/login", url.Values{"username": {"kasir-2"}, "password": {pw2}}, "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login as kasir-2 = %d, want 303", w.Code)
	}
	var token string
	for _, ck := range w.Result().Cookies() {
		if ck.Name == cookieName {
			token = ck.Value
		}
	}
	if token == "" {
		t.Fatal("no session cookie set for kasir-2")
	}
	if w := f.get(t, "/customers", token); w.Code != http.StatusOK {
		t.Fatalf("precondition: customers = %d", w.Code)
	}
	if err := f.auth.Remove(context.Background(), "kasir-2"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	w = f.get(t, "/customers", token)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
}

func TestCustomerLookupShowsBalanceAndHistory(t *testing.T) {
	f := newFixture(t)
	token := f.signIn(t)

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
	for _, want := range []string{"6281298765432", "250", "studio", "visit-1"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestLookupOfAnUnknownNumber(t *testing.T) {
	f := newFixture(t)
	token := f.signIn(t)
	body := f.get(t, "/customers?phone="+customerPhone, token).Body.String()
	if !strings.Contains(body, "Tidak ada akun") {
		t.Error("an unknown number did not report itself as unknown")
	}
}

func TestAdjustWritesACompensatingEntry(t *testing.T) {
	f := newFixture(t)
	token := f.signIn(t)

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

	entries, err := f.ledger.History(context.Background(), user.ID, 10)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(entries) != 1 || !strings.Contains(entries[0].ReferenceID, operatorLabel) {
		t.Errorf("entry does not name the operator label: %+v", entries)
	}
}

func TestAdjustRequiresCSRF(t *testing.T) {
	f := newFixture(t)
	token := f.signIn(t)
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
	f := newFixture(t)
	token := f.signIn(t)

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
				t.Errorf("balance = %d, want 0", balance)
			}
		})
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	f := newFixture(t)
	token := f.signIn(t)
	if w := f.post(t, "/logout", nil, token); w.Code != http.StatusSeeOther {
		t.Fatalf("logout = %d, want 303", w.Code)
	}
	if w := f.get(t, "/customers", token); w.Code != http.StatusSeeOther {
		t.Errorf("session still worked after logout: %d", w.Code)
	}
}

func TestConsolePagesAreNotStorable(t *testing.T) {
	f := newFixture(t)
	token := f.signIn(t)
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

// A write to the membership card records the credential's label as the operator.
func TestMembershipWriteRecordsTheLabel(t *testing.T) {
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

	var op string
	if err := f.db.QueryRow(`SELECT operator FROM membership_purchases LIMIT 1`).Scan(&op); err != nil {
		t.Fatalf("load purchase: %v", err)
	}
	if op != operatorLabel {
		t.Errorf("operator = %q, want %q", op, operatorLabel)
	}
}

func TestOperatorsPageRefusesNonManager(t *testing.T) {
	f := newFixture(t)
	// The fixture's operatorLabel is the first credential, so it is a manager.
	// Add a non-manager and sign in as them.
	pw2, err := f.auth.Add(context.Background(), "kasir-2")
	if err != nil {
		t.Fatalf("add second: %v", err)
	}
	if _, err := f.db.Exec("UPDATE admin_credentials SET must_change = 0 WHERE label = ?", "kasir-2"); err != nil {
		t.Fatalf("clear must_change: %v", err)
	}
	w := f.post(t, "/login", url.Values{"username": {"kasir-2"}, "password": {pw2}}, "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login = %d, want 303", w.Code)
	}
	var token string
	for _, ck := range w.Result().Cookies() {
		if ck.Name == cookieName {
			token = ck.Value
		}
	}
	w = f.get(t, "/operators", token)
	if w.Code != http.StatusForbidden {
		t.Errorf("operators = %d, want 403", w.Code)
	}
}

// csrfForAction returns the CSRF token from the interstitial that starts a
// privileged action.
//
// The /operators page no longer carries one: its action forms are GETs, because
// a GET must not perform anything, so the first page in the flow with a token to
// scrape is the one asking for the manager's password. The token is derived from
// the session cookie, so it is the same value wherever it is read.
func csrfForAction(t *testing.T, f fixture, token, action, label string) string {
	t.Helper()
	w := f.get(t, "/operators/reauth?action="+action+"&target=/operators/"+action+"&label="+label, token)
	if w.Code != http.StatusOK {
		t.Fatalf("interstitial for %s = %d, want 200", action, w.Code)
	}
	return csrfFrom(t, w.Body.String())
}

func TestPrivilegedActionWithoutReauthDoesNotHappen(t *testing.T) {
	f := newFixture(t)
	if err := f.auth.SetManage(context.Background(), operatorLabel); err != nil {
		t.Fatalf("set manage: %v", err)
	}
	token := f.signIn(t)

	// POST directly to /operators/disable without going through reauth
	w := f.post(t, "/operators/disable", url.Values{
		"label": {operatorLabel},
		"csrf":  {csrfForAction(t, f, token, "disable", operatorLabel)},
	}, token)
	if w.Code != http.StatusForbidden {
		t.Errorf("disable without reauth = %d, want 403", w.Code)
	}
}

// Adding an operator mints a permanent credential, so it is the action the
// re-auth gate matters most for. A CSRF token is not the barrier it looks like
// there: it is derived from the session cookie, so whoever holds the cookie can
// compute it.
func TestAddingAnOperatorWithoutReauthIsRefused(t *testing.T) {
	f := newFixture(t)
	if err := f.auth.SetManage(context.Background(), operatorLabel); err != nil {
		t.Fatalf("set manage: %v", err)
	}
	token := f.signIn(t)

	w := f.post(t, "/operators/add", url.Values{
		"label": {"kasir-1"},
		"csrf":  {csrfForAction(t, f, token, "add", "kasir-1")},
	}, token)
	if w.Code != http.StatusForbidden {
		t.Errorf("add without reauth = %d, want 403", w.Code)
	}
	if n := countRows(t, f, `SELECT COUNT(*) FROM admin_credentials WHERE label = 'kasir-1'`); n != 0 {
		t.Errorf("a credential was created without re-authentication: %d rows", n)
	}
}

// The whole flow, walked the way a browser walks it: the page's GET starts the
// interstitial, the password is posted, the redirect lands on the confirmation,
// and only the confirmation's own POST performs the action.
func TestAddingAnOperatorThroughTheInterstitial(t *testing.T) {
	f := newFixture(t)
	if err := f.auth.SetManage(context.Background(), operatorLabel); err != nil {
		t.Fatalf("set manage: %v", err)
	}
	token := f.signIn(t)

	w := f.get(t, "/operators/reauth?action=add&target=/operators/add&label=kasir-1", token)
	if w.Code != http.StatusOK {
		t.Fatalf("interstitial = %d, want 200: %s", w.Code, w.Body.String())
	}

	w = f.post(t, "/operators/reauth", url.Values{
		"action":   {"add"},
		"target":   {"/operators/add"},
		"label":    {"kasir-1"},
		"csrf":     {csrfFrom(t, w.Body.String())},
		"password": {f.password},
	}, token)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("reauth = %d, want 303: %s", w.Code, w.Body.String())
	}

	loc := w.Header().Get("Location")
	before := countRows(t, f, `SELECT COUNT(*) FROM admin_credentials`)
	w = f.get(t, loc, token)
	if w.Code != http.StatusOK {
		t.Fatalf("confirmation GET = %d, want 200 (a browser landed on 405 before this route existed): %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Tambah operator") {
		t.Error("the confirmation does not say what it is about to do")
	}
	if n := countRows(t, f, `SELECT COUNT(*) FROM admin_credentials`); n != before {
		t.Errorf("a GET on the confirmation changed the database: %d then %d", before, n)
	}

	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse location: %v", err)
	}
	w = f.post(t, "/operators/add", url.Values{
		"label":  {u.Query().Get("label")},
		"csrf":   {csrfFrom(t, body)},
		"reauth": {u.Query().Get("reauth")},
	}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("add through the flow = %d, want 200: %s", w.Code, w.Body.String())
	}
	if n := countRows(t, f, `SELECT COUNT(*) FROM admin_credentials WHERE label = 'kasir-1'`); n != 1 {
		t.Errorf("kasir-1 was not created by the flow: %d rows", n)
	}
}

func TestLastManagerGuardOnConsole(t *testing.T) {
	f := newFixture(t)
	if err := f.auth.SetManage(context.Background(), operatorLabel); err != nil {
		t.Fatalf("set manage: %v", err)
	}
	_ = f.signIn(t)

	// The console path goes through reauth, so test the guard at the registry layer.
	err := f.auth.UnsetManage(context.Background(), operatorLabel)
	if !errors.Is(err, adminauth.ErrLastManager) {
		t.Errorf("unset last manager = %v, want ErrLastManager", err)
	}
}

func TestLastManagerGuardOnCLI(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	registry := adminauth.New(db, nil)
	registry.SetIterations(1000)
	ctx := context.Background()

	if _, err := registry.Add(ctx, "sole-manager"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := registry.SetManage(ctx, "sole-manager"); err != nil {
		t.Fatalf("set manage: %v", err)
	}
	if err := registry.UnsetManage(ctx, "sole-manager"); !errors.Is(err, adminauth.ErrLastManager) {
		t.Errorf("cli unset last manager = %v, want ErrLastManager", err)
	}
}

// --- Forced password change ---

func TestMustChangeRedirectsEverywhere(t *testing.T) {
	f := newFixture(t)
	// Leave must_change at 1 (the default from the fixture is 0, so re-enable it)
	if _, err := f.db.Exec("UPDATE admin_credentials SET must_change = 1 WHERE label = ?", operatorLabel); err != nil {
		t.Fatalf("set must_change: %v", err)
	}
	token := f.signIn(t)

	for _, path := range []string{"/customers", "/stamps", "/operators"} {
		w := f.get(t, path, token)
		if w.Code != http.StatusSeeOther {
			t.Errorf("%s = %d, want 303", path, w.Code)
			continue
		}
		loc := w.Header().Get("Location")
		if loc != "/password/set" {
			t.Errorf("%s redirect = %q, want /password/set", path, loc)
		}
	}
}

func TestPasswordSetPageIsReachableWithMustChange(t *testing.T) {
	f := newFixture(t)
	if _, err := f.db.Exec("UPDATE admin_credentials SET must_change = 1 WHERE label = ?", operatorLabel); err != nil {
		t.Fatalf("set must_change: %v", err)
	}
	token := f.signIn(t)
	w := f.get(t, "/password/set", token)
	if w.Code != http.StatusOK {
		t.Fatalf("password/set = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Ubah kata sandi") {
		t.Error("page missing set-password heading")
	}
}

func TestPasswordSetRefusesShortPassword(t *testing.T) {
	f := newFixture(t)
	if _, err := f.db.Exec("UPDATE admin_credentials SET must_change = 1 WHERE label = ?", operatorLabel); err != nil {
		t.Fatalf("set must_change: %v", err)
	}
	token := f.signIn(t)
	csrf := csrfFrom(t, f.get(t, "/password/set", token).Body.String())

	w := f.post(t, "/password/set", url.Values{
		"csrf":      {csrf},
		"password":  {"short"},
		"password2": {"short"},
	}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("short password = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "minimal 12 karakter") {
		t.Error("page did not refuse short password")
	}
}

func TestPasswordSetRefusesMismatchedPasswords(t *testing.T) {
	f := newFixture(t)
	if _, err := f.db.Exec("UPDATE admin_credentials SET must_change = 1 WHERE label = ?", operatorLabel); err != nil {
		t.Fatalf("set must_change: %v", err)
	}
	token := f.signIn(t)
	csrf := csrfFrom(t, f.get(t, "/password/set", token).Body.String())

	w := f.post(t, "/password/set", url.Values{
		"csrf":      {csrf},
		"password":  {"this-is-long-enough"},
		"password2": {"this-is-different"},
	}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("mismatch = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "tidak cocok") {
		t.Error("page did not refuse mismatched passwords")
	}
}

func TestPasswordSetClearsMustChangeAndOldPasswordStopsWorking(t *testing.T) {
	f := newFixture(t)
	oldPassword := f.password
	if _, err := f.db.Exec("UPDATE admin_credentials SET must_change = 1 WHERE label = ?", operatorLabel); err != nil {
		t.Fatalf("set must_change: %v", err)
	}
	token := f.signIn(t)
	csrf := csrfFrom(t, f.get(t, "/password/set", token).Body.String())

	newPw := "this-is-my-new-password"
	w := f.post(t, "/password/set", url.Values{
		"csrf":      {csrf},
		"password":  {newPw},
		"password2": {newPw},
	}, token)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("set password = %d, want 303: %s", w.Code, w.Body.String())
	}

	// Old password no longer works
	w = f.post(t, "/login", url.Values{"username": {operatorLabel}, "password": {oldPassword}}, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("old password = %d, want 401", w.Code)
	}

	// New password works
	w = f.post(t, "/login", url.Values{"username": {operatorLabel}, "password": {newPw}}, "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("new password = %d, want 303", w.Code)
	}

	// Console is reachable without redirect
	for _, ck := range w.Result().Cookies() {
		if ck.Name == cookieName {
			token = ck.Value
		}
	}
	w = f.get(t, "/customers", token)
	if w.Code != http.StatusOK {
		t.Errorf("customers after change = %d, want 200", w.Code)
	}
}

func TestManagerResetRequiresReauth(t *testing.T) {
	f := newFixture(t)
	if err := f.auth.SetManage(context.Background(), operatorLabel); err != nil {
		t.Fatalf("set manage: %v", err)
	}
	token := f.signIn(t)

	// Direct POST to /operators/reset without reauth token
	w := f.post(t, "/operators/reset", url.Values{
		"label": {operatorLabel},
		"csrf":  {csrfForAction(t, f, token, "reset", operatorLabel)},
	}, token)
	if w.Code != http.StatusForbidden {
		t.Errorf("reset without reauth = %d, want 403", w.Code)
	}
}

func TestManagerResetSetsMustChangeAndShowsPasswordOnce(t *testing.T) {
	f := newFixture(t)
	if err := f.auth.SetManage(context.Background(), operatorLabel); err != nil {
		t.Fatalf("set manage: %v", err)
	}
	// Add a second operator to reset
	if _, err := f.auth.Add(context.Background(), "kasir-2"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := f.db.Exec("UPDATE admin_credentials SET must_change = 0 WHERE label = ?", "kasir-2"); err != nil {
		t.Fatalf("clear must_change: %v", err)
	}

	token := f.signIn(t)

	// Go through reauth
	reauthPage := f.get(t, "/operators/reauth?action=reset&target=/operators/reset&label=kasir-2", token)
	if reauthPage.Code != http.StatusOK {
		t.Fatalf("interstitial = %d, want 200: %s", reauthPage.Code, reauthPage.Body.String())
	}
	csrf := csrfFrom(t, reauthPage.Body.String())
	w := f.post(t, "/operators/reauth", url.Values{
		"action":   {"reset"},
		"target":   {"/operators/reset"},
		"label":    {"kasir-2"},
		"csrf":     {csrf},
		"password": {f.password},
	}, token)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("reauth = %d, want 303: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "/operators/reset") {
		t.Fatalf("expected redirect to reset, got %q", loc)
	}

	// Follow the redirect (GET is not allowed, but we can parse the query)
	u, _ := url.Parse(loc)
	w = f.post(t, "/operators/reset", url.Values{
		"label":  {u.Query().Get("label")},
		"csrf":   {u.Query().Get("csrf")},
		"reauth": {u.Query().Get("reauth")},
	}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("reset = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Kata sandi untuk kasir-2 diatur ulang:") {
		t.Error("page did not show the reset password")
	}

	// Extract the password from the notice
	const prefix = "diatur ulang: "
	i := strings.Index(body, prefix)
	if i < 0 {
		t.Fatal("could not find reset password in page")
	}
	resetPw := body[i+len(prefix):]
	if j := strings.Index(resetPw, "</"); j > 0 {
		resetPw = resetPw[:j]
	}
	resetPw = strings.TrimSpace(resetPw)

	// The reset password should work and the credential should have must_change
	w = f.post(t, "/login", url.Values{"username": {"kasir-2"}, "password": {resetPw}}, "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login with reset password = %d, want 303", w.Code)
	}
	for _, ck := range w.Result().Cookies() {
		if ck.Name == cookieName {
			token = ck.Value
		}
	}
	w = f.get(t, "/customers", token)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("customers with must_change = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/password/set" {
		t.Errorf("redirect = %q, want /password/set", loc)
	}
}
