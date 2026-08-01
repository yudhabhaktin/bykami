package clip

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// DefaultInterval is how often the render queue is checked.
//
// A second, like the derive worker, but the reason is different. A derivative
// is wanted immediately — the review screen is the next thing the customer
// sees. Nobody is waiting on a GIF: it is wanted at the delivery screen, a
// minute or two later, and it lives for seven days after that. The second is
// here so the queue drains steadily during the session rather than in a burst
// at the end.
const DefaultInterval = time.Second

// DefaultBatch caps one pass, and it is one.
//
// A five-second clip is fifty frames to decode, scale, dither and encode —
// measured at two to three seconds of one core. That is far and away the
// heaviest background job on the booth, and doing several per pass would take
// the CPU in a burst while somebody is mid-session. One at a time still keeps
// pace with capture, because a shot costs about five seconds of countdown and
// hold before the next one arrives.
const DefaultBatch = 1

// Worker renders every clip's animation, in the background.
//
// Background for the same reason derive is: the shutter path is where latency
// is the product, and this is seconds of CPU per frame captured.
type Worker struct {
	clips *Store
	root  string
	log   *slog.Logger

	interval time.Duration
	batch    int
	opts     Options

	// broken remembers clips that failed, so a clip whose frames will not
	// decode is reported once instead of every second for the seven days until
	// its row is purged. In memory because a restart is exactly when it is
	// worth trying again — the failure may have been a full disk.
	broken map[string]bool
}

func NewWorker(clips *Store, root string, log *slog.Logger) *Worker {
	return &Worker{
		clips:    clips,
		root:     root,
		log:      log,
		interval: DefaultInterval,
		batch:    DefaultBatch,
		broken:   map[string]bool{},
	}
}

// Run renders until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, err := w.Sweep(ctx); err != nil {
				w.log.Error("clip: sweep", "err", err)
			}
		}
	}
}

// Sweep renders one batch and returns how many it wrote. Exported so a test
// does not have to race a ticker.
func (w *Worker) Sweep(ctx context.Context) (int, error) {
	// One extra so a batch made entirely of known-broken clips still reaches
	// the ones behind them instead of stalling the queue forever.
	due, err := w.clips.Unrendered(ctx, w.batch+len(w.broken))
	if err != nil {
		return 0, err
	}

	n := 0
	for _, c := range due {
		if w.broken[c.ID] {
			continue
		}
		if n >= w.batch {
			break
		}
		switch err := w.one(ctx, c); {
		case err == nil:
			n++
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, ErrTooShort):
			// The frames are gone, or there were never enough of them to
			// animate — an upload cut off halfway when a customer's browser
			// moved on. Nothing will bring them back, so stop asking.
			w.broken[c.ID] = true
			w.log.Warn("clip: nothing to animate", "clip", c.ID, "dir", c.Dir, "err", err)
		default:
			w.broken[c.ID] = true
			w.log.Error("clip: render failed", "clip", c.ID, "dir", c.Dir, "err", err)
		}
	}
	return n, nil
}

func (w *Worker) one(ctx context.Context, c Clip) error {
	dir := filepath.Join(w.root, filepath.FromSlash(c.Dir))
	if _, err := os.Stat(dir); err != nil {
		return err
	}

	// Built from the recorded count rather than by listing the directory: the
	// row is written only after every frame has landed, so the count is the
	// truth about what this clip is, and a stray file dropped in beside them
	// can never join the animation.
	frames := make([]string, 0, c.Frames)
	for i := range c.Frames {
		frames = append(frames, filepath.Join(dir, FrameName(i)))
	}

	rel := GIFPathFor(c.SessionID, c.PhotoID)
	dest := filepath.Join(w.root, filepath.FromSlash(rel))

	started := time.Now()
	if err := Render(frames, dest, w.opts); err != nil {
		return err
	}

	// The row is written after the file exists, the same ordering ingest and
	// derive use. A crash in between leaves an unreferenced GIF that the next
	// sweep overwrites; the other order would point a row at nothing, and the
	// download page would show a customer a broken image.
	if err := w.clips.SetGIF(ctx, c.ID, rel); err != nil {
		return err
	}

	w.log.Debug("clip: rendered", "clip", c.ID, "frames", c.Frames, "took", time.Since(started))
	return nil
}
