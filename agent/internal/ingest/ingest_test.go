package ingest_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/ingest"
	"github.com/bhaktiyudha/bykami/agent/internal/photo"
	"github.com/bhaktiyudha/bykami/agent/internal/session"
	"github.com/bhaktiyudha/bykami/agent/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// testPackage stands in for whatever the customer taps on the first screen.
var testPackage = session.Package{
	ID: "mini", Name: "MINI", PriceIDR: 45_000,
	TemplateID: "strip-3", PrintCopies: 1, TakeLimit: 15,
}

type fixture struct {
	hot      string
	root     string
	photos   *photo.Store
	sessions *session.Store
	watcher  *ingest.Watcher
	clock    *clock
}

func setup(t *testing.T) *fixture {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	c := newClock()
	hot := t.TempDir()
	root := t.TempDir()

	photos := photo.NewWithClock(db, c.now)
	sessions := session.New(db, session.WithClock(c.now), session.WithGrace(20*time.Second))
	log := slog.New(slog.DiscardHandler)

	return &fixture{
		hot: hot, root: root, photos: photos, sessions: sessions, clock: c,
		watcher: ingest.New(hot, root, photos, sessions, log,
			ingest.WithClock(c.now), ingest.WithSettle(500*time.Millisecond)),
	}
}

// start opens a paid session. Ingest is not the payment gate's test, so every
// case here wants a booth that is already taking photos.
func (f *fixture) start(t *testing.T) session.Session {
	t.Helper()
	ctx := context.Background()

	s, err := f.sessions.Start(ctx, "jajag", testPackage)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	s, err = f.sessions.MarkPaid(ctx, s.ID)
	if err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	return s
}

