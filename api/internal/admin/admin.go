// Package admin is the operator console at app.bykami.id.
//
// Server-rendered HTML with no JavaScript and no build step. That is not
// minimalism for its own sake: the alternative is a second toolchain, a bundle
// to ship, and a client-side session — three things to keep correct so that a
// staff member can look up a phone number.
//
// # Who is an operator
//
// Staff are a configured list of phone numbers, checked on every request
// against the *currently verified* session. There is no role column, and that
// is deliberate. A role in the database has a bootstrap problem — the first
// operator has to be promoted by an operator — which is normally solved by a
// seed script that quietly becomes a way to grant admin. A list supplied at
// startup has no such path: changing who is staff means changing the service
// configuration, which is an act with an audit trail.
//
// It also means a stolen customer session cannot become an operator session.
// Privilege is derived from the phone on every request, never stored in the
// session, so there is nothing in the cookie to tamper with.
//
// # Why this one gets a cookie when the API does not
//
// internal/httpapi is bearer-only, because the kiosk at http://localhost cannot
// send a bykami.id cookie. A browser can, and an HTML form has nowhere to keep
// a bearer token without the JavaScript this package exists to avoid.
//
// The cookie is host-only — no Domain attribute — which is the rule
// design/platform-architecture.md sets for this hostname. A Domain=.bykami.id
// cookie reaches every subdomain including gallery.bykami.id, the surface most
// exposed to shared links, and the jar has no opt-out.
package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/booking"
	"github.com/bhaktiyudha/bykami/api/internal/frames"
	"github.com/bhaktiyudha/bykami/api/internal/identity"
	"github.com/bhaktiyudha/bykami/api/internal/loyalty"
	"github.com/bhaktiyudha/bykami/api/internal/mfa"
	"github.com/bhaktiyudha/bykami/api/internal/phone"
)

//go:embed templates/*.html
var templateFS embed.FS

// The console's own copies of the brand assets. Served as routes rather than
// inlined as `data:` URIs, because the CSP below names `img-src 'self'` and
// deliberately excludes `data:` — see secHeaders. Copies rather than an import
// from packages/ui for the same reason the tokens in layout.html are copied:
// this is served by Go and never passes through the Astro build.
//
//go:embed logo.png icon.svg
var brandFS embed.FS

// sessionCookie is host-only and therefore prefixed __Host-, which browsers
// enforce: the prefix is rejected unless the cookie is Secure, has no Domain,
// and has Path=/. That turns the scoping rule above from a convention this code
// must remember into one the browser refuses to let it break.
const sessionCookie = "__Host-bykami-admin"

// verticals a compensating entry can be attributed to. A fixed list rather than
// a free-text field: the ledger is queried by vertical for settlement, and a
// typo there is a row that silently never appears in a report.
var verticals = []string{"studio", "booth", "dimsamcong"}

type Console struct {
	identity *identity.Service
	loyalty  *loyalty.Ledger
	frameCat *frames.Catalogue
	booking  *booking.Desk
	// The calendar sync, or nil when no Google credential is configured. Only the
	// settings page reads it: to print the address calendars must be shared with,
	// and to run a sync on demand.
	calendar *booking.Worker
	log      *slog.Logger
	tmpl     *template.Template

	// staff is a set of E.164 numbers. Normalised at construction so that a
	// mistyped configuration fails at startup rather than silently matching
	// nobody — an allow-list that matches nobody looks exactly like a working
	// one until someone tries to log in.
	staff map[string]bool

	// mfa holds the operator authenticators and checks their codes. It is the
	// console's only way in, and deliberately not the API's — customers still
	// sign in with a one-time code, which is the right trade for somebody who
	// came in to have their photograph taken.
	mfa *mfa.Registry

	// secure omits the Secure attribute in tests, which speak plain HTTP. It is
	// never false in production — main.go does not expose it.
	secure bool
}

