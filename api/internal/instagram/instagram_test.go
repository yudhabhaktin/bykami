package instagram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/store"
)

// photo encodes a JPEG of the given size. The dimensions matter: the mirror
// reads them back out of the bytes rather than being told, so a test that
// asserts them is asserting the decode actually happened.
func photo(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 0x40, 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// meta is a stand-in for Meta: the feed, the refresh endpoint, and the CDN the
// pictures come from, with counters for what was actually asked for.
type meta struct {
	srv *httptest.Server

	feedJSON  string
	feedFails bool

	refreshToken string
	refreshFails bool

	// token presented on the most recent feed call.
	lastToken atomic.Value
	mediaHits atomic.Int64
	feedHits  atomic.Int64
}

func newMeta(t *testing.T) *meta {
	t.Helper()
	m := &meta{}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /me/media", func(w http.ResponseWriter, r *http.Request) {
		m.feedHits.Add(1)
		m.lastToken.Store(r.URL.Query().Get("access_token"))
		if m.feedFails {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Session has expired","type":"OAuthException","code":190}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, m.feedJSON)
	})

	mux.HandleFunc("GET /refresh_access_token", func(w http.ResponseWriter, r *http.Request) {
		if m.refreshFails {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Cannot refresh a token younger than 24 hours","type":"OAuthException","code":190}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"bearer","expires_in":5184000}`, m.refreshToken)
	})

	mux.HandleFunc("GET /cdn/{name}", func(w http.ResponseWriter, r *http.Request) {
		m.mediaHits.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(photo(640, 800))
	})

	// Something that is emphatically not a photograph, for the path that has to
	// refuse it.
	mux.HandleFunc("GET /portal", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html>please sign in to the wifi</html>")
	})

	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *meta) feed(items ...string) {
	m.feedJSON = `{"data":[` + strings.Join(items, ",") + `]}`
}

// entry builds one feed item. Signed CDN URLs are the real shape here, so the
// media url carries a query string that changes between polls — a mirror that
// keyed on the URL rather than the id would re-download on every tick.
func entry(id, shortcode, kind, caption, mediaType string, nonce int) string {
	media := fmt.Sprintf("/cdn/%s.jpg?sig=%d", shortcode, nonce)
	thumb := ""
	if mediaType == "VIDEO" {
		thumb = fmt.Sprintf(`"thumbnail_url":"CDN%s",`, media)
	}
	return fmt.Sprintf(`{"id":%q,"caption":%q,"media_type":%q,"media_url":"CDN%s",%s"permalink":"https://www.instagram.com/%s/%s/","timestamp":"2026-08-0%dT10:00:00+0000"}`,
		id, caption, mediaType, media, thumb, kind, shortcode, nonce%9+1)
}

func newWorker(t *testing.T, m *meta, seed string) (*Worker, *Cache) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	cache := New(db)
	// The feed JSON carries "CDN" where a real payload carries the CDN's host,
	// so the fake server's address can be substituted after the fact.
	m.feedJSON = strings.ReplaceAll(m.feedJSON, "CDN", m.srv.URL)

	w, err := NewWorker(context.Background(), cache, seed, m.srv.URL, time.Hour, DefaultLimit,
		slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if w == nil {
		t.Fatal("expected a worker")
	}
	return w, cache
}

