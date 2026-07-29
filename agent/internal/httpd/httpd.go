// Package httpd is the kiosk's local server: the UI and the API it talks to.
//
// It listens on 127.0.0.1 and embeds the built UI, so there is one artifact and
// one version. Version skew between the screen and the binary driving the
// camera is impossible by construction rather than by handshake.
//
// # No service worker, no offline story here
//
// The UI's server is a local process that is always present. The offline
// machinery a web app needs — precache, storage.persist, quota eviction —
// exists to survive a missing server, and there is no missing server to
// survive.
//
// # This has no authentication, and that is a decision
//
// Anything running on the booth PC can drive this API. Adding a token would not
// change that: the token would have to be readable by the UI, which runs on the
// same machine. The real boundary is the machine — Assigned Access, auto-login
// and a locked-down Windows session — and pretending otherwise in code would be
// security theatre with a maintenance cost.
//
// What is defended is the one thing a local-only bind does not cover: a page on
// the public internet reaching http://localhost in the operator's browser. See
// [Server.guard].
package httpd

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/catalog"
	"github.com/bhaktiyudha/bykami/agent/internal/compose"
	"github.com/bhaktiyudha/bykami/agent/internal/ingest"
	"github.com/bhaktiyudha/bykami/agent/internal/payment"
	"github.com/bhaktiyudha/bykami/agent/internal/photo"
	"github.com/bhaktiyudha/bykami/agent/internal/printer"
	"github.com/bhaktiyudha/bykami/agent/internal/session"
)

// The kiosk bundle, built by apps/kiosk into internal/httpd/dist before
// `go build` embeds it. One artifact and one version: a UI change is an agent
// change, so version skew between the screen and the binary driving the camera
// is impossible by construction rather than by handshake.
//
// `all:` so the committed .gitkeep is included — the built assets are ignored
// by git, and without a file here the embed pattern would fail on a clean
// checkout and the agent module would not compile without a Node toolchain.
//
//go:embed all:dist
var uiFS embed.FS

// ConsentVersion is stored with every number captured.
//
// Bump it whenever the wording changes. "They agreed to something at some
// point" is not a record, and the only way to answer "to what?" later is to
// have written down which text they saw.
const ConsentVersion = "kiosk-consent-2026-07"

// maxFrame bounds an uploaded webcam frame. A 1080p JPEG is a few hundred
// kilobytes; this is generous enough for a high-resolution browser capture and
// small enough that a runaway client cannot fill the booth's disk.
const maxFrame = 16 << 20

// Source is where frames come from.
type Source string

const (
	// SourceHotFolder is production: a real camera tethered by vendor software.
	SourceHotFolder Source = "hotfolder"

	// SourceWebcam is development. The browser owns the camera and POSTs
	// frames, which is what makes the whole flow runnable on a laptop before
	// the camera, the shutter relay, the printer and the booth PC exist.
	//
	// Not a product tier. 1080p is about 180 dpi at 4R — visibly soft — and
	// shipping it to customers would make the one thing they pay for worse than
	// what the studio delivers today.
	SourceWebcam Source = "webcam"
)

type Deps struct {
	Sessions *session.Store
	Photos   *photo.Store
	Payments *payment.Store
	Printer  *printer.Queue
	Ingest   *ingest.Watcher

	// Templates is the live set, not a snapshot: the frame sync worker
	// replaces it while the booth is serving.
	Templates *compose.Set
	Packages  []catalog.Package

	// Root is where session directories and composed sheets live.
	Root string

	// Source decides which capture flow the UI shows.
	Source Source

	// OutletID is stamped on every session. One booth today; the ledger is
	// pooled across outlets by design, so this is not a placeholder.
	OutletID string

	// Simulated is non-nil only when the simulated payment provider is
	// selected, and it is what exposes the "the customer paid" button. Nil in
	// any configuration that could take real money.
	Simulated *payment.Simulated

	// PublicHost is a hostname the booth will answer to besides localhost.
	//
	// Empty in a real booth, and that is the whole security posture: the kiosk
	// is a screen wired to the PC beside it, so nothing about it needs a public
	// address. This exists for a test deployment behind Cloudflare Tunnel,
	// where the flow has to be reachable from a phone over HTTPS because
	// getUserMedia refuses to run on an insecure origin.
	//
	// Setting it without AccessToken is refused at startup. An unauthenticated
	// public endpoint here accepts 16 MB image uploads and writes them to disk.
	PublicHost string

	// AccessToken gates PublicHost. Not a login: one shared secret in the URL,
	// which is the point — the operator console's real auth is unrelated and,
	// for a UI a customer walks up to, a password prompt is the wrong shape.
	AccessToken string

	Log *slog.Logger
}