// New returns the console. staffPhones are raw numbers in any Indonesian form;
// they are normalised here and an unparseable one is an error rather than a
// silently ignored entry.
func New(ident *identity.Service, ledger *loyalty.Ledger, cat *frames.Catalogue, desk *booking.Desk, calendar *booking.Worker, auth *mfa.Registry, log *slog.Logger, staffPhones []string) (*Console, error) {
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"points": formatPoints,
		"time":   func(t time.Time) string { return t.Format("2006-01-02 15:04") },
		"season": seasonText,
		"day":    dayValue,
		"slot":   slotStyle,
		"kb":     func(n int) string { return strconv.Itoa((n + 512) / 1024) },
		"inc":    func(n int) int { return n + 1 },
		// Booking times are stored in UTC and read by somebody standing in
		// Banyuwangi, so every one of these converts before it formats. A
		// template that printed a stored instant directly would tell an operator
		// a 14:00 session starts at seven in the morning.
		"wib":   func(t time.Time) string { return t.In(wib).Format("Monday, 2 January 2006") },
		"clock": func(t time.Time) string { return t.In(wib).Format("15:04") },
		// wa.me wants the number with no plus.
		"wa": func(s string) string { return strings.TrimPrefix(s, "+") },
	}).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}

	staff := make(map[string]bool, len(staffPhones))
	for _, raw := range staffPhones {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		e164, err := phone.Normalize(raw)
		if err != nil {
			return nil, err
		}
		staff[e164] = true
	}

	return &Console{
		identity: ident,
		loyalty:  ledger,
		frameCat: cat,
		booking:  desk,
		calendar: calendar,
		mfa:      auth,
		log:      log,
		tmpl:     tmpl,
		staff:    staff,
		secure:   true,
	}, nil
}

// Handler routes the console. Registered by the caller at "/", so it is also
// what answers for any path the API does not claim.
func (c *Console) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", c.index)
	mux.HandleFunc("POST /login", c.login)
	mux.HandleFunc("POST /logout", c.logout)
	mux.HandleFunc("GET /customers", c.staffOnly(c.customers))
	mux.HandleFunc("POST /customers/{id}/adjust", c.staffOnly(c.adjust))
	mux.HandleFunc("GET /bookings", c.staffOnly(c.bookingDay))
	mux.HandleFunc("POST /bookings/{id}/cancel", c.staffOnly(c.bookingCancel))
	mux.HandleFunc("POST /bookings/block", c.staffOnly(c.bookingBlock))

	mux.HandleFunc("GET /settings", c.staffOnly(c.settings))
	mux.HandleFunc("POST /settings/calendar/{id}", c.staffOnly(c.settingsCalendar))
	mux.HandleFunc("POST /settings/sync", c.staffOnly(c.settingsSync))

	mux.HandleFunc("GET /frames", c.staffOnly(c.frameIndex))
	mux.HandleFunc("POST /frames", c.staffOnly(c.frameUpload))
	mux.HandleFunc("GET /frames/{id}/art.png", c.staffOnly(c.frameArt))
	mux.HandleFunc("POST /frames/{id}/publish", c.staffOnly(c.framePublish))
	mux.HandleFunc("POST /frames/{id}/season", c.staffOnly(c.frameSeason))
	mux.HandleFunc("POST /frames/{id}/delete", c.staffOnly(c.frameDelete))

	// Not behind staffOnly: the login page wears the same chrome as the rest of
	// the console, so the logo has to load for someone who has not signed in.
	// Neither file says anything a stranger could not read off the marketing
	// site anyway.
	mux.Handle("GET /logo.png", brandAsset("logo.png", "image/png"))
	mux.Handle("GET /icon.svg", brandAsset("icon.svg", "image/svg+xml"))
	return mux
}

// brandAsset serves one embedded brand file.
//
// Read once here rather than per request: Handler is called at startup, the
// files are a few kilobytes, and a missing one is a build mistake rather than a
// runtime condition — so it panics now instead of 404ing later.
func brandAsset(name, mime string) http.Handler {
	body, err := brandFS.ReadFile(name)
	if err != nil {
		panic("admin: embedded brand asset missing: " + name)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", mime)
		h.Set("X-Content-Type-Options", "nosniff")
		// Unlike every page here, this holds nothing about an operator or a
		// customer, so it is the one response worth letting a browser keep.
		h.Set("Cache-Control", "public, max-age=86400")
		w.Write(body)
	})
}

