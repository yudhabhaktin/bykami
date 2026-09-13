// Package admin is the operator console at admin.bykami.id.
//
// Server-rendered HTML with no JavaScript and no build step. That is not
// minimalism for its own sake: the alternative is a second toolchain, a bundle
// to ship, and a client-side session — three things to keep correct so that a
// staff member can look up a phone number.
//
// # Who is an operator
//
// Staff sign in with a username (the credential's label) and a password. One
// generated password per person. The label is the username and the password
// proves it; they are the same thing, not two columns.
//
// An operator with can_manage may create and disable other operators. The
// bootstrap is a shell subcommand — `bykami admin password add` — because
// doing it in the console would need somebody already signed in, which is the
// thing that does not exist until the first credential does.
//
// It also means a stolen customer session cannot become an operator session.
// A console session is a console session, resolved from admin_sessions on
// every request, and a customer token from identity.sessions opens nothing here.
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
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/adminauth"
	"github.com/bhaktiyudha/bykami/api/internal/booking"
	"github.com/bhaktiyudha/bykami/api/internal/frames"
	"github.com/bhaktiyudha/bykami/api/internal/gcal"
	"github.com/bhaktiyudha/bykami/api/internal/identity"
	"github.com/bhaktiyudha/bykami/api/internal/loyalty"
	"github.com/bhaktiyudha/bykami/api/internal/membership"
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
// geist-latin.woff2 is the same 29KB file the four sites are set in, and it is
// here for the same reason the tokens are: so the console reads as part of the
// platform rather than as a tool that happens to sit beside it. An earlier note
// here refused a webfont as "a network round trip and a CSP exception bought
// for nothing" — that was right while the console had no house typeface to
// match. It now has one, and the round trip is same-origin, cached for a day,
// and paid once by a handful of staff on known devices.
//
// OFL.txt travels with it because the SIL Open Font License requires the licence
// to accompany the font wherever it goes; it is not served, only carried.
//
//go:embed logo.png icon.svg geist-latin.woff2
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
	// members is the studio's stamp card: the paper card as a view over the
	// ledger above it. See internal/membership.
	members  *membership.Service
	frameCat *frames.Catalogue
	// booths is what each booth reports it is offering. Read here and written
	// only by the booths themselves — see internal/frames/booth.go.
	booths  *frames.Booths
	booking *booking.Desk
	// The calendar sync, or nil when no Google credential is configured. Only the
	// settings page reads it: to print the address calendars must be shared with,
	// and to run a sync on demand.
	calendar *booking.Worker
	log      *slog.Logger
	tmpl     *template.Template

	// auth holds the operator credentials and their sessions. One generated
	// password per operator; the password is the identity. See internal/adminauth.
	auth *adminauth.Registry

	// connect runs the one-time Google consent that shares a calendar with the
	// service account above. Nil when no OAuth client is configured, which
	// leaves the console's paste-a-calendar-id form as the only way in — see
	// internal/admin/google.go.
	connect *gcal.Connect
	// grants holds those consents for the few minutes a mapping run takes, and
	// never writes them down.
	grants *grants

	// reauthTokens are short-lived single-use tokens issued after a manager
	// re-enters their password on the interstitial. Privileged actions check
	// for one and consume it, so a direct POST to /operators/disable without
	// passing through reauth is refused.
	reauthMu     sync.Mutex
	reauthTokens map[string]time.Time

	// secure omits the Secure attribute in tests, which speak plain HTTP. It is
	// never false in production — main.go does not expose it.
	secure bool
}

