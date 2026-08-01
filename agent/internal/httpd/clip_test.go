package httpd_test

import (
	"bytes"
	"context"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bhaktiyudha/bykami/agent/internal/clip"
	"github.com/bhaktiyudha/bykami/agent/internal/httpd"
)

// burst builds the multipart body the kiosk posts after a shutter: n JPEGs,
// every part named "frame", in playback order.
func burst(t *testing.T, n int) (string, []byte) {
	t.Helper()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for i := range n {
		part, err := mw.CreateFormFile("frame", clip.FrameName(i))
		if err != nil {
			t.Fatalf("create part: %v", err)
		}
		if _, err := part.Write(frameBytes(t, 320, 240)); err != nil {
			t.Fatalf("write part: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return mw.FormDataContentType(), body.Bytes()
}

// postBurst sends one, which f.do cannot do: it decides the content type from
// the body's Go type, and multipart carries a generated boundary.
func (f *fixture) postBurst(t *testing.T, photoID string, n int) *httptest.ResponseRecorder {
	t.Helper()

	ctype, body := burst(t, n)
	r := httptest.NewRequest("POST", "/api/capture/"+photoID+"/clip", bytes.NewReader(body))
	r.Header.Set("Content-Type", ctype)
	r.Host = "localhost:8899"

	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)
	return w
}

// shootWithClip fires one frame and posts its burst, returning the photo id.
func (f *fixture) shootWithClip(t *testing.T, frames int) string {
	t.Helper()

	w := f.do(t, "POST", "/api/capture", frameBytes(t, 1600, 1200))
	if w.Code != http.StatusCreated {
		t.Fatalf("capture: %d %s", w.Code, w.Body)
	}
	id := decode[struct {
		Photo struct {
			ID string `json:"id"`
		} `json:"photo"`
	}](t, w).Photo.ID

	if got := f.postBurst(t, id, frames); got.Code != http.StatusCreated {
		t.Fatalf("post burst: %d %s", got.Code, got.Body)
	}
	return id
}

// render drains the queue, so a test can assert on what a customer sees rather
// than racing the background worker.
func (f *fixture) render(t *testing.T) {
	t.Helper()

	w := clip.NewWorker(f.clips, f.root, slog.New(slog.DiscardHandler))
	for range 20 {
		n, err := w.Sweep(context.Background())
		if err != nil {
			t.Fatalf("render sweep: %v", err)
		}
		if n == 0 {
			return
		}
	}
	t.Fatal("the render queue never drained")
}

func TestBurstIsStoredAgainstItsFrame(t *testing.T) {
	f := setup(t)
	f.pay(t)

	id := f.shootWithClip(t, 5)

	c, err := f.clips.ByPhoto(t.Context(), id)
	if err != nil {
		t.Fatalf("by photo: %v", err)
	}
	if c.Frames != 5 {
		t.Fatalf("recorded %d frames, want 5", c.Frames)
	}

	dir := filepath.Join(f.root, filepath.FromSlash(c.Dir))
	for i := range 5 {
		if _, err := os.Stat(filepath.Join(dir, clip.FrameName(i))); err != nil {
			t.Fatalf("frame %d is not on disk: %v", i, err)
		}
	}
}

// The burst must not appear in the strip picker or count against the take
// limit. Fifty frames a shot would exhaust a fifteen-take session on the
// second one, and fill the review screen with near-identical thumbnails.
func TestBurstFramesAreNotPhotos(t *testing.T) {
	f := setup(t)
	f.pay(t)

	f.shootWithClip(t, 12)

	if ids := f.photoIDs(t); len(ids) != 1 {
		t.Fatalf("the session holds %d photos, want the 1 that was shot", len(ids))
	}
}

// A burst can only be filed against a frame from the session that is live.
// Without that check one customer's five seconds could be attached to another
// customer's photograph.
func TestBurstIsRefusedForAnotherSessionsFrame(t *testing.T) {
	f := setup(t)

	f.pay(t)
	stranger := f.shootWithClip(t, 4)
	if w := f.do(t, "POST", "/api/session/close", nil); w.Code != http.StatusOK {
		t.Fatalf("close: %d %s", w.Code, w.Body)
	}

	f.pay(t)
	if w := f.postBurst(t, stranger, 4); w.Code != http.StatusNotFound {
		t.Fatalf("post burst: %d %s, want 404", w.Code, w.Body)
	}
}

// The kiosk posts these without waiting for a response, so a retry of a request
// whose answer was never seen is ordinary. It must not become a second copy of
// the same five seconds, nor rewrite frames underneath a render in flight.
func TestASecondBurstForOneFrameIsAccepted(t *testing.T) {
	f := setup(t)
	f.pay(t)

	id := f.shootWithClip(t, 4)

	w := f.postBurst(t, id, 4)
	if w.Code != http.StatusOK {
		t.Fatalf("second burst: %d %s, want 200", w.Code, w.Body)
	}

	c, err := f.clips.ByPhoto(t.Context(), id)
	if err != nil {
		t.Fatalf("by photo: %v", err)
	}
	if c.Frames != 4 {
		t.Fatalf("the clip now has %d frames, want the original 4", c.Frames)
	}
}

// Nothing to animate, and nothing left behind: a directory with no row is
// invisible to purge, which walks rows, so it would hold a customer's face on
// the booth PC until somebody found it by hand.
func TestATooShortBurstLeavesNothingOnDisk(t *testing.T) {
	f := setup(t)
	f.pay(t)

	w := f.do(t, "POST", "/api/capture", frameBytes(t, 1600, 1200))
	if w.Code != http.StatusCreated {
		t.Fatalf("capture: %d %s", w.Code, w.Body)
	}
	id := decode[struct {
		Photo struct {
			ID string `json:"id"`
		} `json:"photo"`
	}](t, w).Photo.ID

	if got := f.postBurst(t, id, 1); got.Code != http.StatusBadRequest {
		t.Fatalf("one-frame burst: %d %s, want 400", got.Code, got.Body)
	}

	if _, err := f.clips.ByPhoto(t.Context(), id); err == nil {
		t.Fatal("a refused burst still recorded a clip")
	}
	if _, err := os.Stat(filepath.Join(f.root, "clips")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(f.root, "clips"))
		for _, e := range entries {
			inner, _ := os.ReadDir(filepath.Join(f.root, "clips", e.Name()))
			if len(inner) > 0 {
				t.Fatalf("a refused burst left %d directories on disk", len(inner))
			}
		}
	}
}

func TestGalleryOffersTheMovingVersion(t *testing.T) {
	f := setup(t)
	f.pay(t)

	id := f.shootWithClip(t, 4)
	f.render(t)
	token := f.shareToken(t)

	w := f.do(t, "GET", "/g/"+token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("gallery: %d", w.Code)
	}

	c, err := f.clips.ByPhoto(t.Context(), id)
	if err != nil {
		t.Fatalf("by photo: %v", err)
	}
	if !strings.Contains(w.Body.String(), "/g/"+token+"/m/"+c.ID) {
		t.Fatal("the page does not link the animation")
	}

	// Linked, not embedded. An <img> pointing at the animation would pull well
	// over a megabyte per frame before the customer asked for it.
	if strings.Contains(w.Body.String(), `src="/g/`+token+"/m/") {
		t.Fatal("the page embeds the animation instead of linking it")
	}

	got := f.do(t, "GET", "/g/"+token+"/m/"+c.ID, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("serve clip: %d", got.Code)
	}
	if !bytes.HasPrefix(got.Body.Bytes(), []byte("GIF89a")) {
		t.Fatal("the clip route served something that is not a GIF")
	}

	// Displayed, never attached. The gesture that puts this in a phone's camera
	// roll is a long press on an image the browser is showing.
	if cd := got.Header().Get("Content-Disposition"); cd != "" {
		t.Fatalf("the animation is served as an attachment (%q), which breaks Add to Photos", cd)
	}
}

// The token names one session. It must not open another session's animations
// any more than it opens their photographs.
func TestGallerySeesOnlyItsOwnSessionsClips(t *testing.T) {
	f := setup(t)

	f.pay(t)
	stranger := f.shootWithClip(t, 4)
	f.render(t)
	strangerClip, err := f.clips.ByPhoto(t.Context(), stranger)
	if err != nil {
		t.Fatalf("by photo: %v", err)
	}
	if w := f.do(t, "POST", "/api/session/close", nil); w.Code != http.StatusOK {
		t.Fatalf("close: %d %s", w.Code, w.Body)
	}

	f.pay(t)
	f.shootWithClip(t, 4)
	f.render(t)
	mine := f.shareToken(t)

	if w := f.do(t, "GET", "/g/"+mine+"/m/"+strangerClip.ID, nil); w.Code != http.StatusNotFound {
		t.Fatalf("one token opened another session's animation: %d", w.Code)
	}
}

// Every gallery route has to be taught to isGalleryPath, or the guard serves
// the booth's own interface in its place. This is that check for the third one.
func TestTheClipRouteIsReachableWithoutTheBoothsAccessToken(t *testing.T) {
	f := setupWith(t, func(d *httpd.Deps) {
		d.PublicHost = "booth-test.bykami.id"
		d.AccessTokens = []string{"s3cret"}
	})
	f.pay(t)

	id := f.shootWithClip(t, 4)
	f.render(t)
	token := f.shareToken(t)

	c, err := f.clips.ByPhoto(t.Context(), id)
	if err != nil {
		t.Fatalf("by photo: %v", err)
	}

	r := httptest.NewRequest("GET", "/g/"+token+"/m/"+c.ID, nil)
	r.Host = "booth-test.bykami.id"
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("a customer could not fetch their own animation: %d %s", w.Code, w.Body)
	}
}
