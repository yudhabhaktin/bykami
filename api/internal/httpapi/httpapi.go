// Package httpapi is the HTTP transport for the modular monolith.
//
// It is transport and nothing else: parse, authenticate, delegate, encode. No
// business rule lives here, because a rule enforced in a handler is a rule the
// kiosk can reach around the moment it talks to the database directly. The
// identity and loyalty packages own their invariants; this package owns status
// codes.
//
// Everything is bearer-token authenticated, deliberately, and not cookies. The
// first consumer is the kiosk, which runs at http://localhost on the booth PC —
// a different origin from bykami.id, so it could not send a platform cookie
// even if one were set. And app.bykami.id is the operator-admin surface, which
// platform-architecture.md keeps out of the .bykami.id cookie jar on purpose.
// Tokens sidestep both facts rather than working around either. Cookie SSO for
// the vertical sites is a separate decision on a separate surface.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/identity"
	"github.com/bhaktiyudha/bykami/api/internal/loyalty"
	"github.com/bhaktiyudha/bykami/api/internal/phone"
)

// maxBody is generous for the largest request here — a phone number and a
// six-digit code — and small enough that an unauthenticated caller cannot make
// the process allocate anything worth noticing.
const maxBody = 4 << 10

// historyLimit is how many ledger entries a statement returns. Fixed rather
// than a query parameter: nothing has asked for paging, and an unbounded limit
// on a public endpoint is a way to make the server do arbitrary work.
const historyLimit = 50

// API holds the wiring. Concrete types rather than interfaces: there is one
// implementation of each and inventing a seam here would be scaffolding for a
// test that does not need it — the store runs in memory.
type API struct {
	identity *identity.Service
	loyalty  *loyalty.Ledger
	health   HealthFunc
	log      *slog.Logger

	// authEnabled gates every auth route. False is the safe default and the
	// production setting today: infrastructure.md holds the trial box to
	// synthetic sessions until data residency is settled, and the only OTP
	// sender that exists writes codes to the log. Enforcing that in code beats
	// remembering it — a deployment cannot accidentally start taking real
	// customer logins because nobody was passed the flag that turns them on.
	authEnabled bool
}

// HealthFunc reports whether the app can reach its own storage. A func rather
// than the database itself keeps this package free of a SQL import.
type HealthFunc func(ctx context.Context) error

// New returns the router. authEnabled false leaves /healthz working and every
// auth route answering 503, which is the deployed configuration today.
func New(ident *identity.Service, ledger *loyalty.Ledger, health HealthFunc, log *slog.Logger, authEnabled bool) http.Handler {
	a := &API{identity: ident, loyalty: ledger, health: health, log: log, authEnabled: authEnabled}

	mux := http.NewServeMux()

	// Readiness, not liveness: it touches the database, because a process that
	// is running but cannot reach its own storage is not ready to take traffic.
	mux.HandleFunc("GET /healthz", a.healthz)

	// Requesting a code and redeeming it are separate resources on purpose. The
	// second creates a session, so it is a POST to the session collection
	// rather than a verb, and logging out is the DELETE that mirrors it.
	mux.HandleFunc("POST /v1/auth/code", a.gated(a.requestCode))
	mux.HandleFunc("POST /v1/auth/session", a.gated(a.createSession))
	mux.HandleFunc("DELETE /v1/auth/session", a.authenticated(a.endSession))

	mux.HandleFunc("GET /v1/me", a.authenticated(a.me))
	mux.HandleFunc("GET /v1/me/loyalty", a.authenticated(a.statement))

	return mux
}