func TestMirrorsPostsAndReadsDimensionsOutOfTheBytes(t *testing.T) {
	m := newMeta(t)
	m.feed(entry("1", "Cabc123", "p", "Sesi MINI", "IMAGE", 1))
	w, cache := newWorker(t, m, "seed-token")

	if err := w.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	posts, err := cache.Posts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 {
		t.Fatalf("got %d posts, want 1", len(posts))
	}
	p := posts[0]
	if p.Shortcode != "Cabc123" || p.Kind != "p" {
		t.Errorf("permalink parsed to %q/%q", p.Kind, p.Shortcode)
	}
	if p.Width != 640 || p.Height != 800 {
		t.Errorf("dimensions %dx%d, want 640x800 — these must come from the image, not the feed", p.Width, p.Height)
	}
	if p.Caption != "Sesi MINI" {
		t.Errorf("caption = %q", p.Caption)
	}

	body, mediaType, sum, err := cache.Media(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 || mediaType != "image/jpeg" || sum != p.SHA256 {
		t.Errorf("media stored as %d bytes of %q, hash %q vs %q", len(body), mediaType, sum, p.SHA256)
	}
}

func TestSecondPollDownloadsNothing(t *testing.T) {
	m := newMeta(t)
	m.feed(entry("1", "Cabc123", "p", "Sesi MINI", "IMAGE", 1))
	w, _ := newWorker(t, m, "seed-token")

	if err := w.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := m.mediaHits.Load(); got != 1 {
		t.Fatalf("first sync fetched %d pictures, want 1", got)
	}

	// The same post, re-signed — which is what Instagram actually returns.
	m.feed(entry("1", "Cabc123", "p", "Sesi MINI", "IMAGE", 7))
	m.feedJSON = strings.ReplaceAll(m.feedJSON, "CDN", m.srv.URL)

	if err := w.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := m.mediaHits.Load(); got != 1 {
		t.Errorf("second sync fetched %d pictures in total, want 1 — a re-signed URL is not new content", got)
	}
}

func TestAnEditedCaptionIsPickedUpWithoutRefetchingThePicture(t *testing.T) {
	m := newMeta(t)
	m.feed(entry("1", "Cabc123", "p", "Sesi MINI", "IMAGE", 1))
	w, cache := newWorker(t, m, "seed-token")
	if err := w.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	m.feed(entry("1", "Cabc123", "p", "Sesi MINI — promo Buy 1 Get 1", "IMAGE", 1))
	m.feedJSON = strings.ReplaceAll(m.feedJSON, "CDN", m.srv.URL)
	if err := w.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	posts, _ := cache.Posts(context.Background())
	if posts[0].Caption != "Sesi MINI — promo Buy 1 Get 1" {
		t.Errorf("caption = %q, want the edited one", posts[0].Caption)
	}
	if got := m.mediaHits.Load(); got != 1 {
		t.Errorf("fetched %d pictures, want 1 — editing a caption does not change the photograph", got)
	}
}

// The claim the whole design rests on: a failed poll leaves the mirror alone.
func TestAFailedPollKeepsWhatIsAlreadyMirrored(t *testing.T) {
	m := newMeta(t)
	m.feed(entry("1", "Cabc123", "p", "Sesi MINI", "IMAGE", 1))
	w, cache := newWorker(t, m, "seed-token")
	if err := w.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	m.feedFails = true
	if err := w.Sync(context.Background()); err == nil {
		t.Fatal("expected the failed poll to report an error")
	}

	posts, err := cache.Posts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 {
		t.Fatalf("got %d posts after a failed poll, want the 1 already mirrored", len(posts))
	}
	if _, _, _, err := cache.Media(context.Background(), "1"); err != nil {
		t.Errorf("the picture went with it: %v", err)
	}
}

func TestAWithdrawnPostIsRemovedOnASuccessfulPoll(t *testing.T) {
	m := newMeta(t)
	m.feed(
		entry("1", "Cabc123", "p", "Satu", "IMAGE", 1),
		entry("2", "Cdef456", "reel", "Dua", "VIDEO", 2),
	)
	w, cache := newWorker(t, m, "seed-token")
	if err := w.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if posts, _ := cache.Posts(context.Background()); len(posts) != 2 {
		t.Fatalf("got %d posts, want 2", len(posts))
	}

	m.feed(entry("1", "Cabc123", "p", "Satu", "IMAGE", 1))
	m.feedJSON = strings.ReplaceAll(m.feedJSON, "CDN", m.srv.URL)
	if err := w.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	posts, _ := cache.Posts(context.Background())
	if len(posts) != 1 || posts[0].ID != "1" {
		t.Errorf("got %d posts, want only the one still in the feed", len(posts))
	}
	if _, _, _, err := cache.Media(context.Background(), "2"); err != ErrNoPost {
		t.Errorf("the withdrawn post's picture is still being served: %v", err)
	}
}

// An account with no posts is a real answer and empties the mirror. The
// failed-poll half of that distinction is TestAFailedPollKeepsWhatIsAlreadyMirrored.
func TestAnEmptyFeedEmptiesTheMirror(t *testing.T) {
	m := newMeta(t)
	m.feed(entry("1", "Cabc123", "p", "Satu", "IMAGE", 1))
	w, cache := newWorker(t, m, "seed-token")
	if err := w.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	m.feed()
	if err := w.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if posts, _ := cache.Posts(context.Background()); len(posts) != 0 {
		t.Errorf("an account with no posts should mirror no posts, got %d", len(posts))
	}
}

func TestAVideoIsMirroredFromItsPosterFrame(t *testing.T) {
	m := newMeta(t)
	m.feed(entry("9", "Creel99", "reel", "Klip", "VIDEO", 3))
	w, cache := newWorker(t, m, "seed-token")
	if err := w.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	posts, _ := cache.Posts(context.Background())
	if len(posts) != 1 {
		t.Fatalf("got %d posts, want 1", len(posts))
	}
	if posts[0].Kind != "reel" {
		t.Errorf("kind = %q, want reel", posts[0].Kind)
	}
	if _, mediaType, _, err := cache.Media(context.Background(), "9"); err != nil || mediaType != "image/jpeg" {
		t.Errorf("a reel should mirror as a still: %q %v", mediaType, err)
	}
}

func TestBytesThatAreNotAPictureAreRefused(t *testing.T) {
	m := newMeta(t)
	m.feed(`{"id":"1","caption":"","media_type":"IMAGE","media_url":"CDN/portal","permalink":"https://www.instagram.com/p/Cabc123/","timestamp":"2026-08-01T10:00:00+0000"}`)
	w, cache := newWorker(t, m, "seed-token")

	if err := w.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if posts, _ := cache.Posts(context.Background()); len(posts) != 0 {
		t.Errorf("stored %d posts; a captive portal is not a photograph", len(posts))
	}
}

func TestTokenIsRefreshedBeforeItExpiresAndTheNewOneIsUsed(t *testing.T) {
	m := newMeta(t)
	m.refreshToken = "refreshed-token"
	m.feed(entry("1", "Cabc123", "p", "Satu", "IMAGE", 1))
	w, cache := newWorker(t, m, "seed-token")

	// Wind the stored expiry into the refresh window.
	if err := cache.putToken(context.Background(), "seed-token", time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := w.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	tok, expires, err := cache.token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "refreshed-token" {
		t.Errorf("stored token = %q, want the refreshed one", tok)
	}
	if time.Until(expires) < 50*24*time.Hour {
		t.Errorf("expiry not extended: %s", expires)
	}
	if got := m.lastToken.Load(); got != "refreshed-token" {
		t.Errorf("the feed was polled with %v, want the token just refreshed", got)
	}
}

func TestAFailedRefreshStillPollsWithTheTokenItHas(t *testing.T) {
	m := newMeta(t)
	m.refreshFails = true
	m.feed(entry("1", "Cabc123", "p", "Satu", "IMAGE", 1))
	w, cache := newWorker(t, m, "seed-token")

	if err := cache.putToken(context.Background(), "seed-token", time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// The refresh fails; the poll must still happen, because the current token
	// is valid until it is not.
	if err := w.Sync(context.Background()); err != nil {
		t.Fatalf("a failed refresh should not fail the sync: %v", err)
	}
	if posts, _ := cache.Posts(context.Background()); len(posts) != 1 {
		t.Errorf("got %d posts, want 1", len(posts))
	}
	if tok, _, _ := cache.token(context.Background()); tok != "seed-token" {
		t.Errorf("stored token = %q, want the original kept", tok)
	}
}

// The bug this guards is the one that only shows up on restart: re-seeding from
// the environment would overwrite a token Meta has already superseded.
func TestRestartDoesNotOverwriteARefreshedTokenWithTheSeed(t *testing.T) {
	m := newMeta(t)
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cache := New(db)
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))

	if _, err := NewWorker(context.Background(), cache, "seed-token", m.srv.URL, time.Hour, 0, log); err != nil {
		t.Fatal(err)
	}
	if err := cache.putToken(context.Background(), "refreshed-token", time.Now().Add(60*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Same environment file, second start.
	if _, err := NewWorker(context.Background(), cache, "seed-token", m.srv.URL, time.Hour, 0, log); err != nil {
		t.Fatal(err)
	}

	tok, _, err := cache.token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "refreshed-token" {
		t.Errorf("stored token = %q; restarting must not hand back the superseded seed", tok)
	}
}

func TestNoTokenMeansNoWorker(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	w, err := NewWorker(context.Background(), New(db), "", "", 0, 0,
		slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if w != nil {
		t.Error("a deployment with no token should get no worker, not a broken one")
	}
}

func TestParsePermalink(t *testing.T) {
	for _, tc := range []struct {
		raw       string
		shortcode string
		kind      string
		wantErr   bool
	}{
		{raw: "https://www.instagram.com/p/Cabc123/", shortcode: "Cabc123", kind: "p"},
		{raw: "https://www.instagram.com/reel/Cdef456/", shortcode: "Cdef456", kind: "reel"},
		{raw: "https://www.instagram.com/stories/studiobykami/123/", wantErr: true},
		{raw: "https://www.instagram.com/studiobykami/", wantErr: true},
		{raw: "https://www.instagram.com/p/", wantErr: true},
	} {
		code, kind, err := parsePermalink(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: expected an error, got %q/%q", tc.raw, kind, code)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.raw, err)
			continue
		}
		if code != tc.shortcode || kind != tc.kind {
			t.Errorf("%s: got %q/%q, want %q/%q", tc.raw, kind, code, tc.kind, tc.shortcode)
		}
	}
}

func TestFeedRequestAsksForTheFieldsTheMirrorNeeds(t *testing.T) {
	m := newMeta(t)
	m.feed()
	w, _ := newWorker(t, m, "seed-token")

	var asked url.Values
	m.srv.Config.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query()
		rw.Header().Set("Content-Type", "application/json")
		io.WriteString(rw, `{"data":[]}`)
	})

	if err := w.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"media_url", "thumbnail_url", "permalink", "timestamp", "caption", "media_type"} {
		if !strings.Contains(asked.Get("fields"), field) {
			t.Errorf("the feed request does not ask for %q", field)
		}
	}
}

// A network failure is the normal failure here, not an exotic one: this polls a
// foreign API from a shop's internet connection. net/http reports it as a
// *url.Error carrying the whole URL, and the credential is a query parameter,
// so without scrubbing every such failure writes the token to the journal.
func TestTheTokenNeverReachesAnErrorAnyoneLogs(t *testing.T) {
	const secret = "IGAAsuperSecretTokenValue123"

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cache := New(db)

	// Nothing is listening, which is what a dropped connection looks like.
	w, err := NewWorker(context.Background(), cache, secret, "http://127.0.0.1:1", time.Hour, 0,
		slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	syncErr := w.Sync(context.Background())
	if syncErr == nil {
		t.Fatal("expected the sync to fail against a dead endpoint")
	}
	if strings.Contains(syncErr.Error(), secret) {
		t.Errorf("the access token is in the error Run logs verbatim:\n%s", syncErr)
	}
	if !strings.Contains(syncErr.Error(), "[redacted]") {
		t.Errorf("expected the token to be replaced rather than dropped:\n%s", syncErr)
	}
}

// The refresh call carries the token too, and its failure is logged on its own
// path rather than through the one above.
func TestTheTokenIsScrubbedFromRefreshFailuresToo(t *testing.T) {
	const secret = "IGAArefreshPathSecret456"

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cache := New(db)

	w, err := NewWorker(context.Background(), cache, secret, "http://127.0.0.1:1", time.Hour, 0,
		slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// Due for refresh, so Sync takes that branch first.
	if err := cache.putToken(context.Background(), secret, time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	_, _, refreshErr := w.refresh(context.Background(), secret)
	if refreshErr == nil {
		t.Fatal("expected the refresh to fail")
	}
	if strings.Contains(refreshErr.Error(), secret) {
		t.Errorf("the token is in the refresh error:\n%s", refreshErr)
	}
}

// Scrubbing must not flatten the error, because Run tests for this to keep a
// normal shutdown from logging a warning.
func TestScrubbingKeepsCancellationRecognisable(t *testing.T) {
	m := newMeta(t)
	m.feed()
	w, _ := newWorker(t, m, "seed-token")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := w.Sync(ctx)
	if err == nil {
		t.Fatal("expected the cancelled sync to fail")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is lost context.Canceled through redaction: %v", err)
	}
}
