package httpd

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"

	"github.com/bhaktiyudha/bykami/agent/internal/clip"
	"github.com/bhaktiyudha/bykami/agent/internal/photo"
)

// maxClip bounds an uploaded burst.
//
// A five-second clip at twenty frames a second, grabbed at the size it will be
// delivered, is a hundred JPEGs of roughly eighty kilobytes — call it eight
// megabytes. This is generous against that and still small enough that a
// runaway client cannot fill the booth's disk one shutter at a time.
//
// It has to stay ahead of what the kiosk actually sends. This cap is enforced by
// MaxBytesReader, which cuts the body off mid-frame, and the handler below
// treats a truncated burst as a bad upload and deletes the lot — so a cap set
// under what the booth grabs would not degrade the clips, it would silently
// drop every one of them. See CLIP_FPS and CLIP_LONG_EDGE in the kiosk.
const maxClip = 24 << 20

// minClipFrames is the fewest frames worth keeping. Two is what it takes to
// animate anything; below that the customer is better served by the still they
// already have than by a "moving" photo that does not move.
const minClipFrames = 2

// captureClip accepts the seconds of camera around one shutter.
//
// Posted after the frame itself rather than with it, and that ordering is the
// whole reason this is a separate route. /api/capture is on the shutter path —
// the one place in the booth where latency is the product — and a burst is
// twenty times the bytes of the frame it belongs to. Sending them together
// would put the next countdown behind an upload nobody is waiting for.
//
// The frames land in a tree of their own and never touch the photos table. See
// the clips migration for what happens to the strip picker and the take limit
// if they ever do.
func (s *Server) captureClip(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if s.Clips == nil {
		// Motion is off on this booth. Not an error the screen should shout
		// about: the customer's photographs are unaffected, and the kiosk posts
		// these without waiting for an answer.
		s.reject(w, http.StatusNotFound, "Booth ini tidak merekam gerak.")
		return
	}

	sess, ok, err := s.Sessions.Current(ctx)
	if err != nil {
		s.fail(w, "current session", err)
		return
	}
	if !ok {
		s.reject(w, http.StatusConflict, "Tidak ada sesi.")
		return
	}

	p, err := s.Photos.Get(ctx, r.PathValue("photo"))
	switch {
	case errors.Is(err, photo.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		s.fail(w, "clip photo", err)
		return
	}

	// The frame has to belong to the session that is live. Without this a burst
	// could be filed against any photo the booth has ever taken, which would
	// put one customer's five seconds into another customer's download page.
	if p.SessionID != sess.ID {
		http.NotFound(w, r)
		return
	}

	// Already have this moment. The kiosk posts fire-and-forget, so a client
	// retrying a request whose response it never saw is the ordinary way this
	// happens — and rewriting the frames underneath a render in flight would
	// turn a duplicate into a corrupt clip.
	if _, err := s.Clips.ByPhoto(ctx, p.ID); err == nil {
		s.write(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true})
		return
	} else if !errors.Is(err, clip.ErrNotFound) {
		s.fail(w, "clip by photo", err)
		return
	}

	// Wrapped before the multipart reader is built, so the cap applies to what
	// is read off the wire rather than to any one part.
	r.Body = http.MaxBytesReader(w, r.Body, maxClip)

	mr, err := r.MultipartReader()
	if err != nil {
		s.reject(w, http.StatusBadRequest, "Rekaman tidak valid.")
		return
	}

	rel := clip.DirFor(p.SessionID, p.ID)
	dir := filepath.Join(s.Root, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.fail(w, "clip dir", err)
		return
	}

	n, err := drainFrames(mr, dir)
	if err != nil {
		// Everything or nothing. A half-written directory with no row would be
		// invisible to purge, which walks rows — so it would sit on the booth
		// PC holding a customer's face until somebody found it by hand.
		os.RemoveAll(dir)
		s.reject(w, http.StatusBadRequest, "Rekaman terputus.")
		return
	}
	if n < minClipFrames {
		os.RemoveAll(dir)
		s.reject(w, http.StatusBadRequest, "Rekaman terlalu pendek.")
		return
	}

	// The row after the files, the same ordering ingest and derive use: a crash
	// in between leaves a directory the next upload overwrites, where the other
	// order would leave the render worker failing on it for seven days.
	if _, err := s.Clips.Record(ctx, clip.Clip{
		PhotoID: p.ID, SessionID: p.SessionID,
		Dir: rel, Frames: n, CapturedAt: p.CapturedAt,
	}); err != nil {
		if errors.Is(err, clip.ErrDuplicate) {
			// Two bursts for one frame raced. The frames on disk are the same
			// five seconds either way, so the loser reports success.
			s.write(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true})
			return
		}
		os.RemoveAll(dir)
		s.fail(w, "record clip", err)
		return
	}

	s.write(w, http.StatusCreated, map[string]any{"ok": true, "frames": n})
}

// drainFrames writes each part to its own numbered file and returns how many
// landed.
//
// Streamed part by part rather than through ParseMultipartForm, which would
// buffer the whole burst in memory or spill it to a temporary directory before
// a single frame reached where it belongs.
func drainFrames(mr *multipart.Reader, dir string) (int, error) {
	n := 0
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		if part.FormName() != "frame" {
			part.Close()
			continue
		}

		if err := writeFrame(part, filepath.Join(dir, clip.FrameName(n))); err != nil {
			part.Close()
			return n, err
		}
		part.Close()
		n++
	}
}

func writeFrame(r io.Reader, dest string) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dest)
		return err
	}
	return nil
}