func (a *API) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.health(ctx); err != nil {
		a.log.Error("health check failed", "err", err)
		a.fail(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	a.write(w, http.StatusOK, map[string]string{"status": "ok"})
}

type codeRequest struct {
	Phone string `json:"phone"`
}

// requestCode mints a one-time code and hands it to the sender. It answers 202
// rather than 200: delivery is someone else's network, and a success here means
// the code was accepted for sending, not that it arrived.
func (a *API) requestCode(w http.ResponseWriter, r *http.Request) {
	var req codeRequest
	if !a.decode(w, r, &req) {
		return
	}

	switch err := a.identity.RequestCode(r.Context(), req.Phone); {
	case err == nil:
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusAccepted)
	case errors.Is(err, phone.ErrInvalid):
		a.fail(w, http.StatusBadRequest, "not a valid Indonesian mobile number")
	case errors.Is(err, identity.ErrTooManyRequests):
		a.fail(w, http.StatusTooManyRequests, "too many code requests for this number; try again later")
	default:
		a.internal(w, "request code", err)
	}
}

type sessionRequest struct {
	Phone string `json:"phone"`
	Code  string `json:"code"`
}

type sessionResponse struct {
	Token string   `json:"token"`
	User  userBody `json:"user"`
}

// createSession redeems a code. Every failure is one status and one message —
// a wrong code, an expired one, one already spent, and one for a number that
// never asked all answer identically, because telling them apart tells an
// attacker which numbers have accounts.
func (a *API) createSession(w http.ResponseWriter, r *http.Request) {
	var req sessionRequest
	if !a.decode(w, r, &req) {
		return
	}

	user, token, err := a.identity.VerifyCode(r.Context(), req.Phone, req.Code)
	switch {
	case err == nil:
		a.write(w, http.StatusOK, sessionResponse{Token: token, User: newUserBody(user)})
	case errors.Is(err, identity.ErrInvalidCode):
		a.fail(w, http.StatusUnauthorized, "invalid or expired code")
	default:
		a.internal(w, "verify code", err)
	}
}

