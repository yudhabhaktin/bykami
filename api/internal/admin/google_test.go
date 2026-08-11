package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/bhaktiyudha/bykami/api/internal/gcal"
)

// The service account the fake calendar in settings_test.go reports, and so the
// address every share in these tests has to be made out to.
const serviceAccount = "booking@bykami.iam.gserviceaccount.com"

// fakeGoogle stands in for the token endpoint and the two Calendar routes this
// flow touches, and records the ACL rules written through it — which is the
// thing worth asserting, because that rule is the entire point of the consent.
type fakeGoogle struct {
	srv *httptest.Server

	mu sync.Mutex
	// shares is calendar id → the grants written to it.
	shares map[string][]aclRule
	// owned is the calendars the signed-in account may grant on. A calendar
	// absent from here is one shared *to* the account, and Google answers 403.
	owned map[string]bool
	// codes that /token will accept.
	codes map[string]bool
}

type aclRule struct {
	Role  string `json:"role"`
	Scope struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"scope"`
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	g := &fakeGoogle{
		shares: map[string][]aclRule{},
		owned:  map[string]bool{"studio@gmail.com": true, "y2k@group.calendar.google.com": true},
		codes:  map[string]bool{"good-code": true},
	}

	mux := http.NewServeMux()

	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		g.mu.Lock()
		ok := g.codes[r.FormValue("code")]
		g.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "invalid_grant", "error_description": "Bad Request",
			})
			return
		}
		// No refresh_token, deliberately: the flow asks for online access only,
		// so Google would not issue one and the fake must not either.
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "live-token", "expires_in": 3600,
		})
	})

	mux.HandleFunc("GET /users/me/calendarList", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer live-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
			{"id": "studio@gmail.com", "summary": "Studio", "primary": true, "accessRole": "owner"},
			{"id": "y2k@group.calendar.google.com", "summary": "Booth Y2K", "accessRole": "owner"},
			// Shared to this account, not owned by it.
			{"id": "vendor@group.calendar.google.com", "summary": "Vendor", "accessRole": "reader"},
			{"id": "gone@group.calendar.google.com", "summary": "Lama", "accessRole": "owner", "deleted": true},
		}})
	})

	mux.HandleFunc("POST /calendars/{id}/acl", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer live-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, err := url.PathUnescape(r.PathValue("id"))
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}

		g.mu.Lock()
		owned := g.owned[id]
		g.mu.Unlock()
		if !owned {
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "forbiddenForNonOrganizer"},
			})
			return
		}

		var rule aclRule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		g.mu.Lock()
		g.shares[id] = append(g.shares[id], rule)
		g.mu.Unlock()
		json.NewEncoder(w).Encode(rule)
	})

	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

// connect points a real Connect at the fake, so the whole exchange runs.
func (g *fakeGoogle) connect(t *testing.T) *gcal.Connect {
	t.Helper()
	c := gcal.NewConnect("client-id", "client-secret")
	if c == nil {
		t.Fatal("NewConnect returned nil for a complete credential")
	}
	c.Endpoints(g.srv.URL+"/auth", g.srv.URL+"/token", g.srv.URL)
	return c
}

func (g *fakeGoogle) sharesFor(id string) []aclRule {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.shares[id]
}

// grantFor drives start → callback and returns the grant cookie, which is what
// the mapping screen authenticates on.
func grantFor(t *testing.T, f fixture, cookie, csrf string) string {
	t.Helper()

	w := f.post(t, "/settings/google/start", url.Values{"csrf": {csrf}}, cookie)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("start = %d, want 303: %s", w.Code, w.Body.String())
	}
	state := cookieNamed(w, "bykami_gstate")
	if state == "" {
		t.Fatal("no state cookie was set")
	}

	// Google's redirect back. Deliberately sent with no session cookie: it is a
	// cross-site navigation, and a SameSite=Strict session cookie would not be
	// sent on one — that is exactly the case this route has to survive.
	back := httptest.NewRequest(http.MethodGet,
		"/settings/google/callback?code=good-code&state="+url.QueryEscape(state), nil)
	back.AddCookie(&http.Cookie{Name: "bykami_gstate", Value: state})
	cb := httptest.NewRecorder()
	f.h.ServeHTTP(cb, back)
	if cb.Code != http.StatusOK {
		t.Fatalf("callback = %d, want 200: %s", cb.Code, cb.Body.String())
	}

	grant := cookieNamed(cb, "bykami_ggrant")
	if grant == "" {
		t.Fatalf("callback issued no grant cookie: %s", cb.Body.String())
	}
	return grant
}

func cookieNamed(w *httptest.ResponseRecorder, name string) string {
	for _, ck := range w.Result().Cookies() {
		if ck.Name == name && ck.Value != "" {
			return ck.Value
		}
	}
	return ""
}

// withGrant is get/post carrying both the session and the grant, which is what
// the mapping screen and its form require.
func (f fixture) getGrant(t *testing.T, path, cookie, grant string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	r.AddCookie(&http.Cookie{Name: "bykami_ggrant", Value: grant})
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f fixture) postGrant(t *testing.T, path string, form url.Values, cookie, grant string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	r.AddCookie(&http.Cookie{Name: "bykami_ggrant", Value: grant})
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

// The whole point of the flow: one consent, spent on writing the same ACL rule
// an owner would write by hand, and a resource pointed at the calendar.
func TestConnectingACalendarSharesItWithTheServiceAccount(t *testing.T) {
	g := newFakeGoogle(t)
	f := newFixtureConnect(t, &fakeCalendar{}, g.connect(t), operatorPhone)
	seedBookingCatalogue(t, f.db)

	cookie := f.signIn(t, operatorPhone)
	csrf := csrfFrom(t, f.get(t, "/settings", cookie).Body.String())
	grant := grantFor(t, f, cookie, csrf)

	// The mapping screen lists this account's calendars against the resources.
	page := f.getGrant(t, "/settings/google", cookie, grant).Body.String()
	for _, want := range []string{"Booth Y2K", "y2k@group.calendar.google.com", "self-photo", "studio@gmail.com"} {
		if !strings.Contains(page, want) {
			t.Errorf("mapping screen does not mention %q", want)
		}
	}
	// A calendar shared *to* the account cannot be granted on, so it is shown
	// but not offerable rather than silently dropped.
	if !strings.Contains(page, "bukan milik akun ini") {
		t.Error("the screen does not mark a calendar this account does not own")
	}
	// A deleted calendar is not a choice at all.
	if strings.Contains(page, "gone@group.calendar.google.com") {
		t.Error("a deleted calendar was offered")
	}

	w := f.postGrant(t, "/settings/google/connect", url.Values{
		"csrf": {csrfFrom(t, page)}, "resource": {"self-photo"},
		"calendar_id": {"y2k@group.calendar.google.com"},
	}, cookie, grant)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("connect = %d, want 303: %s", w.Code, w.Body.String())
	}

	// The share Google actually received. Writer, not reader: bookings are
	// mirrored into the calendar as events, not only read out of it.
	rules := g.sharesFor("y2k@group.calendar.google.com")
	if len(rules) != 1 {
		t.Fatalf("%d ACL rules written, want 1", len(rules))
	}
	if rules[0].Role != "writer" {
		t.Errorf("granted %q, want writer", rules[0].Role)
	}
	if rules[0].Scope.Type != "user" || rules[0].Scope.Value != serviceAccount {
		t.Errorf("granted to (%q, %q), want (user, %s)",
			rules[0].Scope.Type, rules[0].Scope.Value, serviceAccount)
	}

	// And the resource now points at it.
	var stored string
	if err := f.db.QueryRow(
		`SELECT google_calendar_id FROM booking_resources WHERE id = 'self-photo'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "y2k@group.calendar.google.com" {
		t.Errorf("resource points at %q, want the connected calendar", stored)
	}
}

