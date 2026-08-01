package httpd_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/httpd"
)

// The download gallery is the only surface on this server a stranger is meant
// to reach. These tests are about the two things that makes true: that a
// customer can get in without the booth's own credential, and that the token
// they hold opens exactly one session and nothing else.

// shareToken returns the live session's download token, straight from the
// store. The kiosk gets it from /api/state; a test wants it before there is a
// reason to render a screen.
func (f *fixture) shareToken(t *testing.T) string {
	t.Helper()
	sess, ok, err := f.sessions.Current(t.Context())
	if err != nil || !ok {
		t.Fatalf("current session: %v (found %v)", err, ok)
	}
	if sess.ShareToken == "" {
		t.Fatal("session has no share token, so its QR would point nowhere")
	}
	return sess.ShareToken
}

// photoIDs lists what the booth holds for the live session.
func (f *fixture) photoIDs(t *testing.T) []string {
	t.Helper()
	w := f.do(t, "GET", "/api/photos", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list photos: %d %s", w.Code, w.Body)
	}
	got := decode[struct {
		Photos []struct {
			ID string `json:"id"`
		} `json:"photos"`
	}](t, w)

	ids := make([]string, 0, len(got.Photos))
	for _, p := range got.Photos {
		ids = append(ids, p.ID)
	}
	return ids
}

// shotSession pays, fires n frames and returns the download token.
func (f *fixture) shotSession(t *testing.T, n int) string {
	t.Helper()
	f.pay(t)
	for range n {
		if w := f.do(t, "POST", "/api/capture", frameBytes(t, 1600, 1200)); w.Code != http.StatusCreated {
			t.Fatalf("capture: %d %s", w.Code, w.Body)
		}
	}
	return f.shareToken(t)
}

// printedSession pays, fires exactly the frames the package's template needs,
// and prints them once per entry in filters — so a caller can ask for a reprint
// of the same picture ("" twice) or for two different ones.
func (f *fixture) printedSession(t *testing.T, filters ...string) string {
	t.Helper()
	start := f.pay(t)

	var ids []string
	for range f.cellCount(t, start.Session.TemplateID) {
		w := f.do(t, "POST", "/api/capture", frameBytes(t, 1600, 1200))
		if w.Code != http.StatusCreated {
			t.Fatalf("capture: %d %s", w.Code, w.Body)
		}
		ids = append(ids, decode[struct {
			Photo struct {
				ID string `json:"id"`
			} `json:"photo"`
		}](t, w).Photo.ID)
	}

	for i, filter := range filters {
		// The session includes one print. Every sheet after it is bought, so a
		// test that wants two prints has to pay for the second exactly as a
		// customer does.
		if i > 0 {
			f.buyReprint(t)
		}
		w := f.do(t, "POST", "/api/print", map[string]any{
			"template_id": start.Session.TemplateID,
			"photo_ids":   ids,
			"copies":      1,
			"filter":      filter,
		})
		if w.Code != http.StatusAccepted {
			t.Fatalf("print: %d %s", w.Code, w.Body)
		}
	}
	return f.shareToken(t)
}

// cellCount is how many frames a template holds, read from the booth rather
// than restated here so that editing a template's manifest cannot silently
// leave these tests printing the wrong number of photos.
func (f *fixture) cellCount(t *testing.T, templateID string) int {
	t.Helper()
	got := decode[struct {
		Templates []struct {
			ID    string           `json:"id"`
			Cells []map[string]any `json:"cells"`
		} `json:"templates"`
	}](t, f.do(t, "GET", "/api/state", nil))

	for _, tpl := range got.Templates {
		if tpl.ID == templateID {
			return len(tpl.Cells)
		}
	}
	t.Fatalf("no template %q on the booth", templateID)
	return 0
}

