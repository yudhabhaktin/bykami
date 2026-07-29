// Package framesync keeps the booth's frame catalogue in step with the cloud.
//
// Frames are the shared asset a franchise consumes: drawn once, published once,
// and offered by every outlet. This is the half that runs in the shop.
//
// # It is a cache, not a dependency
//
// The booth composes and prints from files on its own disk. A sync that fails —
// no internet, the VPS down, a rotated token nobody told the booth about — is a
// booth that keeps offering the frames it already has, because the alternative
// is a photobooth that stops selling when a server in Singapore does. Nothing
// here is on the request path; the worker writes to a directory and swaps the
// live set, and every failure is logged and retried at the next tick.
//
// # Why it writes files rather than holding frames in memory
//
// compose already loads a template from any fs.FS, and a directory of
// directories is what it reads at startup. Writing there means a synced frame
// and a hand-installed one are the same thing to everything downstream, and a
// booth that has been offline since it was last switched on still has the
// catalogue on disk.
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
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultInterval between polls.
//
// Five minutes because publishing a frame is not urgent and a booth is one of
// several: the console tells the operator the booth will pick it up in a few
// minutes, which is true and is a promise a shorter interval would not make
// meaningfully truer. It is also 288 requests a day against a 1 GB box.
const DefaultInterval = 5 * time.Minute

// maxArtwork bounds a single download. The catalogue refuses anything larger on
// the way in, so a bigger response means something is answering that is not the
// catalogue — a captive portal, a proxy error page — and it should be refused
// rather than written to disk as a frame.
const maxArtwork = 8 << 20

type cell struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type manifest struct {
	ServerTime time.Time `json:"server_time"`
	Frames     []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Group  string `json:"group"`
		Layout string `json:"layout"`
		Cells  []cell `json:"cells"`
		SHA256 string `json:"sha256"`
	} `json:"frames"`
}

// Reload is called after the directory changes, with nothing else to say. The
// worker does not know how to build a template set — that is main's job, and
// keeping it there is what stops this package importing half the binary.
type Reload func() error

type Worker struct {
	base     string
	token    string
	dir      string
	interval time.Duration
	reload   Reload
	log      *slog.Logger
	client   *http.Client
}

