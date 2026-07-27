// Package ingest moves frames out of the camera's hot folder and into the
// booth's own storage.
//
// The camera never talks to this program. Canon's EOS Utility tethers over USB
// and writes full-resolution JPEGs into a directory of its choosing; the agent
// watches that directory. Those two concerns stay decoupled, which is what
// keeps a vendor SDK and cgo out of the build — and it is why every hard part
// here is about a file that is being written to while we look at it.
package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // DecodeConfig for dimensions; the header, not the pixels.
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/photo"
	"github.com/bhaktiyudha/bykami/agent/internal/session"
)

const (
	// DefaultPoll is how often the hot folder is listed.
	DefaultPoll = 500 * time.Millisecond

	// DefaultSettle is how long a file's size must stay unchanged before it is
	// considered finished.
	//
	// This is half of the classic hot-folder bug. Vendor software writes
	// progressively, so a file that exists is not a file that is complete, and
	// reading one at the wrong moment yields a truncated JPEG. The other half is
	// the EOI check below — size can also be briefly stable mid-write while the
	// writer does something else.
	DefaultSettle = 500 * time.Millisecond
)

// SessionsDir and UnassignedDir are where ingested frames live, relative to the
// store root. Orphans get their own directory rather than a flag on a row in a
// shared one, so that the 7-day purge stays a directory delete.
const (
	SessionsDir   = "sessions"
	UnassignedDir = "unassigned"
)

// eoi is the JPEG End Of Image marker. A complete JPEG ends with these two
// bytes; a half-written one does not.
var eoi = []byte{0xFF, 0xD9}

type Watcher struct {
	hotFolder string
	root      string
	photos    *photo.Store
	sessions  *session.Store
	log       *slog.Logger

	poll   time.Duration
	settle time.Duration
	now    func() time.Time

	// seen tracks size and first-observed time per path between sweeps. Held in
	// memory rather than in SQLite because it is worthless after a restart: a
	// file that was mid-write when the agent died is either complete by the time
	// it comes back, or abandoned.
	seen map[string]observation
}

type observation struct {
	size    int64
	firstAt time.Time
}

type Option func(*Watcher)

func WithPoll(d time.Duration) Option     { return func(w *Watcher) { w.poll = d } }
func WithSettle(d time.Duration) Option   { return func(w *Watcher) { w.settle = d } }
func WithClock(f func() time.Time) Option { return func(w *Watcher) { w.now = f } }

