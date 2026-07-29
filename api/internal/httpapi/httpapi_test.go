package httpapi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bhaktiyudha/bykami/api/internal/frames"
	"github.com/bhaktiyudha/bykami/api/internal/httpapi"
	"github.com/bhaktiyudha/bykami/api/internal/identity"
	"github.com/bhaktiyudha/bykami/api/internal/loyalty"
	"github.com/bhaktiyudha/bykami/api/internal/store"
)

// capturingSender stands in for WhatsApp. It keeps the last code so a test can
// complete the flow — the real service never reveals one, which is the point of
// hashing it, so the only way to test verification is to intercept delivery.
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

func newTestAPI(t *testing.T, authEnabled bool) (http.Handler, *capturingSender, *sql.DB, *loyalty.Ledger) {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	sender := &capturingSender{}
	ledger := loyalty.New(db)
	health := func(ctx context.Context) error { return db.PingContext(ctx) }

	// Discard: these tests exercise 500 paths, and a logger writing to stderr
	// would bury the actual failures under expected noise.
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))

	return httpapi.New(identity.New(db, sender), ledger, frames.New(db), health, log, authEnabled, ""), sender, db, ledger
}

func do(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return out
}

func TestHealthzReportsOK(t *testing.T) {
	h, _, _, _ := newTestAPI(t, false)

	w := do(t, h, http.MethodGet, "/healthz", "", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := decodeBody[map[string]string](t, w)["status"]; got != "ok" {
		t.Errorf("status field = %q, want ok", got)
	}
}

// The deployed configuration. The trial box is held to synthetic sessions until
// residency is settled, and the only sender that exists writes codes to a log —
// so a deployment nobody explicitly enabled must not take real logins.
func TestAuthRoutesAreClosedWhenDeliveryIsNotConfigured(t *testing.T) {
	h, sender, _, _ := newTestAPI(t, false)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/auth/code", `{"phone":"081234567890"}`},
		{http.MethodPost, "/v1/auth/session", `{"phone":"081234567890","code":"123456"}`},
	} {
		w := do(t, h, tc.method, tc.path, "", tc.body)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", tc.method, tc.path, w.Code)
		}
	}

	// The gate must stop the request before the service runs, not merely hide
	// the result: a challenge row written here would still be redeemable.
	if sender.sent != 0 {
		t.Errorf("sender was called %d times behind a closed gate", sender.sent)
	}
}