// New returns a worker, or nil if syncing is not configured.
//
// Nil rather than an error: no base URL and no token is the normal state of a
// booth that has not been enrolled, and it should start and sell photos.
func New(base, token, dir string, interval time.Duration, reload Reload, log *slog.Logger) *Worker {
	if base == "" || token == "" {
		return nil
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Worker{
		base: strings.TrimSuffix(base, "/"), token: token, dir: dir,
		interval: interval, reload: reload, log: log,
		// Bounded, because this runs behind a shop's internet connection and a
		// stalled poll must not hold a goroutine until the process restarts.
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

// Run polls until ctx is cancelled. It syncs once immediately: a booth that has
// just been switched on has most likely been off for a while.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.interval)
	defer t.Stop()

	for {
		if err := w.Sync(ctx); err != nil && !errors.Is(err, context.Canceled) {
			// Logged and dropped. The booth keeps the frames it has; see the
			// package doc for why this is never fatal.
			w.log.Warn("frame sync failed; keeping the frames already installed", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Sync fetches the manifest, writes what changed, and reloads.
func (w *Worker) Sync(ctx context.Context) error {
	m, err := w.manifest(ctx)
	if err != nil {
		return err
	}

	if skew := time.Since(m.ServerTime); skew > time.Hour || skew < -time.Hour {
		// Not corrected, only reported. Seasons are resolved on the server for
		// exactly this reason, so a wrong clock here changes nothing about
		// which frames arrive — but it will make every timestamp in this
		// booth's logs misleading, and somebody should know.
		w.log.Warn("this booth's clock disagrees with the server", "skew", skew.Round(time.Second))
	}

	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return fmt.Errorf("framesync: template directory: %w", err)
	}

	changed := 0
	keep := make(map[string]bool, len(m.Frames))
	for _, f := range m.Frames {
		// The id becomes a directory name. The catalogue restricts it at the
		// source and the schema restricts it again, but this is the process
		// that joins it onto a path — so it is checked here too, where the
		// damage would be done.
		if !safeID(f.ID) {
			w.log.Warn("refusing a frame whose id is not a safe directory name", "id", f.ID)
			continue
		}
		keep[f.ID] = true

		wrote, err := w.frame(ctx, f.ID, f.Name, f.Layout, f.SHA256, cellsJSON(f.Cells))
		if err != nil {
			// One bad frame does not abandon the rest: the others are fine and
			// the booth should get them.
			w.log.Warn("could not install a frame", "frame", f.ID, "err", err)
			continue
		}
		if wrote {
			changed++
		}
	}

	removed, err := w.prune(keep)
	if err != nil {
		w.log.Warn("could not remove withdrawn frames", "err", err)
	}

	if changed == 0 && removed == 0 {
		return nil
	}
	w.log.Info("frame catalogue updated", "installed", changed, "removed", removed, "total", len(keep))
	return w.reload()
}

func (w *Worker) manifest(ctx context.Context) (manifest, error) {
	var m manifest
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.base+"/v1/booth/frames", nil)
	if err != nil {
		return m, err
	}
	req.Header.Set("Authorization", "Bearer "+w.token)

	res, err := w.client.Do(req)
	if err != nil {
		return m, fmt.Errorf("framesync: fetch manifest: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return m, fmt.Errorf("framesync: manifest: %s", res.Status)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&m); err != nil {
		return m, fmt.Errorf("framesync: decode manifest: %w", err)
	}
	return m, nil
}

// frame installs one design, and reports whether anything was written.
func (w *Worker) frame(ctx context.Context, id, name, layout, sum, cells string) (bool, error) {
	dir := filepath.Join(w.dir, id)
	art := filepath.Join(dir, "frame.png")

	// The hash is the whole sync protocol. Unchanged artwork means the common
	// poll costs one small manifest and no downloads at all.
	if have, err := fileSHA256(art); err == nil && have == sum {
		return false, nil
	}

	body, err := w.artwork(ctx, id)
	if err != nil {
		return false, err
	}
	got := sha256.Sum256(body)
	if hex.EncodeToString(got[:]) != sum {
		// A truncated download or a proxy that rewrote the body. Writing it
		// would install artwork the catalogue never approved.
		return false, errors.New("artwork does not match the hash in the manifest")
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	if err := writeFile(art, body); err != nil {
		return false, err
	}

	// The manifest is written last. compose skips a directory without one, so
	// a crash between the two leaves a frame that is ignored rather than one
	// whose cells describe artwork that is not there.
	m := fmt.Sprintf(`{"name":%q,"layout":%q,"overlay":"frame.png","cells":%s}`, name, layout, cells)
	if err := writeFile(filepath.Join(dir, "template.json"), []byte(m)); err != nil {
		return false, err
	}
	return true, nil
}

func (w *Worker) artwork(ctx context.Context, id string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.base+"/v1/booth/frames/"+id, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+w.token)

	res, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("artwork: %s", res.Status)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxArtwork+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxArtwork {
		return nil, errors.New("artwork is larger than the catalogue accepts")
	}
	return body, nil
}

// prune removes frames the manifest no longer lists, which is how unpublishing
// one in the console takes it off the booth.
func (w *Worker) prune(keep map[string]bool) (int, error) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if !e.IsDir() || keep[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(w.dir, e.Name())); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// safeID is the same rule the catalogue applies, restated where a path is
// actually built. A manifest is data from the network; that it is served by our
// own API is a reason to expect it to be well formed, not a reason to skip the
// check that stops it writing outside this directory.
func safeID(id string) bool {
	if id == "" || len(id) > 100 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

func cellsJSON(cells []cell) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, c := range cells {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"x":%d,"y":%d,"w":%d,"h":%d}`, c.X, c.Y, c.W, c.H)
	}
	b.WriteByte(']')
	return b.String()
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeFile replaces path atomically. A booth loses power; a half-written PNG
// that compose then refuses is a design missing from the screen with no
// explanation on it.
func writeFile(path string, b []byte) error {
	tmp := path + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