// A calendar this account does not own cannot be shared, and the message has to
// say which account to use instead — Google's own 403 does not.
func TestConnectingACalendarTheAccountDoesNotOwnChangesNothing(t *testing.T) {
	g := newFakeGoogle(t)
	f := newFixtureConnect(t, &fakeCalendar{}, g.connect(t), operatorPhone)
	seedBookingCatalogue(t, f.db)

	cookie := f.signIn(t, operatorPhone)
	csrf := csrfFrom(t, f.get(t, "/settings", cookie).Body.String())
	grant := grantFor(t, f, cookie, csrf)
	page := f.getGrant(t, "/settings/google", cookie, grant).Body.String()

	w := f.postGrant(t, "/settings/google/connect", url.Values{
		"csrf": {csrfFrom(t, page)}, "resource": {"self-photo"},
		"calendar_id": {"vendor@group.calendar.google.com"},
	}, cookie, grant)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("connect = %d, want a redirect carrying the error", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "tidak+memiliki") {
		t.Errorf("the error does not say the account does not own it: %q", loc)
	}

	// The share failed, so nothing may have been written: a resource pointed at
	// a calendar the service account cannot read is a booth that looks connected
	// and syncs nothing, which is the failure this flow exists to remove.
	var stored string
	if err := f.db.QueryRow(
		`SELECT google_calendar_id FROM booking_resources WHERE id = 'self-photo'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "" {
		t.Errorf("resource was pointed at %q despite the share failing", stored)
	}
}

// The state cookie is the callback's only authentication, because the session
// cookie cannot travel on Google's cross-site redirect. So it has to be checked.
func TestTheCallbackRefusesAStateItDidNotIssue(t *testing.T) {
	g := newFakeGoogle(t)
	f := newFixtureConnect(t, &fakeCalendar{}, g.connect(t), operatorPhone)
	seedBookingCatalogue(t, f.db)

	for _, tc := range []struct {
		name   string
		query  string
		cookie string
	}{
		{"no state cookie at all", "?code=good-code&state=abc", ""},
		{"a state that does not match the cookie", "?code=good-code&state=abc", "xyz"},
		{"no state in the query", "?code=good-code", "xyz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/settings/google/callback"+tc.query, nil)
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: "bykami_gstate", Value: tc.cookie})
			}
			w := httptest.NewRecorder()
			f.h.ServeHTTP(w, r)

			if grant := cookieNamed(w, "bykami_ggrant"); grant != "" {
				t.Error("a forged callback was given a grant")
			}
			if !strings.Contains(w.Body.String(), "kedaluwarsa") {
				t.Errorf("the page does not report the attempt as expired: %s", w.Body.String())
			}
		})
	}
}

// Nothing about the consent is written down, so finishing has to be enough to
// end it — and an ended grant must not still open the mapping screen.
func TestFinishingReleasesTheConsent(t *testing.T) {
	g := newFakeGoogle(t)
	f := newFixtureConnect(t, &fakeCalendar{}, g.connect(t), operatorPhone)
	seedBookingCatalogue(t, f.db)

	cookie := f.signIn(t, operatorPhone)
	csrf := csrfFrom(t, f.get(t, "/settings", cookie).Body.String())
	grant := grantFor(t, f, cookie, csrf)

	page := f.getGrant(t, "/settings/google", cookie, grant)
	if page.Code != http.StatusOK {
		t.Fatalf("mapping screen = %d, want 200", page.Code)
	}

	w := f.postGrant(t, "/settings/google/finish",
		url.Values{"csrf": {csrfFrom(t, page.Body.String())}}, cookie, grant)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("finish = %d, want 303", w.Code)
	}

	// The same grant, presented again, is now worth nothing.
	after := f.getGrant(t, "/settings/google", cookie, grant)
	if after.Code != http.StatusSeeOther {
		t.Errorf("a released grant still opened the mapping screen: %d", after.Code)
	}
	if loc := after.Header().Get("Location"); !strings.Contains(loc, "berakhir") {
		t.Errorf("the operator is not told the session ended: %q", loc)
	}
}

// Every write in this flow is a state change behind a form, so each needs the
// token — the console's own rule, and these routes are not exempt from it.
func TestTheGoogleFormsNeedTheCSRFToken(t *testing.T) {
	g := newFakeGoogle(t)
	f := newFixtureConnect(t, &fakeCalendar{}, g.connect(t), operatorPhone)
	seedBookingCatalogue(t, f.db)

	cookie := f.signIn(t, operatorPhone)
	csrf := csrfFrom(t, f.get(t, "/settings", cookie).Body.String())
	grant := grantFor(t, f, cookie, csrf)

	for _, path := range []string{
		"/settings/google/start",
		"/settings/google/connect",
		"/settings/google/finish",
	} {
		w := f.postGrant(t, path, url.Values{
			"resource": {"self-photo"}, "calendar_id": {"y2k@group.calendar.google.com"},
		}, cookie, grant)
		if w.Code != http.StatusForbidden {
			t.Errorf("POST %s without a token = %d, want 403", path, w.Code)
		}
	}
	if rules := g.sharesFor("y2k@group.calendar.google.com"); len(rules) != 0 {
		t.Errorf("a tokenless request shared a calendar anyway: %+v", rules)
	}
}

// The connect button posts to this origin and is answered with a redirect to
// Google's. A form-action that names only 'self' can have that navigation
// blocked, and the symptom is a button that silently does nothing.
func TestTheConsoleCSPAllowsTheRedirectToGoogle(t *testing.T) {
	g := newFakeGoogle(t)
	f := newFixtureConnect(t, &fakeCalendar{}, g.connect(t), operatorPhone)
	seedBookingCatalogue(t, f.db)

	cookie := f.signIn(t, operatorPhone)
	csp := f.get(t, "/settings", cookie).Header().Get("Content-Security-Policy")

	if !strings.Contains(csp, "form-action 'self' https://accounts.google.com") {
		t.Errorf("form-action does not permit the sign-in redirect: %q", csp)
	}
	// The rest of the policy is what makes that widening safe to make.
	for _, want := range []string{"default-src 'none'", "base-uri 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("policy lost %q: %q", want, csp)
		}
	}
}

// With no OAuth client configured the console falls back to what it did before:
// print the address and let somebody share the calendar by hand.
func TestWithoutAnOAuthClientTheConsoleAsksForAManualShare(t *testing.T) {
	f := newFixtureCal(t, &fakeCalendar{}, operatorPhone)
	seedBookingCatalogue(t, f.db)

	cookie := f.signIn(t, operatorPhone)
	body := f.get(t, "/settings", cookie).Body.String()

	if strings.Contains(body, "/settings/google/start") {
		t.Error("the console offered a connect button with no OAuth client configured")
	}
	if !strings.Contains(body, serviceAccount) {
		t.Error("the page does not print the address to share calendars with by hand")
	}

	// And the route itself refuses rather than half-running.
	csrf := csrfFrom(t, body)
	w := f.post(t, "/settings/google/start", url.Values{"csrf": {csrf}}, cookie)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("start = %d, want a redirect carrying the error", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "belum+dikonfigurasi") {
		t.Errorf("the error does not say OAuth is unconfigured: %q", loc)
	}
}
