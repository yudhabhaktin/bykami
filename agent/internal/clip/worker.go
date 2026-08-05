package clip

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/compose"
	"github.com/bhaktiyudha/bykami/agent/internal/photo"
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
// A five-second clip is a hundred frames to decode, scale, quantise and encode,
// twice over — once to choose the palette and once to write it. That is far and
// away the heaviest background job on the booth, and doing several per pass
// would take the CPU in a burst while somebody is mid-session. One at a time
// still keeps pace with capture, because a shot costs about five seconds of
// countdown and hold before the next one arrives.
//
// It is cheaper than it looks, and cheaper than what it replaced: the palette
// lookup in quantize.go turns the per-pixel cost from a scan of 256 colours into
// an array read, which on the same machine renders five times the pixels in less
// time than the old fixed-palette version took.
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

	// templates and photos are what a sheet's animation needs and a frame's
	// does not: which design was printed, and where the photographs in its
	// cells are. Nil until WithSheets is called, which leaves the sheet queue
	// alone entirely.
	templates *compose.Set
	photos    *photo.Store

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

// WithSheets turns on the animated-sheet queue.
//
// Separate from NewWorker because the frame queue needs neither of these, and a
// booth wired without them should render the per-photo animations rather than
// fail: the sheet animation is the newer half of the feature and the one with
// more to go wrong.
func (w *Worker) WithSheets(templates *compose.Set, photos *photo.Store) *Worker {
	w.templates, w.photos = templates, photos
	return w
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
//
// Frames first, and sheets only in the gaps between them. A sheet animation
// decodes every cell's clip rather than one — six times the work of the
// heaviest job the booth already runs — and the customer waiting on it has
// already got their print, while the customer waiting on a frame's animation is
// still in the booth taking the next shot.
func (w *Worker) Sweep(ctx context.Context) (int, error) {
	n, err := w.sweepFrames(ctx)
	if err != nil || n > 0 {
		return n, err
	}
	m, err := w.sweepSheets(ctx)
	return n + m, err
}

func (w *Worker) sweepFrames(ctx context.Context) (int, error) {
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

// sweepSheets renders one queued sheet animation.
//
// A row whose sources are gone is abandoned in the database rather than
// remembered in memory, unlike a broken frame clip. The difference is that this
// queue is not swept by retention: nothing marks a sheet clip purged when its
// photographs go, so a row left queued would be retried on every restart for as
// long as the booth lives.
func (w *Worker) sweepSheets(ctx context.Context) (int, error) {
	if w.templates == nil || w.photos == nil {
		return 0, nil
	}

	due, err := w.clips.UnrenderedSheets(ctx, w.batch+len(w.broken))
	if err != nil {
		return 0, err
	}

	n := 0
	for _, sc := range due {
		if w.broken[sc.ID] {
			continue
		}
		if n >= w.batch {
			break
		}
		switch err := w.oneSheet(ctx, sc); {
		case err == nil:
			n++
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, ErrTooShort), errors.Is(err, photo.ErrNotFound):
			// The frames reached retention first, or nothing on this sheet
			// moves at all. Neither will change.
			w.log.Warn("clip: no sheet to animate", "sheet", sc.ID, "job", sc.JobID, "err", err)
			if err := w.clips.AbandonSheet(ctx, sc.ID); err != nil {
				w.log.Error("clip: abandon sheet", "sheet", sc.ID, "err", err)
			}
		default:
			// Including an unknown template, which is why this is remembered in
			// memory rather than written down: the frame catalogue syncs after
			// the booth starts serving, so a design this row names can be
			// missing for a minute and present afterwards.
			w.broken[sc.ID] = true
			w.log.Error("clip: sheet render failed", "sheet", sc.ID, "job", sc.JobID, "err", err)
		}
	}
	return n, nil
}

func (w *Worker) oneSheet(ctx context.Context, sc SheetClip) error {
	// A reprint is the same sheet again under a new job, and most packages
	// include more than one print — so this is the ordinary path, not a corner.
	// Rendering it twice would be a minute of a core to produce a file the
	// booth already has, byte for byte.
	if same, ok, err := w.clips.RenderedSheetLike(ctx, sc); err != nil {
		return err
	} else if ok {
		w.log.Debug("clip: sheet already animated", "sheet", sc.ID, "reused", same.ID)
		return w.clips.SetSheetGIF(ctx, sc.ID, same.GIFPath)
	}

	tpl, ok := w.templates.ByID(sc.TemplateID)
	if !ok {
		return fmt.Errorf("%w: %q", compose.ErrNoTemplate, sc.TemplateID)
	}

	cells := make([]SheetSource, len(sc.PhotoIDs))
	for i, id := range sc.PhotoIDs {
		p, err := w.photos.Get(ctx, id)
		if err != nil {
			return err
		}
		if !p.PurgedAt.IsZero() {
			return fmt.Errorf("photo %s reached retention first: %w", id, fs.ErrNotExist)
		}

		// The derivative, like the download page serves — 2048px against a
		// 6-10 MB original. This composes at 500 pixels for the whole sheet, so
		// a cell is a couple of hundred wide and decoding a 24-megapixel
		// original six times to reach it would be most of the render's cost for
		// none of its quality.
		rel := p.DerivedPath
		if rel == "" {
			rel = p.Path
		}
		cells[i].Still = filepath.Join(w.root, filepath.FromSlash(rel))

		c, err := w.clips.ByPhoto(ctx, id)
		switch {
		case errors.Is(err, ErrNotFound), err == nil && !c.PurgedAt.IsZero():
			// This cell does not move. Not a failure: a burst can fail to
			// arrive without the print being any less printed, and a sheet
			// where five of six faces move is still the thing they asked for.
		case err != nil:
			return err
		default:
			dir := filepath.Join(w.root, filepath.FromSlash(c.Dir))
			frames := make([]string, 0, c.Frames)
			for j := range c.Frames {
				frames = append(frames, filepath.Join(dir, FrameName(j)))
			}
			cells[i].Frames = frames
		}
	}

	rel := SheetGIFPathFor(sc.SessionID, sc.ID)
	dest := filepath.Join(w.root, filepath.FromSlash(rel))

	started := time.Now()
	if err := RenderSheet(tpl, cells, compose.FilterByID(sc.Filter), dest, w.opts); err != nil {
		return err
	}

	// The row after the file, the same ordering every other writer here uses.
	if err := w.clips.SetSheetGIF(ctx, sc.ID, rel); err != nil {
		return err
	}

	w.log.Debug("clip: rendered sheet", "sheet", sc.ID, "cells", len(cells), "took", time.Since(started))
	return nil
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