type Server struct {
	Deps
	mux *http.ServeMux
}

func New(d Deps) (*Server, error) {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	// Refused rather than warned about. Every route here is unauthenticated by
	// design — see the package comment — which is correct while the only client
	// is a browser on the same machine and indefensible the moment the hostname
	// is public: /api/capture accepts 16 MB uploads and writes them to disk.
	if d.PublicHost != "" && d.AccessToken == "" {
		return nil, errors.New("httpd: a public host needs an access token; without one every route is open to the internet")
	}
	s := &Server{Deps: d, mux: http.NewServeMux()}
	s.routes()
	return s, nil
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/state", s.state)
	s.mux.HandleFunc("POST /api/session", s.startSession)
	s.mux.HandleFunc("POST /api/session/cancel", s.cancelSession)
	s.mux.HandleFunc("POST /api/session/close", s.closeSession)
	s.mux.HandleFunc("GET /api/payment", s.pollPayment)
	s.mux.HandleFunc("POST /api/payment/simulate", s.simulatePayment)
	s.mux.HandleFunc("POST /api/capture", s.capture)
	s.mux.HandleFunc("GET /api/photos", s.listPhotos)
	s.mux.HandleFunc("GET /api/photos/{id}/file", s.servePhoto)
	s.mux.HandleFunc("GET /api/templates/{id}/{kind}", s.templateAsset)
	s.mux.HandleFunc("POST /api/print", s.print)
	s.mux.HandleFunc("GET /api/print/{id}", s.printStatus)
	s.mux.HandleFunc("POST /api/delivery", s.delivery)

	s.mux.Handle("GET /", s.ui())
}

// Handler returns the server with its guard applied.
func (s *Server) Handler() http.Handler { return s.guard(s.mux) }

// guard rejects requests that did not come from the booth's own browser.
//
// Binding to 127.0.0.1 stops the network reaching this, but it does not stop a
// page the operator opened from reaching it: any website can POST to
// http://localhost, and a DNS rebinding attack turns an attacker's hostname
// into 127.0.0.1 so that even same-origin checks pass. Two cheap defences,
// both of which a real kiosk browser satisfies and a hostile page does not:
//
//   - the Host header must name localhost, which defeats rebinding because the
//     browser sends the attacker's hostname, not the address it resolved to;
//   - a cross-site Origin is refused outright.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}

		// Whether this request arrived over the public hostname rather than the
		// loopback one. The two are held to different rules: loopback is the
		// booth's own browser and is trusted, the public hostname is the
		// internet and has to prove it holds the token.
		public := s.PublicHost != "" && host == s.PublicHost

		switch {
		case host == "localhost", host == "127.0.0.1", host == "::1", host == "[::1]":
		case public:
		default:
			http.Error(w, "this booth answers only to localhost", http.StatusForbidden)
			return
		}

		if public && !s.admit(w, r) {
			http.Error(w, "this test booth needs its access token", http.StatusUnauthorized)
			return
		}

		if origin := r.Header.Get("Origin"); origin != "" && !s.allowedOrigin(origin) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}

		// Nothing here is cacheable: it is all the live state of one session.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) allowedOrigin(origin string) bool {
	rest, ok := strings.CutPrefix(origin, "http://")
	if !ok {
		// The test deployment is served over TLS, so its own same-origin
		// requests arrive with an https Origin and would otherwise be refused
		// by the check that exists to keep other sites out.
		if rest, ok = strings.CutPrefix(origin, "https://"); !ok {
			return false
		}
		host := rest
		if h, _, err := net.SplitHostPort(rest); err == nil {
			host = h
		}
		return s.PublicHost != "" && host == s.PublicHost
	}

	host := rest
	if h, _, err := net.SplitHostPort(rest); err == nil {
		host = h
	}
	return host == "localhost" || host == "127.0.0.1" || host == "[::1]"
}

// accessCookie holds the token once it has been presented in a URL, so the
// customer's next tap does not need it and the token stops appearing in the
// address bar.
const accessCookie = "bykami_booth_access"

