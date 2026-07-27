// Package purge deletes the originals off the booth PC.
//
// This is the rule that matters most on this machine, and the original plan had
// no equivalent. A hot folder never empties itself: twelve months in, an
// unmanaged studio PC holds every customer's face at full resolution,
// unencrypted, on the machine most likely to be stolen, resold or handed to a
// repair shop — sitting in a room where strangers are left alone with it.
//
// Seven days means a theft leaks a week, not a year. It is not a cost decision;
// R2 makes a year of galleries rounding-error money. It is risk versus
// complaints, and it must not depend on anyone remembering.
package purge

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/photo"
)

// DefaultAge is the retention window for originals on the booth PC.
const DefaultAge = 7 * 24 * time.Hour

// DefaultInterval is how often the sweep runs. Hourly, because the window is
// measured in days and a booth PC that was switched off overnight should catch
// up shortly after it is switched on rather than at some fixed hour it missed.
const DefaultInterval = time.Hour

type Purger struct {
	photos *photo.Store
	root   string
	age    time.Duration
	log    *slog.Logger
}

func New(photos *photo.Store, root string, age time.Duration, log *slog.Logger) *Purger {
	if age <= 0 {
		age = DefaultAge
	}
	return &Purger{photos: photos, root: root, age: age, log: log}
}

// Run sweeps on a ticker until ctx is cancelled, starting immediately.
func (p *Purger) Run(ctx context.Context) error {
	// Immediately, not after the first interval. A booth that is switched on
	// for two hours a day would otherwise never reach the end of a tick.
	if n, err := p.Sweep(ctx); err != nil {
		p.log.Error("purge: sweep", "err", err)
	} else if n > 0 {
		p.log.Info("purge: removed originals past retention", "files", n)
	}

	t := time.NewTicker(DefaultInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if n, err := p.Sweep(ctx); err != nil {
				p.log.Error("purge: sweep", "err", err)
			} else if n > 0 {
				p.log.Info("purge: removed originals past retention", "files", n)
			}
		}
	}
}

// Sweep deletes originals older than the window and returns how many went.
//
// The row stays and is marked purged. It is how the agent knows not to
// re-ingest those bytes if a copy turns up again, and how a question asked
// three weeks later is answerable at all once the pixels are gone.
func (p *Purger) Sweep(ctx context.Context) (int, error) {
	due, err := p.photos.Purgeable(ctx, p.age)
	if err != nil {
		return 0, err
	}

	n := 0
	for _, ph := range due {
		path := filepath.Join(p.root, filepath.FromSlash(ph.Path))
		switch err := os.Remove(path); {
		case err == nil, errors.Is(err, fs.ErrNotExist):
			// Already gone is the same outcome as just deleted. The mark is what
			// stops it being reconsidered on every sweep from now on.
		default:
			// Logged and skipped rather than aborting the sweep: one locked file
			// must not stop every other customer's photos being deleted.
			p.log.Error("purge: remove", "path", ph.Path, "err", err)
			continue
		}

		if ph.DerivedPath != "" {
			derived := filepath.Join(p.root, filepath.FromSlash(ph.DerivedPath))
			if err := os.Remove(derived); err != nil && !errors.Is(err, fs.ErrNotExist) {
				p.log.Error("purge: remove derivative", "path", ph.DerivedPath, "err", err)
			}
		}

		if err := p.photos.MarkPurged(ctx, ph.ID); err != nil {
			return n, err
		}
		n++
	}

	// Composed sheets are the same faces, laid out for the printer. Retention
	// that covered only the originals would leave a full-resolution copy of
	// every customer on the machine indefinitely — which is precisely the
	// failure this package exists to prevent.
	//
	// Swept by file age rather than through the photos table: a sheet has no
	// row, and one composed from frames that were themselves purged at
	// different times has no single original to inherit a deadline from.
	n += p.sweepSheets()

	if n > 0 {
		p.pruneEmptyDirs()
	}
	return n, nil
}

func (p *Purger) sweepSheets() int {
	cutoff := time.Now().Add(-p.age)
	root := filepath.Join(p.root, "sheets")

	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil
		case err != nil:
			return err
		case d.IsDir():
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().After(cutoff) {
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			p.log.Error("purge: remove sheet", "path", path, "err", err)
			return nil
		}
		n++
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		p.log.Error("purge: sweep sheets", "err", err)
	}
	return n
}

// pruneEmptyDirs removes session directories left behind with nothing in them.
//
// Cosmetic on its own, but a directory named after a session id is itself a
// record that a session happened at a time — and leaving thousands of them is
// the sort of thing that makes a "we deleted it" claim look untrue.
func (p *Purger) pruneEmptyDirs() {
	for _, dir := range []string{"sessions", "sheets"} {
		root := filepath.Join(p.root, dir)
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(root, e.Name())
			if inner, err := os.ReadDir(path); err == nil && len(inner) == 0 {
				if err := os.Remove(path); err != nil {
					p.log.Debug("purge: remove empty directory", "path", path, "err", err)
				}
			}
		}
	}
}