// New returns the console.
func New(ident *identity.Service, ledger *loyalty.Ledger, members *membership.Service, cat *frames.Catalogue, booths *frames.Booths, desk *booking.Desk, calendar *booking.Worker, auth *adminauth.Registry, connect *gcal.Connect, log *slog.Logger) (*Console, error) {
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"points": formatPoints,
		"rupiah": formatRupiah,
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
		// Cloudflare's Email Address Obfuscation rewrites every address in the
		// HTML into a "[email protected]" placeholder plus a decode script — and the
		// CSP below names no script-src, so that script is blocked and the
		// placeholder is what an operator reads, forever. The address it eats is
		// the service account, which is the single thing this page exists to
		// show. Hence Cloudflare's documented opt-out.
		//
		// Emitted as template.HTML because html/template elides comments written
		// literally in a template, so the obvious version of this fix compiles,
		// renders, and does nothing at all.
		"email": func(s string) template.HTML {
			return template.HTML("<!--email_off-->" + template.HTMLEscapeString(s) + "<!--/email_off-->")
		},
	}).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}

	return &Console{
		identity:     ident,
		loyalty:      ledger,
		members:      members,
		frameCat:     cat,
		booths:       booths,
		booking:      desk,
		calendar:     calendar,
		auth:         auth,
		connect:      connect,
		grants:       newGrants(),
		reauthTokens: make(map[string]time.Time),
		log:          log,
		tmpl:         tmpl,
		secure:       true,
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

	// The stamp card. The read is a phone-number search like the one above;
	// everything that takes a gift or writes a stamp is a POST, because a gift
	// is a fulfilment somebody has to hand over.
	mux.HandleFunc("GET /stamps", c.staffOnly(c.stamps))
	mux.HandleFunc("POST /stamps/purchase", c.staffOnly(c.stampsPurchase))
	mux.HandleFunc("POST /stamps/redeem", c.staffOnly(c.stampsRedeem))
	mux.HandleFunc("POST /stamps/void", c.staffOnly(c.stampsVoid))
	mux.HandleFunc("GET /bookings", c.staffOnly(c.bookingDay))
	mux.HandleFunc("POST /bookings/{id}/cancel", c.staffOnly(c.bookingCancel))
	mux.HandleFunc("POST /bookings/block", c.staffOnly(c.bookingBlock))

	mux.HandleFunc("GET /settings", c.staffOnly(c.settings))
	mux.HandleFunc("POST /settings/calendar/{id}", c.staffOnly(c.settingsCalendar))
	mux.HandleFunc("POST /settings/sync", c.staffOnly(c.settingsSync))

	mux.HandleFunc("POST /settings/google/start", c.staffOnly(c.googleStart))
	// Not behind staffOnly, and the one route here that is not: Google's
	// redirect is a cross-site navigation, which a SameSite=Strict session
	// cookie is not sent on. It authenticates on the state cookie instead —
	// see internal/admin/google.go.
	mux.HandleFunc("GET /settings/google/callback", c.googleCallback)
	mux.HandleFunc("GET /settings/google", c.staffOnly(c.googleMap))
	mux.HandleFunc("POST /settings/google/connect", c.staffOnly(c.googleConnect))
	mux.HandleFunc("POST /settings/google/finish", c.staffOnly(c.googleFinish))

	mux.HandleFunc("GET /frames", c.staffOnly(c.frameIndex))
	mux.HandleFunc("POST /frames", c.staffOnly(c.frameUpload))
	mux.HandleFunc("GET /frames/{id}/art.png", c.staffOnly(c.frameArt))
	mux.HandleFunc("POST /frames/{id}/publish", c.staffOnly(c.framePublish))
	mux.HandleFunc("POST /frames/{id}/season", c.staffOnly(c.frameSeason))
	mux.HandleFunc("POST /frames/{id}/delete", c.staffOnly(c.frameDelete))

	// Artwork for a design a booth reported, addressed by hash rather than by
	// id. Its own prefix and not /frames/…: most of what a booth offers has no
	// catalogue row, and a pattern under /frames would collide with the one
	// above — /frames/art/art.png matches both, which ServeMux refuses at
	// registration rather than at request time.
	mux.HandleFunc("GET /booth/art/{sha256}", c.staffOnly(c.boothArt))

	// Operator management. Protected by managerOnly: only an operator with
	// can_manage may list, add, or disable operators.
	mux.HandleFunc("GET /operators", c.managerOnly(c.operators))
	mux.HandleFunc("POST /operators/add", c.managerOnly(c.operatorsAdd))
	mux.HandleFunc("POST /operators/manage", c.managerOnly(c.operatorsManage))
	mux.HandleFunc("POST /operators/unmanage", c.managerOnly(c.operatorsUnmanage))
	mux.HandleFunc("POST /operators/disable", c.managerOnly(c.operatorsDisable))
	mux.HandleFunc("POST /operators/reset", c.managerOnly(c.operatorsReset))
	// Re-authentication interstitial for privileged actions, and the
	// confirmation page that follows it.
	//
	// Each action needs its own GET because the interstitial's success
	// redirects here, and without these a browser landed on 405 Method Not
	// Allowed after typing the password correctly — the action never ran. The
	// GET renders a form and changes nothing; the action still runs only from
	// the POST that carries the token.
	mux.HandleFunc("GET /operators/reauth", c.staffOnly(c.operatorsReauth))
	mux.HandleFunc("POST /operators/reauth", c.staffOnly(c.operatorsReauth))
	mux.HandleFunc("GET /operators/add", c.managerOnly(c.operatorsConfirm("add")))
	mux.HandleFunc("GET /operators/manage", c.managerOnly(c.operatorsConfirm("manage")))
	mux.HandleFunc("GET /operators/unmanage", c.managerOnly(c.operatorsConfirm("unmanage")))
	mux.HandleFunc("GET /operators/disable", c.managerOnly(c.operatorsConfirm("disable")))
	mux.HandleFunc("GET /operators/reset", c.managerOnly(c.operatorsConfirm("reset")))

	// Password change. Allowed even when must_change is set, because this is
	// the one page a credential with must_change is permitted to reach.
	mux.HandleFunc("GET /password/set", c.staffOnly(c.passwordSet))
	mux.HandleFunc("POST /password/set", c.staffOnly(c.passwordSet))

	// Not behind staffOnly: the login page wears the same chrome as the rest of
	// the console, so the logo and the typeface have to load for someone who has
	// not signed in. None of the three says anything a stranger could not read
	// off the marketing site anyway — they are the same files it serves.
	mux.Handle("GET /logo.png", brandAsset("logo.png", "image/png"))
	mux.Handle("GET /icon.svg", brandAsset("icon.svg", "image/svg+xml"))
	mux.Handle("GET /geist.woff2", brandAsset("geist-latin.woff2", "font/woff2"))
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
	Title     string
	Operator  string
	CanManage bool
	CSRF      string
	Notice    string
	Error     string

	// Login. Enrolled is whether any credential exists at all — none means
	// the console cannot be signed in to by anybody, and the page says which
	// command fixes that rather than refusing every password with no
	// explanation.
	Enrolled  bool
	Verticals []string

	// Customer views
	Query    string
	Customer *identity.User
	Balance  int64
	Entries  []loyalty.Entry
	Searched bool

	// The stamp card. StampSearched separates "no number typed" from "this
	// number is not a member", which decides whether the page offers to create
	// the account as part of the next purchase.
	StampQuery    string
	StampSearched bool
	StampCard     *membership.CardView
	StampRewards  []membership.Reward
	StampToday    []membership.Purchase

	// Frame catalogue
	Frames []frames.Frame
	Sheets string

	// What the booths report they are actually offering, which is the catalogue
	// plus the designs built into the agent binary plus anything in that
	// machine's own templates folder. OnBooth is the catalogue ids that reached
	// at least one of them — the difference between a frame being published and
	// a frame being on sale.
	Booths  []boothView
	OnBooth map[string]bool
	// BoothsKnown separates "no booth has this frame" from "no booth has said".
	// Without it a console whose booths have never reported would mark every
	// published frame as undelivered, which is a worse lie than the one this
	// page was built to fix.
	BoothsKnown bool

	// The booking day
	Bookings  []booking.Booking
	Day       time.Time
	DayISO    string
	PrevDay   string
	NextDay   string
	Calendars []calendarRow

	// Settings
	ServiceAccount string
	// GoogleConnect is whether the console can drive the calendar share itself,
	// which decides whether the settings page offers a button or the manual
	// instructions.
	GoogleConnect bool

	// The Google connect run. GoogleAccount is the address the operator signed
	// in as, so a page that is about to hand a calendar to a service account can
	// say whose calendars it is showing — the whole flow is run twice, once per
	// account, and the two look identical otherwise.
	GoogleAccount   string
	GoogleCalendars []googleCalendarRow

	// Operator management
	Operators    []adminauth.Credential
	ManagerCount int
	// Re-auth interstitial
	ReauthAction string
	ReauthTarget string
	ReauthLabel  string
	// ReauthToken is the single-use proof that the manager re-entered their
	// own password. It travels from the interstitial into the confirmation
	// page and from there into the action's own POST, and every privileged
	// action refuses without it — which is what stops a stolen session cookie
	// from minting a credential of its own.
	ReauthToken string
	// ConfirmAction is the same action written out for a human. The template
	// shows this and never has to know the action keys.
	ConfirmAction string
}

