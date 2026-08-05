package httpd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/catalog"
	"github.com/bhaktiyudha/bykami/agent/internal/clip"
	"github.com/bhaktiyudha/bykami/agent/internal/compose"
	"github.com/bhaktiyudha/bykami/agent/internal/httpd"
	"github.com/bhaktiyudha/bykami/agent/internal/ingest"
	"github.com/bhaktiyudha/bykami/agent/internal/payment"
	"github.com/bhaktiyudha/bykami/agent/internal/photo"
	"github.com/bhaktiyudha/bykami/agent/internal/printer"
	"github.com/bhaktiyudha/bykami/agent/internal/purge"
	"github.com/bhaktiyudha/bykami/agent/internal/session"
	"github.com/bhaktiyudha/bykami/agent/internal/store"
)

type fixture struct {
	srv       http.Handler
	simulated *payment.Simulated
	sessions  *session.Store
	photos    *photo.Store
	clips     *clip.Store
	printer   *printer.Queue
	root      string
}

func setup(t *testing.T) *fixture { return setupWith(t, nil) }

// setupWith builds the booth with tweak applied to its dependencies, for the
// tests that need a configuration a real booth never has.
func setupWith(t *testing.T, tweak func(*httpd.Deps)) *fixture {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	log := slog.New(slog.DiscardHandler)
	root := t.TempDir()

	photos := photo.New(db)
	clips := clip.New(db)
	sessions := session.New(db)
	sim := payment.NewSimulated(log, 0)
	payments := payment.New(db, sim)
	prints := printer.New(db, printer.NewSimulated(log, 10000), log)
	watcher := ingest.New(t.TempDir(), root, photos, sessions, log)

	templates, err := compose.Builtin()
	if err != nil {
		t.Fatalf("templates: %v", err)
	}
	packages, err := catalog.All()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}

	deps := httpd.Deps{
		Sessions: sessions, Photos: photos, Payments: payments, Printer: prints,
		Clips:  clips,
		Ingest: watcher, Templates: compose.NewSet(templates), Packages: packages,
		Root: root, Source: httpd.SourceWebcam, OutletID: "jajag",
		Simulated: sim, Log: log,
	}
	if tweak != nil {
		tweak(&deps)
	}

	srv, err := httpd.New(deps)
	if err != nil {
		t.Fatalf("httpd: %v", err)
	}

	if err := prints.LoadRoll(t.Context(), printer.RollSheets, "roll 1"); err != nil {
		t.Fatalf("load roll: %v", err)
	}

	return &fixture{
		srv: srv.Handler(), simulated: sim, sessions: sessions, photos: photos,
		clips: clips, printer: prints, root: root,
	}
}

func (f *fixture) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var r *http.Request
	switch b := body.(type) {
	case nil:
		r = httptest.NewRequest(method, path, nil)
	case []byte:
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "image/jpeg")
	default:
		enc, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		r = httptest.NewRequest(method, path, bytes.NewReader(enc))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Host = "localhost:8899"

	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return out
}

// frameCounter makes every generated frame different. Two byte-identical
// captures are a duplicate to the content-addressed store, which is correct and
// is tested separately — it just makes a poor stand-in for a customer moving
// between shots.
var frameCounter atomic.Uint32

func frameBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	shade := uint8(frameCounter.Add(1))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: shade, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

type startResponse struct {
	Session struct {
		ID          string `json:"id"`
		State       string `json:"state"`
		PriceIDR    int64  `json:"price_idr"`
		TakeLimit   int    `json:"take_limit"`
		TemplateID  string `json:"template_id"`
		PrintCopies int    `json:"print_copies"`
	} `json:"session"`
	Payment struct {
		ID        string `json:"id"`
		State     string `json:"state"`
		QRPayload string `json:"qr_payload"`
		AmountIDR int64  `json:"amount_idr"`
		ExpiresIn int    `json:"expires_in"`
	} `json:"payment"`
}

// pay walks the booth from an idle screen to a session that may fire.
//
// No package is named, exactly as the kiosk names none: the booth sells one
// session and the screen it is bought from has nothing to choose.
func (f *fixture) pay(t *testing.T) startResponse {
	t.Helper()

	w := f.do(t, "POST", "/api/session", map[string]any{})
	if w.Code != http.StatusCreated {
		t.Fatalf("start session: %d %s", w.Code, w.Body)
	}
	start := decode[startResponse](t, w)

	f.settle(t)
	return start
}

