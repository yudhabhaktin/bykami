// Package photo is the record of every frame the booth has taken.
//
// Rows outlive files. The original is purged from the booth PC seven days
// after upload, and the row stays — it is how the agent knows not to re-ingest
// bytes it has already seen, and how a question asked three weeks later is
// answerable at all once the pixels are gone.
package photo

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/store"
)

// ErrDuplicate means these exact bytes are already recorded.
//
// Not a failure. The crash-recovery rescan re-offers everything it finds on
// disk, so the ordinary case for a restart is a run of these.
var ErrDuplicate = errors.New("photo: already ingested")

var ErrNotFound = errors.New("photo: not found")

// Source is how a frame arrived. Two values, and the difference is not
// cosmetic: see the resolution table in design/kiosk.md.
type Source string

const (
	// HotFolder is the production path — a real camera, tethered by vendor
	// software, writing full-resolution JPEGs into a watched directory.
	HotFolder Source = "hotfolder"

	// Webcam is the development path. It exists so the whole flow can be run
	// before a camera, a shutter relay, a printer and a booth PC exist, and it
	// is not a product tier: 1080p is ~180 dpi at 4R, visibly soft, and would
	// make the one thing customers pay for worse than what the studio delivers
	// today.
	Webcam Source = "webcam"
)

type Photo struct {
	ID string
	// Empty means an orphan: a staff test shot or an accidental fire that
	// landed outside any session.
	SessionID   string
	ContentHash string
	// Relative to the store root. The booth PC's drive letter is not a fact
	// worth persisting.
	Path        string
	Bytes       int64
	Width       int
	Height      int
	Source      Source
	CapturedAt  time.Time
	IngestedAt  time.Time
	DerivedPath string
	DerivedAt   time.Time
	UploadedAt  time.Time
	PurgedAt    time.Time
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

// Record inserts a frame. sessionID may be empty for an orphan.
//
// Returns ErrDuplicate when the content hash is already present, which is the
// mechanism that makes re-scanning the disk at startup safe rather than a
// source of duplicate rows.
func (s *Store) Record(ctx context.Context, p Photo) (Photo, error) {
	p.ID = newID()
	p.IngestedAt = s.now()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO photos
		   (id, session_id, content_hash, path, bytes, width, height, source, captured_at, ingested_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, nullIfEmpty(p.SessionID), p.ContentHash, p.Path, p.Bytes,
		p.Width, p.Height, string(p.Source), p.CapturedAt.Unix(), p.IngestedAt.Unix(),
	)
	switch {
	case store.IsConstraint(err):
		return Photo{}, ErrDuplicate
	case err != nil:
		return Photo{}, fmt.Errorf("photo: record: %w", err)
	}
	return p, nil
}

// Has reports whether these bytes are already recorded. Used by the rescan to
// skip work before hashing a file a second time is wasted.
func (s *Store) Has(ctx context.Context, contentHash string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM photos WHERE content_hash = ?`, contentHash,
	).Scan(&n); err != nil {
		return false, fmt.Errorf("photo: has: %w", err)
	}
	return n > 0, nil
}

// ByHash returns the row holding these exact bytes.
//
// Ingest uses it to check that the copy it already holds is still on disk
// before deleting a second one, so that "we have this already" can never become
// "we had this already".
func (s *Store) ByHash(ctx context.Context, contentHash string) (Photo, error) {
	p, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM photos WHERE content_hash = ?`, contentHash))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Photo{}, ErrNotFound
	case err != nil:
		return Photo{}, fmt.Errorf("photo: by hash: %w", err)
	}
	return p, nil
}