// googleCalendarRow is one calendar on the signed-in Google account.
type googleCalendarRow struct {
	ID      string
	Name    string
	Primary bool
	// Owned is false for a calendar shared *to* this account. Listed anyway, but
	// not offerable: the grant this flow makes is one only an owner can make, so
	// offering it would fail at the last step with a message from Google.
	Owned bool
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
	if _, _, _, _, ok := c.operator(r); ok {
		// Already an operator, so the login form would be a dead end.
		c.redirect(w, r, "/customers")
		return
	}
	c.render(w, r, http.StatusOK, "login.html", page{
		Title:    "Masuk",
		Enrolled: c.anyoneEnrolled(r),
	})
}

// login is the whole sign-in: username and password. Every way of failing
// renders the same page with the same message. That is the property the
// previous flow had and the one most worth keeping: a form that distinguished
// them would answer, for anyone who cared to ask it, which passwords exist.
func (c *Console) login(w http.ResponseWriter, r *http.Request) {
	label := strings.TrimSpace(r.FormValue("username"))
	pw := strings.TrimSpace(r.FormValue("password"))
	ip := callerIP(r)

	refuse := func() {
		c.render(w, r, http.StatusUnauthorized, "login.html", page{
			Title:    "Masuk",
			Enrolled: c.anyoneEnrolled(r),
			Error:    "Kata sandi salah. Setelah beberapa kali gagal, alamat ini dikunci 15 menit.",
		})
	}

	locked, err := c.auth.IsLockedOut(r.Context(), ip)
	if err != nil {
		c.log.Error("admin: check lockout", "err", err)
	}
	if locked {
		c.log.Info("admin: sign-in refused", "ip", ip, "reason", "locked_out")
		refuse()
		return
	}

	cred, err := c.auth.VerifyByLabel(r.Context(), label, pw)
	if err != nil {
		// Logged, because this is what an operator will phone about and the
		// page deliberately does not tell them apart.
		c.log.Info("admin: sign-in refused", "ip", ip, "reason", err)
		if recordErr := c.auth.RecordAttempt(r.Context(), ip); recordErr != nil {
			c.log.Error("admin: record attempt", "err", recordErr)
		}
		refuse()
		return
	}

	token, err := c.auth.StartSession(r.Context(), cred.ID)
	if err != nil {
		c.log.Error("admin: start session", "err", err)
		c.render(w, r, http.StatusInternalServerError, "login.html", page{
			Title:    "Masuk",
			Enrolled: true,
			Error:    "Terjadi kesalahan. Coba lagi.",
		})
		return
	}
	// Record the success too, so the sweep has something to clean up and the
	// count stays honest.
	if err := c.auth.RecordAttempt(r.Context(), ip); err != nil {
		c.log.Error("admin: record attempt", "err", err)
	}
	if err := c.auth.SweepAttempts(r.Context()); err != nil {
		c.log.Error("admin: sweep attempts", "err", err)
	}
	c.log.Info("admin: signed in", "operator", cred.Label)

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
		if err := c.auth.EndSession(r.Context(), ck.Value); err != nil {
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

func (c *Console) customers(w http.ResponseWriter, r *http.Request, op string) {
	q := strings.TrimSpace(r.URL.Query().Get("phone"))
	_, canManage, _, _, _ := c.operator(r)
	p := page{
		Title:     "Cari pelanggan",
		Operator:  op,
		CanManage: canManage,
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
func (c *Console) adjust(w http.ResponseWriter, r *http.Request, op string) {
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
	note := reason + " (oleh " + op + ")"
	if _, err := c.loyalty.Adjust(r.Context(), id, vertical, points, note); err != nil {
		c.log.Error("admin: adjust", "err", err, "user", id)
		c.back(w, r, "Gagal menyimpan penyesuaian.")
		return
	}

	c.log.Info("admin: loyalty adjusted", "operator", op, "user", id, "points", points, "vertical", vertical)
	c.redirect(w, r, "/customers?phone="+urlQueryEscape(r.FormValue("phone"))+"&ok=1")
}

// --- Operator management ---

func (c *Console) operators(w http.ResponseWriter, r *http.Request, op string) {
	ctx := r.Context()
	_, canManage, _, _, _ := c.operator(r)
	p := page{
		Title:     "Operator",
		Operator:  op,
		CanManage: canManage,
		CSRF:      csrfToken(r),
	}

	all, err := c.auth.List(ctx)
	if err != nil {
		c.serverError(w, r, "list operators", err, p)
		return
	}
	p.Operators = all

	mc, err := c.auth.ManagerCount(ctx)
	if err != nil {
		c.serverError(w, r, "count managers", err, p)
		return
	}
	p.ManagerCount = mc

	// Flash messages from POST redirects
	if r.URL.Query().Get("ok") != "" {
		p.Notice = "Perubahan tersimpan."
	}
	if msg := r.URL.Query().Get("err"); msg != "" {
		p.Error = msg
	}

	c.render(w, r, http.StatusOK, "operators.html", p)
}

func (c *Console) operatorsAdd(w http.ResponseWriter, r *http.Request, op string) {
	// The re-auth token, not the CSRF token, is what makes this safe. CSRF is
	// derived from the session cookie, so anybody holding that cookie can
	// compute it — and creating an operator is the act of minting a credential,
	// which must cost a password typed a moment ago, not a cookie borrowed a
	// week ago.
	if !validCSRF(r) || !c.consumeReauthToken(r.FormValue("reauth")) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}
	label := strings.TrimSpace(r.FormValue("label"))
	if label == "" {
		c.redirect(w, r, "/operators?err="+url.QueryEscape("Nama operator wajib diisi."))
		return
	}

	pw, err := c.auth.AddWithCreator(r.Context(), label, op)
	if err != nil {
		c.log.Error("admin: add operator", "err", err, "label", label)
		c.redirect(w, r, "/operators?err="+url.QueryEscape("Gagal menambahkan operator."))
		return
	}
	c.log.Info("admin: operator added", "manager", op, "label", label)

	// Show the password once, in the page itself. This is the only time it is
	// ever visible, so the manager must copy it now.
	p := page{
		Title:    "Operator",
		Operator: op,
		CSRF:     csrfToken(r),
		Notice:   "Operator " + label + " ditambahkan. Kata sandi: " + pw,
	}
	all, err := c.auth.List(r.Context())
	if err != nil {
		c.serverError(w, r, "list operators", err, p)
		return
	}
	p.Operators = all
	mc, err := c.auth.ManagerCount(r.Context())
	if err != nil {
		c.serverError(w, r, "count managers", err, p)
		return
	}
	p.ManagerCount = mc
	c.render(w, r, http.StatusOK, "operators.html", p)
}

func (c *Console) operatorsManage(w http.ResponseWriter, r *http.Request, op string) {
	if !validCSRF(r) || !c.consumeReauthToken(r.FormValue("reauth")) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}
	label := strings.TrimSpace(r.FormValue("label"))
	if err := c.auth.SetManage(r.Context(), label); err != nil {
		c.log.Error("admin: set manage", "err", err, "label", label)
		c.redirect(w, r, "/operators?err="+url.QueryEscape("Gagal mengubah hak akses."))
		return
	}
	c.log.Info("admin: operator promoted", "manager", op, "label", label)
	c.redirect(w, r, "/operators?ok=1")
}

func (c *Console) operatorsUnmanage(w http.ResponseWriter, r *http.Request, op string) {
	if !validCSRF(r) || !c.consumeReauthToken(r.FormValue("reauth")) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}
	label := strings.TrimSpace(r.FormValue("label"))
	if err := c.auth.UnsetManage(r.Context(), label); err != nil {
		c.log.Error("admin: unset manage", "err", err, "label", label)
		if errors.Is(err, adminauth.ErrLastManager) {
			c.redirect(w, r, "/operators?err="+url.QueryEscape("Tidak bisa mencabut hak akses manajer terakhir."))
			return
		}
		c.redirect(w, r, "/operators?err="+url.QueryEscape("Gagal mengubah hak akses."))
		return
	}
	c.log.Info("admin: operator demoted", "manager", op, "label", label)
	c.redirect(w, r, "/operators?ok=1")
}

func (c *Console) operatorsDisable(w http.ResponseWriter, r *http.Request, op string) {
	if !validCSRF(r) || !c.consumeReauthToken(r.FormValue("reauth")) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}
	label := strings.TrimSpace(r.FormValue("label"))
	if err := c.auth.RemoveWithActor(r.Context(), label, op); err != nil {
		c.log.Error("admin: disable operator", "err", err, "label", label)
		if errors.Is(err, adminauth.ErrLastManager) {
			c.redirect(w, r, "/operators?err="+url.QueryEscape("Tidak bisa menonaktifkan manajer terakhir."))
			return
		}
		c.redirect(w, r, "/operators?err="+url.QueryEscape("Gagal menonaktifkan operator."))
		return
	}
	c.log.Info("admin: operator disabled", "manager", op, "label", label)
	c.redirect(w, r, "/operators?ok=1")
}

