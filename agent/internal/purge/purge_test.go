package purge_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

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

	p := purge.New(photos, root, purge.DefaultAge, slog.New(slog.DiscardHandler))
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

	p := purge.New(photos, t.TempDir(), purge.DefaultAge, slog.New(slog.DiscardHandler))
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

	p := purge.New(photo.New(db), root, purge.DefaultAge, slog.New(slog.DiscardHandler))
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