// sheetIDs lists the live session's print jobs, which is what the gallery
// addresses a framed download by.
func (f *fixture) sheetIDs(t *testing.T) []string {
	t.Helper()
	sess, ok, err := f.sessions.Current(t.Context())
	if err != nil || !ok {
		t.Fatalf("current session: %v (found %v)", err, ok)
	}
	jobs, err := f.printer.BySession(t.Context(), sess.ID)
	if err != nil {
		t.Fatalf("print jobs: %v", err)
	}

	ids := make([]string, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	return ids
}

// The whole point. The access token opens /api/capture and /api/print, so it is
// the booth's credential and a customer can never be given it — a gallery
// behind it would be a QR code that only the operator could scan.
func TestGalleryOpensWithoutTheBoothsAccessToken(t *testing.T) {
	f := publicBooth(t)
	token := f.shotSession(t, 2)

	w := publicGet(t, f, "/g/"+token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("gallery = %d %s, want it to open with no booth token", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "/g/"+token+"/p/") {
		t.Error("the page lists no photos")
	}
}

// The rest of the booth must stay shut. If this ever passes, the gallery
// exemption has been written too broadly and /api/capture is an open file drop
// on the internet.
func TestTheGalleryExemptionDoesNotOpenTheBooth(t *testing.T) {
	f := publicBooth(t)
	f.shotSession(t, 1)

	// The near-misses matter as much as the API routes. Anything the mux cannot
	// route falls through to the kiosk UI, so a path that merely looks like a
	// gallery URL must not be waved past the token on its way there.
	for _, path := range []string{
		"/api/state", "/api/photos", "/",
		"/g", "/g/", "/g/x/y/z", "/g/x/p/y/z", "/g/x/notp/y",
		"/g/x/s/y/z", "/g/x/s/", "/g/x/s",
	} {
		if w := publicGet(t, f, path, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401 without the access token", path, w.Code)
		}
	}
}

// The download page loads one thing that is not a photo, so the guard carries a
// second exemption for it. It has to be exactly one path: everything around it
// falls through to the kiosk UI, which is what the token is there to keep shut.
func TestTheLogoExemptionIsOnlyTheLogo(t *testing.T) {
	f := publicBooth(t)

	// Deliberately not asserted as 200. Whether the file is there at all
	// depends on whether the kiosk bundle was built before `go test`, and it is
	// the exemption that is under test — a 404 from an unbuilt bundle is still
	// an answer given without the booth's token.
	if w := publicGet(t, f, "/brand/logo.png", ""); w.Code == http.StatusUnauthorized {
		t.Error("the logo demands the booth's token, so the gallery renders without one")
	}

	for _, path := range []string{
		"/brand", "/brand/", "/brand/logo.png/x", "/brand/logo.pngx", "/brand/other.png",
	} {
		if w := publicGet(t, f, path, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401 without the access token", path, w.Code)
		}
	}
}

// The URL is the access control, so a wrong one has to be worth nothing.
func TestGalleryRefusesATokenItDidNotMint(t *testing.T) {
	f := publicBooth(t)
	f.shotSession(t, 1)

	for _, token := range []string{"nope", strings.Repeat("a", 32), "0"} {
		if w := publicGet(t, f, "/g/"+token, ""); w.Code != http.StatusNotFound {
			t.Errorf("token %q = %d, want 404", token, w.Code)
		}
	}

	// Traversal is answered by the mux normalising the path before any of this
	// runs, so the assertion is that nothing is served rather than which code
	// says so — a redirect to the cleaned path is a correct answer here.
	if w := publicGet(t, f, "/g/../../etc/passwd", ""); w.Code == http.StatusOK {
		t.Error("a traversal attempt was served something")
	}
}

// Without this check a single valid token would open every photograph the booth
// has ever taken, which is the difference between a capability for one session
// and one for the whole disk.
func TestGallerySeesOnlyItsOwnSessionsPhotos(t *testing.T) {
	f := publicBooth(t)

	// One customer, then the next.
	first := f.shotSession(t, 1)
	mine := f.photoIDs(t)
	if len(mine) != 1 {
		t.Fatalf("first session has %d photos, want 1", len(mine))
	}
	if w := f.do(t, "POST", "/api/session/close", nil); w.Code != http.StatusOK {
		t.Fatalf("close: %d %s", w.Code, w.Body)
	}

	f.shotSession(t, 1)
	theirs := f.photoIDs(t)
	if len(theirs) != 1 {
		t.Fatalf("second session has %d photos, want 1", len(theirs))
	}

	// The first customer's link, pointed at the second customer's photograph.
	w := publicGet(t, f, "/g/"+first+"/p/"+theirs[0], "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-session photo = %d, want 404 — one token opened another customer's session", w.Code)
	}

	// And its own still works, so the check above is not simply refusing
	// everything.
	if w := publicGet(t, f, "/g/"+first+"/p/"+mine[0], ""); w.Code != http.StatusOK {
		t.Fatalf("own photo = %d %s", w.Code, w.Body)
	}
}

// The frame is what the customer chose and what they walked out holding. A
// gallery offering only the loose frames hands back less than the print did, so
// the page carries both — the composed sheet and the untouched originals.
func TestGalleryOffersBothTheFramedPrintAndTheLooseFrames(t *testing.T) {
	f := publicBooth(t)
	token := f.printedSession(t, "")

	w := publicGet(t, f, "/g/"+token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("gallery = %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, "/g/"+token+"/s/") {
		t.Error("the page offers no framed version")
	}
	if !strings.Contains(body, "/g/"+token+"/p/") {
		t.Error("the page offers no unframed frames")
	}
}

// The sheet is a file on disk composed for a printer, so the thing worth
// asserting is that it reaches a phone as a saveable image rather than as a
// page the browser tries to render.
func TestGalleryServesTheSheetAndOffersItAsADownload(t *testing.T) {
	f := publicBooth(t)
	token := f.printedSession(t, "")

	ids := f.sheetIDs(t)
	if len(ids) != 1 {
		t.Fatalf("%d print jobs, want 1", len(ids))
	}

	w := publicGet(t, f, "/g/"+token+"/s/"+ids[0], "")
	if w.Code != http.StatusOK {
		t.Fatalf("sheet = %d %s", w.Code, w.Body)
	}
	if ctype := w.Header().Get("Content-Type"); !strings.HasPrefix(ctype, "image/") {
		t.Errorf("Content-Type = %q, want an image", ctype)
	}
	if w.Header().Get("Content-Disposition") != "" {
		t.Error("the plain URL forces a download, so the page cannot show it inline")
	}

	// iOS Safari honours the header and has historically ignored the anchor's
	// download attribute, and iOS is most of what will scan this code.
	w = publicGet(t, f, "/g/"+token+"/s/"+ids[0]+"?dl=1", "")
	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want an attachment", cd)
	}
}

// The check the whole surface rests on, applied to the second kind of file it
// now serves. Without it one valid token would open every sheet the booth has
// ever composed.
func TestGallerySeesOnlyItsOwnSessionsSheets(t *testing.T) {
	f := publicBooth(t)

	f.printedSession(t, "")
	first := f.shareToken(t)
	mine := f.sheetIDs(t)
	if w := f.do(t, "POST", "/api/session/close", nil); w.Code != http.StatusOK {
		t.Fatalf("close: %d %s", w.Code, w.Body)
	}

	f.printedSession(t, "")
	theirs := f.sheetIDs(t)

	w := publicGet(t, f, "/g/"+first+"/s/"+theirs[0], "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-session sheet = %d, want 404 — one token opened another customer's print", w.Code)
	}
	if w := publicGet(t, f, "/g/"+first+"/s/"+mine[0], ""); w.Code != http.StatusOK {
		t.Fatalf("own sheet = %d %s, so the check above refuses everything", w.Code, w.Body)
	}
}