func (c *Console) operatorsReset(w http.ResponseWriter, r *http.Request, op string) {
	if !validCSRF(r) || !c.consumeReauthToken(r.FormValue("reauth")) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}
	label := strings.TrimSpace(r.FormValue("label"))
	pw, err := c.auth.ResetPassword(r.Context(), label, op)
	if err != nil {
		c.log.Error("admin: reset password", "err", err, "label", label)
		c.redirect(w, r, "/operators?err="+url.QueryEscape("Gagal mengatur ulang kata sandi."))
		return
	}
	c.log.Info("admin: password reset", "manager", op, "label", label)

	p := page{
		Title:    "Operator",
		Operator: op,
		CSRF:     csrfToken(r),
		Notice:   "Kata sandi untuk " + label + " diatur ulang: " + pw,
	}
	all, err := c.auth.List(r.Context())
	if err != nil {
		c.serverError(w, r, "list operators", err, p)
		return
	}
	p.Operators = all
	mc, err := c.auth.ManagerCount(r.Context())
	if err != nil {
		c.serverError(w, r, "count managers", err, p)
		return
	}
	p.ManagerCount = mc
	c.render(w, r, http.StatusOK, "operators.html", p)
}

func (c *Console) passwordSet(w http.ResponseWriter, r *http.Request, op string) {
	p := page{
		Title:    "Ubah kata sandi",
		Operator: op,
		CSRF:     csrfToken(r),
	}

	if r.Method == "GET" {
		c.render(w, r, http.StatusOK, "password-set.html", p)
		return
	}

	pw1 := strings.TrimSpace(r.FormValue("password"))
	pw2 := strings.TrimSpace(r.FormValue("password2"))
	if pw1 != pw2 {
		p.Error = "Kata sandi tidak cocok."
		c.render(w, r, http.StatusOK, "password-set.html", p)
		return
	}
	if len(pw1) < 12 {
		p.Error = "Kata sandi minimal 12 karakter."
		c.render(w, r, http.StatusOK, "password-set.html", p)
		return
	}
	if err := c.auth.SetPassword(r.Context(), op, pw1); err != nil {
		c.log.Error("admin: set password", "err", err, "operator", op)
		p.Error = "Gagal mengubah kata sandi."
		c.render(w, r, http.StatusOK, "password-set.html", p)
		return
	}
	c.log.Info("admin: password changed", "operator", op)
	c.redirect(w, r, "/customers")
}