// admit reports whether a request over the public hostname carries the token.
//
// Accepted from ?t= once, then moved into a cookie. A query parameter is how
// somebody opens the link on a phone; a cookie is how the fifteen requests that
// follow do not each need one, and keeps the secret out of the Referer header
// on any link the page might later carry.
func (s *Server) admit(w http.ResponseWriter, r *http.Request) bool {
	if s.AccessToken == "" {
		// Unreachable: New refuses this combination. Belt and braces, because
		// the failure mode is an open photo-upload endpoint on the internet.
		return false
	}

	if c, err := r.Cookie(accessCookie); err == nil && tokenEqual(c.Value, s.AccessToken) {
		return true
	}

	if t := r.URL.Query().Get("t"); tokenEqual(t, s.AccessToken) {
		http.SetCookie(w, &http.Cookie{
			Name:     accessCookie,
			Value:    s.AccessToken,
			Path:     "/",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int((12 * time.Hour).Seconds()),
		})
		return true
	}
	return false
}

func tokenEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// ui serves the embedded bundle, falling back to index.html so that the UI can
// own its own routes without the server knowing what they are.
func (s *Server) ui() http.Handler {
	sub, err := fs.Sub(uiFS, "dist")
	if err != nil {
		// Only reachable if the embed directive and this path disagree, which
		// is a build-time mistake rather than a runtime condition.
		panic("httpd: embedded UI is missing: " + err.Error())
	}

	// A binary built without running the UI build first embeds only the marker
	// file. Saying so beats serving a 404 that looks like a routing bug, and it
	// is the symptom of the one build-ordering mistake this design can make.
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		s.Log.Error("no kiosk UI was embedded; build apps/kiosk before `go build`")
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintln(w, "kiosk UI was not built into this binary.")
			fmt.Fprintln(w, "run: pnpm --filter @bykami/kiosk build && go build ./cmd/bykami-agent")
		})
	}

	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(sub, strings.TrimPrefix(r.URL.Path, "/")); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

// ---- API ----

type stateResponse struct {
	Source    Source            `json:"source"`
	Packages  []catalog.Package `json:"packages"`
	Templates []templateView    `json:"templates"`
	Session   *sessionView      `json:"session"`
	Payment   *paymentView      `json:"payment"`
	Media     mediaView         `json:"media"`
	Consent   consentView       `json:"consent"`
	Flags     map[string]bool   `json:"flags"`
}

type templateView struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Layout printer.Layout `json:"layout"`
	Cells  []compose.Cell `json:"cells"`
	Sheet  [2]int         `json:"sheet"`

	// URLs for the frame artwork, empty when the template has none. Cells and
	// Sheet are in pixels at 300 dpi, so the screen can lay the preview out as
	// percentages of the template's own geometry and cannot drift away from
	// what compose.Sheet will draw.
	Overlay    string `json:"overlay"`
	Background string `json:"background"`
}

type sessionView struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	PackageID   string `json:"package_id"`
	PackageName string `json:"package_name"`
	PriceIDR    int64  `json:"price_idr"`
	TemplateID  string `json:"template_id"`
	PrintCopies int    `json:"print_copies"`
	// PrintsDone is how many of PrintCopies have been claimed. Served rather
	// than counted in the browser so that a refresh mid-session cannot hand a
	// customer their allowance a second time.
	PrintsDone int  `json:"prints_done"`
	TakeLimit  int  `json:"take_limit"`
	Takes      int  `json:"takes"`
	PhoneGiven bool `json:"phone_given"`
}

type paymentView struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	AmountIDR int64  `json:"amount_idr"`
	QRPayload string `json:"qr_payload"`
	// ExpiresIn is seconds, because the screen shows a countdown and an
	// unexplained expiry looks like a broken machine.
	ExpiresIn int `json:"expires_in"`
}

type mediaView struct {
	SheetsRemaining int  `json:"sheets_remaining"`
	Low             bool `json:"low"`
}

