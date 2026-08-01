package purge_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/clip"
	"github.com/bhaktiyudha/bykami/agent/internal/photo"
	"github.com/bhaktiyudha/bykami/agent/internal/purge"
	"github.com/bhaktiyudha/bykami/agent/internal/store"
)

func TestSweepDeletesOriginalsPastRetention(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	photos := photo.NewWithClock(db, clock)

	root := t.TempDir()
	write := func(rel string) string {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("pixels"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		return path
	}

	oldPath := write("unassigned/old.jpg")
	newPath := write("unassigned/new.jpg")
	derivedPath := write("derived/old.jpg")

	old, err := photos.Record(context.Background(), photo.Photo{
		ContentHash: "old", Path: "unassigned/old.jpg", Bytes: 6, Width: 100, Height: 100,
		Source: photo.HotFolder, CapturedAt: now,
	})
	if err != nil {
		t.Fatalf("record old: %v", err)
	}
	if err := photos.SetDerived(context.Background(), old.ID, "derived/old.jpg"); err != nil {
		t.Fatalf("set derived: %v", err)
	}

	// Eight days pass.
	now = now.Add(8 * 24 * time.Hour)
	if _, err := photos.Record(context.Background(), photo.Photo{
		ContentHash: "new", Path: "unassigned/new.jpg", Bytes: 6, Width: 100, Height: 100,
		Source: photo.HotFolder, CapturedAt: now,
	}); err != nil {
		t.Fatalf("record new: %v", err)
	}

	p := purge.New(photos, nil, root, purge.DefaultAge, slog.New(slog.DiscardHandler))
	n, err := p.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("purged %d files, want 1", n)
	}

	if _, err := os.Stat(oldPath); err == nil {
		t.Fatal("an original past the retention window is still on disk")
	}
	if _, err := os.Stat(derivedPath); err == nil {
		t.Fatal("the derivative outlived its original")
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatal("a frame inside the retention window was deleted")
	}

	// The row outlives the file: it is how the agent knows not to re-ingest
	// those bytes, and how a question is answerable once the pixels are gone.
	got, err := photos.Get(context.Background(), old.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PurgedAt.IsZero() {
		t.Fatal("the row was not marked purged, so every sweep will reconsider it")
	}

	// A second sweep must find nothing left to do.
	if n, err := p.Sweep(context.Background()); err != nil || n != 0 {
		t.Fatalf("second sweep: n=%d err=%v", n, err)
	}
}

// Already gone is the same outcome as just deleted: a file a human removed by
// hand must not keep the row unpurged forever.
func TestSweepToleratesAMissingFile(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	photos := photo.NewWithClock(db, clock)

	if _, err := photos.Record(context.Background(), photo.Photo{
		ContentHash: "gone", Path: "unassigned/gone.jpg", Bytes: 1, Width: 10, Height: 10,
		Source: photo.HotFolder, CapturedAt: now,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	now = now.Add(8 * 24 * time.Hour)

	p := purge.New(photos, nil, t.TempDir(), purge.DefaultAge, slog.New(slog.DiscardHandler))
	if n, err := p.Sweep(context.Background()); err != nil || n != 1 {
		t.Fatalf("sweep: n=%d err=%v", n, err)
	}
}

// Composed sheets hold the same faces, laid out for the printer. Retention
// that covered only the originals would leave a full-resolution copy of every
// customer on the machine indefinitely.
func TestSweepDeletesOldComposedSheets(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	root := t.TempDir()
	sheets := filepath.Join(root, "sheets", "s1")
	if err := os.MkdirAll(sheets, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	old := filepath.Join(sheets, "old.jpg")
	recent := filepath.Join(sheets, "recent.jpg")
	for _, p := range []string{old, recent} {
		if err := os.WriteFile(p, []byte("composed sheet"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	stale := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	p := purge.New(photo.New(db), nil, root, purge.DefaultAge, slog.New(slog.DiscardHandler))
	if _, err := p.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if _, err := os.Stat(old); err == nil {
		t.Fatal("a composed sheet past the retention window is still on disk")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatal("a sheet inside the retention window was deleted")
	}
}

// An animated sheet is the same faces again, and moving — the most identifying
// artefact the booth produces. It has no purged column of its own and needs
// none: it is written under sheets/, so the sweep above reaches it. This is the
// test that the two agree about where that is, because a path built anywhere
// else is a video of six customers that retention silently never touches.
func TestSweepDeletesAnAnimatedSheetWithTheSheets(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	root := t.TempDir()
	rel := clip.SheetGIFPathFor("s1", "abc123")
	animated := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(animated), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(animated, []byte("GIF89a"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	stale := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(animated, stale, stale); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	p := purge.New(photo.New(db), nil, root, purge.DefaultAge, slog.New(slog.DiscardHandler))
	if _, err := p.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if _, err := os.Stat(animated); err == nil {
		t.Fatalf("an animated sheet past the retention window is still on disk at %s", rel)
	}
}

// A clip is the same face at the same moment as the frame it belongs to —
// arguably more identifying, since it carries how somebody moves. It must not
// outlive the still by so much as a sweep.
func TestSweepDeletesTheClipWithItsPhoto(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	photos := photo.NewWithClock(db, clock)
	clips := clip.NewWithClock(db, clock)

	root := t.TempDir()
	ph, err := photos.Record(ctx, photo.Photo{
		ContentHash: "one", Path: "unassigned/one.jpg", Bytes: 6, Width: 100, Height: 100,
		Source: photo.Webcam, CapturedAt: now,
	})
	if err != nil {
		t.Fatalf("record photo: %v", err)
	}

	// The state an upload and a render leave behind: a directory of frames, and
	// a GIF beside it.
	dir := clip.DirFor("", ph.ID)
	frames := filepath.Join(root, filepath.FromSlash(dir))
	if err := os.MkdirAll(frames, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(frames, clip.FrameName(0)), []byte("pixels"), 0o644); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	gifRel := clip.GIFPathFor("", ph.ID)
	gifPath := filepath.Join(root, filepath.FromSlash(gifRel))
	if err := os.WriteFile(gifPath, []byte("animation"), 0o644); err != nil {
		t.Fatalf("write gif: %v", err)
	}

	c, err := clips.Record(ctx, clip.Clip{PhotoID: ph.ID, Dir: dir, Frames: 1, CapturedAt: now})
	if err != nil {
		t.Fatalf("record clip: %v", err)
	}
	if err := clips.SetGIF(ctx, c.ID, gifRel); err != nil {
		t.Fatalf("set gif: %v", err)
	}

	now = now.Add(8 * 24 * time.Hour)

	p := purge.New(photos, clips, root, purge.DefaultAge, slog.New(slog.DiscardHandler))
	if _, err := p.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if _, err := os.Stat(frames); err == nil {
		t.Fatal("a clip's frames outlived the photo they belong to")
	}
	if _, err := os.Stat(gifPath); err == nil {
		t.Fatal("a clip's animation outlived the photo it belongs to")
	}

	// The mark is what stops the download page listing an animation whose file
	// is gone, and what stops the render worker reconsidering it forever.
	got, err := clips.Get(ctx, c.ID)
	if err != nil {
		t.Fatalf("get clip: %v", err)
	}
	if got.PurgedAt.IsZero() {
		t.Fatal("the clip row was not marked purged")
	}
}