// page is what every template renders against. One struct rather than one per
// view: the shared chrome needs the same three fields on every page, and
// keeping them in separate types means remembering to populate them separately.
type page struct {
	Title    string
	Operator string
	CSRF     string
	Notice   string
	Error    string

	// Login. Enrolled is whether any operator has an authenticator at all —
	// none means the console cannot be signed in to by anybody, and the page
	// says which command fixes that rather than refusing every correct code
	// with no explanation.
	Phone     string
	Enrolled  bool
	Verticals []string

	// Customer views
	Query    string
	Customer *identity.User
	Balance  int64
	Entries  []loyalty.Entry
	Searched bool

	// Frame catalogue
	Frames []frames.Frame
	Sheets string

	// The booking day
	Bookings  []booking.Booking
	Day       time.Time
	DayISO    string
	PrevDay   string
	NextDay   string
	Calendars []calendarRow

	// Settings
	ServiceAccount string
}

// calendarRow is one resource's calendar, as the console reports it. Flattened
// out of booking.Sync because a template cannot format a time or decide what
// counts as stale, and putting either in the template would put a policy where
// nobody can test it.
type calendarRow struct {
	Resource string
	Name     string
	Calendar string
	Synced   string
	Stale    bool
	Error    string
}

func (c *Console) index(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := c.operator(r); ok {
		// Already an operator, so the login form would be a dead end.
		c.redirect(w, r, "/customers")
		return
	}
	c.render(w, r, http.StatusOK, "login.html", page{
		Title:    "Masuk",
		Enrolled: c.anyoneEnrolled(r),
	})
}

// login is the whole sign-in: a number and the six digits its authenticator is
// showing, checked together.
//
// One step rather than the two the one-time-code flow needed, because there is
// nothing to wait for in between — no code is sent, so there is no page whose
// only job is to say one is on its way.
//
// Every way of failing renders the same page with the same message. That is the
// property the previous flow had and the one most worth keeping: a form that
// distinguished "not an operator" from "wrong code" would answer, for anyone
// who cared to ask it, which of the numbers they tried belongs to staff.
func (c *Console) login(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.FormValue("phone"))
	code := strings.TrimSpace(r.FormValue("code"))

	refuse := func() {
		c.render(w, r, http.StatusUnauthorized, "login.html", page{
			Title:    "Masuk",
			Enrolled: c.anyoneEnrolled(r),
			Phone:    raw,
			Error: "Nomor atau kode salah. Kode berganti setiap 30 detik, dan " +
				"setelah beberapa kali gagal nomor dikunci 15 menit.",
		})
	}

	// The allow-list is checked before the registry is touched, which is not
	// only about disclosure: a stranger who guessed an operator's number could
	// otherwise spend that operator's failed attempts for them, and lock them
	// out of their own console.
	e164, err := phone.Normalize(raw)
	if err != nil || !c.staff[e164] {
		refuse()
		return
	}

	switch err := c.mfa.Verify(r.Context(), e164, code); {
	case err == nil:
	case errors.Is(err, mfa.ErrBadCode), errors.Is(err, mfa.ErrNotEnrolled), errors.Is(err, mfa.ErrLockedOut):
		// Logged, because these are the three an operator will phone about and
		// the page deliberately does not tell them apart.
		c.log.Info("admin: sign-in refused", "phone", e164, "reason", err)
		refuse()
		return
	default:
		c.log.Error("admin: verify authenticator", "err", err)
		c.render(w, r, http.StatusInternalServerError, "login.html", page{
			Title:    "Masuk",
			Enrolled: true,
			Phone:    raw,
			Error:    "Terjadi kesalahan. Coba lagi.",
		})
		return
	}

	// The code was right, so this number is who it says it is and is on the
	// allow-list. identity.StartSession is what turns that into a session; it
	// takes the number on trust, which is exactly why the two checks above come
	// first and why nothing else in this repository calls it.
	_, token, err := c.identity.StartSession(r.Context(), e164)
	if err != nil {
		c.log.Error("admin: start session", "err", err)
		c.render(w, r, http.StatusInternalServerError, "login.html", page{
			Title:    "Masuk",
			Enrolled: true,
			Phone:    raw,
			Error:    "Terjadi kesalahan. Coba lagi.",
		})
		return
	}
	c.log.Info("admin: signed in", "operator", e164)

	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookie,
		Value: token,
		Path:  "/",
		// No Domain: host-only, so this never enters the .bykami.id jar.
		HttpOnly: true,
		Secure:   c.secure,
		// Strict, not Lax. There is no cross-site entry point that needs to
		// carry this session — the console is reached by typing the address.
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int((30 * 24 * time.Hour).Seconds()),
	})
	c.redirect(w, r, "/customers")
}