type consentView struct {
	Version string `json:"version"`
	// RetentionDays is shown to the customer, so it comes from the same place
	// the purge uses rather than being typed into the UI separately.
	RetentionDays int `json:"retention_days"`
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	packages := s.Packages
	if packages == nil {
		packages = []catalog.Package{}
	}

	templates := s.Templates.All()
	views := make([]templateView, 0, len(templates))
	for _, t := range templates {
		w, h, err := compose.SheetSize(t.Layout)
		if err != nil {
			continue
		}
		v := templateView{ID: t.ID, Name: t.Name, Layout: t.Layout, Cells: t.Cells, Sheet: [2]int{w, h}}
		if t.Overlay != "" {
			v.Overlay = "/api/templates/" + t.ID + "/overlay"
		}
		if t.Background != "" {
			v.Background = "/api/templates/" + t.ID + "/background"
		}
		views = append(views, v)
	}

	resp := stateResponse{
		Source:    s.Source,
		Packages:  packages,
		Templates: views,
		Consent:   consentView{Version: ConsentVersion, RetentionDays: 30},
		Flags: map[string]bool{
			"payments_enabled":   s.Payments.Enabled(),
			"payments_simulated": s.Simulated != nil,
		},
	}

	remaining, err := s.Printer.Remaining(ctx)
	if err != nil {
		s.fail(w, "media", err)
		return
	}
	resp.Media = mediaView{SheetsRemaining: remaining, Low: remaining <= 50}

	sess, ok, err := s.Sessions.Current(ctx)
	if err != nil {
		s.fail(w, "current session", err)
		return
	}
	if ok {
		view, err := s.sessionView(ctx, sess)
		if err != nil {
			s.fail(w, "session", err)
			return
		}
		resp.Session = &view

		if p, err := s.Payments.Latest(ctx, sess.ID); err == nil {
			pv := paymentView(paymentViewOf(p))
			resp.Payment = &pv
		} else if !errors.Is(err, payment.ErrNotFound) {
			s.fail(w, "payment", err)
			return
		}
	}

	s.write(w, http.StatusOK, resp)
}

func (s *Server) sessionView(ctx context.Context, sess session.Session) (sessionView, error) {
	takes, err := s.Sessions.Takes(ctx, sess.ID)
	if err != nil {
		return sessionView{}, err
	}
	printed, err := s.Printer.CopiesForSession(ctx, sess.ID)
	if err != nil {
		return sessionView{}, err
	}
	return sessionView{
		ID:          sess.ID,
		State:       string(sess.State),
		PackageID:   sess.Package.ID,
		PackageName: sess.Package.Name,
		PriceIDR:    sess.Package.PriceIDR,
		TemplateID:  sess.Package.TemplateID,
		PrintCopies: sess.Package.PrintCopies,
		PrintsDone:  printed,
		TakeLimit:   sess.TakeLimit,
		Takes:       takes,
		PhoneGiven:  sess.Phone != "",
	}, nil
}

func paymentViewOf(p payment.Payment) paymentView {
	expiresIn := int(time.Until(p.ExpiresAt).Seconds())
	if expiresIn < 0 {
		expiresIn = 0
	}
	return paymentView{
		ID:        p.ID,
		State:     string(p.State),
		AmountIDR: p.AmountIDR,
		QRPayload: p.QRPayload,
		ExpiresIn: expiresIn,
	}
}

// startSession is the first tap: a package is chosen and a QR code is minted.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PackageID string `json:"package_id"`
	}
	if !s.decode(w, r, &req) {
		return
	}

	pkg, err := catalog.Get(req.PackageID)
	if err != nil {
		s.reject(w, http.StatusBadRequest, "Paket tidak dikenal.")
		return
	}

	ctx := r.Context()
	sess, err := s.Sessions.Start(ctx, s.OutletID, session.Package{
		ID: pkg.ID, Name: pkg.Name, PriceIDR: pkg.PriceIDR,
		TemplateID: pkg.TemplateID, PrintCopies: pkg.PrintCopies, TakeLimit: pkg.TakeLimit,
	})
	switch {
	case errors.Is(err, session.ErrAlreadyOpen):
		s.reject(w, http.StatusConflict, "Booth sedang dipakai.")
		return
	case err != nil:
		s.fail(w, "start session", err)
		return
	}

	if !s.Payments.Enabled() {
		// No provider configured. The session is discarded rather than left
		// stuck: a booth that cannot take money is a screen that says "pay at
		// the counter", not one holding a dead session.
		if err := s.Sessions.Abandon(ctx, sess.ID); err != nil {
			s.Log.Error("httpd: abandon unpayable session", "err", err)
		}
		s.reject(w, http.StatusServiceUnavailable, "Pembayaran belum aktif di booth ini.")
		return
	}

	p, err := s.Payments.Create(ctx, sess.ID, pkg.PriceIDR)
	if err != nil {
		if aerr := s.Sessions.Abandon(ctx, sess.ID); aerr != nil {
			s.Log.Error("httpd: abandon after failed charge", "err", aerr)
		}
		s.fail(w, "create payment", err)
		return
	}

	view, err := s.sessionView(ctx, sess)
	if err != nil {
		s.fail(w, "session", err)
		return
	}
	pv := paymentViewOf(p)
	s.write(w, http.StatusCreated, map[string]any{"session": view, "payment": pv})
}

