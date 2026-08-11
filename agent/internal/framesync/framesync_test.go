package framesync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/compose"
)

const token = "booth-secret"

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// catalogue is a stand-in for the cloud API, holding whatever a test publishes.
type catalogue struct {
	frames []published
	hits   int
	reject int // status to answer with instead, when non-zero
}

type published struct {
	id, name, layout string
	cells            []Cell
	art              []byte
}

func (c *catalogue) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/booth/frames", func(w http.ResponseWriter, r *http.Request) {
		c.hits++
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if c.reject != 0 {
			w.WriteHeader(c.reject)
			return
		}
		var m manifest
		m.ServerTime = time.Now().UTC()
		for _, f := range c.frames {
			sum := sha256.Sum256(f.art)
			m.Frames = append(m.Frames, struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Group  string `json:"group"`
				Layout string `json:"layout"`
				Cells  []Cell `json:"cells"`
				SHA256 string `json:"sha256"`
			}{f.id, f.name, "", f.layout, f.cells, hex.EncodeToString(sum[:])})
		}
		_ = json.NewEncoder(w).Encode(m)
	})

	mux.HandleFunc("GET /v1/booth/frames/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		for _, f := range c.frames {
			if f.id == r.PathValue("id") {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(f.art)
				return
			}
		}
		http.NotFound(w, r)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func strip(t *testing.T, tint uint8) []byte {
	t.Helper()
	// Not a real PNG for most of these tests — framesync checks the hash, not
	// the format, because compose is what decides whether a design loads. The
	// one test that needs a decodable frame builds it properly.
	return []byte(fmt.Sprintf("fake-png-bytes-%d", tint))
}

func newWorker(t *testing.T, base, dir string, reload Reload) *Worker {
	t.Helper()
	if reload == nil {
		reload = func() error { return nil }
	}
	w := New(base, token, "jajag", dir, time.Minute, reload, nil, discard())
	if w == nil {
		t.Fatal("New returned nil with a base URL and a token")
	}
	return w
}

