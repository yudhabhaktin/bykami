package admin

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/gcal"
	"github.com/bhaktiyudha/bykami/api/internal/identity"
)

// Connecting a Google account from the console.
//
// What the operator does: press "Hubungkan Google", sign in as the account that
// owns the booth calendars, then map each calendar to a resource. What actually
// happens is that the consent is used once to grant the service account writer
// access to the chosen calendars — the same grant the manual "share with
// specific people" flow makes — and then thrown away. internal/gcal/connect.go
// carries the reasoning; nothing about the ongoing sync changes.
//
// Two accounts, one at a time. Y2K sits on the studio's account and the other
// two booths on the booth account's, and each connect run maps whatever the
// signed-in account can offer.
//
// # Why the callback does not require a session
//
// The console's session cookie is SameSite=Strict, which is right and stays
// that way. A Strict cookie is not sent on a cross-site navigation, and Google's
// redirect back here is exactly that — so a callback behind staffOnly would
// bounce every operator to the login page at the last step of the flow.
//
// So the callback authenticates on the state cookie instead, which is the
// standard OAuth defence and is enough: it is HttpOnly, random, short-lived, and
// was set only for a request that had already passed staffOnly. An attacker
// cannot plant one. The callback then renders a page carrying nothing but a
// link, and the operator's click on that link is a same-site navigation, which
// is what finally brings the session cookie back and puts the mapping screen
// behind staffOnly like every other page here.

const (
	// The OAuth state, tying a callback to the browser that started the flow.
	// Lax rather than Strict precisely because it must survive Google's redirect.
	googleStateCookie = "bykami_gstate"
	// Names the access token held in memory for this operator's mapping run.
	googleGrantCookie = "bykami_ggrant"

	// How long an operator has to finish mapping before the token is dropped.
	// Google's access tokens last an hour; this is shorter because the token is
	// only needed for the few minutes the screen is open, and an abandoned tab
	// should not leave one live.
	grantTTL = 15 * time.Minute
)

// grant is one operator's live consent, held in memory and never written down.
//
// In memory rather than in the database because that is what "we do not keep
// this" has to mean to be worth saying: a restart drops it, and the recovery is
// to press the button again. A tokens table would be a store of live credentials
// to protect, back up and eventually leak — for a value whose entire useful life
// is the next few minutes.
type grant struct {
	token   string
	account string
	expires time.Time
}

// grants is the whole store. Keyed by a random id that lives in a cookie, so a
// stolen key is useless without the browser and expires either way.
type grants struct {
	mu sync.Mutex
	m  map[string]grant
}

func newGrants() *grants { return &grants{m: map[string]grant{}} }

func (g *grants) put(key string, val grant) {
	g.mu.Lock()
	defer g.mu.Unlock()
	// Swept on write rather than by a goroutine: this map gains an entry a
	// handful of times a year, and a background timer for it would be a
	// lifecycle to own for no benefit.
	for k, v := range g.m {
		if time.Now().After(v.expires) {
			delete(g.m, k)
		}
	}
	g.m[key] = val
}

func (g *grants) get(key string) (grant, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	v, ok := g.m[key]
	if !ok || time.Now().After(v.expires) {
		delete(g.m, key)
		return grant{}, false
	}
	return v, true
}

func (g *grants) drop(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.m, key)
}