// pollPayment is how settlement arrives. The booth is at localhost with no
// inbound path, so a gateway webhook could never reach it — and a lost callback
// is a slow answer here rather than a stuck screen.
func (s *Server) pollPayment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	sess, ok, err := s.Sessions.Current(ctx)
	if err != nil {
		s.fail(w, "current session", err)
		return
	}
	if !ok {
		s.reject(w, http.StatusNotFound, "Tidak ada sesi.")
		return
	}

	p, err := s.Payments.Latest(ctx, sess.ID)
	if errors.Is(err, payment.ErrNotFound) {
		s.reject(w, http.StatusNotFound, "Belum ada pembayaran.")
		return
	}
	if err != nil {
		s.fail(w, "latest payment", err)
		return
	}

	refreshed, err := s.Payments.Refresh(ctx, p.ID)
	if err != nil {
		// The customer is standing at the screen. A failed poll is not a failed
		// payment: report what is known and let the UI ask again.
		s.Log.Warn("httpd: payment poll failed", "err", err)
		refreshed = p
	}

	if refreshed.State == payment.Settled && sess.State == session.AwaitingPayment {
		if sess, err = s.Sessions.MarkPaid(ctx, sess.ID); err != nil {
			s.fail(w, "mark paid", err)
			return
		}
	}

	view, err := s.sessionView(ctx, sess)
	if err != nil {
		s.fail(w, "session", err)
		return
	}
	s.write(w, http.StatusOK, map[string]any{"session": view, "payment": paymentViewOf(refreshed)})
}