// operatorsReauth is the interstitial that asks a manager to re-enter their
// password before a privileged action. GET renders the form; POST checks it.
// On success it redirects to the target action with the same form values,
// carrying a short-lived re-auth token in a hidden field so the target action
// can verify the manager proved themselves recently.
func (c *Console) operatorsReauth(w http.ResponseWriter, r *http.Request, op string) {
	action := r.FormValue("action")
	target := r.FormValue("target")
	label := strings.TrimSpace(r.FormValue("label"))

	// Validate the action and target are one of the known privileged actions.
	if !isPrivilegedAction(action) {
		http.Error(w, "bad action", http.StatusBadRequest)
		return
	}

	if r.Method == "GET" {
		_, canManage, _, _, _ := c.operator(r)
		p := page{
			Title:        "Konfirmasi kata sandi",
			Operator:     op,
			CanManage:    canManage,
			CSRF:         csrfToken(r),
			ReauthAction: action,
			ReauthTarget: target,
			ReauthLabel:  label,
		}
		c.render(w, r, http.StatusOK, "reauth.html", p)
		return
	}

	// POST: verify the manager's password.
	pw := strings.TrimSpace(r.FormValue("password"))
	cred, err := c.auth.VerifyByLabel(r.Context(), op, pw)
	if err != nil {
		_, canManage, _, _, _ := c.operator(r)
		p := page{
			Title:        "Konfirmasi kata sandi",
			Operator:     op,
			CanManage:    canManage,
			CSRF:         csrfToken(r),
			Error:        "Kata sandi salah.",
			ReauthAction: action,
			ReauthTarget: target,
			ReauthLabel:  label,
		}
		c.render(w, r, http.StatusUnauthorized, "reauth.html", p)
		return
	}
	if !cred.CanManage {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Re-auth successful. Mint a single-use token and redirect to the target
	// with the form values preserved so the target handler can replay them.
	q := url.Values{}
	q.Set("label", label)
	q.Set("csrf", csrfToken(r))
	q.Set("reauth", c.mintReauthToken())
	c.redirect(w, r, target+"?"+q.Encode())
}

// confirmActionWords names each privileged action for a person, so the
// confirmation page can say what is about to happen without the template
// knowing the action keys.
var confirmActionWords = map[string]string{
	"add":      "Tambah operator",
	"manage":   "Jadikan manajer",
	"unmanage": "Cabut manajer",
	"disable":  "Nonaktifkan operator",
	"reset":    "Atur ulang kata sandi",
}

// operatorsConfirm is the page between re-authentication and a privileged
// action.
//
// It exists because the interstitial's success redirect lands here by GET, and
// a GET must never perform one of these actions. So this renders a form, and
// the action runs from that form's POST — the only request that carries the
// single-use token onward. Without it a manager was redirected onto a POST-only
// route and got 405 Method Not Allowed after typing their password correctly,
// so the action never happened at all.
func (c *Console) operatorsConfirm(action string) staffHandler {
	return func(w http.ResponseWriter, r *http.Request, op string) {
		q := r.URL.Query()
		c.render(w, r, http.StatusOK, "confirm.html", page{
			Title:         "Konfirmasi",
			Operator:      op,
			CSRF:          csrfToken(r),
			ReauthAction:  action,
			ReauthTarget:  "/operators/" + action,
			ReauthLabel:   strings.TrimSpace(q.Get("label")),
			ReauthToken:   q.Get("reauth"),
			ConfirmAction: confirmActionWords[action],
		})
	}
}

func (c *Console) mintReauthToken() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("admin: entropy unavailable: " + err.Error())
	}
	tok := hex.EncodeToString(b[:])
	c.reauthMu.Lock()
	// Sweep the expired ones on the way past. Without this a token that is
	// minted and never used stays in the map for the life of the process, and
	// the only thing keeping the map honest is that most tokens get consumed.
	now := time.Now()
	for k, exp := range c.reauthTokens {
		if now.After(exp) {
			delete(c.reauthTokens, k)
		}
	}
	c.reauthTokens[tok] = now.Add(60 * time.Second)
	c.reauthMu.Unlock()
	return tok
}