func randomKey() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("admin: entropy unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// redirectURI is the address Google sends the browser back to, and has to match
// the one registered on the OAuth client exactly.
//
// Built from the request rather than configured because the console is reached
// at one hostname and that hostname is in front of us. Scheme follows the same
// flag the session cookie's Secure attribute does, so a test over plain HTTP
// builds an http:// URI and production always builds https://.
func (c *Console) redirectURI(r *http.Request) string {
	scheme := "http"
	if c.secure {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/settings/google/callback"
}

// googleStart sends the operator to Google's consent screen.
func (c *Console) googleStart(w http.ResponseWriter, r *http.Request, op identity.User) {
	if !validCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}
	if c.connect == nil {
		c.backToSettings(w, r, "OAuth Google belum dikonfigurasi di server.")
		return
	}
	// Nothing to grant *to*. Connecting without a service account would walk an
	// operator through a consent screen and then have no address to share with.
	if c.calendar == nil {
		c.backToSettings(w, r, "Kunci service account Google belum dipasang, jadi belum ada alamat untuk dibagikan.")
		return
	}

	state := randomKey()
	http.SetCookie(w, &http.Cookie{
		Name:     googleStateCookie,
		Value:    state,
		Path:     "/settings/google",
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((10 * time.Minute).Seconds()),
	})

	c.log.Info("admin: google connect started", "operator", op.Phone)
	c.redirect(w, r, c.connect.AuthCodeURL(c.redirectURI(r), state))
}

// googleCallback is where Google sends the browser back. Deliberately not behind
// staffOnly — see the note at the top of this file.
func (c *Console) googleCallback(w http.ResponseWriter, r *http.Request) {
	// Cleared whatever happens: a state that has been presented once must not be
	// usable again.
	defer http.SetCookie(w, &http.Cookie{
		Name: googleStateCookie, Value: "", Path: "/settings/google",
		HttpOnly: true, Secure: c.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})

	if c.connect == nil || c.calendar == nil {
		c.googleFailed(w, r, "Google belum dikonfigurasi di server.")
		return
	}

	ck, err := r.Cookie(googleStateCookie)
	state := r.URL.Query().Get("state")
	if err != nil || ck.Value == "" || state == "" || ck.Value != state {
		// No detail. A mismatch here is either an expired attempt or a forgery,
		// and the page cannot tell which.
		c.googleFailed(w, r, "Sesi hubung Google sudah kedaluwarsa. Coba lagi dari Pengaturan.")
		return
	}
	if msg := r.URL.Query().Get("error"); msg != "" {
		// The operator pressed "Cancel", most likely.
		c.googleFailed(w, r, "Google menolak atau membatalkan izin.")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		c.googleFailed(w, r, "Google tidak mengirim kode izin.")
		return
	}

	token, err := c.connect.Exchange(r.Context(), code, c.redirectURI(r))
	if err != nil {
		c.log.Error("admin: google exchange", "err", err)
		c.googleFailed(w, r, "Gagal menukar kode izin dengan Google.")
		return
	}

	cals, err := c.connect.Calendars(r.Context(), token)
	if err != nil {
		c.log.Error("admin: google calendar list", "err", err)
		c.googleFailed(w, r, "Gagal membaca daftar kalender dari Google.")
		return
	}

	key := randomKey()
	c.grants.put(key, grant{token: token, account: primaryAddress(cals), expires: time.Now().Add(grantTTL)})
	http.SetCookie(w, &http.Cookie{
		Name:     googleGrantCookie,
		Value:    key,
		Path:     "/settings/google",
		HttpOnly: true,
		Secure:   c.secure,
		// Strict from here on: every remaining step is same-site.
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(grantTTL.Seconds()),
	})

	c.log.Info("admin: google connected", "account", primaryAddress(cals), "calendars", len(cals))
	// A page with a link and nothing else. The click is the same-site navigation
	// that brings the Strict session cookie back.
	c.render(w, r, http.StatusOK, "google-handoff.html", page{
		Title:  "Google terhubung",
		Notice: "Akun Google terhubung. Lanjutkan untuk memilih kalender tiap ruang.",
	})
}

// googleFailed renders the same bare handoff page carrying an error, because at
// this point in the flow there is still no session to render a real page with.
func (c *Console) googleFailed(w http.ResponseWriter, r *http.Request, msg string) {
	c.render(w, r, http.StatusOK, "google-handoff.html", page{
		Title: "Hubungkan Google",
		Error: msg,
	})
}

// primaryAddress is the signed-in account's own address. For a consumer Google
// account the primary calendar's id *is* the address, which is the only place
// this flow can learn it without asking for a second scope purely to print a
// name on a page.
func primaryAddress(cals []gcal.Calendar) string {
	for _, cal := range cals {
		if cal.Primary {
			return cal.ID
		}
	}
	return ""
}

// googleMap is the mapping screen: this account's calendars against the studio's
// resources.
func (c *Console) googleMap(w http.ResponseWriter, r *http.Request, op identity.User) {
	g, key, ok := c.liveGrant(r)
	if !ok {
		c.backToSettings(w, r, "Sesi hubung Google sudah berakhir. Tekan “Hubungkan Google” lagi.")
		return
	}

	cals, err := c.connect.Calendars(r.Context(), g.token)
	if err != nil {
		c.log.Error("admin: google calendar list", "operator", op.Phone, "err", err)
		c.grants.drop(key)
		c.backToSettings(w, r, "Gagal membaca daftar kalender dari Google.")
		return
	}

	resources, err := c.booking.Resources(r.Context())
	if err != nil {
		c.log.Error("admin: list resources", "err", err)
		c.backToSettings(w, r, "Gagal memuat daftar ruang.")
		return
	}

	p := page{
		Title:          "Hubungkan Google",
		Operator:       op.Phone,
		CSRF:           csrfToken(r),
		GoogleAccount:  g.account,
		ServiceAccount: c.calendar.ServiceAccount(),
	}
	if msg := r.URL.Query().Get("ok"); msg != "" {
		p.Notice = msg
	}
	if msg := r.URL.Query().Get("err"); msg != "" {
		p.Error = msg
	}
	for _, cal := range cals {
		p.GoogleCalendars = append(p.GoogleCalendars, googleCalendarRow{
			ID: cal.ID, Name: cal.Name, Primary: cal.Primary, Owned: cal.Owned,
		})
	}
	for _, res := range resources {
		p.Calendars = append(p.Calendars, calendarRow{
			Resource: res.ID, Name: res.Name, Calendar: res.GoogleCalendarID,
		})
	}

	c.render(w, r, http.StatusOK, "google-map.html", p)
}

// googleConnect grants the service account access to one calendar and points one
// resource at it.
//
// The order matters. The share is made first, because pointing a resource at a
// calendar the service account cannot read produces a booth that looks connected
// and syncs nothing — the exact failure this whole flow exists to remove. If the
// share fails, nothing is written.
func (c *Console) googleConnect(w http.ResponseWriter, r *http.Request, op identity.User) {
	if !validCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}
	g, _, ok := c.liveGrant(r)
	if !ok {
		c.backToSettings(w, r, "Sesi hubung Google sudah berakhir. Tekan “Hubungkan Google” lagi.")
		return
	}

	resource := r.FormValue("resource")
	calendarID := r.FormValue("calendar_id")
	if resource == "" || calendarID == "" {
		c.backToGoogle(w, r, "Pilih kalender untuk ruang itu dulu.")
		return
	}

	err := c.connect.Share(r.Context(), g.token, calendarID, c.calendar.ServiceAccount())
	switch {
	case err == nil:
	case errors.Is(err, gcal.ErrNotOwned):
		c.backToGoogle(w, r, "Akun ini tidak memiliki kalender itu, jadi tidak bisa membagikannya. "+
			"Masuk dengan akun pemilik kalender tersebut.")
		return
	default:
		c.log.Error("admin: google share", "operator", op.Phone,
			"resource", resource, "calendar", calendarID, "err", err)
		c.backToGoogle(w, r, "Gagal membagikan kalender ke service account.")
		return
	}

	if err := c.booking.SetCalendar(r.Context(), resource, calendarID); err != nil {
		c.log.Error("admin: set calendar", "operator", op.Phone, "resource", resource, "err", err)
		c.backToGoogle(w, r, "Kalender sudah dibagikan, tapi gagal disimpan. Coba lagi.")
		return
	}

	c.log.Info("admin: calendar connected through google", "operator", op.Phone,
		"resource", resource, "calendar", calendarID, "account", g.account)
	c.redirect(w, r, "/settings/google?ok="+urlQueryEscape(
		"Kalender terhubung untuk "+resource+"."))
}