func (c *Console) logout(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(sessionCookie); err == nil {
		if err := c.identity.EndSession(r.Context(), ck.Value); err != nil {
			c.log.Error("admin: end session", "err", err)
		}
	}
	// Cleared server-side above; this only tidies the browser. MaxAge -1 rather
	// than an empty value, so a browser that ignores the deletion is left with
	// a token that is already dead rather than a live one.
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/",
		HttpOnly: true, Secure: c.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	c.redirect(w, r, "/")
}

func (c *Console) customers(w http.ResponseWriter, r *http.Request, op identity.User) {
	q := strings.TrimSpace(r.URL.Query().Get("phone"))
	p := page{
		Title:     "Cari pelanggan",
		Operator:  op.Phone,
		CSRF:      csrfToken(r),
		Query:     q,
		Verticals: verticals,
	}

	// Outcomes of the adjust POST, which redirects rather than rendering so
	// that a refresh cannot resubmit it.
	if r.URL.Query().Get("ok") != "" {
		p.Notice = "Penyesuaian tersimpan."
	}
	if msg := r.URL.Query().Get("err"); msg != "" {
		p.Error = msg
	}
	if q == "" {
		c.render(w, r, http.StatusOK, "customers.html", p)
		return
	}
	p.Searched = true

	user, err := c.identity.UserByPhone(r.Context(), q)
	switch {
	case err == nil:
		p.Customer = &user
	case errors.Is(err, identity.ErrNoUser):
		p.Error = "Tidak ada akun dengan nomor itu."
		c.render(w, r, http.StatusOK, "customers.html", p)
		return
	case errors.Is(err, phone.ErrInvalid):
		p.Error = "Nomor tidak valid."
		c.render(w, r, http.StatusOK, "customers.html", p)
		return
	default:
		c.serverError(w, r, "lookup customer", err, p)
		return
	}

	if p.Balance, err = c.loyalty.Balance(r.Context(), user.ID); err != nil {
		c.serverError(w, r, "balance", err, p)
		return
	}
	if p.Entries, err = c.loyalty.History(r.Context(), user.ID, 100); err != nil {
		c.serverError(w, r, "history", err, p)
		return
	}
	c.render(w, r, http.StatusOK, "customers.html", p)
}

// adjust writes a compensating entry — the only way the ledger permits history
// to be corrected, and the reason nothing in it is mutable.
func (c *Console) adjust(w http.ResponseWriter, r *http.Request, op identity.User) {
	if !validCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	points, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("points")), 10, 64)
	vertical := r.FormValue("vertical")
	reason := strings.TrimSpace(r.FormValue("reason"))

	switch {
	case err != nil || points == 0:
		c.back(w, r, "Jumlah poin harus berupa angka selain nol.")
		return
	case !slices.Contains(verticals, vertical):
		c.back(w, r, "Vertical tidak dikenal.")
		return
	case reason == "":
		// The ledger's audit value is the reason, not the number. An
		// unexplained adjustment is the row that cannot be defended later.
		c.back(w, r, "Alasan wajib diisi.")
		return
	}

	// The operator is recorded in the reason because the ledger has no actor
	// column. Adding one is a migration; this keeps the attribution in the row
	// today so that no adjustment is anonymous in the meantime.
	note := reason + " (oleh " + op.Phone + ")"
	if _, err := c.loyalty.Adjust(r.Context(), id, vertical, points, note); err != nil {
		c.log.Error("admin: adjust", "err", err, "user", id)
		c.back(w, r, "Gagal menyimpan penyesuaian.")
		return
	}

	c.log.Info("admin: loyalty adjusted", "operator", op.Phone, "user", id, "points", points, "vertical", vertical)
	c.redirect(w, r, "/customers?phone="+urlQueryEscape(r.FormValue("phone"))+"&ok=1")
}

