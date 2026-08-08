package httpapi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bhaktiyudha/bykami/api/internal/frames"
	"github.com/bhaktiyudha/bykami/api/internal/httpapi"
	"github.com/bhaktiyudha/bykami/api/internal/identity"
	"github.com/bhaktiyudha/bykami/api/internal/instagram"
	"github.com/bhaktiyudha/bykami/api/internal/loyalty"
	"github.com/bhaktiyudha/bykami/api/internal/store"
)

// newSocialAPI returns the handler and the database behind it. Rows go in
// through SQL rather than through the mirror, because what is under test here
// is the read surface — wiring a fake Instagram into an HTTP test would be
// testing internal/instagram twice and the endpoint not at all.
func newSocialAPI(t *testing.T, account string) (http.Handler, *sql.DB) {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h := httpapi.New(identity.New(db, &capturingSender{}), loyalty.New(db), frames.New(db),
		instagram.New(db), account,
		func(ctx context.Context) error { return db.PingContext(ctx) }, log, false, "")
	return h, db
}

func mirrorPost(t *testing.T, db *sql.DB, id, shortcode, kind string, media []byte, takenAt int64) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO instagram_posts
			(id, shortcode, kind, permalink, caption, media, media_type, sha256,
			 width, height, taken_at, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, 'image/jpeg', ?, 640, 800, ?, ?)`,
		id, shortcode, kind, "https://www.instagram.com/"+kind+"/"+shortcode+"/",
		"Sesi MINI", media, "hash-"+id, takenAt, takenAt)
	if err != nil {
		t.Fatal(err)
	}
}

func TestInstagramFeedIsEmptyRatherThanMissingWhenNothingIsMirrored(t *testing.T) {
	h, _ := newSocialAPI(t, "studiobykami")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/social/instagram", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — nothing mirrored yet is a normal state", w.Code)
	}
	var got struct {
		Account string `json:"account"`
		Posts   []any  `json:"posts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Posts == nil {
		t.Error("posts is null; an empty array is what lets the site render nothing without special-casing")
	}
	if got.Account != "studiobykami" {
		t.Errorf("account = %q", got.Account)
	}
}

func TestInstagramFeedListsPostsNewestFirstWithRelativeMediaPaths(t *testing.T) {
	h, db := newSocialAPI(t, "studiobykami")
	mirrorPost(t, db, "old", "Cold111", "p", []byte("jpeg-bytes-1"), 1_700_000_000)
	mirrorPost(t, db, "new", "Cnew222", "reel", []byte("jpeg-bytes-2"), 1_800_000_000)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/social/instagram", nil))

	var got struct {
		Posts []struct {
			ID     string `json:"id"`
			Kind   string `json:"kind"`
			Media  string `json:"media"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"posts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Posts) != 2 {
		t.Fatalf("got %d posts, want 2", len(got.Posts))
	}
	if got.Posts[0].ID != "new" {
		t.Errorf("first post is %q, want the newest", got.Posts[0].ID)
	}
	if got.Posts[0].Media != "/v1/social/instagram/new" {
		t.Errorf("media = %q, want a host-relative path so a build can resolve it against its own API", got.Posts[0].Media)
	}
	if got.Posts[0].Width != 640 || got.Posts[0].Height != 800 {
		t.Error("dimensions are missing; the site needs them to reserve space before the picture loads")
	}
}

func TestInstagramMediaServesBytesAndRevalidates(t *testing.T) {
	h, db := newSocialAPI(t, "studiobykami")
	mirrorPost(t, db, "1", "Cabc123", "p", []byte("jpeg-bytes"), 1_800_000_000)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/social/instagram/1", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Body.String(); got != "jpeg-bytes" {
		t.Errorf("body = %q", got)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("content-type = %q", ct)
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	// A build that already has these bytes should be told so rather than sent
	// them again.
	r := httptest.NewRequest(http.MethodGet, "/v1/social/instagram/1", nil)
	r.Header.Set("If-None-Match", etag)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r)
	if w2.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", w2.Code)
	}
}

func TestInstagramMediaToleratesAFileExtension(t *testing.T) {
	h, db := newSocialAPI(t, "studiobykami")
	mirrorPost(t, db, "1", "Cabc123", "p", []byte("jpeg-bytes"), 1_800_000_000)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/social/instagram/1.jpg", nil))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want the same picture under a name a build can save", w.Code)
	}
}

func TestInstagramMediaIsNotFoundForAWithdrawnPost(t *testing.T) {
	h, _ := newSocialAPI(t, "studiobykami")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/social/instagram/gone", nil))

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// The mirror is public by design — see social.go. This pins that down so it
// cannot be quietly changed by someone wiring the routes through a guard.
func TestTheMirrorNeedsNoToken(t *testing.T) {
	h, db := newSocialAPI(t, "studiobykami")
	mirrorPost(t, db, "1", "Cabc123", "p", []byte("jpeg-bytes"), 1_800_000_000)

	for _, path := range []string{"/v1/social/instagram", "/v1/social/instagram/1"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Errorf("%s: status = %d without a token, want 200", path, w.Code)
		}
	}
}