// simulatePayment is the "the customer paid" button, and it exists only while
// the simulated provider is selected. With a real provider this route is a 404,
// so there is no path from a running booth to a free session.
func (s *Server) simulatePayment(w http.ResponseWriter, r *http.Request) {
	if s.Simulated == nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()

	sess, ok, err := s.Sessions.Current(ctx)
	if err != nil || !ok {
		s.reject(w, http.StatusNotFound, "Tidak ada sesi.")
		return
	}
	p, err := s.Payments.Latest(ctx, sess.ID)
	if err != nil {
		s.reject(w, http.StatusNotFound, "Belum ada pembayaran.")
		return
	}
	if err := s.Simulated.Settle(p.ExternalID); err != nil {
		s.fail(w, "simulate settlement", err)
		return
	}
	s.write(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) cancelSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess, ok, err := s.Sessions.Current(ctx)
	if err != nil {
		s.fail(w, "current session", err)
		return
	}
	if !ok {
		s.write(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if sess.State != session.AwaitingPayment {
		// Money moved. A paid session is closed, never deleted.
		s.reject(w, http.StatusConflict, "Sesi sudah dibayar; gunakan Selesai.")
		return
	}
	if err := s.Sessions.Abandon(ctx, sess.ID); err != nil && !errors.Is(err, session.ErrNotFound) {
		s.fail(w, "abandon", err)
		return
	}
	s.write(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) closeSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess, ok, err := s.Sessions.Current(ctx)
	if err != nil {
		s.fail(w, "current session", err)
		return
	}
	if ok {
		if err := s.Sessions.Close(ctx, sess.ID); err != nil {
			s.fail(w, "close", err)
			return
		}
	}
	s.write(w, http.StatusOK, map[string]any{"ok": true})
}

// capture releases the shutter, or accepts a frame the browser took.
//
// Two paths because the two sources are genuinely different. With a tethered
// camera the agent asks for a frame and the file arrives later through the hot
// folder; with a webcam the browser already has the pixels and posts them.
func (s *Server) capture(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	sess, err := s.Sessions.MayFire(ctx)
	switch {
	case errors.Is(err, session.ErrNoSession):
		s.reject(w, http.StatusConflict, "Tidak ada sesi.")
		return
	case errors.Is(err, session.ErrNotPaid):
		s.reject(w, http.StatusPaymentRequired, "Belum dibayar.")
		return
	case errors.Is(err, session.ErrTakeLimit):
		s.reject(w, http.StatusConflict, fmt.Sprintf("Sudah %d take, batasnya %d.", sess.TakeLimit, sess.TakeLimit))
		return
	case err != nil:
		s.fail(w, "may fire", err)
		return
	}

	if s.Source != SourceWebcam {
		// The tethered path. How a tap reaches a Canon's shutter is the last
		// open question in design/kiosk.md — a USB relay into the RS-60E3 jack
		// is the recommendation, and until that hardware exists the countdown
		// runs and the frame is fired by hand.
		s.write(w, http.StatusAccepted, map[string]any{
			"awaiting_file": true,
			"note":          "shutter release is not wired up yet; fire the camera",
		})
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxFrame)
	defer body.Close()

	tmp, err := os.CreateTemp("", "bykami-frame-*.jpg")
	if err != nil {
		s.fail(w, "temp frame", err)
		return
	}
	defer os.Remove(tmp.Name())

	if _, err := io.Copy(tmp, body); err != nil {
		tmp.Close()
		s.reject(w, http.StatusRequestEntityTooLarge, "Frame terlalu besar.")
		return
	}
	if err := tmp.Close(); err != nil {
		s.fail(w, "temp frame", err)
		return
	}

	// Ingested through the same path as a tethered frame — hashed, attributed,
	// filed and recorded — so the webcam is a different source, not a different
	// pipeline with its own bugs.
	p, err := s.Ingest.Ingest(ctx, tmp.Name(), time.Now(), photo.Webcam)
	if err != nil {
		s.fail(w, "ingest frame", err)
		return
	}
	if p.ID == "" {
		// Byte-identical to a frame already held, so the content-addressed store
		// discarded it. Reported rather than answered with an empty success: the
		// UI would otherwise show a take that does not exist, and the take
		// counter would disagree with the strip.
		s.reject(w, http.StatusConflict, "Frame ini identik dengan yang sebelumnya. Coba lagi.")
		return
	}
	// The same number the review screen shows, so a customer who is about to
	// find out their frame prints soft finds out now rather than three shots
	// later.
	cellW, cellH := s.cellSize(sess.Package.TemplateID)
	view := photoViewOf(p)
	view.PrintDPI = printDPI(p, cellW, cellH)

	s.write(w, http.StatusCreated, map[string]any{"photo": view})
}

type photoView struct {
	ID         string `json:"id"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	CapturedAt int64  `json:"captured_at"`
	// PrintDPI is what this frame would print at in the current layout. Shown
	// in the UI because the resolution argument in design/kiosk.md is the
	// reason a webcam is a development tool and not a product tier.
	PrintDPI int `json:"print_dpi"`
}

func photoViewOf(p photo.Photo) photoView {
	return photoView{
		ID: p.ID, Width: p.Width, Height: p.Height,
		CapturedAt: p.CapturedAt.Unix(),
	}
}

func (s *Server) listPhotos(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	sess, ok, err := s.Sessions.Current(ctx)
	if err != nil {
		s.fail(w, "current session", err)
		return
	}
	if !ok {
		s.write(w, http.StatusOK, map[string]any{"photos": []photoView{}})
		return
	}

	photos, err := s.Photos.BySession(ctx, sess.ID)
	if err != nil {
		s.fail(w, "photos", err)
		return
	}

	// The cell the chosen template would put this frame in decides the print
	// resolution, so the number the UI shows is the real one.
	//
	// The template being looked at, not the one the package opened on: the
	// review screen lets the customer switch, and a 4R cell is 1200x1800 where a
	// strip cell is 540x360. Reading the package's template here reported the
	// resolution of a layout the customer had already switched away from — which
	// fails in the direction that matters, since it can hide a genuine
	// below-300-dpi warning.
	templateID := r.URL.Query().Get("template")
	if templateID == "" {
		templateID = sess.Package.TemplateID
	}
	cellW, cellH := s.cellSize(templateID)

	out := make([]photoView, 0, len(photos))
	for _, p := range photos {
		v := photoViewOf(p)
		v.PrintDPI = printDPI(p, cellW, cellH)
		out = append(out, v)
	}
	s.write(w, http.StatusOK, map[string]any{"photos": out})
}

func (s *Server) cellSize(templateID string) (int, int) {
	if t, ok := s.Templates.ByID(templateID); ok && len(t.Cells) > 0 {
		return t.Cells[0].W, t.Cells[0].H
	}
	return 0, 0
}

// printDPI is the effective resolution of a frame once it fills its cell.
//
// The cell is drawn at 300 dpi, so a frame that has fewer pixels than the cell
// prints proportionally softer. This is the arithmetic behind the webcam row of
// the table in design/kiosk.md, computed rather than asserted.
func printDPI(p photo.Photo, cellW, cellH int) int {
	if cellW <= 0 || cellH <= 0 || p.Width <= 0 || p.Height <= 0 {
		return 0
	}
	// Fill-and-crop, so the limiting factor is whichever axis has to stretch
	// furthest.
	scale := max(float64(cellW)/float64(p.Width), float64(cellH)/float64(p.Height))
	if scale <= 1 {
		return compose.DPI
	}
	return int(float64(compose.DPI) / scale)
}

// servePhoto sends a frame's pixels to the screen for review.
func (s *Server) servePhoto(w http.ResponseWriter, r *http.Request) {
	p, err := s.Photos.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, photo.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, "photo", err)
		return
	}
	if !p.PurgedAt.IsZero() {
		http.Error(w, "this frame has been deleted", http.StatusGone)
		return
	}

	// The derivative when there is one: the review screen does not need 24
	// megapixels, and decoding them costs a visible pause on a booth PC.
	rel := p.Path
	if p.DerivedPath != "" {
		rel = p.DerivedPath
	}
	http.ServeFile(w, r, filepath.Join(s.Root, filepath.FromSlash(rel)))
}

// templateAsset serves a template's frame artwork to the review screen.
//
// The screen composites the same three layers compose.Sheet draws — background,
// photos, overlay — so what the customer approves is what the printer produces.
// Rendering the real sheet server-side instead would mean a full 300 dpi
// compose, which takes seconds, on every template a customer taps.
func (s *Server) templateAsset(w http.ResponseWriter, r *http.Request) {
	tpl, ok := s.template(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	f, ctype, err := tpl.Asset(r.PathValue("kind"))
	if errors.Is(err, compose.ErrNoAsset) {
		// A template with no artwork is the normal case — pas foto is a plain
		// photo — so this is a 404 rather than a failure.
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, "template asset", err)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", ctype)
	// Long enough that tapping between templates does not re-fetch and flicker,
	// short enough that an outlet redrawing its own artwork sees the change
	// without clearing the kiosk browser's cache. Not immutable: the URL carries
	// the template id, which is stable, so an immutable response would pin the
	// old artwork for as long as the cache survives.
	w.Header().Set("Cache-Control", "max-age=300")
	if _, err := io.Copy(w, f); err != nil {
		s.Log.Error("httpd: write template asset", "template", tpl.ID, "err", err)
	}
}

// print composes the chosen frames and queues a sheet.
func (s *Server) print(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TemplateID string   `json:"template_id"`
		PhotoIDs   []string `json:"photo_ids"`
		Copies     int      `json:"copies"`
	}
	if !s.decode(w, r, &req) {
		return
	}

	ctx := r.Context()
	sess, ok, err := s.Sessions.Current(ctx)
	if err != nil {
		s.fail(w, "current session", err)
		return
	}
	if !ok || sess.State != session.Open {
		s.reject(w, http.StatusConflict, "Tidak ada sesi yang siap cetak.")
		return
	}

	tpl, ok := s.template(req.TemplateID)
	if !ok {
		s.reject(w, http.StatusBadRequest, "Template tidak dikenal.")
		return
	}
	if len(req.PhotoIDs) != len(tpl.Cells) {
		s.reject(w, http.StatusBadRequest,
			fmt.Sprintf("Template ini butuh %d foto, dipilih %d.", len(tpl.Cells), len(req.PhotoIDs)))
		return
	}

	copies := req.Copies
	if copies <= 0 {
		copies = sess.Package.PrintCopies
	}

	// The backstop kiosk.md keeps: a stray file in the folder must never become
	// a free print, and neither must a customer asking for more copies than the
	// package includes.
	//
	// Checked against everything this session has already claimed, not against
	// this request alone. Reprint hands the copies out one at a time, so a
	// per-request check would let a customer with a two-print package tap "cetak
	// lagi" indefinitely — each request passing on its own while the total ran
	// away.
	printed, err := s.Printer.CopiesForSession(ctx, sess.ID)
	if err != nil {
		s.fail(w, "copies for session", err)
		return
	}
	if printed+copies > sess.Package.PrintCopies {
		s.reject(w, http.StatusForbidden,
			fmt.Sprintf("Paket ini termasuk %d cetak, sudah %d.", sess.Package.PrintCopies, printed))
		return
	}

	// Composed from the ORIGINALS. The delivered derivative exists to be small
	// enough to send to a phone; printing from it gives back exactly what
	// full-resolution capture bought.
	paths := make([]string, 0, len(req.PhotoIDs))
	for _, id := range req.PhotoIDs {
		p, err := s.Photos.Get(ctx, id)
		if err != nil {
			s.reject(w, http.StatusBadRequest, "Foto tidak ditemukan.")
			return
		}
		if p.SessionID != sess.ID {
			// Frames from another customer's session are not printable here,
			// whatever id the client sends.
			s.reject(w, http.StatusForbidden, "Foto bukan milik sesi ini.")
			return
		}
		if !p.PurgedAt.IsZero() {
			s.reject(w, http.StatusGone, "Foto sudah terhapus.")
			return
		}
		paths = append(paths, filepath.Join(s.Root, filepath.FromSlash(p.Path)))
	}

	sheet := filepath.Join(s.Root, "sheets", sess.ID, fmt.Sprintf("%s-%d.jpg", tpl.ID, time.Now().UnixNano()))
	if _, err := tpl.Sheet(paths, sheet); err != nil {
		s.fail(w, "compose sheet", err)
		return
	}

	rel, err := filepath.Rel(s.Root, sheet)
	if err != nil {
		s.fail(w, "sheet path", err)
		return
	}

	job, err := s.Printer.Submit(ctx, sess.ID, tpl.Layout, copies, filepath.ToSlash(rel))
	if err != nil {
		// The sheet was composed before the queue was asked, so a refusal here
		// would otherwise leave a file of the customer's faces on disk that no
		// job references and nothing will ever print.
		if rmErr := os.Remove(sheet); rmErr != nil {
			s.Log.Error("httpd: remove sheet for a refused job", "path", sheet, "err", rmErr)
		}
		if errors.Is(err, printer.ErrNoMedia) {
			s.reject(w, http.StatusConflict, "Kertas printer tidak cukup. Panggil petugas.")
			return
		}
		s.fail(w, "submit print", err)
		return
	}

	s.write(w, http.StatusAccepted, map[string]any{"job": jobViewOf(job)})
}

func (s *Server) printStatus(w http.ResponseWriter, r *http.Request) {
	job, err := s.Printer.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, printer.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, "print status", err)
		return
	}
	s.write(w, http.StatusOK, map[string]any{"job": jobViewOf(job)})
}

type jobView struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Layout string `json:"layout"`
	Copies int    `json:"copies"`
	Error  string `json:"error,omitempty"`
}

func jobViewOf(j printer.Job) jobView {
	return jobView{ID: j.ID, State: string(j.State), Layout: string(j.Layout), Copies: j.Copies, Error: j.Error}
}

// delivery captures the phone number for the digital files, with consent.
func (s *Server) delivery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone     string `json:"phone"`
		Consent   bool   `json:"consent"`
		Marketing bool   `json:"marketing"`
	}
	if !s.decode(w, r, &req) {
		return
	}

	// The required consent is required. Refusing here rather than trusting the
	// UI's disabled button is the difference between a rule and a decoration.
	if !req.Consent {
		s.reject(w, http.StatusBadRequest, "Persetujuan wajib dicentang.")
		return
	}

	e164, err := normalizePhone(req.Phone)
	if err != nil {
		s.reject(w, http.StatusBadRequest, "Nomor tidak valid.")
		return
	}

	ctx := r.Context()
	sess, ok, err := s.Sessions.Current(ctx)
	if err != nil {
		s.fail(w, "current session", err)
		return
	}
	if !ok || sess.State != session.Open {
		s.reject(w, http.StatusConflict, "Tidak ada sesi.")
		return
	}

	if err := s.Sessions.RecordDelivery(ctx, sess.ID, e164, ConsentVersion, req.Marketing); err != nil {
		s.fail(w, "record delivery", err)
		return
	}
	s.Log.Info("kiosk: delivery number captured",
		"session", sess.ID, "consent_version", ConsentVersion, "marketing", req.Marketing)

	s.write(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- plumbing ----

func (s *Server) template(id string) (compose.Template, bool) {
	return s.Templates.ByID(id)
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		s.reject(w, http.StatusBadRequest, "Permintaan tidak valid.")
		return false
	}
	return true
}

func (s *Server) write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.Log.Error("httpd: encode", "err", err)
	}
}

// reject sends a message meant for a customer's eyes. Indonesian, because it
// appears on the booth screen.
func (s *Server) reject(w http.ResponseWriter, status int, message string) {
	s.write(w, status, map[string]string{"error": message})
}

// fail logs the real cause and tells the screen something useful. A stack of
// Go error text in front of a customer helps nobody.
func (s *Server) fail(w http.ResponseWriter, op string, err error) {
	s.Log.Error("httpd: "+op, "err", err)
	s.write(w, http.StatusInternalServerError, map[string]string{"error": "Terjadi kesalahan. Panggil petugas."})
}
