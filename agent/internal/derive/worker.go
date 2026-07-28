package derive

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/photo"
)

// Dir is where derivatives live, relative to the store root.
//
// A tree of their own, and this is load-bearing rather than tidy. ingest.Recover
// walks sessions/ and unassigned/ and records every JPEG it finds without a row
// — so a derivative filed beside its original would be ingested as a brand new
// photo on the next restart, and the customer's strip would fill up with
// half-resolution copies of frames they already had.
const Dir = "derived"

// Unassigned mirrors ingest.UnassignedDir so an orphan's derivative has
// somewhere to go that is not a directory named after a session that never
// existed.
const Unassigned = "unassigned"

// DefaultInterval is how often the queue is checked. Short, because the review
// screen is the next thing the customer sees after the shutter and it is the
// screen these files exist to make fast.
const DefaultInterval = time.Second

// DefaultBatch caps one pass. A restart after a busy day has a backlog, and
// working through it in batches keeps a single sweep from holding the CPU while
// somebody is mid-session.
const DefaultBatch = 8

// Worker builds the delivered derivative of every frame, in the background.
//
// Background and not inline in the capture handler on purpose: File takes a few
// hundred milliseconds on a 24 MP original, and the capture handler is on the
// shutter path — the one place in the booth where latency is the product.
type Worker struct {
	photos *photo.Store
	root   string
	log    *slog.Logger

	interval time.Duration
	batch    int
	opts     Options

	// broken remembers frames that failed, so a file that cannot be decoded is
	// reported once instead of every interval for the seven days until its row
	// is purged. In memory because a restart is exactly when it is worth trying
	// again — the failure may have been a full disk.
	broken map[string]bool
}

func NewWorker(photos *photo.Store, root string, log *slog.Logger) *Worker {
	return &Worker{
		photos:   photos,
		root:     root,
		log:      log,
		interval: DefaultInterval,
		batch:    DefaultBatch,
		broken:   map[string]bool{},
	}
}

// Run derives until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, err := w.Sweep(ctx); err != nil {
				w.log.Error("derive: sweep", "err", err)
			}
		}
	}
}

// Sweep derives one batch and returns how many it wrote. Exported so a test does
// not have to race a ticker.
func (w *Worker) Sweep(ctx context.Context) (int, error) {
	// One extra so a batch made entirely of known-broken frames still reaches
	// the ones behind them instead of stalling the queue forever.
	due, err := w.photos.Underived(ctx, w.batch+len(w.broken))
	if err != nil {
		return 0, err
	}

	n := 0
	for _, p := range due {
		if w.broken[p.ID] {
			continue
		}
		if n >= w.batch {
			break
		}
		switch err := w.one(ctx, p); {
		case err == nil:
			n++
		case errors.Is(err, fs.ErrNotExist):
			// The original is gone but the row is not purged. Nothing to derive
			// from and nothing that will bring it back, so stop asking.
			w.broken[p.ID] = true
			w.log.Warn("derive: original is missing", "photo", p.ID, "path", p.Path)
		default:
			w.broken[p.ID] = true
			w.log.Error("derive: failed", "photo", p.ID, "path", p.Path, "err", err)
		}
	}
	return n, nil
}

func (w *Worker) one(ctx context.Context, p photo.Photo) error {
	src := filepath.Join(w.root, filepath.FromSlash(p.Path))
	if _, err := os.Stat(src); err != nil {
		return err
	}

	rel := DerivedPath(p)
	dest := filepath.Join(w.root, filepath.FromSlash(rel))

	size, err := File(src, dest, w.opts)
	if err != nil {
		return err
	}

	// The row is written after the file exists, the same ordering ingest uses.
	// A crash in between leaves an unreferenced file that the next sweep
	// overwrites; the other order would point a row at nothing.
	if err := w.photos.SetDerived(ctx, p.ID, rel); err != nil {
		return err
	}

	w.log.Debug("derive: wrote", "photo", p.ID,
		"from", fmt.Sprintf("%dx%d", p.Width, p.Height),
		"to", fmt.Sprintf("%dx%d", size.X, size.Y))
	return nil
}

// DerivedPath is where a frame's derivative belongs, relative to the store root.
//
// Flat under Dir, one directory per session, so that purge's empty-directory
// prune reaches these the same way it reaches sessions/ and sheets/.
func DerivedPath(p photo.Photo) string {
	dir := p.SessionID
	if dir == "" {
		dir = Unassigned
	}
	return filepath.ToSlash(filepath.Join(Dir, dir, filepath.Base(p.Path)))
}