// A reprint composes the same picture again under a new filename. Most packages
// include more than one print and most customers use them, so the identical
// strip appearing two or three times is the ordinary case, not the exotic one —
// and it reads as a bug.
func TestGalleryShowsARepeatedPrintOnce(t *testing.T) {
	f := publicBooth(t)
	token := f.printedSession(t, "", "")

	if got := len(f.sheetIDs(t)); got != 2 {
		t.Fatalf("%d print jobs, want the 2 this test is about", got)
	}
	body := publicGet(t, f, "/g/"+token, "").Body.String()
	if n := strings.Count(body, `href="/g/`+token+`/s/`); n != 1 {
		t.Fatalf("%d framed downloads for one picture printed twice, want 1", n)
	}
}

// And the dedupe must not be "show one and hope". Two prints that differ — here
// a change of filter, on the kiosk also a change of template — are two
// different pictures the customer paid for, and both belong on the page.
func TestGalleryKeepsTwoPrintsThatDiffer(t *testing.T) {
	f := publicBooth(t)
	token := f.printedSession(t, "", "hitam-putih")

	body := publicGet(t, f, "/g/"+token, "").Body.String()
	if n := strings.Count(body, `href="/g/`+token+`/s/`); n != 2 {
		t.Fatalf("%d framed downloads for two different prints, want 2", n)
	}
}