// New returns a watcher over hotFolder that files frames under root.
func New(hotFolder, root string, photos *photo.Store, sessions *session.Store, log *slog.Logger, opts ...Option) *Watcher {
	w := &Watcher{
		hotFolder: hotFolder,
		root:      root,
		photos:    photos,
		sessions:  sessions,
		log:       log,
		poll:      DefaultPoll,
		settle:    DefaultSettle,
		now:       time.Now,
		seen:      map[string]observation{},
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Run sweeps until ctx is cancelled.
//
// Polling rather than fsnotify, and not only to avoid a dependency. fsnotify's
// CREATE fires on the first byte, so every event still has to be debounced
// exactly as below — the notification saves no work. Polling also behaves the
// same on Windows, where the booth PC actually runs, and does not silently drop
// events when a watch buffer overflows during a burst.
func (w *Watcher) Run(ctx context.Context) error {
	if err := w.Recover(ctx); err != nil {
		// Not fatal. A booth that will not start because a rescan failed is
		// worse than one that starts having missed a file a human can find.
		w.log.Error("ingest: recovery scan failed", "err", err)
	}

	t := time.NewTicker(w.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, err := w.Sweep(ctx); err != nil {
				w.log.Error("ingest: sweep", "err", err)
			}
		}
	}
}

// Sweep makes one pass over the hot folder and returns how many frames it
// ingested. Exported so a test does not have to race a ticker.
func (w *Watcher) Sweep(ctx context.Context) (int, error) {
	entries, err := os.ReadDir(w.hotFolder)
	if errors.Is(err, fs.ErrNotExist) {
		// The hot folder is created by EOS Utility's preferences, not by us. Its
		// absence at startup is a configuration state, not a crash.
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("ingest: read hot folder: %w", err)
	}

	live := make(map[string]bool, len(entries))
	n := 0
	for _, e := range entries {
		if e.IsDir() || !isJPEG(e.Name()) {
			continue
		}
		path := filepath.Join(w.hotFolder, e.Name())
		live[path] = true

		info, err := e.Info()
		if err != nil {
			// Vanished between listing and stat: the file was moved by something
			// else. Nothing to do.
			continue
		}

		if !w.settled(path, info.Size()) {
			continue
		}
		if !endsWithEOI(path) {
			// Size held still but the file is not a complete JPEG yet. Reset the
			// clock rather than rejecting it — the writer may simply have paused.
			w.seen[path] = observation{size: info.Size(), firstAt: w.now()}
			continue
		}

		filed, err := w.ingest(ctx, path, info.ModTime(), photo.HotFolder)
		if err != nil {
			w.log.Error("ingest: file", "path", path, "err", err)
			continue
		}
		delete(w.seen, path)
		if filed {
			n++
		}
	}

	// Forget files that are gone, or the map grows for the life of the process.
	for path := range w.seen {
		if !live[path] {
			delete(w.seen, path)
		}
	}
	return n, nil
}

// settled reports whether size has been unchanged for the settle window.
func (w *Watcher) settled(path string, size int64) bool {
	now := w.now()
	prev, ok := w.seen[path]
	if !ok || prev.size != size {
		w.seen[path] = observation{size: size, firstAt: now}
		return false
	}
	// An empty file has a stable size too, and it is never a photo.
	if size == 0 {
		return false
	}
	return now.Sub(prev.firstAt) >= w.settle
}

// Ingest files one frame that is already known to be complete, and reports the
// row it created. Used by the webcam capture path, which writes its own file
// and therefore needs no debounce.
func (w *Watcher) Ingest(ctx context.Context, path string, capturedAt time.Time, src photo.Source) (photo.Photo, error) {
	return w.file(ctx, path, capturedAt, src)
}

// ingest reports whether a new frame was filed. Discarding a duplicate is a
// success that files nothing, and the two must not look the same to a caller
// counting frames.
func (w *Watcher) ingest(ctx context.Context, path string, capturedAt time.Time, src photo.Source) (bool, error) {
	p, err := w.file(ctx, path, capturedAt, src)
	return p.ID != "", err
}

func (w *Watcher) file(ctx context.Context, path string, capturedAt time.Time, src photo.Source) (photo.Photo, error) {
	f, err := os.Open(path)
	if err != nil {
		return photo.Photo{}, err
	}
	defer f.Close()

	sum := sha256.New()
	size, err := io.Copy(sum, f)
	if err != nil {
		return photo.Photo{}, fmt.Errorf("hash: %w", err)
	}
	hash := hex.EncodeToString(sum.Sum(nil))

	// Ask before doing any work. A restart re-offers everything on disk, so the
	// ordinary case here is a file we already hold.
	switch held, err := w.photos.ByHash(ctx, hash); {
	case errors.Is(err, photo.ErrNotFound):
		// New bytes. Carry on below.
	case err != nil:
		return photo.Photo{}, err
	default:
		return photo.Photo{}, w.discardDuplicate(path, held)
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return photo.Photo{}, err
	}
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return photo.Photo{}, fmt.Errorf("decode header: %w", err)
	}

	// Attributed on filesystem mtime, never EXIF. Camera clocks drift and
	// nobody resets them after a battery change.
	sessionID, _, err := w.sessions.Attribute(ctx, capturedAt)
	if err != nil {
		return photo.Photo{}, err
	}

	dir := filepath.Join(w.root, UnassignedDir)
	if sessionID != "" {
		dir = filepath.Join(w.root, SessionsDir, sessionID)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return photo.Photo{}, err
	}

	name := fmt.Sprintf("%d-%s%s", capturedAt.Unix(), hash[:12], filepath.Ext(path))
	dest := filepath.Join(dir, name)
	if err := move(path, dest); err != nil {
		return photo.Photo{}, err
	}

	rel, err := filepath.Rel(w.root, dest)
	if err != nil {
		return photo.Photo{}, err
	}

	// The file moves before the row is written, deliberately. A crash in
	// between leaves a file with no row, which the recovery scan repairs; the
	// other order would leave a row pointing at a path that does not exist,
	// which nothing can repair.
	p, err := w.photos.Record(ctx, photo.Photo{
		SessionID:   sessionID,
		ContentHash: hash,
		Path:        filepath.ToSlash(rel),
		Bytes:       size,
		Width:       cfg.Width,
		Height:      cfg.Height,
		Source:      src,
		CapturedAt:  capturedAt,
	})
	if errors.Is(err, photo.ErrDuplicate) {
		// Raced with the recovery scan. The file is filed; the row exists.
		return w.photos.ByHash(ctx, hash)
	}
	if err != nil {
		return photo.Photo{}, err
	}

	w.log.Info("ingest: filed",
		"session", orOrphan(sessionID), "hash", hash[:12],
		"px", fmt.Sprintf("%dx%d", cfg.Width, cfg.Height), "source", src)
	return p, nil
}

