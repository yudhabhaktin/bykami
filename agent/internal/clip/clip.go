// Package clip is the moving version of a frame.
//
// The booth's camera is already running and already visible on screen during
// the countdown, so the seconds before the shutter cost nothing to keep. They
// are the seconds worth keeping: people arrange themselves, react to the frame
// they just took, and stop performing about a second before they think the
// picture happens. That is the whole appeal of a Live Photo, and it is sitting
// in a MediaStream the kiosk already holds.
//
// A clip is stored as a run of small JPEGs — not video. The booth binary is
// cross-compiled from macOS with GOOS=windows and cgo would make that
// impossible, which rules out every video encoder worth having; stills go in,
// and an animated GIF comes out of image/gif in the standard library. See
// gif.go.
//
// # Rows outlive files, and frames never become photos
//
// The retention rule is the photo's, inherited: a clip is the same face at the
// same moment, so it dies when its photo does. See [Store.ByPhoto], which is
// how purge finds it.
//
// Nothing here writes to the photos table, and that is load-bearing rather than
// tidy — see the migration for what happens to the take limit if it does.
package clip

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/store"
)

var (
	ErrNotFound = errors.New("clip: not found")

	// ErrDuplicate means this photo already has its moment recorded.
	//
	// Not a failure. The burst is posted fire-and-forget after the shutter, so a
	// client that retries a request whose response it never saw is the ordinary
	// way this happens.
	ErrDuplicate = errors.New("clip: this photo already has a clip")
)

// Dir is where clips live, relative to the store root.
//
// A tree of their own, beside derived/ and for the same reason: ingest.Recover
// walks sessions/ and unassigned/ recording every JPEG without a row, so burst
// frames filed anywhere near the originals would be ingested as brand new
// photos on the next restart — fifty of them per shot.
const Dir = "clips"

// Unassigned mirrors ingest.UnassignedDir, so an orphan frame's clip has
// somewhere to go that is not a directory named after a session that never was.
const Unassigned = "unassigned"

type Clip struct {
	ID      string
	PhotoID string
	// Empty for an orphan, exactly as on the photo it belongs to.
	SessionID string

	// Dir holds this clip's frames, relative to the store root.
	Dir    string
	Frames int

	CapturedAt time.Time

	// GIFPath is the delivered animation, empty until the worker has built it.
	GIFPath  string
	GIFAt    time.Time
	PurgedAt time.Time
}

// FrameName is the file one frame of a clip is stored under.
//
// Zero-padded so that a plain lexical sort is chronological. The order of these
// is the order of time itself; a shuffled clip is not a slower clip, it is a
// broken one.
func FrameName(i int) string { return fmt.Sprintf("%04d.jpg", i) }

// DirFor is where a clip's frames belong, relative to the store root.
//
// One directory per clip, so deleting it is a single RemoveAll. Nested one
// level under a session directory, which is what lets purge's empty-directory
// prune reach these the same way it reaches derived/.
func DirFor(sessionID, photoID string) string {
	dir := sessionID
	if dir == "" {
		dir = Unassigned
	}
	return path.Join(Dir, dir, photoID)
}

// GIFPathFor is where a clip's rendered animation belongs.
//
// Beside the frame directory rather than inside it, so that a render can be
// deleted or rebuilt without walking into the frames, and so the session
// directory holding both is what goes empty at retention.
func GIFPathFor(sessionID, photoID string) string {
	dir := sessionID
	if dir == "" {
		dir = Unassigned
	}
	return path.Join(Dir, dir, photoID+".gif")
}

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Store { return &Store{db: db, now: time.Now} }

// NewWithClock is for tests that assert on ordering or retention windows.
func NewWithClock(db *sql.DB, now func() time.Time) *Store {
	return &Store{db: db, now: now}
}