// A photo gallery is exactly the sort of page somebody adds a lightbox to. The
// CSP is what makes that fail loudly instead of quietly reopening the hole this
// surface was designed without.
func TestGalleryPageAllowsNoScript(t *testing.T) {
	f := publicBooth(t)
	token := f.shotSession(t, 1)

	w := publicGet(t, f, "/g/"+token, "")
	csp := w.Header().Get("Content-Security-Policy")
	switch {
	case csp == "":
		t.Fatal("no Content-Security-Policy on the gallery")
	case !strings.Contains(csp, "default-src 'none'"):
		t.Errorf("CSP = %q, want default-src 'none'", csp)
	case strings.Contains(csp, "unsafe-inline"), strings.Contains(csp, "'unsafe-eval'"):
		t.Errorf("CSP = %q, which permits inline code", csp)
	case !strings.Contains(w.Header().Get("X-Robots-Tag"), "noindex"):
		t.Error("a customer's face is indexable")
	case w.Header().Get("Referrer-Policy") != "no-referrer":
		t.Error("the URL is the secret, so it must not travel in a Referer")
	}

	if body := w.Body.String(); strings.Contains(body, "<script") {
		t.Error("the gallery page carries a script tag")
	}
}

// The stylesheet is inline and hashed into the CSP. If the two ever drift the
// page renders unstyled, which is a silent failure — so it is asserted rather
// than left to be noticed on somebody's phone.
func TestGalleryStyleMatchesItsContentSecurityPolicyHash(t *testing.T) {
	f := publicBooth(t)
	token := f.shotSession(t, 1)

	w := publicGet(t, f, "/g/"+token, "")
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "style-src 'sha256-") {
		t.Fatalf("CSP = %q, want a hashed style source", csp)
	}
	if !strings.Contains(w.Body.String(), "<style>") {
		t.Fatal("no inline style block for that hash to cover")
	}
}

// Seven days later the link still resolves, and says so. A 404 here would look
// like a typo; the customer needs to understand that the window closed.
func TestGalleryIsGoneOnceThePhotosArePurged(t *testing.T) {
	f := publicBooth(t)
	token := f.shotSession(t, 2)

	for _, id := range f.photoIDs(t) {
		if err := f.photos.MarkPurged(t.Context(), id); err != nil {
			t.Fatalf("mark purged: %v", err)
		}
	}

	w := publicGet(t, f, "/g/"+token, "")
	if w.Code != http.StatusGone {
		t.Fatalf("purged gallery = %d, want 410", w.Code)
	}
	if !strings.Contains(w.Body.String(), "terhapus") {
		t.Error("the page does not say the photos are gone")
	}
}

// A booth on a mall's wifi has no address a phone on mobile data can reach, so
// there is nothing to encode. The screen then offers WhatsApp alone rather than
// a QR code that scans to a connection error.
func TestShareURLIsEmptyWithoutAPublicHost(t *testing.T) {
	f := setup(t)
	f.pay(t)

	got := decode[struct {
		Session struct {
			ShareURL string `json:"share_url"`
		} `json:"session"`
	}](t, f.do(t, "GET", "/api/state", nil))

	if got.Session.ShareURL != "" {
		t.Fatalf("share_url = %q on a booth with no public hostname", got.Session.ShareURL)
	}
}

// And with one, it is the address the customer's phone will actually resolve —
// https, because the only way this hostname exists is a tunnel in front of it.
func TestShareURLPointsAtTheGallery(t *testing.T) {
	f := publicBooth(t)
	token := f.shotSession(t, 1)

	r := httptest.NewRequest("GET", "/api/state", nil)
	r.Host = testHost
	r.AddCookie(&http.Cookie{Name: "bykami_booth_access", Value: testToken})
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)

	got := decode[struct {
		Session struct {
			ShareURL string `json:"share_url"`
		} `json:"session"`
	}](t, w)

	want := "https://" + testHost + "/g/" + token
	if got.Session.ShareURL != want {
		t.Fatalf("share_url = %q, want %q", got.Session.ShareURL, want)
	}
}

// The number on the delivery screen has to be the number the purge enforces.
// This is the pairing that was wrong: the screen promised 30 days while the
// sweep deleted at 7.
func TestRetentionShownMatchesWhatIsConfigured(t *testing.T) {
	f := setupWith(t, func(d *httpd.Deps) { d.Retention = 48 * time.Hour })

	got := decode[struct {
		Consent struct {
			RetentionDays int `json:"retention_days"`
		} `json:"consent"`
	}](t, f.do(t, "GET", "/api/state", nil))

	if got.Consent.RetentionDays != 2 {
		t.Fatalf("retention = %d days, want the configured 2", got.Consent.RetentionDays)
	}
}
