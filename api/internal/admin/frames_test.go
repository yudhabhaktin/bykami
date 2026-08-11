package admin_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/frames"
)

// stripArt draws a 600×1800 strip with three clear rectangles — the shape of a
// frame a designer would export.
func stripArt(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 600, 1800))
	for y := range 1800 {
		for x := range 600 {
			img.SetNRGBA(x, y, color.NRGBA{0x20, 0x20, 0x20, 0xff})
		}
	}
	for _, top := range []int{36, 516, 996} {
		for y := top; y < top+450; y++ {
			for x := 30; x < 570; x++ {
				img.SetNRGBA(x, y, color.NRGBA{})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// upload posts the multipart form the console's own page submits.
func (f fixture) upload(t *testing.T, cookie, csrf, name string, art []byte, extra url.Values) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("csrf", csrf)
	_ = mw.WriteField("name", name)
	for k, vs := range extra {
		for _, v := range vs {
			_ = mw.WriteField(k, v)
		}
	}
	part, err := mw.CreateFormFile("art", "frame.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(art); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodPost, "/frames", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f fixture) framesPage(t *testing.T, cookie string) string {
	t.Helper()
	w := f.get(t, "/frames", cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /frames = %d, want 200", w.Code)
	}
	return w.Body.String()
}

func TestUploadingAPNGProducesAWorkingFrame(t *testing.T) {
	f := newFixture(t, operatorPhone)
	cookie := f.signIn(t, operatorPhone)
	csrf := csrfFrom(t, f.framesPage(t, cookie))

	w := f.upload(t, cookie, csrf, "Wisuda 2026", stripArt(t), url.Values{"group": {"wisuda"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("upload = %d, want 303: %s", w.Code, w.Body.String())
	}

	body := f.framesPage(t, cookie)
	for _, want := range []string{
		"Wisuda 2026",
		"wisuda",
		"strip2x6", // the layout, chosen by the artwork's dimensions
		"3 foto",   // the cells, read out of its transparent regions
		"/frames/wisuda-2026/art.png",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("catalogue page is missing %q", want)
		}
	}
}

// The check that matters most: a new frame is not on the booth. Cells are
// inferred from a picture, and the operator looking at the detected slots is
// what catches a wrong inference.
func TestAnUploadIsNotPublishedUntilSomebodySaysSo(t *testing.T) {
	f := newFixture(t, operatorPhone)
	cookie := f.signIn(t, operatorPhone)
	csrf := csrfFrom(t, f.framesPage(t, cookie))
	f.upload(t, cookie, csrf, "Draf", stripArt(t), nil)

	body := f.framesPage(t, cookie)
	if !strings.Contains(body, "Draf — belum di booth") {
		t.Error("a fresh upload is not shown as a draft")
	}

	if w := f.post(t, "/frames/draf/publish",
		url.Values{"csrf": {csrf}, "publish": {"1"}}, cookie); w.Code != http.StatusSeeOther {
		t.Fatalf("publish = %d, want 303: %s", w.Code, w.Body.String())
	}
	// "Terbit" and not "Tayang di booth": no booth has said anything, so
	// whether it arrived is unknown. See TestPublishedIsNotTheSameAsOnTheBooth.
	body = f.framesPage(t, cookie)
	switch {
	case strings.Contains(body, "Draf — belum di booth"):
		t.Error("the frame is still shown as a draft after publishing")
	case !strings.Contains(body, "Terbit"):
		t.Error("the frame is not shown as published after publishing")
	}
}

func TestUploadRejectsArtworkWithNothingToFill(t *testing.T) {
	f := newFixture(t, operatorPhone)
	cookie := f.signIn(t, operatorPhone)
	csrf := csrfFrom(t, f.framesPage(t, cookie))

	// A flat frame with no holes — the mistake a designer makes by exporting
	// with a white background instead of transparency.
	img := image.NewNRGBA(image.Rect(0, 0, 600, 1800))
	for y := range 1800 {
		for x := range 600 {
			img.SetNRGBA(x, y, color.NRGBA{0xff, 0xff, 0xff, 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}

	w := f.upload(t, cookie, csrf, "Polos", buf.Bytes(), nil)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("upload = %d, want a redirect carrying the error", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "transparan") {
		t.Errorf("error does not tell the operator what is wrong with the file: %q", loc)
	}
	if body := f.framesPage(t, cookie); strings.Contains(body, "Polos") {
		t.Error("the rejected frame was stored anyway")
	}
}

func TestFrameRoutesAreStaffOnly(t *testing.T) {
	f := newFixture(t, operatorPhone)
	cookie := f.signIn(t, operatorPhone)
	csrf := csrfFrom(t, f.framesPage(t, cookie))
	f.upload(t, cookie, csrf, "Rahasia", stripArt(t), nil)

	// No session at all.
	for _, path := range []string{"/frames", "/frames/rahasia/art.png"} {
		w := f.get(t, path, "")
		if w.Code != http.StatusSeeOther {
			t.Errorf("GET %s without a session = %d, want a redirect to the login page", path, w.Code)
		}
	}

	// A token that never came from a sign-in. Sessions are only ever issued to
	// an allow-listed number, so this and "no cookie at all" are the two ways a
	// stranger can arrive; that a non-operator cannot get a session in the first
	// place is TestACorrectCodeFromANonOperatorIsRefused's job.
	if w := f.get(t, "/frames", "not-a-real-session"); w.Code != http.StatusSeeOther {
		t.Errorf("GET /frames with a bogus cookie = %d, want a redirect", w.Code)
	}
}

func TestChangingAFrameNeedsTheCSRFToken(t *testing.T) {
	f := newFixture(t, operatorPhone)
	cookie := f.signIn(t, operatorPhone)
	csrf := csrfFrom(t, f.framesPage(t, cookie))
	f.upload(t, cookie, csrf, "Klasik", stripArt(t), nil)

	for _, path := range []string{
		"/frames/klasik/publish",
		"/frames/klasik/season",
		"/frames/klasik/delete",
	} {
		w := f.post(t, path, url.Values{"publish": {"1"}}, cookie)
		if w.Code != http.StatusForbidden {
			t.Errorf("POST %s without a token = %d, want 403", path, w.Code)
		}
	}
	// And the frame survived all three.
	if body := f.framesPage(t, cookie); !strings.Contains(body, "Klasik") {
		t.Error("a frame was changed by a request with no CSRF token")
	}
}

func TestSeasonRoundTripsThroughTheForm(t *testing.T) {
	f := newFixture(t, operatorPhone)
	cookie := f.signIn(t, operatorPhone)
	csrf := csrfFrom(t, f.framesPage(t, cookie))

	// The form asks for the last day the frame runs. Stored exclusively and
	// rendered back inclusively, so what an operator types is what they see.
	f.upload(t, cookie, csrf, "Ramadan 2027", stripArt(t), url.Values{
		"active_from":  {"2027-02-08"},
		"active_until": {"2027-03-09"},
	})

	body := f.framesPage(t, cookie)
	if !strings.Contains(body, `value="2027-02-08"`) {
		t.Error("start date did not round-trip")
	}
	if !strings.Contains(body, `value="2027-03-09"`) {
		t.Error("end date did not round-trip: an inclusive last day came back as something else")
	}
	if !strings.Contains(body, "8 Feb 2027 – 9 Mar 2027") {
		t.Errorf("season is not described in words on the card")
	}
}

func TestArtworkComesBackByteForByte(t *testing.T) {
	f := newFixture(t, operatorPhone)
	cookie := f.signIn(t, operatorPhone)
	csrf := csrfFrom(t, f.framesPage(t, cookie))
	art := stripArt(t)
	f.upload(t, cookie, csrf, "Klasik", art, nil)

	w := f.get(t, "/frames/klasik/art.png", cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("GET art = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("content-type = %q, want image/png", ct)
	}
	if !bytes.Equal(w.Body.Bytes(), art) {
		t.Error("the artwork served back is not the artwork uploaded")
	}
}

// report is a booth checking in with what it is offering.
func (f fixture) report(t *testing.T, outlet string, designs ...frames.Design) {
	t.Helper()
	if _, err := frames.NewBooths(f.db).Report(context.Background(), outlet, designs); err != nil {
		t.Fatalf("report %s: %v", outlet, err)
	}
}

// The failure this page was rebuilt to fix. A booth's set is the catalogue plus
// the designs compiled into its binary, so the console showed four frames while
// customers chose from eleven — and nothing anywhere said so.
func TestTheBoothSectionShowsDesignsThatAreNotInTheCatalogue(t *testing.T) {
	f := newFixture(t, operatorPhone)
	cookie := f.signIn(t, operatorPhone)

	f.report(t, "jajag", frames.Design{
		ID: "gacoan-1-taplak", Name: "Taplak Gacoan", Layout: frames.R4,
		Cells: []frames.Cell{{X: 624, Y: 147, W: 560, H: 385}},
	})

	body := f.framesPage(t, cookie)
	for _, want := range []string{"Di booth sekarang", "jajag", "Taplak Gacoan", "bawaan aplikasi booth"} {
		if !strings.Contains(body, want) {
			t.Errorf("the booth section is missing %q", want)
		}
	}
}

// Published means an operator flipped a switch. On the booth means a machine in
// a shop downloaded it. The page used to call both of them "Tayang di booth".
func TestPublishedIsNotTheSameAsOnTheBooth(t *testing.T) {
	f := newFixture(t, operatorPhone)
	cookie := f.signIn(t, operatorPhone)
	csrf := csrfFrom(t, f.framesPage(t, cookie))

	f.upload(t, cookie, csrf, "Wisuda", stripArt(t), nil)
	if w := f.post(t, "/frames/wisuda/publish",
		url.Values{"csrf": {csrf}, "publish": {"1"}}, cookie); w.Code != http.StatusSeeOther {
		t.Fatalf("publish = %d, want 303", w.Code)
	}

	// A booth has reported, and this frame is not in what it reported — so it
	// has been published and has not arrived.
	f.report(t, "jajag", frames.Design{ID: "gacoan-1-taplak", Name: "Taplak Gacoan", Layout: frames.R4})
	if body := f.framesPage(t, cookie); !strings.Contains(body, "Terbit — belum sampai di booth") {
		t.Error("a published frame no booth has is not distinguished from one on sale")
	}

	// And once the booth has it, it is genuinely on sale.
	f.report(t, "jajag",
		frames.Design{ID: "gacoan-1-taplak", Name: "Taplak Gacoan", Layout: frames.R4},
		frames.Design{ID: "wisuda", Name: "Wisuda", Layout: frames.Strip2x6})
	if body := f.framesPage(t, cookie); !strings.Contains(body, "Tayang di booth") {
		t.Error("a frame the booth reports is not shown as on the booth")
	}
}

// A booth that has stopped reporting is showing a historical list. Saying so is
// the difference between a page that is a few minutes stale and one that is a
// week stale with no way to tell.
func TestABoothThatHasGoneQuietSaysSo(t *testing.T) {
	f := newFixture(t, operatorPhone)
	cookie := f.signIn(t, operatorPhone)

	f.report(t, "jajag", frames.Design{ID: "gacoan-1-taplak", Name: "Taplak Gacoan", Layout: frames.R4})
	if _, err := f.db.Exec(`UPDATE booth_reports SET reported_at = ?`,
		time.Now().Add(-2*time.Hour).Unix()); err != nil {
		t.Fatalf("age the report: %v", err)
	}

	if body := f.framesPage(t, cookie); !strings.Contains(body, "kondisi terakhir yang diketahui") {
		t.Error("a booth that has not reported for two hours is presented as current")
	}
}

// The artwork for a built-in design has no catalogue row to hang an id off, so
// it is served by hash — and, like every other route here, only to staff.
func TestBoothArtworkIsServedByHashAndOnlyToStaff(t *testing.T) {
	f := newFixture(t, operatorPhone)
	cookie := f.signIn(t, operatorPhone)

	art := stripArt(t)
	sum := sha256.Sum256(art)
	hash := hex.EncodeToString(sum[:])
	booths := frames.NewBooths(f.db)
	f.report(t, "jajag", frames.Design{
		ID: "gacoan-1-taplak", Name: "Taplak Gacoan", Layout: frames.Strip2x6, SHA256: hash,
	})
	if err := booths.StoreArtwork(context.Background(), hash, art); err != nil {
		t.Fatalf("StoreArtwork: %v", err)
	}

	w := f.get(t, "/booth/art/"+hash, cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !bytes.Equal(w.Body.Bytes(), art) {
		t.Error("the bytes served are not the bytes the booth uploaded")
	}

	if w := f.get(t, "/booth/art/"+hash, ""); w.Code != http.StatusSeeOther {
		t.Errorf("signed out = %d, want a redirect to the login page", w.Code)
	}
}
