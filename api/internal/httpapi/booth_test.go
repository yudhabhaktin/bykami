package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/booking"
	"github.com/bhaktiyudha/bykami/api/internal/frames"
	"github.com/bhaktiyudha/bykami/api/internal/httpapi"
	"github.com/bhaktiyudha/bykami/api/internal/identity"
	"github.com/bhaktiyudha/bykami/api/internal/instagram"
	"github.com/bhaktiyudha/bykami/api/internal/loyalty"
	"github.com/bhaktiyudha/bykami/api/internal/store"
)

func stripPNG(t *testing.T) []byte {
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

// newBoothAPI returns the handler plus the catalogue behind it, so a test can
// publish a frame without going through the console.
func newBoothAPI(t *testing.T, token string) (http.Handler, *frames.Catalogue) {
	t.Helper()
	h, cat, _ := newBoothFleet(t, token)
	return h, cat
}

// newBoothFleet also hands back the reports store, for the two routes a booth
// writes to rather than reads.
func newBoothFleet(t *testing.T, token string) (http.Handler, *frames.Catalogue, *frames.Booths) {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cat, booths := frames.New(db), frames.NewBooths(db)
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h := httpapi.New(httpapi.Config{
		Identity: identity.New(db, &capturingSender{}), Loyalty: loyalty.New(db),
		Frames: cat, Booths: booths, Booking: booking.New(db, 0), Instagram: instagram.New(db),
		Health: func(ctx context.Context) error { return db.PingContext(ctx) }, Log: log,
		BoothToken: token,
	})
	return h, cat, booths
}

func boothGet(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const boothSecret = "booth-secret-for-tests"

func TestBoothSyncNeedsTheToken(t *testing.T) {
	h, _ := newBoothAPI(t, boothSecret)

	for _, tc := range []struct {
		name, token string
		want        int
	}{
		{"no token at all", "", http.StatusUnauthorized},
		{"the wrong token", "not-the-secret", http.StatusUnauthorized},
		// A prefix of the real secret. Worth its own case: it is what a timing
		// attack that leaked one byte at a time would produce, and the
		// constant-time comparison is what stops that being useful.
		{"a prefix of the real one", boothSecret[:8], http.StatusUnauthorized},
		{"the real token", boothSecret, http.StatusOK},
	} {
		if w := boothGet(t, h, "/v1/booth/frames", tc.token); w.Code != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, w.Code, tc.want)
		}
	}
}

// A deployment nobody configured serves no catalogue, rather than serving it to
// everybody. Same shape as the OTP gate.
func TestBoothSyncIsClosedWhenNoTokenIsConfigured(t *testing.T) {
	h, _ := newBoothAPI(t, "")

	for _, token := range []string{"", "anything"} {
		if w := boothGet(t, h, "/v1/booth/frames", token); w.Code != http.StatusServiceUnavailable {
			t.Errorf("token %q: status = %d, want 503", token, w.Code)
		}
	}
}

