package clip_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/bhaktiyudha/bykami/agent/internal/clip"
)

// stage writes a clip's frames under root and records the row, which is the
// state the kiosk's upload leaves behind.
func (f fixture) stage(t *testing.T, root, hash string, n int) clip.Clip {
	t.Helper()

	p := f.frame(t, hash)
	dir := clip.DirFor(p.SessionID, p.ID)
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	frames(t, filepath.Join(root, filepath.FromSlash(dir)), n, 320, 240)

	return f.record(t, p, n)
}

func TestWorkerRendersAndRecords(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	ctx := context.Background()

	c := f.stage(t, root, "one", 6)

	w := clip.NewWorker(f.clips, root, slog.New(slog.DiscardHandler))
	n, err := w.Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("rendered %d clips, want 1", n)
	}

	got, err := f.clips.Get(ctx, c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GIFPath == "" {
		t.Fatal("the clip was rendered but the row does not point at the file")
	}
	if got.GIFAt.IsZero() {
		t.Fatal("a rendered clip has no render time")
	}

	dest := filepath.Join(root, filepath.FromSlash(got.GIFPath))
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("the row points at a file that is not there: %v", err)
	}
	if g := decode(t, dest); len(g.Image) != 6 {
		t.Fatalf("animated %d frames, want 6", len(g.Image))
	}

	// A second sweep must find nothing: the row is what takes it out of the
	// queue, and a worker that re-renders every second never stops.
	if n, err := w.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("second sweep: n=%d err=%v", n, err)
	}
}

// A clip whose frames never arrived must be given up on, not retried every
// second for the seven days until its row is purged.
func TestWorkerGivesUpOnAClipWithNoFrames(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	ctx := context.Background()

	p := f.frame(t, "one")
	f.record(t, p, 50) // recorded, but nothing was ever written to disk

	w := clip.NewWorker(f.clips, root, slog.New(slog.DiscardHandler))
	for range 3 {
		if n, err := w.Sweep(ctx); err != nil || n != 0 {
			t.Fatalf("sweep: n=%d err=%v", n, err)
		}
	}
}

// One unrenderable clip must not park the queue in front of the ones behind it.
func TestWorkerReachesPastABrokenClip(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	ctx := context.Background()

	broken := f.frame(t, "broken")
	f.record(t, broken, 50) // no frames on disk

	good := f.stage(t, root, "good", 4)

	w := clip.NewWorker(f.clips, root, slog.New(slog.DiscardHandler))

	// The first sweep meets the broken clip and marks it off. The second must
	// then reach the one behind it.
	if _, err := w.Sweep(ctx); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if _, err := w.Sweep(ctx); err != nil {
		t.Fatalf("second sweep: %v", err)
	}

	got, err := f.clips.Get(ctx, good.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GIFPath == "" {
		t.Fatal("a good clip queued behind a broken one was never rendered")
	}
}