func TestSyncInstallsAPublishedFrame(t *testing.T) {
	cat := &catalogue{frames: []published{{
		id: "wisuda-2026", name: "Wisuda 2026", layout: "strip2x6",
		cells: []Cell{{30, 36, 540, 450}, {30, 516, 540, 450}, {30, 996, 540, 450}},
		art:   strip(t, 1),
	}}}
	srv := cat.server(t)
	dir := t.TempDir()

	reloaded := 0
	w := newWorker(t, srv.URL, dir, func() error { reloaded++; return nil })
	if err := w.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	art, err := os.ReadFile(filepath.Join(dir, "wisuda-2026", "frame.png"))
	if err != nil {
		t.Fatalf("artwork not installed: %v", err)
	}
	if string(art) != string(strip(t, 1)) {
		t.Error("installed artwork differs from what was served")
	}

	// The manifest has to be exactly what compose expects, because compose is
	// what reads it back and it rejects an unknown field outright.
	var m struct {
		Name    string         `json:"name"`
		Layout  string         `json:"layout"`
		Overlay string         `json:"overlay"`
		Cells   []compose.Cell `json:"cells"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "wisuda-2026", "template.json"))
	if err != nil {
		t.Fatalf("manifest not written: %v", err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	switch {
	case m.Name != "Wisuda 2026":
		t.Errorf("name = %q", m.Name)
	case m.Layout != "strip2x6":
		t.Errorf("layout = %q", m.Layout)
	case m.Overlay != "frame.png":
		t.Errorf("overlay = %q, want frame.png", m.Overlay)
	case len(m.Cells) != 3:
		t.Errorf("got %d cells, want 3", len(m.Cells))
	case m.Cells[0] != (compose.Cell{X: 30, Y: 36, W: 540, H: 450}):
		t.Errorf("first cell = %+v", m.Cells[0])
	case reloaded != 1:
		t.Errorf("reload called %d times, want 1", reloaded)
	}
}

// The whole reason the manifest carries a hash. A booth polls all day and
// almost never finds anything new; that poll must not re-download the
// catalogue, and must not churn the template set for no reason.
func TestASecondSyncDownloadsNothingAndDoesNotReload(t *testing.T) {
	cat := &catalogue{frames: []published{{
		id: "klasik", name: "Klasik", layout: "strip2x6",
		cells: []Cell{{30, 36, 540, 450}}, art: strip(t, 2),
	}}}
	srv := cat.server(t)
	dir := t.TempDir()

	reloaded := 0
	w := newWorker(t, srv.URL, dir, func() error { reloaded++; return nil })
	ctx := context.Background()
	if err := w.Sync(ctx); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if err := w.Sync(ctx); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if reloaded != 1 {
		t.Errorf("reload called %d times, want 1 — an unchanged catalogue was reinstalled", reloaded)
	}
}

// Unpublishing in the console has to take the frame off the booth. Otherwise
// "withdraw" means "withdraw from new booths".
func TestWithdrawingAFrameRemovesItFromTheBooth(t *testing.T) {
	cat := &catalogue{frames: []published{
		{id: "keep", name: "Keep", layout: "strip2x6", cells: []Cell{{30, 36, 540, 450}}, art: strip(t, 3)},
		{id: "drop", name: "Drop", layout: "strip2x6", cells: []Cell{{30, 36, 540, 450}}, art: strip(t, 4)},
	}}
	srv := cat.server(t)
	dir := t.TempDir()

	w := newWorker(t, srv.URL, dir, nil)
	ctx := context.Background()
	if err := w.Sync(ctx); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	cat.frames = cat.frames[:1] // "drop" is unpublished
	if err := w.Sync(ctx); err != nil {
		t.Fatalf("second sync: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "drop")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a withdrawn frame is still installed on the booth")
	}
	if _, err := os.Stat(filepath.Join(dir, "keep", "frame.png")); err != nil {
		t.Errorf("the frame that is still published was removed too: %v", err)
	}
}

func TestChangedArtworkIsReinstalled(t *testing.T) {
	cat := &catalogue{frames: []published{{
		id: "klasik", name: "Klasik", layout: "strip2x6",
		cells: []Cell{{30, 36, 540, 450}}, art: strip(t, 5),
	}}}
	srv := cat.server(t)
	dir := t.TempDir()

	w := newWorker(t, srv.URL, dir, nil)
	ctx := context.Background()
	if err := w.Sync(ctx); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	cat.frames[0].art = strip(t, 6)
	if err := w.Sync(ctx); err != nil {
		t.Fatalf("second sync: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "klasik", "frame.png"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(strip(t, 6)) {
		t.Error("the booth is still serving the old artwork")
	}
}

// A body that does not match the advertised hash is a truncated download or
// something on the path rewriting responses. Installing it would put artwork on
// the printer that the catalogue never approved.
func TestArtworkThatDoesNotMatchItsHashIsRefused(t *testing.T) {
	dir := t.TempDir()

	// Serve a manifest whose hash belongs to different bytes.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/booth/frames", func(w http.ResponseWriter, r *http.Request) {
		sum := sha256.Sum256([]byte("the bytes the catalogue approved"))
		fmt.Fprintf(w, `{"server_time":%q,"frames":[{"id":"tampered","name":"T","layout":"strip2x6",`+
			`"cells":[{"x":30,"y":36,"w":540,"h":450}],"sha256":%q}]}`,
			time.Now().UTC().Format(time.RFC3339), hex.EncodeToString(sum[:]))
	})
	mux.HandleFunc("GET /v1/booth/frames/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("something else entirely"))
	})
	bad := httptest.NewServer(mux)
	defer bad.Close()

	reloaded := 0
	w := newWorker(t, bad.URL, dir, func() error { reloaded++; return nil })
	if err := w.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tampered", "frame.png")); !errors.Is(err, os.ErrNotExist) {
		t.Error("artwork that failed its hash check was installed anyway")
	}
	if reloaded != 0 {
		t.Error("reloaded after installing nothing")
	}
}

// The manifest is data from the network. The catalogue restricts ids and the
// schema restricts them again, but this is the process that joins one onto a
// path.
func TestAnIdThatIsNotASafeDirectoryNameIsRefused(t *testing.T) {
	dir := t.TempDir()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/booth/frames", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"server_time":%q,"frames":[{"id":"../../escaped","name":"E","layout":"strip2x6",`+
			`"cells":[{"x":30,"y":36,"w":540,"h":450}],"sha256":"deadbeef"}]}`,
			time.Now().UTC().Format(time.RFC3339))
	})
	mux.HandleFunc("GET /v1/booth/frames/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	w := newWorker(t, srv.URL, dir, nil)
	if err := w.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(dir)), "escaped")); err == nil {
		t.Fatal("a frame was written outside the templates directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("wrote %d entries for a frame with an unsafe id", len(entries))
	}
}

func TestSafeID(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"wisuda-2026", true},
		{"a", true},
		{"", false},
		{"../escape", false},
		{"has/slash", false},
		{"has\\backslash", false},
		{"Has-Capitals", false},
		{"has space", false},
		{".", false},
		{"..", false},
		{"has.dot", false},
	} {
		if got := safeID(tc.id); got != tc.want {
			t.Errorf("safeID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// A booth with no internet keeps selling. This is the property the whole
// package is arranged around.
func TestAFailedSyncLeavesTheInstalledFramesAlone(t *testing.T) {
	cat := &catalogue{frames: []published{{
		id: "klasik", name: "Klasik", layout: "strip2x6",
		cells: []Cell{{30, 36, 540, 450}}, art: strip(t, 7),
	}}}
	srv := cat.server(t)
	dir := t.TempDir()

	w := newWorker(t, srv.URL, dir, nil)
	ctx := context.Background()
	if err := w.Sync(ctx); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// The server starts refusing — a rotated token, or the VPS in trouble.
	cat.reject = http.StatusUnauthorized
	if err := w.Sync(ctx); err == nil {
		t.Fatal("a refused sync reported success")
	}

	if _, err := os.Stat(filepath.Join(dir, "klasik", "frame.png")); err != nil {
		t.Errorf("the booth lost its frames when the server refused it: %v", err)
	}
}

func TestNewReturnsNilWhenSyncingIsNotConfigured(t *testing.T) {
	for _, tc := range []struct{ base, tok string }{
		{"", ""},
		{"https://app.bykami.id", ""},
		{"", "a-token"},
	} {
		if w := New(tc.base, tc.tok, "jajag", t.TempDir(), time.Minute, nil, nil, discard()); w != nil {
			t.Errorf("New(%q, %q) returned a worker; an unenrolled booth should not poll", tc.base, tc.tok)
		}
	}
}