func (c *Console) consumeReauthToken(tok string) bool {
	c.reauthMu.Lock()
	defer c.reauthMu.Unlock()
	exp, ok := c.reauthTokens[tok]
	if !ok || time.Now().After(exp) {
		return false
	}
	delete(c.reauthTokens, tok)
	return true
}

// isPrivilegedAction lists the actions that require the manager to re-enter
// their own password first.
//
// "add" is here for the same reason as the rest and more urgently than any of
// them: it is the action that mints a credential, so leaving it out would let a
// stolen session cookie create a permanent way in rather than borrow one for a
// month. Every caller of this is a check that the token exists.
func isPrivilegedAction(action string) bool {
	switch action {
	case "add", "disable", "manage", "unmanage", "reset":
		return true
	}
	return false
}

// anyoneEnrolled reports whether any credential exists.
//
// A database error is reported as "yes", which is the useful way to be wrong:
// the login form then works as normal and a genuinely correct password still
// gets in, where answering "no" would replace the form with instructions to
// add somebody who is already there.
func (c *Console) anyoneEnrolled(r *http.Request) bool {
	n, err := c.auth.Count(r.Context())
	if err != nil {
		c.log.Error("admin: count credentials", "err", err)
		return true
	}
	return n > 0
}

// operator resolves the session cookie to a credential label, can_manage, and
// whether the password must be changed.
func (c *Console) operator(r *http.Request) (string, bool, bool, string, bool) {
	ck, err := r.Cookie(sessionCookie)
	if err != nil || ck.Value == "" {
		return "", false, false, "", false
	}
	cred, err := c.auth.SessionForToken(r.Context(), ck.Value)
	if err != nil {
		return "", false, false, "", false
	}
	return cred.Label, cred.CanManage, cred.MustChange, ck.Value, true
}