// /healthz must keep working with auth closed, because it is what the deploy's
// health gate and the tunnel check both poll.
func TestHealthzIsNotGated(t *testing.T) {
	h, _, _, _ := newTestAPI(t, false)

	if w := do(t, h, http.MethodGet, "/healthz", "", ""); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestFullLoginFlow(t *testing.T) {
	h, sender, _, ledger := newTestAPI(t, true)

	w := do(t, h, http.MethodPost, "/v1/auth/code", "", `{"phone":"081234567890"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("request code = %d (%s), want 202", w.Code, w.Body.String())
	}

	// A wrong code must not open a session, and must not say why it failed.
	if w := do(t, h, http.MethodPost, "/v1/auth/session", "", `{"phone":"081234567890","code":"000000"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong code = %d, want 401", w.Code)
	}

	w = do(t, h, http.MethodPost, "/v1/auth/session", "",
		`{"phone":"081234567890","code":"`+sender.last()+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("verify = %d (%s), want 200", w.Code, w.Body.String())
	}

	type session struct {
		Token string `json:"token"`
		User  struct {
			ID    string `json:"id"`
			Phone string `json:"phone"`
		} `json:"user"`
	}
	s := decodeBody[session](t, w)
	if s.Token == "" {
		t.Fatal("no token returned")
	}
	// Normalised on the way in, so the client learns the canonical form.
	if s.User.Phone != "+6281234567890" {
		t.Errorf("phone = %q, want +6281234567890", s.User.Phone)
	}

	w = do(t, h, http.MethodGet, "/v1/me", s.Token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("me = %d (%s), want 200", w.Code, w.Body.String())
	}
	if got := decodeBody[map[string]any](t, w)["id"]; got != s.User.ID {
		t.Errorf("me id = %v, want %s", got, s.User.ID)
	}

	// The ledger reaches the statement, and the balance is the sum.
	if _, err := ledger.Earn(context.Background(), s.User.ID, "studio", 120, "visit-1", "key-1"); err != nil {
		t.Fatalf("earn: %v", err)
	}
	w = do(t, h, http.MethodGet, "/v1/me/loyalty", s.Token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("statement = %d (%s), want 200", w.Code, w.Body.String())
	}
	type statement struct {
		Balance int64 `json:"balance"`
		Entries []struct {
			Kind        string `json:"kind"`
			Points      int64  `json:"points"`
			ReferenceID string `json:"reference_id"`
		} `json:"entries"`
	}
	st := decodeBody[statement](t, w)
	if st.Balance != 120 {
		t.Errorf("balance = %d, want 120", st.Balance)
	}
	if len(st.Entries) != 1 || st.Entries[0].Kind != "earn" || st.Entries[0].Points != 120 {
		t.Errorf("entries = %+v, want one earn of 120", st.Entries)
	}

	// Logging out ends it, and the token stops working immediately.
	if w := do(t, h, http.MethodDelete, "/v1/auth/session", s.Token, ""); w.Code != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", w.Code)
	}
	if w := do(t, h, http.MethodGet, "/v1/me", s.Token, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("me after logout = %d, want 401", w.Code)
	}
}

// The statement must never carry the idempotency key. It is an internal
// correlation value — for a payment-driven earn it is the gateway's event id.
func TestStatementDoesNotLeakIdempotencyKey(t *testing.T) {
	h, sender, _, ledger := newTestAPI(t, true)

	do(t, h, http.MethodPost, "/v1/auth/code", "", `{"phone":"081234567890"}`)
	w := do(t, h, http.MethodPost, "/v1/auth/session", "",
		`{"phone":"081234567890","code":"`+sender.last()+`"}`)
	s := decodeBody[struct {
		Token string `json:"token"`
		User  struct {
			ID string `json:"id"`
		} `json:"user"`
	}](t, w)

	const secret = "xendit-event-abc123"
	if _, err := ledger.Earn(context.Background(), s.User.ID, "studio", 10, "ref", secret); err != nil {
		t.Fatalf("earn: %v", err)
	}

	w = do(t, h, http.MethodGet, "/v1/me/loyalty", s.Token, "")
	if strings.Contains(w.Body.String(), secret) {
		t.Errorf("statement leaked the idempotency key: %s", w.Body.String())
	}
}

// Entries must encode as [] and not null, so a client never has to handle both.
func TestEmptyStatementEncodesAsArray(t *testing.T) {
	h, sender, _, _ := newTestAPI(t, true)

	do(t, h, http.MethodPost, "/v1/auth/code", "", `{"phone":"081234567890"}`)
	w := do(t, h, http.MethodPost, "/v1/auth/session", "",
		`{"phone":"081234567890","code":"`+sender.last()+`"}`)
	token := decodeBody[struct {
		Token string `json:"token"`
	}](t, w).Token

	w = do(t, h, http.MethodGet, "/v1/me/loyalty", token, "")
	if !strings.Contains(w.Body.String(), `"entries":[]`) {
		t.Errorf("entries = %s, want []", w.Body.String())
	}
}

func TestAuthenticationRejections(t *testing.T) {
	h, _, _, _ := newTestAPI(t, true)

	for _, tc := range []struct{ name, token string }{
		{"no token", ""},
		{"unknown token", "not-a-real-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, http.MethodGet, "/v1/me", tc.token, "")
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
			if got := w.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want Bearer", got)
			}
		})
	}
}

