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

	w := clip.NewWorker(f.clips, f.root, slog.New(slog.DiscardHandler)).
		WithSheets(f.templates, f.photos)
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
	// Embedded, so the customer sees it move without asking. It costs the page
	// roughly a megabyte per animated frame, which is the trade this booth has
	// chosen: a thumbnail that does not move does not read as a live photo, and
	// a badge nobody taps is a feature nobody knows they bought.
	if !strings.Contains(w.Body.String(), `src="/g/`+token+"/m/"+c.ID+`"`) {
		t.Fatal("the page does not show the animation")
	}

	// And the still stays one tap away beside it. The animation is an extra on
	// top of the photograph, never a replacement for it.
	if !strings.Contains(w.Body.String(), `href="/g/`+token+"/p/") {
		t.Fatal("the page dropped the still now that it shows the animation")
	}

	got := f.do(t, "GET", "/g/"+token+"/m/"+c.ID, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("serve clip: %d", got.Code)
	}
	if !bytes.HasPrefix(got.Body.Bytes(), []byte("GIF89a")) {
		t.Fatal("the clip route served something that is not a GIF")
	}

	// Displayed, never attached — for the URL the page puts in the <img>. The
	// gesture that puts this in a phone's camera roll is a long press on an
	// image the browser is showing, and Content-Disposition would break it.
	if cd := got.Header().Get("Content-Disposition"); cd != "" {
		t.Fatalf("the animation is served as an attachment (%q), which breaks Add to Photos", cd)
	}

	// The download link beside it is the other request for the same bytes, for
	// the customer who wants a file rather than a camera roll entry.
	dl := f.do(t, "GET", "/g/"+token+"/m/"+c.ID+"?dl=1", nil)
	if dl.Code != http.StatusOK {
		t.Fatalf("download clip: %d", dl.Code)
	}
	if cd := dl.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
		t.Fatalf("the download link does not attach (%q)", cd)
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