func (a *API) endSession(w http.ResponseWriter, r *http.Request, _ identity.User, token string) {
	if err := a.identity.EndSession(r.Context(), token); err != nil {
		a.internal(w, "end session", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

type userBody struct {
	ID        string `json:"id"`
	Phone     string `json:"phone"`
	Name      string `json:"name,omitempty"`
	Email     string `json:"email,omitempty"`
	CreatedAt string `json:"created_at"`
}

func newUserBody(u identity.User) userBody {
	return userBody{
		ID:        u.ID,
		Phone:     u.Phone,
		Name:      u.Name,
		Email:     u.Email,
		CreatedAt: u.CreatedAt.Format(time.RFC3339),
	}
}

func (a *API) me(w http.ResponseWriter, _ *http.Request, u identity.User, _ string) {
	a.write(w, http.StatusOK, newUserBody(u))
}

type entryBody struct {
	ID          string `json:"id"`
	Vertical    string `json:"vertical"`
	Kind        string `json:"kind"`
	Points      int64  `json:"points"`
	ReferenceID string `json:"reference_id,omitempty"`
	CreatedAt   string `json:"created_at"`
}

type statementResponse struct {
	Balance int64       `json:"balance"`
	Entries []entryBody `json:"entries"`
}

// statement is the customer's own view of the ledger: the balance and how it
// got there. Balance is read separately rather than summed from the entries
// below, because those are truncated to the most recent page and summing them
// would quietly report the wrong number as soon as a user has more than a page
// of history.
//
// idempotency_key is deliberately not exposed. It is an internal correlation
// value — for a payment-driven earn it is the gateway's event id — and it is of
// no use to the customer whose entry it is.
func (a *API) statement(w http.ResponseWriter, r *http.Request, u identity.User, _ string) {
	balance, err := a.loyalty.Balance(r.Context(), u.ID)
	if err != nil {
		a.internal(w, "loyalty balance", err)
		return
	}

	entries, err := a.loyalty.History(r.Context(), u.ID, historyLimit)
	if err != nil {
		a.internal(w, "loyalty history", err)
		return
	}

	// Non-nil so the field encodes as [] rather than null. A client that has to
	// handle both is a client with a bug waiting in it.
	out := statementResponse{Balance: balance, Entries: make([]entryBody, 0, len(entries))}
	for _, e := range entries {
		out.Entries = append(out.Entries, entryBody{
			ID:          e.ID,
			Vertical:    e.Vertical,
			Kind:        string(e.Kind),
			Points:      e.Points,
			ReferenceID: e.ReferenceID,
			CreatedAt:   e.CreatedAt.Format(time.RFC3339),
		})
	}
	a.write(w, http.StatusOK, out)
}

// gated refuses the route when auth is switched off, which is the deployed
// state. 503 rather than 404: the endpoint exists and will work once delivery
// is configured, and a 404 would send an integrator looking for a typo.
func (a *API) gated(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authEnabled {
			a.fail(w, http.StatusServiceUnavailable,
				"authentication is not enabled on this deployment: no OTP delivery is configured")
			return
		}
		h(w, r)
	}
}

// authedHandler receives the resolved user and the raw token. Both, because
// logging out needs the token itself and passing it through the request context
// would be plumbing for one caller.
type authedHandler func(w http.ResponseWriter, r *http.Request, u identity.User, token string)

// authenticated is not gated by authEnabled. A session issued before the switch
// was turned off is still a valid session, and revoking every login by changing
// a delivery setting would be a surprising way for that flag to behave.
func (a *API) authenticated(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			// The scheme, so a caller sending the wrong one is told which is
			// expected rather than guessing.
			w.Header().Set("WWW-Authenticate", "Bearer")
			a.fail(w, http.StatusUnauthorized, "missing bearer token")
			return
		}

		u, err := a.identity.UserForSession(r.Context(), token)
		switch {
		case err == nil:
			h(w, r, u, token)
		case errors.Is(err, identity.ErrNoSession):
			w.Header().Set("WWW-Authenticate", "Bearer")
			a.fail(w, http.StatusUnauthorized, "invalid or expired session")
		default:
			a.internal(w, "resolve session", err)
		}
	}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	// EqualFold because the scheme is case-insensitive per RFC 7235, and a
	// client sending "bearer" is not making a mistake.
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// decode reads a JSON body, reporting false when it has already answered.
//
// The content-type check is not ceremony. Without it a cross-origin HTML form
// can POST to these routes, because form submissions are not subject to the
// preflight that would otherwise stop them.
func (a *API) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		a.fail(w, http.StatusUnsupportedMediaType, "expected Content-Type: application/json")
		return false
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)

	// An unknown field is almost always a misspelled known one, and without
	// this the request succeeds with that field's zero value — a "phone_number"
	// typo becomes an empty phone and the error names the wrong problem.
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			a.fail(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		a.fail(w, http.StatusBadRequest, "malformed JSON body")
		return false
	}

	// A second JSON value after the first. Rejected so that two documents in
	// one body cannot mean different things to this parser and the next one.
	if dec.More() {
		a.fail(w, http.StatusBadRequest, "unexpected data after JSON body")
		return false
	}
	return true
}

type errorBody struct {
	Error string `json:"error"`
}

func (a *API) fail(w http.ResponseWriter, status int, msg string) {
	a.write(w, status, errorBody{Error: msg})
}

// internal logs the cause and tells the caller nothing about it. The message
// would otherwise carry SQL, file paths, or a phone number into a response.
func (a *API) internal(w http.ResponseWriter, op string, err error) {
	a.log.Error("request failed", "op", op, "err", err)
	a.fail(w, http.StatusInternalServerError, "internal error")
}

func (a *API) write(w http.ResponseWriter, status int, body any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	// Every response here is either personal data or an auth result, and
	// Cloudflare sits in front of this. no-store rather than no-cache: the
	// weaker one still permits a stored copy revalidated later.
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")

	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already sent, so this cannot become a 500. Log it:
		// a client that saw a truncated body deserves a trace on this side.
		a.log.Error("write response", "err", err)
	}
}