func TestBoothManifestCarriesWhatTheBoothNeeds(t *testing.T) {
	h, cat := newBoothAPI(t, boothSecret)
	ctx := context.Background()

	f, err := cat.Create(ctx, frames.NewFrame{Name: "Klasik Tiga", Group: "klasik", PNG: stripPNG(t)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := cat.SetPublished(ctx, f.ID, true); err != nil {
		t.Fatalf("publish: %v", err)
	}

	w := boothGet(t, h, "/v1/booth/frames", boothSecret)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	body := decodeBody[struct {
		ServerTime time.Time `json:"server_time"`
		Frames     []struct {
			ID     string                     `json:"id"`
			Name   string                     `json:"name"`
			Group  string                     `json:"group"`
			Layout string                     `json:"layout"`
			SHA256 string                     `json:"sha256"`
			Cells  []struct{ X, Y, W, H int } `json:"cells"`
		} `json:"frames"`
	}](t, w)

	if len(body.Frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(body.Frames))
	}
	got := body.Frames[0]
	switch {
	case got.ID != "klasik-tiga":
		t.Errorf("id = %q", got.ID)
	case got.Layout != "strip2x6":
		t.Errorf("layout = %q, want strip2x6", got.Layout)
	case len(got.Cells) != 3:
		t.Errorf("got %d cells, want 3", len(got.Cells))
	case got.SHA256 != f.SHA256:
		t.Errorf("hash = %q, want %q — the booth cannot tell whether it has these bytes", got.SHA256, f.SHA256)
	case body.ServerTime.IsZero():
		t.Error("no server time, so a booth cannot notice its own clock is wrong")
	}
}

// The whole point of a draft. An unpublished frame must not reach a customer,
// and "the booth would have to guess the id" is not the reason.
func TestBoothSyncNeverServesADraft(t *testing.T) {
	h, cat := newBoothAPI(t, boothSecret)
	f, err := cat.Create(context.Background(), frames.NewFrame{Name: "Draf", PNG: stripPNG(t)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if w := boothGet(t, h, "/v1/booth/frames", boothSecret); !bytes.Contains(w.Body.Bytes(), []byte(`"frames":[]`)) {
		t.Errorf("manifest lists a draft: %s", w.Body.String())
	}
	if w := boothGet(t, h, "/v1/booth/frames/"+f.ID, boothSecret); w.Code != http.StatusNotFound {
		t.Errorf("artwork for a draft = %d, want 404", w.Code)
	}
}

func TestBoothSyncRespectsTheSeason(t *testing.T) {
	h, cat := newBoothAPI(t, boothSecret)
	ctx := context.Background()

	// A window that closed yesterday: published, but out of season.
	past := time.Now().AddDate(0, 0, -30)
	f, err := cat.Create(ctx, frames.NewFrame{
		Name: "Lebaran Lalu", PNG: stripPNG(t),
		ActiveFrom: past, ActiveUntil: time.Now().AddDate(0, 0, -1),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := cat.SetPublished(ctx, f.ID, true); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if w := boothGet(t, h, "/v1/booth/frames", boothSecret); !bytes.Contains(w.Body.Bytes(), []byte(`"frames":[]`)) {
		t.Errorf("an expired frame is still offered: %s", w.Body.String())
	}
	if w := boothGet(t, h, "/v1/booth/frames/"+f.ID, boothSecret); w.Code != http.StatusNotFound {
		t.Errorf("artwork for an out-of-season frame = %d, want 404", w.Code)
	}
}

func TestBoothArtworkIsTheStoredBytesAndSkippableByHash(t *testing.T) {
	h, cat := newBoothAPI(t, boothSecret)
	ctx := context.Background()
	art := stripPNG(t)

	f, err := cat.Create(ctx, frames.NewFrame{Name: "Klasik", PNG: art})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := cat.SetPublished(ctx, f.ID, true); err != nil {
		t.Fatalf("publish: %v", err)
	}

	w := boothGet(t, h, "/v1/booth/frames/"+f.ID, boothSecret)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !bytes.Equal(w.Body.Bytes(), art) {
		t.Error("the artwork served is not the artwork stored")
	}

	// The second poll costs nothing, which is why the hash is in the manifest.
	etag := w.Header().Get("ETag")
	r := httptest.NewRequest(http.MethodGet, "/v1/booth/frames/"+f.ID, nil)
	r.Header.Set("Authorization", "Bearer "+boothSecret)
	r.Header.Set("If-None-Match", etag)
	again := httptest.NewRecorder()
	h.ServeHTTP(again, r)
	if again.Code != http.StatusNotModified {
		t.Errorf("repeat fetch = %d, want 304", again.Code)
	}
}

// A customer session is not a booth. The two credentials are different kinds of
// thing and neither route may accept the other's.
func TestACustomerTokenIsNotABoothToken(t *testing.T) {
	h, _ := newBoothAPI(t, boothSecret)
	if w := boothGet(t, h, "/v1/booth/frames", "a-perfectly-valid-looking-session-token"); w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func boothSend(t *testing.T, h http.Handler, method, path, token, ctype string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func report(t *testing.T, h http.Handler, body string) []string {
	t.Helper()
	w := boothSend(t, h, http.MethodPost, "/v1/booth/templates", boothSecret, "application/json", []byte(body))
	if w.Code != http.StatusOK {
		t.Fatalf("report = %d %s, want 200", w.Code, w.Body)
	}
	var got struct {
		Want []string `json:"want"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode report response: %v", err)
	}
	return got.Want
}

// The round trip the console depends on: a booth names a design nobody here has
// seen, is asked for the artwork, sends it, and is not asked again.
func TestABoothReportsWhatItHasAndUploadsWhatIsMissing(t *testing.T) {
	h, _, booths := newBoothFleet(t, boothSecret)
	art := stripPNG(t)
	sum := sha256.Sum256(art)
	hash := hex.EncodeToString(sum[:])

	body := `{"outlet":"jajag","templates":[{"id":"gacoan-1-taplak","name":"Taplak Gacoan",` +
		`"layout":"strip2x6","cells":[{"x":30,"y":36,"w":540,"h":450}],"sha256":"` + hash + `"}]}`

	want := report(t, h, body)
	if len(want) != 1 || want[0] != hash {
		t.Fatalf("want = %v, want the one hash the server has never seen", want)
	}

	w := boothSend(t, h, http.MethodPut, "/v1/booth/templates/art/"+hash, boothSecret, "image/png", art)
	if w.Code != http.StatusNoContent {
		t.Fatalf("upload = %d %s, want 204", w.Code, w.Body)
	}
	if want := report(t, h, body); len(want) != 0 {
		t.Errorf("want = %v after the upload, want nothing", want)
	}

	// And the console can now draw a design that exists only inside the agent
	// binary, which is the entire point of the exchange.
	got, err := booths.Artwork(context.Background(), hash)
	if err != nil {
		t.Fatalf("Artwork: %v", err)
	}
	if !bytes.Equal(got, art) {
		t.Error("stored artwork is not the artwork the booth sent")
	}
}

// Bytes that are not their hash are a truncated upload or a proxy that rewrote
// the body. Either way they are not the design the booth said it had.
func TestArtworkUploadRefusesBytesThatAreNotTheirHash(t *testing.T) {
	h, _, _ := newBoothFleet(t, boothSecret)
	sum := sha256.Sum256(stripPNG(t))
	w := boothSend(t, h, http.MethodPut, "/v1/booth/templates/art/"+hex.EncodeToString(sum[:]),
		boothSecret, "image/png", []byte("not the artwork"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// The report is not a way in. Same token rule as the read routes.
func TestReportingNeedsTheBoothToken(t *testing.T) {
	h, _, _ := newBoothFleet(t, boothSecret)
	w := boothSend(t, h, http.MethodPost, "/v1/booth/templates", "not-the-secret",
		"application/json", []byte(`{"outlet":"jajag","templates":[]}`))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// A rejected report has to say what to change, because the person who can fix
// it is reading the booth's log rather than this server's.
func TestReportRejectsANonsenseOutletAndSaysSo(t *testing.T) {
	h, _, _ := newBoothFleet(t, boothSecret)
	w := boothSend(t, h, http.MethodPost, "/v1/booth/templates", boothSecret,
		"application/json", []byte(`{"outlet":"Booth Y2K","templates":[]}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("slug")) {
		t.Errorf("body = %s, want it to name the rule that was broken", w.Body)
	}
}

// A booth reporting a layout this server cannot print is a mistake worth a
// message, not a 500 from a CHECK constraint.
func TestReportRejectsAnUnknownLayout(t *testing.T) {
	h, _, _ := newBoothFleet(t, boothSecret)
	w := boothSend(t, h, http.MethodPost, "/v1/booth/templates", boothSecret, "application/json",
		[]byte(`{"outlet":"jajag","templates":[{"id":"x","name":"X","layout":"a3","cells":[],"sha256":""}]}`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d %s, want 400", w.Code, w.Body)
	}
}