// The scheme is case-insensitive per RFC 7235; a client sending "bearer" is not
// making a mistake and must not be told it is unauthenticated.
func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	h, sender, _, _ := newTestAPI(t, true)

	do(t, h, http.MethodPost, "/v1/auth/code", "", `{"phone":"081234567890"}`)
	w := do(t, h, http.MethodPost, "/v1/auth/session", "",
		`{"phone":"081234567890","code":"`+sender.last()+`"}`)
	token := decodeBody[struct {
		Token string `json:"token"`
	}](t, w).Token

	r := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	r.Header.Set("Authorization", "bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestRequestValidation(t *testing.T) {
	h, _, _, _ := newTestAPI(t, true)

	tests := []struct {
		name string
		body string
		want int
	}{
		{"invalid phone", `{"phone":"12345"}`, http.StatusBadRequest},
		{"malformed json", `{"phone":`, http.StatusBadRequest},
		// A misspelled field would otherwise become an empty phone, and the
		// error would name the wrong problem.
		{"unknown field", `{"phone_number":"081234567890"}`, http.StatusBadRequest},
		{"trailing document", `{"phone":"081234567890"}{"phone":"081234567891"}`, http.StatusBadRequest},
		{"oversized body", `{"phone":"` + strings.Repeat("0", 5000) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if w := do(t, h, http.MethodPost, "/v1/auth/code", "", tc.body); w.Code != tc.want {
				t.Errorf("status = %d, want %d (%s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// Without a content-type check a cross-origin HTML form can POST here, because
// form submissions are not subject to the preflight that would otherwise stop
// them.
func TestFormContentTypeIsRefused(t *testing.T) {
	h, _, _, _ := newTestAPI(t, true)

	r := httptest.NewRequest(http.MethodPost, "/v1/auth/code",
		strings.NewReader("phone=081234567890"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", w.Code)
	}
}

// Each send is billed, so an unthrottled endpoint is a way to spend someone
// else's money. The service enforces the limit; this proves the transport
// surfaces it as 429 rather than 500.
func TestRateLimitSurfacesAs429(t *testing.T) {
	h, _, _, _ := newTestAPI(t, true)

	const body = `{"phone":"081234567890"}`
	for i := range 3 {
		if w := do(t, h, http.MethodPost, "/v1/auth/code", "", body); w.Code != http.StatusAccepted {
			t.Fatalf("send %d = %d, want 202", i+1, w.Code)
		}
	}
	if w := do(t, h, http.MethodPost, "/v1/auth/code", "", body); w.Code != http.StatusTooManyRequests {
		t.Errorf("fourth send = %d, want 429", w.Code)
	}
}

// Personal data behind Cloudflare. no-store rather than no-cache: the weaker
// one still permits a stored copy revalidated later.
func TestResponsesAreNotStorable(t *testing.T) {
	h, _, _, _ := newTestAPI(t, true)

	for _, path := range []string{"/healthz", "/v1/me"} {
		w := do(t, h, http.MethodGet, path, "", "")
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s Cache-Control = %q, want no-store", path, got)
		}
	}
}

// A session issued before delivery was switched off is still a valid session.
// Revoking every login by changing a delivery setting would be a surprising way
// for that flag to behave.
func TestExistingSessionsSurviveTheGate(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	sender := &capturingSender{}
	ident := identity.New(db, sender)
	ledger := loyalty.New(db)
	health := func(ctx context.Context) error { return db.PingContext(ctx) }
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))

	open := httpapi.New(ident, ledger, frames.New(db), health, log, true, "")
	do(t, open, http.MethodPost, "/v1/auth/code", "", `{"phone":"081234567890"}`)
	w := do(t, open, http.MethodPost, "/v1/auth/session", "",
		`{"phone":"081234567890","code":"`+sender.last()+`"}`)
	token := decodeBody[struct {
		Token string `json:"token"`
	}](t, w).Token

	// Same database, same sessions, delivery switched off.
	closed := httpapi.New(ident, ledger, frames.New(db), health, log, false, "")
	if w := do(t, closed, http.MethodGet, "/v1/me", token, ""); w.Code != http.StatusOK {
		t.Errorf("me = %d, want 200", w.Code)
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	h, _, _, _ := newTestAPI(t, true)

	if w := do(t, h, http.MethodGet, "/v1/nope", "", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown route = %d, want 404", w.Code)
	}
	if w := do(t, h, http.MethodPost, "/healthz", "", `{}`); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("wrong method = %d, want 405", w.Code)
	}
}