// printedSessionWithClips pays, fires the template's frames with a burst behind
// each one, and prints them once — which is the whole path an animated sheet
// needs, because the render inputs are captured at the print request and
// nowhere else.
func (f *fixture) printedSessionWithClips(t *testing.T, frames int) string {
	t.Helper()
	start := f.pay(t)

	var ids []string
	for range f.cellCount(t, start.Session.TemplateID) {
		ids = append(ids, f.shootWithClip(t, frames))
	}

	w := f.do(t, "POST", "/api/print", map[string]any{
		"template_id": start.Session.TemplateID,
		"photo_ids":   ids,
		"copies":      1,
		"filter":      "asli",
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("print: %d %s", w.Code, w.Body)
	}
	return f.shareToken(t)
}

// sheetClipID is the live session's one animated sheet.
func (f *fixture) sheetClipID(t *testing.T) string {
	t.Helper()
	sess, ok, err := f.sessions.Current(t.Context())
	if err != nil || !ok {
		t.Fatalf("current session: %v (found %v)", err, ok)
	}
	moving, err := f.clips.RenderedSheets(t.Context(), sess.ID)
	if err != nil {
		t.Fatalf("rendered sheets: %v", err)
	}
	if len(moving) != 1 {
		t.Fatalf("the session has %d animated sheets, want 1", len(moving))
	}
	for _, sc := range moving {
		return sc.ID
	}
	return ""
}

// The headline of the whole feature: not one face moving, but the frame the
// customer is holding, moving.
func TestGalleryShowsTheWholeFrameMoving(t *testing.T) {
	f := setup(t)
	token := f.printedSessionWithClips(t, 4)
	f.render(t)

	id := f.sheetClipID(t)

	body := f.do(t, "GET", "/g/"+token, nil).Body.String()
	if !strings.Contains(body, `src="/g/`+token+"/f/"+id+`"`) {
		t.Fatal("the page does not show the sheet moving")
	}
	if !strings.Contains(body, `href="/g/`+token+"/f/"+id+`?dl=1"`) {
		t.Fatal("the moving sheet cannot be downloaded")
	}
	// The still sheet is still offered. The animation is the extra.
	if !strings.Contains(body, `href="/g/`+token+"/s/") {
		t.Fatal("the page dropped the printed sheet now that it moves")
	}

	got := f.do(t, "GET", "/g/"+token+"/f/"+id, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("serve sheet animation: %d", got.Code)
	}
	if !bytes.HasPrefix(got.Body.Bytes(), []byte("GIF89a")) {
		t.Fatal("the sheet route served something that is not a GIF")
	}
	if cd := got.Header().Get("Content-Disposition"); cd != "" {
		t.Fatalf("the sheet animation attaches by default (%q), which breaks Add to Photos", cd)
	}
}

// A sheet's animation is a capability for one session, exactly as its photos
// and its clips are.
func TestGallerySeesOnlyItsOwnSessionsSheetAnimation(t *testing.T) {
	f := setup(t)

	f.printedSessionWithClips(t, 4)
	f.render(t)
	stranger := f.sheetClipID(t)
	if w := f.do(t, "POST", "/api/session/close", nil); w.Code != http.StatusOK {
		t.Fatalf("close: %d %s", w.Code, w.Body)
	}

	f.printedSessionWithClips(t, 4)
	f.render(t)
	mine := f.shareToken(t)

	if w := f.do(t, "GET", "/g/"+mine+"/f/"+stranger, nil); w.Code != http.StatusNotFound {
		t.Fatalf("one token opened another session's animated sheet: %d", w.Code)
	}
}

// A booth that never recorded a burst still prints, and its download page still
// works — it just has nothing moving on it. The sheet animation must not be a
// precondition for the sheet.
func TestAPrintWithNoClipsStillHasItsSheet(t *testing.T) {
	f := setup(t)
	token := f.printedSession(t, "")
	f.render(t)

	body := f.do(t, "GET", "/g/"+token, nil).Body.String()
	if !strings.Contains(body, `href="/g/`+token+"/s/") {
		t.Fatal("a session with no clips lost its printed sheet")
	}
	if strings.Contains(body, "/g/"+token+"/f/") {
		t.Fatal("a session with nothing moving offered an animated sheet anyway")
	}
}

// A reprint composes the identical sheet under a new job, and most packages
// include more than one print. The animation of it is a minute of a core, so
// the second job has to reuse the first one's file rather than build it again.
func TestAReprintReusesTheAnimationItAlreadyHas(t *testing.T) {
	f := setup(t)
	start := f.pay(t)

	var ids []string
	for range f.cellCount(t, start.Session.TemplateID) {
		ids = append(ids, f.shootWithClip(t, 4))
	}

	body := map[string]any{
		"template_id": start.Session.TemplateID,
		"photo_ids":   ids,
		"copies":      1,
		"filter":      "asli",
	}
	if w := f.do(t, "POST", "/api/print", body); w.Code != http.StatusAccepted {
		t.Fatalf("print: %d %s", w.Code, w.Body)
	}
	f.buyReprint(t)
	if w := f.do(t, "POST", "/api/print", body); w.Code != http.StatusAccepted {
		t.Fatalf("reprint: %d %s", w.Code, w.Body)
	}

	f.render(t)

	sess, _, err := f.sessions.Current(t.Context())
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	moving, err := f.clips.RenderedSheets(t.Context(), sess.ID)
	if err != nil {
		t.Fatalf("rendered sheets: %v", err)
	}
	if len(moving) != 2 {
		t.Fatalf("%d jobs have an animation, want both", len(moving))
	}

	paths := map[string]bool{}
	for _, sc := range moving {
		paths[sc.GIFPath] = true
	}
	if len(paths) != 1 {
		t.Fatalf("the reprint rendered its own copy: %v", paths)
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

// And the same for the fourth, which is the one this exemption most recently
// grew by. A route the guard has not been taught is served the kiosk UI, so the
// symptom is not a 404 — it is the booth's own interface on the public host.
func TestTheSheetAnimationIsReachableWithoutTheBoothsAccessToken(t *testing.T) {
	f := setupWith(t, func(d *httpd.Deps) {
		d.PublicHost = "booth-test.bykami.id"
		d.AccessTokens = []string{"s3cret"}
	})

	token := f.printedSessionWithClips(t, 4)
	f.render(t)
	id := f.sheetClipID(t)

	r := httptest.NewRequest("GET", "/g/"+token+"/f/"+id, nil)
	r.Host = "booth-test.bykami.id"
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("a customer could not fetch their own animated sheet: %d %s", w.Code, w.Body)
	}
}