// googleFinish drops the consent early, for an operator who has finished mapping
// and would rather not leave a live token sitting in memory for the rest of the
// quarter hour.
func (c *Console) googleFinish(w http.ResponseWriter, r *http.Request, op identity.User) {
	if !validCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}
	if ck, err := r.Cookie(googleGrantCookie); err == nil {
		c.grants.drop(ck.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: googleGrantCookie, Value: "", Path: "/settings/google",
		HttpOnly: true, Secure: c.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	c.log.Info("admin: google consent released", "operator", op.Phone)
	c.redirect(w, r, "/settings?ok="+urlQueryEscape("Selesai. Izin Google sudah dilepas."))
}

// liveGrant reads the consent this browser is holding, if it still has one.
func (c *Console) liveGrant(r *http.Request) (grant, string, bool) {
	if c.connect == nil || c.calendar == nil {
		return grant{}, "", false
	}
	ck, err := r.Cookie(googleGrantCookie)
	if err != nil || ck.Value == "" {
		return grant{}, "", false
	}
	g, ok := c.grants.get(ck.Value)
	if !ok {
		return grant{}, "", false
	}
	return g, ck.Value, true
}

func (c *Console) backToGoogle(w http.ResponseWriter, r *http.Request, msg string) {
	c.redirect(w, r, "/settings/google?err="+urlQueryEscape(msg))
}
