package derive_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/bhaktiyudha/bykami/agent/internal/derive"
	"github.com/bhaktiyudha/bykami/agent/internal/ingest"
	"github.com/bhaktiyudha/bykami/agent/internal/photo"
	"github.com/bhaktiyudha/bykami/agent/internal/session"
	"github.com/bhaktiyudha/bykami/agent/internal/store"
)

type fixture struct {
	root   string
	db     *sql.DB
	photos *photo.Store
	worker *derive.Worker
	n      int
}

func setup(t *testing.T) *fixture {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	root := t.TempDir()
	photos := photo.New(db)
	f := &fixture{
		root:   root,
		db:     db,
		photos: photos,
		worker: derive.NewWorker(photos, root, slog.New(slog.DiscardHandler)),
	}
	f.openSession(t, "sess-1")
	return f
}

// openSession inserts the row the photos foreign key needs. Written directly
// rather than through session.Start because these tests want a session with a
// known id, and because Start's partial unique index allows only one live one.
func (f *fixture) openSession(t *testing.T, id string) {
	t.Helper()
	if _, err := f.db.Exec(
		`INSERT INTO sessions
		   (id, outlet_id, state, package_id, package_name, price_idr, template_id,
		    print_copies, take_limit, opened_at, paid_at)
		 VALUES (?, 'jajag', 'open', 'mini', 'MINI', 45000, 'strip-3', 1, 15, unixepoch(), unixepoch())`, id,
	); err != nil {
		t.Fatalf("open session %s: %v", id, err)
	}
}

// record files a JPEG under the store root the way ingest would, and returns
// the row. Every frame gets distinct pixels: content_hash is UNIQUE, so two
// identical images would be a duplicate rather than a second frame.
func (f *fixture) record(t *testing.T, sessionID string, w, h int) photo.Photo {
	t.Helper()
	f.n++

	dir := filepath.Join(f.root, "unassigned")
	rel := "unassigned"
	if sessionID != "" {
		dir = filepath.Join(f.root, "sessions", sessionID)
		rel = filepath.ToSlash(filepath.Join("sessions", sessionID))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: uint8(f.n * 37), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 92}); err != nil {
		t.Fatalf("encode: %v", err)
	}

	name := fmt.Sprintf("frame-%d.jpg", f.n)
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	sum := sha256.Sum256(buf.Bytes())
	p, err := f.photos.Record(context.Background(), photo.Photo{
		SessionID:   sessionID,
		ContentHash: hex.EncodeToString(sum[:]),
		Path:        filepath.ToSlash(filepath.Join(rel, name)),
		Bytes:       int64(buf.Len()),
		Width:       w,
		Height:      h,
		Source:      photo.Webcam,
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	return p
}

func TestSweepDerivesAndRecordsThePath(t *testing.T) {
	f := setup(t)
	p := f.record(t, "sess-1", 2400, 1800)

	n, err := f.worker.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("derived %d frames, want 1", n)
	}

	got, err := f.photos.Get(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DerivedPath == "" {
		t.Fatal("derived_path is still empty, so the review screen would keep serving the original")
	}

	di, err := os.Stat(filepath.Join(f.root, filepath.FromSlash(got.DerivedPath)))
	if err != nil {
		t.Fatalf("derivative missing at %s: %v", got.DerivedPath, err)
	}
	oi, err := os.Stat(filepath.Join(f.root, filepath.FromSlash(got.Path)))
	if err != nil {
		t.Fatalf("original missing: %v", err)
	}
	if di.Size() >= oi.Size() {
		t.Fatalf("derivative is %d bytes against an original of %d; it is meant to be the cheap one",
			di.Size(), oi.Size())
	}
}

// The whole reason derivatives live outside sessions/. Recover records every
// JPEG it finds there without a row, so a derivative filed beside its original
// would come back as a second photo on the next restart and duplicate every
// frame in the customer's filmstrip.
func TestDerivativesAreInvisibleToTheRecoveryScan(t *testing.T) {
	f := setup(t)
	f.record(t, "sess-1", 600, 400)

	if _, err := f.worker.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	before, err := f.photos.BySession(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("by session: %v", err)
	}

	w := ingest.New(t.TempDir(), f.root, f.photos, session.New(f.db), slog.New(slog.DiscardHandler))
	if err := w.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}

	after, err := f.photos.BySession(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("by session: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("recovery scan turned %d frames into %d; it picked up the derivatives",
			len(before), len(after))
	}
}

func TestSweepStopsRetryingAFrameItCannotRead(t *testing.T) {
	f := setup(t)
	p := f.record(t, "sess-1", 800, 600)

	// The row survives, the file does not — the state a half-finished manual
	// cleanup leaves behind.
	if err := os.Remove(filepath.Join(f.root, filepath.FromSlash(p.Path))); err != nil {
		t.Fatalf("remove original: %v", err)
	}

	for i := range 3 {
		n, err := f.worker.Sweep(context.Background())
		if err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
		if n != 0 {
			t.Fatalf("sweep %d derived %d frames from a file that is gone", i, n)
		}
	}

	got, err := f.photos.Get(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DerivedPath != "" {
		t.Fatal("a missing original produced a derived_path")
	}
}

// A broken frame must not wedge the queue: it is skipped, and the frames behind
// it still get derived.
func TestSweepHonoursTheBatchAndKeepsGoing(t *testing.T) {
	f := setup(t)
	for range derive.DefaultBatch + 3 {
		f.record(t, "sess-1", 600, 400)
	}

	first, err := f.worker.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if first != derive.DefaultBatch {
		t.Fatalf("first sweep derived %d, want the batch size %d", first, derive.DefaultBatch)
	}

	second, err := f.worker.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if second != 3 {
		t.Fatalf("second sweep derived %d, want the remaining 3", second)
	}
}