// Record inserts a clip whose frames are already on disk.
//
// Written after the files, the same ordering ingest and derive use: a crash in
// between leaves an unreferenced directory that the next upload overwrites,
// where the other order would leave a row pointing at nothing for the render
// worker to fail on for seven days.
func (s *Store) Record(ctx context.Context, c Clip) (Clip, error) {
	c.ID = newID()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO clips (id, photo_id, session_id, dir, frames, captured_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, c.PhotoID, nullIfEmpty(c.SessionID), c.Dir, c.Frames, c.CapturedAt.Unix(),
	)
	switch {
	case store.IsConstraint(err):
		return Clip{}, ErrDuplicate
	case err != nil:
		return Clip{}, fmt.Errorf("clip: record: %w", err)
	}
	return c, nil
}

func (s *Store) Get(ctx context.Context, id string) (Clip, error) {
	c, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM clips WHERE id = ?`, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Clip{}, ErrNotFound
	case err != nil:
		return Clip{}, fmt.Errorf("clip: get: %w", err)
	}
	return c, nil
}

// ByPhoto is how purge finds what to delete when a frame reaches retention.
func (s *Store) ByPhoto(ctx context.Context, photoID string) (Clip, error) {
	c, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM clips WHERE photo_id = ?`, photoID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Clip{}, ErrNotFound
	case err != nil:
		return Clip{}, fmt.Errorf("clip: by photo: %w", err)
	}
	return c, nil
}

// Rendered returns a session's finished animations, keyed by the photo each one
// belongs to.
//
// A map because the caller is the download page, which walks the session's
// photos in the order they were taken and asks of each whether it moves. Only
// rendered, unpurged clips are returned: a row whose GIF is not built yet or
// whose frames are gone would become a broken image on a customer's phone.
func (s *Store) Rendered(ctx context.Context, sessionID string) (map[string]Clip, error) {
	cs, err := s.query(ctx,
		`SELECT `+columns+` FROM clips
		  WHERE session_id = ? AND gif_path IS NOT NULL AND purged_at IS NULL`, sessionID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Clip, len(cs))
	for _, c := range cs {
		out[c.PhotoID] = c
	}
	return out, nil
}

// Unrendered returns clips with no animation yet, oldest first.
//
// Purged clips are excluded: their frames are gone and nothing will bring them
// back, so re-offering them would make the worker retry forever.
func (s *Store) Unrendered(ctx context.Context, limit int) ([]Clip, error) {
	return s.query(ctx,
		`SELECT `+columns+` FROM clips
		  WHERE gif_path IS NULL AND purged_at IS NULL
		  ORDER BY captured_at, rowid
		  LIMIT ?`, limit)
}

// SetGIF records the rendered animation.
func (s *Store) SetGIF(ctx context.Context, id, path string) error {
	return s.touch(ctx, id, `UPDATE clips SET gif_path = ?, gif_at = ? WHERE id = ?`,
		path, s.now().Unix(), id)
}

// MarkPurged records that the frames and the animation are gone. The row stays,
// which is what stops the render worker reconsidering it.
func (s *Store) MarkPurged(ctx context.Context, id string) error {
	return s.touch(ctx, id, `UPDATE clips SET purged_at = ? WHERE id = ?`, s.now().Unix(), id)
}

func (s *Store) touch(ctx context.Context, id, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("clip: update %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]Clip, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("clip: query: %w", err)
	}
	defer rows.Close()

	var out []Clip
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("clip: scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

const columns = `id, photo_id, session_id, dir, frames, captured_at, gif_path, gif_at, purged_at`

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Clip, error) {
	var (
		c                  Clip
		sessionID, gifPath sql.NullString
		gifAt, purgedAt    sql.NullInt64
		capturedAt         int64
	)
	if err := row.Scan(
		&c.ID, &c.PhotoID, &sessionID, &c.Dir, &c.Frames, &capturedAt,
		&gifPath, &gifAt, &purgedAt,
	); err != nil {
		return Clip{}, err
	}
	c.SessionID = sessionID.String
	c.GIFPath = gifPath.String
	c.CapturedAt = time.Unix(capturedAt, 0)
	if gifAt.Valid {
		c.GIFAt = time.Unix(gifAt.Int64, 0)
	}
	if purgedAt.Valid {
		c.PurgedAt = time.Unix(purgedAt.Int64, 0)
	}
	return c, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("clip: entropy unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