// jpegBytes returns a real, complete JPEG. Real rather than a stub because the
// ingest path decodes the header for dimensions and checks the EOI marker, so
// a fake would prove nothing about either.
func jpegBytes(t *testing.T, w, h int, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: shade, G: uint8(x % 256), B: uint8(y % 256), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func write(t *testing.T, path string, b []byte, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// The frame has to sit still through one sweep before it is eligible, so the
// helper does what the poller would.
func sweepUntilSettled(t *testing.T, f *fixture) int {
	t.Helper()
	ctx := context.Background()

	if _, err := f.watcher.Sweep(ctx); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	f.clock.advance(time.Second)
	n, err := f.watcher.Sweep(ctx)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	return n
}

func TestIngestFilesAFrameIntoItsSession(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	s := f.start(t)
	f.clock.advance(2 * time.Second)

	write(t, filepath.Join(f.hot, "IMG_0001.JPG"), jpegBytes(t, 64, 48, 10), f.clock.now())

	if n := sweepUntilSettled(t, f); n != 1 {
		t.Fatalf("ingested %d frames, want 1", n)
	}

	// The hot folder must end up empty. It is the camera's output, not storage:
	// a folder that accumulates is the twelve-months-of-faces failure.
	if entries, _ := os.ReadDir(f.hot); len(entries) != 0 {
		t.Fatalf("hot folder still holds %d entries", len(entries))
	}

	got, err := f.photos.BySession(ctx, s.ID)
	if err != nil {
		t.Fatalf("by session: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("session holds %d frames, want 1", len(got))
	}
	if got[0].Width != 64 || got[0].Height != 48 {
		t.Fatalf("dimensions %dx%d, want 64x48", got[0].Width, got[0].Height)
	}
	if got[0].Source != photo.HotFolder {
		t.Fatalf("source = %q", got[0].Source)
	}

	// Path is relative to the store root, and the file is really there.
	if _, err := os.Stat(filepath.Join(f.root, got[0].Path)); err != nil {
		t.Fatalf("filed frame is not on disk: %v", err)
	}
	if filepath.IsAbs(got[0].Path) {
		t.Fatalf("stored an absolute path: %q", got[0].Path)
	}
}

// The classic hot-folder bug: fsnotify CREATE fires on the first byte, so a
// naive reader gets half a JPEG. This is the regression test for it.
func TestTruncatedFileIsNotIngestedUntilComplete(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	f.start(t)
	f.clock.advance(time.Second)

	full := jpegBytes(t, 64, 48, 20)
	path := filepath.Join(f.hot, "IMG_0002.JPG")

	// Vendor software is mid-write: the bytes so far, missing the EOI marker.
	write(t, path, full[:len(full)/2], f.clock.now())

	// Size is stable across both sweeps — which is exactly the case a
	// size-only check gets wrong.
	if _, err := f.watcher.Sweep(ctx); err != nil {
		t.Fatalf("sweep 1: %v", err)
	}
	f.clock.advance(time.Second)
	if n, err := f.watcher.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("ingested a truncated JPEG: n=%d err=%v", n, err)
	}

	// The writer finishes.
	write(t, path, full, f.clock.now())
	if n := sweepUntilSettled(t, f); n != 1 {
		t.Fatalf("completed frame was not ingested: n=%d", n)
	}
}

func TestGrowingFileIsNotIngested(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	f.start(t)
	f.clock.advance(time.Second)

	full := jpegBytes(t, 64, 48, 30)
	path := filepath.Join(f.hot, "IMG_0003.JPG")

	for _, n := range []int{len(full) / 4, len(full) / 2, len(full)} {
		write(t, path, full[:n], f.clock.now())
		if got, err := f.watcher.Sweep(ctx); err != nil || got != 0 {
			t.Fatalf("ingested a file that was still growing: n=%d err=%v", got, err)
		}
		f.clock.advance(time.Second)
	}

	// It has stopped growing and it is complete.
	if n, err := f.watcher.Sweep(ctx); err != nil || n != 1 {
		t.Fatalf("final sweep: n=%d err=%v", n, err)
	}
}

// Staff test shots and accidental fires between sessions. Kept, not discarded:
// a customer's frame that missed its session by a second looks identical until
// a human decides.
func TestFrameWithNoSessionBecomesAnOrphan(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	write(t, filepath.Join(f.hot, "IMG_0004.JPG"), jpegBytes(t, 64, 48, 40), f.clock.now())
	if n := sweepUntilSettled(t, f); n != 1 {
		t.Fatalf("orphan was not ingested: n=%d", n)
	}

	orphans, err := f.photos.Orphans(ctx, 10)
	if err != nil {
		t.Fatalf("orphans: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("%d orphans, want 1", len(orphans))
	}
	if got := filepath.Dir(orphans[0].Path); got != ingest.UnassignedDir {
		t.Fatalf("orphan filed under %q, want %q", got, ingest.UnassignedDir)
	}
}

// A frame fired just before "Selesai" and written just after still belongs to
// the customer who paid for it.
func TestStragglerAttachesWithinTheGraceWindow(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	s := f.start(t)
	f.clock.advance(2 * time.Second)
	if err := f.sessions.Close(ctx, s.ID); err != nil {
		t.Fatalf("close: %v", err)
	}

	f.clock.advance(3 * time.Second)
	write(t, filepath.Join(f.hot, "IMG_0005.JPG"), jpegBytes(t, 64, 48, 50), f.clock.now())
	if n := sweepUntilSettled(t, f); n != 1 {
		t.Fatalf("straggler was not ingested: n=%d", n)
	}

	got, err := f.photos.BySession(ctx, s.ID)
	if err != nil {
		t.Fatalf("by session: %v", err)
	}
	if len(got) != 1 {
		t.Fatal("a frame taken during the session was lost to the orphan pile")
	}
}

// Content-addressed rows are what make the crash-recovery rescan safe.
func TestRecoverAdoptsFilesWithNoRow(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	s := f.start(t)

	// The state a crash between the rename and the insert leaves behind.
	dir := filepath.Join(f.root, ingest.SessionsDir, s.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write(t, filepath.Join(dir, "orphaned-by-crash.jpg"), jpegBytes(t, 64, 48, 60), f.clock.now())

	if err := f.watcher.Recover(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}
	got, err := f.photos.BySession(ctx, s.ID)
	if err != nil {
		t.Fatalf("by session: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("recovered %d frames, want 1", len(got))
	}
	// The directory it sits in is the attribution, not the clock: the session
	// may have closed long before this scan runs.
	if got[0].SessionID != s.ID {
		t.Fatalf("recovered frame attributed to %q, want %q", got[0].SessionID, s.ID)
	}

	// Running it again must change nothing, because every agent start runs it.
	if err := f.watcher.Recover(ctx); err != nil {
		t.Fatalf("second recover: %v", err)
	}
	again, err := f.photos.BySession(ctx, s.ID)
	if err != nil {
		t.Fatalf("by session: %v", err)
	}
	if len(again) != 1 {
		t.Fatalf("rescan duplicated rows: %d", len(again))
	}
}

func TestRecoverLeavesAnUnreadableFileInPlace(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	dir := filepath.Join(f.root, ingest.UnassignedDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	broken := filepath.Join(dir, "truncated.jpg")
	full := jpegBytes(t, 64, 48, 70)
	write(t, broken, full[:len(full)/3], f.clock.now())

	if err := f.watcher.Recover(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}
	// It is a customer's frame. Left on disk for a human rather than deleted.
	if _, err := os.Stat(broken); err != nil {
		t.Fatalf("unreadable file was removed: %v", err)
	}
	orphans, err := f.photos.Orphans(ctx, 10)
	if err != nil {
		t.Fatalf("orphans: %v", err)
	}
	if len(orphans) != 0 {
		t.Fatal("recorded a row for a file that cannot be decoded")
	}
}

// The same bytes arriving twice must not become two frames the customer is
// shown, and must not stick in the hot folder forever.
func TestDuplicateBytesAreDiscarded(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	s := f.start(t)
	f.clock.advance(time.Second)

	frame := jpegBytes(t, 64, 48, 80)
	write(t, filepath.Join(f.hot, "IMG_0006.JPG"), frame, f.clock.now())
	if n := sweepUntilSettled(t, f); n != 1 {
		t.Fatalf("first ingest: n=%d", n)
	}

	// The same file again, under a different name.
	write(t, filepath.Join(f.hot, "IMG_0006_COPY.JPG"), frame, f.clock.now())
	if n := sweepUntilSettled(t, f); n != 0 {
		t.Fatalf("ingested the same bytes twice: n=%d", n)
	}
	if entries, _ := os.ReadDir(f.hot); len(entries) != 0 {
		t.Fatal("duplicate was left in the hot folder, where it will be retried forever")
	}

	got, err := f.photos.BySession(ctx, s.ID)
	if err != nil {
		t.Fatalf("by session: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("session holds %d frames, want 1", len(got))
	}
}

// Deleting the second copy is only safe because the first is verifiably still
// there. If it is not, a stuck hot folder is the failure someone notices.
func TestDuplicateIsKeptWhenTheRecordedCopyIsMissing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	f.start(t)
	f.clock.advance(time.Second)

	frame := jpegBytes(t, 64, 48, 90)
	write(t, filepath.Join(f.hot, "IMG_0007.JPG"), frame, f.clock.now())
	if n := sweepUntilSettled(t, f); n != 1 {
		t.Fatalf("first ingest: n=%d", n)
	}

	orphans, err := f.photos.Orphans(ctx, 10)
	if err != nil {
		t.Fatalf("orphans: %v", err)
	}
	// It went into the open session, so look it up there instead.
	if len(orphans) == 0 {
		current, ok, err := f.sessions.Current(ctx)
		if err != nil || !ok {
			t.Fatalf("current: ok=%v err=%v", ok, err)
		}
		filed, err := f.photos.BySession(ctx, current.ID)
		if err != nil || len(filed) != 1 {
			t.Fatalf("filed = %d (err %v)", len(filed), err)
		}
		if err := os.Remove(filepath.Join(f.root, filed[0].Path)); err != nil {
			t.Fatalf("simulate a lost original: %v", err)
		}
	}

	second := filepath.Join(f.hot, "IMG_0007_COPY.JPG")
	write(t, second, frame, f.clock.now())
	if n := sweepUntilSettled(t, f); n != 0 {
		t.Fatalf("re-ingested: n=%d", n)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatal("deleted the only remaining copy of a frame")
	}
}

func TestNonJPEGFilesAreIgnored(t *testing.T) {
	f := setup(t)

	// EOS Utility can be configured to write RAW alongside JPEG, and Windows
	// scatters desktop.ini and Thumbs.db into any folder a user opens.
	write(t, filepath.Join(f.hot, "IMG_0008.CR2"), []byte("raw"), f.clock.now())
	write(t, filepath.Join(f.hot, "Thumbs.db"), []byte("thumbs"), f.clock.now())

	if n := sweepUntilSettled(t, f); n != 0 {
		t.Fatalf("ingested a non-JPEG: n=%d", n)
	}
	if entries, _ := os.ReadDir(f.hot); len(entries) != 2 {
		t.Fatal("touched files that are not ours")
	}
}

func TestUppercaseExtensionIsIngested(t *testing.T) {
	f := setup(t)

	// Canon writes .JPG, not .jpg. Getting this wrong ingests nothing at all.
	write(t, filepath.Join(f.hot, "IMG_0009.JPG"), jpegBytes(t, 32, 24, 100), f.clock.now())
	if n := sweepUntilSettled(t, f); n != 1 {
		t.Fatalf("uppercase .JPG was ignored: n=%d", n)
	}
}

func TestMissingHotFolderIsNotAnError(t *testing.T) {
	f := setup(t)

	// The hot folder is created by EOS Utility's preferences, not by the agent.
	if err := os.RemoveAll(f.hot); err != nil {
		t.Fatalf("remove hot folder: %v", err)
	}
	if n, err := f.watcher.Sweep(context.Background()); err != nil || n != 0 {
		t.Fatalf("missing hot folder: n=%d err=%v", n, err)
	}
}

var _ io.Reader = (*bytes.Reader)(nil)