func (s *Store) Get(ctx context.Context, id string) (Photo, error) {
	p, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM photos WHERE id = ?`, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Photo{}, ErrNotFound
	case err != nil:
		return Photo{}, fmt.Errorf("photo: get: %w", err)
	}
	return p, nil
}

// BySession returns a session's frames oldest first, which is the order the
// customer took them and therefore the order to show them in.
//
// The tiebreak is rowid, not id. captured_at has one-second resolution and a
// photobooth fires faster than that, so several frames routinely share a
// timestamp — and id is random hex, which would shuffle them. rowid is
// insertion order, which is the order they arrived and therefore the order they
// were taken. Getting this wrong reorders the customer's strip.
func (s *Store) BySession(ctx context.Context, sessionID string) ([]Photo, error) {
	return s.query(ctx, `SELECT `+columns+` FROM photos WHERE session_id = ? ORDER BY captured_at, rowid`, sessionID)
}

// Orphans returns frames attributed to no session, newest first. Visible in
// admin so that a frame which missed its session by a second can be found.
func (s *Store) Orphans(ctx context.Context, limit int) ([]Photo, error) {
	return s.query(ctx, `SELECT `+columns+` FROM photos WHERE session_id IS NULL ORDER BY captured_at DESC LIMIT ?`, limit)
}

// SetDerived records the delivered derivative. The original is untouched:
// printing from a recompressed file would give back exactly what full
// resolution capture bought.
func (s *Store) SetDerived(ctx context.Context, id, path string) error {
	return s.touch(ctx, id,
		`UPDATE photos SET derived_path = ?, derived_at = ? WHERE id = ?`,
		path, s.now().Unix(), id)
}

func (s *Store) SetUploaded(ctx context.Context, id string) error {
	return s.touch(ctx, id, `UPDATE photos SET uploaded_at = ? WHERE id = ?`, s.now().Unix(), id)
}

// Purgeable returns frames whose originals are older than the retention window
// and still on disk.
//
// Seven days, and this is the rule that matters most on the booth PC. A hot
// folder never empties itself: twelve months in, an unmanaged studio machine
// holds every customer's face at full resolution, unencrypted, in a room where
// strangers are left alone with it. Seven days means a theft leaks a week.
func (s *Store) Purgeable(ctx context.Context, olderThan time.Duration) ([]Photo, error) {
	cutoff := s.now().Add(-olderThan).Unix()
	return s.query(ctx,
		`SELECT `+columns+` FROM photos
		  WHERE purged_at IS NULL AND ingested_at <= ?
		  ORDER BY ingested_at`, cutoff)
}

// MarkPurged records that the file is gone. The row stays.
func (s *Store) MarkPurged(ctx context.Context, id string) error {
	return s.touch(ctx, id, `UPDATE photos SET purged_at = ? WHERE id = ?`, s.now().Unix(), id)
}

func (s *Store) touch(ctx context.Context, id, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("photo: update %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]Photo, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("photo: query: %w", err)
	}
	defer rows.Close()

	var out []Photo
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("photo: scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const columns = `id, session_id, content_hash, path, bytes, width, height, source,
	captured_at, ingested_at, derived_path, derived_at, uploaded_at, purged_at`

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Photo, error) {
	var (
		p                               Photo
		sessionID, derivedPath          sql.NullString
		derivedAt, uploadedAt, purgedAt sql.NullInt64
		capturedAt, ingestedAt          int64
		source                          string
	)
	if err := row.Scan(
		&p.ID, &sessionID, &p.ContentHash, &p.Path, &p.Bytes, &p.Width, &p.Height,
		&source, &capturedAt, &ingestedAt, &derivedPath, &derivedAt, &uploadedAt, &purgedAt,
	); err != nil {
		return Photo{}, err
	}
	p.SessionID = sessionID.String
	p.Source = Source(source)
	p.DerivedPath = derivedPath.String
	p.CapturedAt = time.Unix(capturedAt, 0)
	p.IngestedAt = time.Unix(ingestedAt, 0)
	if derivedAt.Valid {
		p.DerivedAt = time.Unix(derivedAt.Int64, 0)
	}
	if uploadedAt.Valid {
		p.UploadedAt = time.Unix(uploadedAt.Int64, 0)
	}
	if purgedAt.Valid {
		p.PurgedAt = time.Unix(purgedAt.Int64, 0)
	}
	return p, nil
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
		panic("photo: entropy unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