// settle is the customer scanning whichever QR code is currently on screen.
func (f *fixture) settle(t *testing.T) {
	t.Helper()
	if w := f.do(t, "POST", "/api/payment/simulate", nil); w.Code != http.StatusOK {
		t.Fatalf("simulate payment: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, "GET", "/api/payment", nil); w.Code != http.StatusOK {
		t.Fatalf("poll payment: %d %s", w.Code, w.Body)
	}
}

// buyReprint is the customer paying for one more sheet, which is the only way
// past the single print the session includes.
func (f *fixture) buyReprint(t *testing.T) {
	t.Helper()
	if w := f.do(t, "POST", "/api/reprint", nil); w.Code != http.StatusCreated {
		t.Fatalf("start reprint: %d %s", w.Code, w.Body)
	}
	f.settle(t)
}

// The self-service gate, end to end: no payment, no photos.
func TestShutterIsLockedUntilTheQRIsPaid(t *testing.T) {
	f := setup(t)

	w := f.do(t, "POST", "/api/session", map[string]string{"package_id": "mini"})
	if w.Code != http.StatusCreated {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	start := decode[startResponse](t, w)

	switch {
	case start.Session.State != "awaiting_payment":
		t.Fatalf("state = %q", start.Session.State)
	case start.Payment.QRPayload == "":
		t.Fatal("no QR code for the customer to scan")
	case start.Payment.AmountIDR != 45_000:
		t.Fatalf("amount = %d, want the package price", start.Payment.AmountIDR)
	case start.Payment.ExpiresIn <= 0:
		t.Fatal("the QR code has no countdown, so an expiry will look like a broken machine")
	}

	// Capture before payment is refused by the server, not merely hidden by
	// the UI.
	if w := f.do(t, "POST", "/api/capture", frameBytes(t, 640, 480)); w.Code != http.StatusPaymentRequired {
		t.Fatalf("captured without paying: %d %s", w.Code, w.Body)
	}

	// The customer pays.
	if w := f.do(t, "POST", "/api/payment/simulate", nil); w.Code != http.StatusOK {
		t.Fatalf("simulate: %d %s", w.Code, w.Body)
	}
	poll := decode[startResponse](t, f.do(t, "GET", "/api/payment", nil))
	if poll.Payment.State != "settled" {
		t.Fatalf("payment state = %q", poll.Payment.State)
	}
	if poll.Session.State != "open" {
		t.Fatalf("session state = %q, want open", poll.Session.State)
	}

	if w := f.do(t, "POST", "/api/capture", frameBytes(t, 640, 480)); w.Code != http.StatusCreated {
		t.Fatalf("capture after paying: %d %s", w.Code, w.Body)
	}
}

func TestOnlyOneCustomerHoldsTheBooth(t *testing.T) {
	f := setup(t)

	if w := f.do(t, "POST", "/api/session", map[string]string{"package_id": "mini"}); w.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", w.Code, w.Body)
	}
	// Even unpaid, the first customer keeps the screen.
	if w := f.do(t, "POST", "/api/session", map[string]any{}); w.Code != http.StatusConflict {
		t.Fatalf("second session took the booth: %d %s", w.Code, w.Body)
	}
}

func TestUnknownPackageIsRejected(t *testing.T) {
	f := setup(t)

	if w := f.do(t, "POST", "/api/session", map[string]string{"package_id": "free"}); w.Code != http.StatusBadRequest {
		t.Fatalf("accepted a package that is not on the list: %d %s", w.Code, w.Body)
	}
}

func TestTakeLimitIsEnforcedAtCapture(t *testing.T) {
	f := setup(t)
	start := f.pay(t)

	for i := range start.Session.TakeLimit {
		if w := f.do(t, "POST", "/api/capture", frameBytes(t, 320, 240)); w.Code != http.StatusCreated {
			t.Fatalf("capture %d: %d %s", i, w.Code, w.Body)
		}
	}
	// The app owns the shutter, so it simply stops firing.
	if w := f.do(t, "POST", "/api/capture", frameBytes(t, 320, 240)); w.Code != http.StatusConflict {
		t.Fatalf("fired past the take limit: %d %s", w.Code, w.Body)
	}
}

// The resolution argument in design/kiosk.md, computed rather than asserted:
// the screen shows what a frame would actually print at.
func TestPhotoListReportsPrintResolution(t *testing.T) {
	f := setup(t)
	f.pay(t)

	if w := f.do(t, "POST", "/api/capture", frameBytes(t, 640, 480)); w.Code != http.StatusCreated {
		t.Fatalf("capture: %d %s", w.Code, w.Body)
	}

	got := decode[struct {
		Photos []struct {
			ID       string `json:"id"`
			PrintDPI int    `json:"print_dpi"`
		} `json:"photos"`
	}](t, f.do(t, "GET", "/api/photos", nil))

	if len(got.Photos) != 1 {
		t.Fatalf("%d photos, want 1", len(got.Photos))
	}
	// A 640x480 frame in a 540x450 cell is comfortably above the cell, so it
	// prints at the full 300 dpi.
	if got.Photos[0].PrintDPI != 300 {
		t.Fatalf("print dpi = %d, want 300", got.Photos[0].PrintDPI)
	}
}

func TestPrintComposesAndQueues(t *testing.T) {
	f := setup(t)
	start := f.pay(t)

	var ids []string
	for range sheetCells(t) {
		w := f.do(t, "POST", "/api/capture", frameBytes(t, 1200, 900))
		if w.Code != http.StatusCreated {
			t.Fatalf("capture: %d %s", w.Code, w.Body)
		}
		got := decode[struct {
			Photo struct {
				ID string `json:"id"`
			} `json:"photo"`
		}](t, w)
		ids = append(ids, got.Photo.ID)
	}

	w := f.do(t, "POST", "/api/print", map[string]any{
		"template_id": start.Session.TemplateID,
		"photo_ids":   ids,
		"copies":      1,
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("print: %d %s", w.Code, w.Body)
	}
	job := decode[struct {
		Job struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"job"`
	}](t, w)
	if job.Job.ID == "" {
		t.Fatal("no job id to poll")
	}

	stored, err := f.printer.Get(t.Context(), job.Job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if stored.SheetPath == "" {
		t.Fatal("the queued job has no composed sheet, so it can only fail")
	}
}

// sheetCells is how many photos the frame a session opens on needs.
//
// Read out of the built-ins rather than written down here. A house frame with a
// different cell count would otherwise turn every print test below into a check
// that the wrong photo count is rejected, which is the assertion one of them
// already makes on purpose.
func sheetCells(t *testing.T) int {
	t.Helper()

	packages, err := catalog.All()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	templates, err := compose.Builtin()
	if err != nil {
		t.Fatalf("templates: %v", err)
	}
	for _, tpl := range templates {
		if tpl.ID == packages[0].TemplateID {
			return len(tpl.Cells)
		}
	}
	t.Fatalf("the default package names %q, which no built-in template provides",
		packages[0].TemplateID)
	return 0
}

// sheetPhotos fires the frames the session's own template needs and returns
// their ids.
func (f *fixture) sheetPhotos(t *testing.T) []string {
	t.Helper()
	var ids []string
	for range sheetCells(t) {
		w := f.do(t, "POST", "/api/capture", frameBytes(t, 800, 600))
		if w.Code != http.StatusCreated {
			t.Fatalf("capture: %d %s", w.Code, w.Body)
		}
		ids = append(ids, decode[struct {
			Photo struct {
				ID string `json:"id"`
			} `json:"photo"`
		}](t, w).Photo.ID)
	}
	return ids
}

// The backstop kiosk.md keeps: a stray file must never become a free print,
// and neither must asking for more copies than the session has paid for.
func TestPrintRefusesMoreCopiesThanWerePaidFor(t *testing.T) {
	f := setup(t)
	start := f.pay(t) // one print

	w := f.do(t, "POST", "/api/print", map[string]any{
		"template_id": start.Session.TemplateID,
		"photo_ids":   f.sheetPhotos(t),
		"copies":      5,
	})
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("sold extra prints: %d %s", w.Code, w.Body)
	}
}

// Copies are handed out one at a time, which makes every single request look
// legal on its own. The allowance is therefore cumulative: the session includes
// one print, so the second single-copy request must be refused until it is paid
// for.
func TestPrintRefusesASecondCopyAcrossSeparateRequests(t *testing.T) {
	f := setup(t)
	start := f.pay(t)

	body := map[string]any{
		"template_id": start.Session.TemplateID,
		"photo_ids":   f.sheetPhotos(t),
		"copies":      1,
	}
	if w := f.do(t, "POST", "/api/print", body); w.Code != http.StatusAccepted {
		t.Fatalf("the included print was refused: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, "POST", "/api/print", body); w.Code != http.StatusPaymentRequired {
		t.Fatalf("gave away a second print nobody paid for: %d %s", w.Code, w.Body)
	}
}

// "Cetak 1 lagi" costs money, and the sheet appears only once it has arrived.
func TestASettledReprintBuysExactlyOneMorePrint(t *testing.T) {
	f := setup(t)
	start := f.pay(t)

	body := map[string]any{
		"template_id": start.Session.TemplateID,
		"photo_ids":   f.sheetPhotos(t),
		"copies":      1,
	}
	if w := f.do(t, "POST", "/api/print", body); w.Code != http.StatusAccepted {
		t.Fatalf("the included print was refused: %d %s", w.Code, w.Body)
	}

	// The QR goes up, and until it settles nothing has been bought.
	w := f.do(t, "POST", "/api/reprint", nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("start reprint: %d %s", w.Code, w.Body)
	}
	reprint := decode[startResponse](t, w)
	if reprint.Payment.AmountIDR != catalog.ReprintIDR {
		t.Fatalf("reprint charged %d, want %d", reprint.Payment.AmountIDR, catalog.ReprintIDR)
	}
	if w := f.do(t, "POST", "/api/print", body); w.Code != http.StatusPaymentRequired {
		t.Fatalf("printed against an unscanned QR code: %d %s", w.Code, w.Body)
	}

	f.settle(t)
	if w := f.do(t, "POST", "/api/print", body); w.Code != http.StatusAccepted {
		t.Fatalf("the paid reprint was refused: %d %s", w.Code, w.Body)
	}
	// One sheet, not an open tap.
	if w := f.do(t, "POST", "/api/print", body); w.Code != http.StatusPaymentRequired {
		t.Fatalf("one reprint payment bought more than one sheet: %d %s", w.Code, w.Body)
	}
}

// Selling somebody a sheet they already own is the one failure here that takes
// money and gives nothing back.
func TestReprintIsRefusedWhileAPrintIsStillUnclaimed(t *testing.T) {
	f := setup(t)
	f.pay(t)

	if w := f.do(t, "POST", "/api/reprint", nil); w.Code != http.StatusConflict {
		t.Fatalf("charged for a print the session already includes: %d %s", w.Code, w.Body)
	}
}

// Closing the dialog and opening it again must not leave a second live QR code
// against the same session at the gateway.
func TestReopeningTheReprintDialogReusesTheSameCharge(t *testing.T) {
	f := setup(t)
	start := f.pay(t)

	if w := f.do(t, "POST", "/api/print", map[string]any{
		"template_id": start.Session.TemplateID,
		"photo_ids":   f.sheetPhotos(t),
		"copies":      1,
	}); w.Code != http.StatusAccepted {
		t.Fatalf("the included print was refused: %d %s", w.Code, w.Body)
	}

	first := decode[startResponse](t, f.do(t, "POST", "/api/reprint", nil))
	second := decode[startResponse](t, f.do(t, "POST", "/api/reprint", nil))
	if first.Payment.ID != second.Payment.ID {
		t.Fatalf("minted a second charge %q alongside %q", second.Payment.ID, first.Payment.ID)
	}
}

// prints_done is what the screen decides "cetak lagi" from, so it has to track
// the copies actually claimed rather than the number of requests. print_copies
// beside it is the paid allowance, which grows with every settled reprint.
func TestSessionViewReportsPrintsDone(t *testing.T) {
	f := setup(t)
	start := f.pay(t)

	body := map[string]any{
		"template_id": start.Session.TemplateID,
		"photo_ids":   f.sheetPhotos(t),
		"copies":      1,
	}
	if w := f.do(t, "POST", "/api/print", body); w.Code != http.StatusAccepted {
		t.Fatalf("print: %d %s", w.Code, w.Body)
	}
	f.buyReprint(t)

	w := f.do(t, "GET", "/api/state", nil)
	got := decode[struct {
		Session struct {
			PrintCopies int `json:"print_copies"`
			PrintsDone  int `json:"prints_done"`
		} `json:"session"`
	}](t, w)
	if got.Session.PrintsDone != 1 {
		t.Fatalf("prints_done = %d, want 1", got.Session.PrintsDone)
	}
	if got.Session.PrintCopies != 2 {
		t.Fatalf("print_copies = %d, want 2", got.Session.PrintCopies)
	}
}

func TestPrintRequiresTheRightNumberOfPhotos(t *testing.T) {
	f := setup(t)
	start := f.pay(t)

	w := f.do(t, "POST", "/api/capture", frameBytes(t, 800, 600))
	id := decode[struct {
		Photo struct {
			ID string `json:"id"`
		} `json:"photo"`
	}](t, w).Photo.ID

	// The session's frame needs more than the one photo taken above.
	if w := f.do(t, "POST", "/api/print", map[string]any{
		"template_id": start.Session.TemplateID,
		"photo_ids":   []string{id},
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("composed a three-cell strip from one photo: %d %s", w.Code, w.Body)
	}
}

// PDP: the required consent is required, checked on the server rather than
// trusted from a disabled button.
func TestDeliveryRequiresConsent(t *testing.T) {
	f := setup(t)
	f.pay(t)

	if w := f.do(t, "POST", "/api/delivery", map[string]any{
		"phone": "081234567890", "consent": false,
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("stored a number without consent: %d %s", w.Code, w.Body)
	}

	if w := f.do(t, "POST", "/api/delivery", map[string]any{
		"phone": "081234567890", "consent": true, "marketing": false,
	}); w.Code != http.StatusOK {
		t.Fatalf("delivery: %d %s", w.Code, w.Body)
	}

	sess, ok, err := f.sessions.Current(t.Context())
	if err != nil || !ok {
		t.Fatalf("current: ok=%v err=%v", ok, err)
	}
	switch {
	case sess.Phone != "+6281234567890":
		t.Fatalf("phone stored as %q, not normalised to E.164", sess.Phone)
	case sess.ConsentVersion != httpd.ConsentVersion:
		t.Fatalf("consent version = %q", sess.ConsentVersion)
	case sess.MarketingConsent:
		t.Fatal("declining promotional messages was recorded as accepting them")
	}
}

func TestDeliveryRejectsAnInvalidNumber(t *testing.T) {
	f := setup(t)
	f.pay(t)

	if w := f.do(t, "POST", "/api/delivery", map[string]any{
		"phone": "12345", "consent": true,
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("accepted a number that cannot ring a handset: %d %s", w.Code, w.Body)
	}
}

// Binding to 127.0.0.1 does not stop a page the operator opened from reaching
// this. The Host check is what defeats DNS rebinding.
func TestRebindingHostIsRefused(t *testing.T) {
	f := setup(t)

	r := httptest.NewRequest("GET", "/api/state", nil)
	r.Host = "booth.attacker.example"
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("answered a request for someone else's hostname: %d", w.Code)
	}
}

// The test-deployment hostname. Everything below is about the one configuration
// where this server is reachable from somewhere other than the machine it runs
// on, which is also the only configuration where its lack of authentication
// would matter.
const testHost = "booth-test.bykami.id"

const testToken = "s3cr3t-token-value"

func publicBooth(t *testing.T) *fixture {
	t.Helper()
	return setupWith(t, func(d *httpd.Deps) {
		d.PublicHost = testHost
		d.AccessTokens = []string{testToken}
	})
}

func publicGet(t *testing.T, f *fixture, target string, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", target, nil)
	r.Host = testHost
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "bykami_booth_access", Value: cookie})
	}
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)
	return w
}

// The interlock. /api/capture takes a 16 MB upload and writes it to disk, so a
// public hostname with nothing in front of it is an open file drop.
func TestPublicHostWithoutATokenIsRefusedAtStartup(t *testing.T) {
	_, err := httpd.New(httpd.Deps{PublicHost: testHost, Log: slog.New(slog.DiscardHandler)})
	if err == nil {
		t.Fatal("built a server that answers the internet with no token at all")
	}
}

func TestPublicHostNeedsTheToken(t *testing.T) {
	f := publicBooth(t)

	if w := publicGet(t, f, "/api/state", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("answered an untokened request from the internet: %d %s", w.Code, w.Body)
	}
	if w := publicGet(t, f, "/api/state?t=wrong", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("accepted the wrong token: %d", w.Code)
	}
	if w := publicGet(t, f, "/api/state", "wrong"); w.Code != http.StatusUnauthorized {
		t.Fatalf("accepted a forged cookie: %d", w.Code)
	}
}

func TestPublicHostAcceptsTheTokenAndThenTheCookie(t *testing.T) {
	f := publicBooth(t)

	w := publicGet(t, f, "/api/state?t="+testToken, "")
	if w.Code != http.StatusOK {
		t.Fatalf("refused the right token: %d %s", w.Code, w.Body)
	}

	var handed string
	for _, c := range w.Result().Cookies() {
		if c.Name == "bykami_booth_access" {
			handed = c.Value
			if !c.HttpOnly || !c.Secure {
				t.Errorf("access cookie is not HttpOnly+Secure: %+v", c)
			}
		}
	}
	if handed == "" {
		t.Fatal("no cookie was set, so every following tap would need the token in the URL")
	}

	// The point of the cookie: the fifteen requests after the first carry no
	// token in the URL.
	if w := publicGet(t, f, "/api/state", handed); w.Code != http.StatusOK {
		t.Fatalf("refused the cookie it had just issued: %d", w.Code)
	}
}

// One token per tester, which is the whole reason this is a list. With a single
// shared secret, withdrawing one person's access means rotating for everybody —
// which in practice means nobody's access is ever withdrawn.
func TestAnyOfTheConfiguredTokensAdmits(t *testing.T) {
	tokens := []string{"token-for-rina-000000000", "token-for-adi-0000000000", "token-for-sari-000000000"}
	f := setupWith(t, func(d *httpd.Deps) {
		d.PublicHost = testHost
		d.AccessTokens = tokens
	})

	for _, tok := range tokens {
		if w := publicGet(t, f, "/api/state?t="+tok, ""); w.Code != http.StatusOK {
			t.Errorf("refused a configured token: %d %s", w.Code, w.Body)
		}
		if w := publicGet(t, f, "/api/state", tok); w.Code != http.StatusOK {
			t.Errorf("refused a configured token presented as a cookie: %d", w.Code)
		}
	}

	if w := publicGet(t, f, "/api/state?t=token-for-nobody-00000", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("a token that is not on the list opened the booth: %d", w.Code)
	}
}

// And withdrawing one has to actually withdraw it. The cookie carries the token
// that matched rather than the list, so dropping that token from the
// configuration stops recognising the cookie it was issued for — and leaves
// everyone else's working.
func TestWithdrawingOneTokenLeavesTheOthersWorking(t *testing.T) {
	const withdrawn = "token-for-the-ex-tester0"
	const kept = "token-for-everyone-else0"

	f := setupWith(t, func(d *httpd.Deps) {
		d.PublicHost = testHost
		d.AccessTokens = []string{withdrawn, kept}
	})
	w := publicGet(t, f, "/api/state?t="+withdrawn, "")
	if w.Code != http.StatusOK {
		t.Fatalf("refused a configured token: %d %s", w.Code, w.Body)
	}

	var handed string
	for _, c := range w.Result().Cookies() {
		if c.Name == "bykami_booth_access" {
			handed = c.Value
		}
	}
	if handed != withdrawn {
		t.Fatalf("cookie carries %q, want the token that matched — otherwise one cookie survives every withdrawal", handed)
	}

	// The booth as it is after the token is taken out and the play re-run.
	after := setupWith(t, func(d *httpd.Deps) {
		d.PublicHost = testHost
		d.AccessTokens = []string{kept}
	})
	if w := publicGet(t, after, "/api/state", handed); w.Code != http.StatusUnauthorized {
		t.Errorf("the withdrawn tester's cookie still opens the booth: %d", w.Code)
	}
	if w := publicGet(t, after, "/api/state?t="+kept, ""); w.Code != http.StatusOK {
		t.Errorf("withdrawing one token locked out the others: %d %s", w.Code, w.Body)
	}
}

// The booth's own browser is unaffected. A token demanded on loopback would be
// security theatre — anything on that machine can read it — and would break the
// production configuration, which sets no public host at all.
func TestLoopbackStillNeedsNoToken(t *testing.T) {
	f := publicBooth(t)

	if w := f.do(t, "GET", "/api/state", nil); w.Code != http.StatusOK {
		t.Fatalf("made the booth's own screen log in: %d %s", w.Code, w.Body)
	}
}

// The tunnel serves TLS, so the page's own requests carry an https Origin. It
// has to be allowed without opening the door to every other https site.
func TestPublicHostAcceptsItsOwnHTTPSOriginOnly(t *testing.T) {
	f := publicBooth(t)

	for origin, want := range map[string]int{
		"https://" + testHost:  http.StatusOK,
		"https://attacker.bad": http.StatusForbidden,
		"http://" + testHost:   http.StatusForbidden,
	} {
		r := httptest.NewRequest("GET", "/api/state", nil)
		r.Host = testHost
		r.AddCookie(&http.Cookie{Name: "bykami_booth_access", Value: testToken})
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		f.srv.ServeHTTP(w, r)

		if w.Code != want {
			t.Errorf("Origin %s: got %d, want %d", origin, w.Code, want)
		}
	}
}

// A booth with no public host configured must behave exactly as before: one
// hostname, no token anywhere.
func TestUnconfiguredPublicHostChangesNothing(t *testing.T) {
	f := setup(t)

	r := httptest.NewRequest("GET", "/api/state", nil)
	r.Host = testHost
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("a default booth answered to %s: %d", testHost, w.Code)
	}
}

func TestCrossSiteOriginIsRefused(t *testing.T) {
	f := setup(t)

	r := httptest.NewRequest("POST", "/api/session", strings.NewReader(`{"package_id":"mini"}`))
	r.Host = "localhost:8899"
	r.Header.Set("Origin", "https://attacker.example")
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("accepted a cross-site request: %d", w.Code)
	}
}

func TestStateDescribesTheBooth(t *testing.T) {
	f := setup(t)

	got := decode[struct {
		Source   string `json:"source"`
		Packages []struct {
			ID string `json:"id"`
		} `json:"packages"`
		Templates []struct {
			ID    string `json:"id"`
			Sheet [2]int `json:"sheet"`
		} `json:"templates"`
		Media struct {
			SheetsRemaining int `json:"sheets_remaining"`
		} `json:"media"`
		Consent struct {
			Version       string `json:"version"`
			RetentionDays int    `json:"retention_days"`
		} `json:"consent"`
		Flags map[string]bool `json:"flags"`
	}](t, f.do(t, "GET", "/api/state", nil))

	switch {
	case got.Source != string(httpd.SourceWebcam):
		t.Fatalf("source = %q", got.Source)
	case len(got.Packages) == 0:
		t.Fatal("nothing to sell")
	case len(got.Templates) == 0:
		t.Fatal("no templates to choose from")
	case got.Media.SheetsRemaining != printer.RollSheets:
		t.Fatalf("media = %d, want a full roll", got.Media.SheetsRemaining)
	case got.Consent.Version != httpd.ConsentVersion:
		t.Fatalf("consent version = %q", got.Consent.Version)
	// The number the customer is promised, which has to be the number the purge
	// actually enforces. This asserted 30 while purge.DefaultAge was 7, so the
	// delivery screen offered every customer three weeks that did not exist.
	case got.Consent.RetentionDays != int(purge.DefaultAge/(24*time.Hour)):
		t.Fatalf("retention = %d days, want the purge window", got.Consent.RetentionDays)
	case !got.Flags["payments_enabled"]:
		t.Fatal("payments reported disabled")
	case !got.Flags["payments_simulated"]:
		t.Fatal("the simulated provider is not announced, so the UI cannot warn")
	}
}

// A customer who walks away from the QR code must not hold the booth.
func TestCancelFreesAnUnpaidBooth(t *testing.T) {
	f := setup(t)

	if w := f.do(t, "POST", "/api/session", map[string]string{"package_id": "mini"}); w.Code != http.StatusCreated {
		t.Fatalf("start: %d", w.Code)
	}
	if w := f.do(t, "POST", "/api/session/cancel", nil); w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, "POST", "/api/session", map[string]any{}); w.Code != http.StatusCreated {
		t.Fatalf("booth still held: %d %s", w.Code, w.Body)
	}
}

// Money moved, so the row has to say so.
func TestCancelRefusesAPaidSession(t *testing.T) {
	f := setup(t)
	f.pay(t)

	if w := f.do(t, "POST", "/api/session/cancel", nil); w.Code != http.StatusConflict {
		t.Fatalf("deleted a session that was paid for: %d %s", w.Code, w.Body)
	}
}

// With a real provider the "customer paid" button does not exist at all, so
// there is no path from a running booth to a free session.
func TestSimulateIsAbsentWithoutTheSimulatedProvider(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	log := slog.New(slog.DiscardHandler)
	srv, err := httpd.New(httpd.Deps{
		Sessions: session.New(db),
		Photos:   photo.New(db),
		Payments: payment.New(db, nil),
		Printer:  printer.New(db, printer.NewSimulated(log, 1000), log),
		Root:     t.TempDir(),
		Source:   httpd.SourceHotFolder,
		Log:      log,
	})
	if err != nil {
		t.Fatalf("httpd: %v", err)
	}

	r := httptest.NewRequest("POST", "/api/payment/simulate", nil)
	r.Host = "localhost"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("the simulate button exists on a booth that could take real money: %d", w.Code)
	}
}

// A booth with no payment provider says so instead of holding a dead session.
func TestNoProviderRefusesAndFreesTheBooth(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	log := slog.New(slog.DiscardHandler)
	sessions := session.New(db)
	packages, err := catalog.All()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	srv, err := httpd.New(httpd.Deps{
		Sessions: sessions,
		Photos:   photo.New(db),
		Payments: payment.New(db, nil),
		Printer:  printer.New(db, printer.NewSimulated(log, 1000), log),
		Packages: packages,
		Root:     t.TempDir(),
		Source:   httpd.SourceHotFolder,
		OutletID: "jajag",
		Log:      log,
	})
	if err != nil {
		t.Fatalf("httpd: %v", err)
	}

	r := httptest.NewRequest("POST", "/api/session", strings.NewReader(`{"package_id":"mini"}`))
	r.Host = "localhost"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", w.Code)
	}
	if _, ok, err := sessions.Current(t.Context()); err != nil || ok {
		t.Fatal("a session that can never be paid for is holding the booth")
	}
}

// Nothing here is cacheable: it is the live state of one customer's session.
func TestResponsesAreNotCached(t *testing.T) {
	f := setup(t)
	w := f.do(t, "GET", "/api/state", nil)
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

var _ = time.Second

// The content-addressed store is what makes a crash rescan safe, and it means
// two identical frames are one frame. The screen must say so rather than show a
// take that does not exist.
func TestIdenticalFrameIsRefused(t *testing.T) {
	f := setup(t)
	f.pay(t)

	frame := frameBytes(t, 320, 240)
	if w := f.do(t, "POST", "/api/capture", frame); w.Code != http.StatusCreated {
		t.Fatalf("first capture: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, "POST", "/api/capture", frame); w.Code != http.StatusConflict {
		t.Fatalf("the same bytes became a second take: %d %s", w.Code, w.Body)
	}
}

// The hybrid booth previews a live camera and still takes its frames off the
// tethered one. The preview is decoration: nothing may be captured from it, or
// the booth quietly prints 1080p webcam grabs while a Canon sits idle beside
// it — which looks like working software right up to the moment somebody
// compares a print to what the studio used to deliver.
func TestHybridPreviewsButDoesNotCaptureFromTheBrowser(t *testing.T) {
	f := setupWith(t, func(d *httpd.Deps) {
		d.Source = httpd.SourceHybrid
		d.Camera = "EOS"
	})

	if w := f.do(t, "POST", "/api/session", map[string]string{"package_id": "mini"}); w.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, "POST", "/api/payment/simulate", nil); w.Code != http.StatusOK {
		t.Fatalf("simulate: %d %s", w.Code, w.Body)
	}
	// The charge settles when it is polled, so the shutter is not unlocked
	// until the kiosk has actually seen the money land.
	if poll := decode[startResponse](t, f.do(t, "GET", "/api/payment", nil)); poll.Session.State != "open" {
		t.Fatalf("session state = %q, want open", poll.Session.State)
	}

	// A paid session, a real JPEG in the body, and it is still not a photograph:
	// the answer is "the frame is coming through the hot folder", not "created".
	w := f.do(t, "POST", "/api/capture", frameBytes(t, 640, 480))
	if w.Code != http.StatusAccepted {
		t.Fatalf("hybrid capture = %d %s, want 202", w.Code, w.Body)
	}
	got := decode[struct {
		Awaiting bool `json:"awaiting_file"`
	}](t, w)
	if !got.Awaiting {
		t.Fatal("hybrid capture did not say it was waiting for the tethered frame")
	}

	// And the browser's bytes were not filed as a take behind the customer's
	// back, which is the failure this whole test exists to catch. Asserted on
	// the count the customer is charged against rather than on the row count:
	// that is the number the booth bills, and it is the one that must not move.
	state := decode[struct {
		Session *struct {
			Takes int `json:"takes"`
		} `json:"session"`
	}](t, f.do(t, "GET", "/api/state", nil))
	if state.Session == nil {
		t.Fatal("the paid session vanished")
	}
	if state.Session.Takes != 0 {
		t.Fatalf("the preview stream burned %d take(s) off the session", state.Session.Takes)
	}
}

// Which camera to preview is the booth's hardware talking, so it has to reach
// the browser: the kiosk cannot pick the Canon out of a machine that also has a
// lid webcam without being told what to look for.
func TestStateCarriesTheCameraHint(t *testing.T) {
	f := setupWith(t, func(d *httpd.Deps) {
		d.Source = httpd.SourceHybrid
		d.Camera = "EOS Webcam Utility"
	})

	got := decode[struct {
		Source string `json:"source"`
		Camera string `json:"camera"`
	}](t, f.do(t, "GET", "/api/state", nil))

	if got.Source != string(httpd.SourceHybrid) {
		t.Fatalf("source = %q, want hybrid", got.Source)
	}
	if got.Camera != "EOS Webcam Utility" {
		t.Fatalf("camera = %q, want the hint the booth was started with", got.Camera)
	}
}

// With a shutter wired up, a tap actually fires the camera. The answer is still
// 202 and not a photograph: the frame is travelling down the USB cable and
// becomes a take when the hot-folder watcher finds it.
func TestTetheredCaptureFiresTheShutter(t *testing.T) {
	var fired int
	f := setupWith(t, func(d *httpd.Deps) {
		d.Source = httpd.SourceHybrid
		d.Camera = "EOS"
		d.Shutter = func(context.Context) error {
			fired++
			return nil
		}
	})
	openPaidSession(t, f)

	if w := f.do(t, "POST", "/api/capture", nil); w.Code != http.StatusAccepted {
		t.Fatalf("capture = %d %s, want 202", w.Code, w.Body)
	}
	if fired != 1 {
		t.Fatalf("the camera was fired %d times for one tap", fired)
	}
}

// The failure the whole shutter path exists to make visible. A camera that
// refused to fire must reach the customer as an error, because the alternative
// is a booth that counts down, photographs nobody, and moves on to the next
// pose — money taken, nothing produced, and no sign anything went wrong.
func TestARefusedShutterIsAnError(t *testing.T) {
	f := setupWith(t, func(d *httpd.Deps) {
		d.Source = httpd.SourceHotFolder
		d.Shutter = func(context.Context) error {
			return errors.New("no camera is connected")
		}
	})
	openPaidSession(t, f)

	w := f.do(t, "POST", "/api/capture", nil)
	if w.Code == http.StatusAccepted || w.Code == http.StatusCreated {
		t.Fatalf("a camera that never fired was reported as a photograph: %d %s", w.Code, w.Body)
	}
	if w.Code != http.StatusBadGateway {
		t.Fatalf("capture = %d %s, want 502", w.Code, w.Body)
	}
	// And the customer is told in the language the booth speaks, not handed a
	// Go error about USB.
	if !strings.Contains(w.Body.String(), "petugas") {
		t.Fatalf("the customer was not told to call staff: %s", w.Body)
	}
}

// A booth with no trigger keeps working exactly as it did: the countdown is an
// announcement and somebody fires the camera by hand.
func TestTetheredCaptureWithoutAShutterStillAnnounces(t *testing.T) {
	f := setupWith(t, func(d *httpd.Deps) { d.Source = httpd.SourceHotFolder })
	openPaidSession(t, f)

	if w := f.do(t, "POST", "/api/capture", nil); w.Code != http.StatusAccepted {
		t.Fatalf("capture = %d %s, want 202", w.Code, w.Body)
	}
}

// The kiosk runs the automatic countdown only where something fires at the end
// of it, so whether a shutter exists has to reach the screen.
func TestStateSaysWhetherTheBoothCanFireItsOwnCamera(t *testing.T) {
	hand := setupWith(t, func(d *httpd.Deps) { d.Source = httpd.SourceHotFolder })
	if got := decode[struct {
		Shutter bool `json:"shutter"`
	}](t, hand.do(t, "GET", "/api/state", nil)); got.Shutter {
		t.Fatal("a hand-fired booth claimed it could fire its own camera")
	}

	wired := setupWith(t, func(d *httpd.Deps) {
		d.Source = httpd.SourceHotFolder
		d.Shutter = func(context.Context) error { return nil }
	})
	if got := decode[struct {
		Shutter bool `json:"shutter"`
	}](t, wired.do(t, "GET", "/api/state", nil)); !got.Shutter {
		t.Fatal("a booth with a shutter wired up did not say so")
	}
}

// openPaidSession walks a fixture to the point where the shutter is unlocked.
func openPaidSession(t *testing.T, f *fixture) {
	t.Helper()

	if w := f.do(t, "POST", "/api/session", map[string]string{"package_id": "mini"}); w.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, "POST", "/api/payment/simulate", nil); w.Code != http.StatusOK {
		t.Fatalf("simulate: %d %s", w.Code, w.Body)
	}
	if poll := decode[startResponse](t, f.do(t, "GET", "/api/payment", nil)); poll.Session.State != "open" {
		t.Fatalf("session state = %q, want open", poll.Session.State)
	}
}