// discardDuplicate removes a hot-folder file whose bytes are already stored.
//
// Deleting is safe only because an identical copy exists under the store root,
// so this destroys no information — but that is verified rather than assumed.
// If the recorded copy has gone missing, the file stays where it is: a stuck
// hot folder is a failure someone notices, and a silent deletion is not.
//
// A row whose original was purged at seven days is the one case where the copy
// is meant to be absent. Deleting is right there too — the bytes were already
// filed once, and their retention window has expired.
func (w *Watcher) discardDuplicate(path string, held photo.Photo) error {
	if held.PurgedAt.IsZero() {
		if _, err := os.Stat(filepath.Join(w.root, filepath.FromSlash(held.Path))); err != nil {
			w.log.Error("ingest: recorded copy is missing; leaving the file in the hot folder",
				"path", path, "recorded", held.Path, "err", err)
			return nil
		}
	}

	w.log.Debug("ingest: already held", "path", path, "hash", held.ContentHash[:12])
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove duplicate: %w", err)
	}
	return nil
}

// Recover re-files anything on disk that has no row, and is why a crash between
// the rename and the insert is survivable.
//
// Content-addressed rows make this cheap to be wrong about: re-offering a file
// that is already recorded is a lookup that says yes.
func (w *Watcher) Recover(ctx context.Context) error {
	roots := []string{
		filepath.Join(w.root, SessionsDir),
		filepath.Join(w.root, UnassignedDir),
	}

	for _, dir := range roots {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return nil
			case err != nil:
				return err
			case d.IsDir() || !isJPEG(d.Name()):
				return nil
			}

			hash, err := hashFile(path)
			if err != nil {
				return err
			}
			switch has, err := w.photos.Has(ctx, hash); {
			case err != nil:
				return err
			case has:
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return err
			}
			// The directory it is sitting in is the attribution. Re-deriving it
			// from the clock would be wrong: the session it belonged to may have
			// closed long before this scan runs.
			sessionID := ""
			if parent := filepath.Base(filepath.Dir(path)); parent != UnassignedDir {
				sessionID = parent
			}
			return w.recordFound(ctx, path, sessionID, hash, info)
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("ingest: recover %s: %w", dir, err)
		}
	}
	return nil
}

func (w *Watcher) recordFound(ctx context.Context, path, sessionID, hash string, info fs.FileInfo) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		// A truncated file from a crash mid-write. Left on disk rather than
		// deleted: it is a customer's frame, and a human can look.
		w.log.Warn("ingest: unreadable file left in place", "path", path, "err", err)
		return nil
	}

	if _, err := w.photos.Record(ctx, photo.Photo{
		SessionID:   sessionID,
		ContentHash: hash,
		Path:        filepath.ToSlash(mustRel(w.root, path)),
		Bytes:       info.Size(),
		Width:       cfg.Width,
		Height:      cfg.Height,
		Source:      photo.HotFolder,
		CapturedAt:  info.ModTime(),
	}); err != nil && !errors.Is(err, photo.ErrDuplicate) {
		return err
	}
	w.log.Info("ingest: recovered", "path", path, "session", orOrphan(sessionID))
	return nil
}

// move renames, falling back to copy-and-remove across filesystems.
//
// The rename is the fast path and the one the design assumes: same filesystem,
// so it is a metadata operation and the file is never half-present at the
// destination. But the hot folder is wherever EOS Utility's preferences point,
// which may be another drive on a Windows box, and a booth that refuses to
// ingest because of that is worse than one that copies.
func move(src, dest string) error {
	if err := os.Rename(src, dest); err == nil {
		return nil
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	// Written under a temporary name and renamed into place, so a crash during
	// the copy cannot leave a truncated file that looks ingested.
	tmp := dest + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Remove(src)
}

// endsWithEOI reports whether the file ends with the JPEG End Of Image marker.
//
// The other half of the truncated-JPEG defence. A file whose size has settled
// is not necessarily complete — the writer may simply be between writes — and
// this is the cheap, definitive check that it is.
func endsWithEOI(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	if _, err := f.Seek(-2, io.SeekEnd); err != nil {
		return false
	}
	var tail [2]byte
	if _, err := io.ReadFull(f, tail[:]); err != nil {
		return false
	}
	return bytes.Equal(tail[:], eoi)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func isJPEG(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		return true
	}
	return false
}

func mustRel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}

func orOrphan(id string) string {
	if id == "" {
		return "(orphan)"
	}
	return id
}