type staffHandler func(w http.ResponseWriter, r *http.Request, op string)

func (c *Console) staffOnly(h staffHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		op, _, mustChange, _, ok := c.operator(r)
		if !ok {
			c.redirect(w, r, "/")
			return
		}
		if mustChange && r.URL.Path != "/password/set" {
			c.redirect(w, r, "/password/set")
			return
		}
		h(w, r, op)
	}
}

func (c *Console) managerOnly(h staffHandler) http.HandlerFunc {
	return c.staffOnly(func(w http.ResponseWriter, r *http.Request, op string) {
		_, isManager, _, _, _ := c.operator(r)
		if !isManager {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h(w, r, op)
	})
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

// callerIP is who to count the request against.
//
// The process listens on localhost and Cloudflare Tunnel dials out to it, so
// RemoteAddr is the tunnel and is the same for every caller on the internet.
// Cloudflare sets CF-Connecting-IP to the true client and X-Forwarded-For
// carries the chain, so they are tried in that order. On anything else (a
// test) the socket address is the answer.
func callerIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" {
		return ip
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if first, _, ok := strings.Cut(fwd, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(fwd)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
	// form-action carries Google's sign-in origin because "Hubungkan Google"
	// posts here and is answered with a redirect to it. Whether form-action is
	// checked against the *redirect* target as well as the form's own action is
	// a point browsers have disagreed on, so the origin is named rather than
	// left to that: the cost of being wrong is a connect button that does
	// nothing in one browser, diagnosed from a console error nobody is watching.
	// font-src is the house typeface, served from this origin by the same
	// handler that serves the logo. 'self' only — no data:, and no Google Fonts.
	h.Set("Content-Security-Policy",
		"default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; font-src 'self'; "+
			"form-action 'self' https://accounts.google.com; base-uri 'none'; frame-ancestors 'none'")
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