// anyoneEnrolled reports whether any operator has an authenticator.
//
// A database error is reported as "yes", which is the useful way to be wrong:
// the login form then works as normal and a genuinely correct code still gets
// in, where answering "no" would replace the form with instructions to enrol
// somebody who is already enrolled.
func (c *Console) anyoneEnrolled(r *http.Request) bool {
	n, err := c.mfa.Count(r.Context())
	if err != nil {
		c.log.Error("admin: count enrolments", "err", err)
		return true
	}
	return n > 0
}

// operator resolves the session cookie and reports whether it belongs to staff.
func (c *Console) operator(r *http.Request) (identity.User, string, bool) {
	ck, err := r.Cookie(sessionCookie)
	if err != nil || ck.Value == "" {
		return identity.User{}, "", false
	}
	u, err := c.identity.UserForSession(r.Context(), ck.Value)
	if err != nil {
		return identity.User{}, "", false
	}
	// Checked here, on every request, rather than trusted from the session.
	// Removing a number from the allow-list therefore ends that operator's
	// access at the next request instead of whenever their session expires.
	if !c.staff[u.Phone] {
		return identity.User{}, "", false
	}
	return u, ck.Value, true
}

type staffHandler func(w http.ResponseWriter, r *http.Request, op identity.User)

func (c *Console) staffOnly(h staffHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		op, _, ok := c.operator(r)
		if !ok {
			c.redirect(w, r, "/")
			return
		}
		h(w, r, op)
	}
}

// csrfToken derives a per-session value from the session token itself, so
// nothing has to be stored or expired alongside it.
//
// Safe because the derivation input is the session token, which an attacker
// cannot read: the cookie is HttpOnly, host-only, and SameSite=Strict. A
// cross-site form therefore cannot compute this value, which is the entire job.
func csrfToken(r *http.Request) string {
	ck, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(ck.Value + "|csrf"))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func validCSRF(r *http.Request) bool {
	want := csrfToken(r)
	got := r.FormValue("csrf")
	if want == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

// redirect carries the no-store header that http.Redirect alone would not set.
// A cached 303 from "/" is a cached statement about whether someone is signed
// in, which is exactly the thing an operator console should not leave lying in
// a shared browser or an intermediary.
func (c *Console) redirect(w http.ResponseWriter, r *http.Request, to string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (c *Console) render(w http.ResponseWriter, _ *http.Request, status int, name string, p page) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	// Operator pages list customers' phone numbers and point balances.
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	// No inline script, no external anything. Declared rather than assumed, so
	// that adding a script tag later fails visibly instead of silently widening
	// what a template injection could do.
	// img-src is for the frame previews, which are served by this same origin
	// from the database. data: is not permitted: an <img> is the one element
	// here whose source is operator-supplied, and allowing data: would make an
	// injected src a way to render arbitrary bytes from this origin.
	h.Set("Content-Security-Policy",
		"default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("Referrer-Policy", "same-origin")

	w.WriteHeader(status)
	if err := c.tmpl.ExecuteTemplate(w, name, p); err != nil {
		c.log.Error("admin: render", "template", name, "err", err)
	}
}

func (c *Console) serverError(w http.ResponseWriter, r *http.Request, op string, err error, p page) {
	c.log.Error("admin: "+op, "err", err)
	p.Error = "Terjadi kesalahan. Coba lagi."
	c.render(w, r, http.StatusInternalServerError, "customers.html", p)
}

func (c *Console) back(w http.ResponseWriter, r *http.Request, msg string) {
	c.redirect(w, r, "/customers?phone="+urlQueryEscape(r.FormValue("phone"))+"&err="+urlQueryEscape(msg))
}

func formatPoints(n int64) string {
	if n > 0 {
		return "+" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }
