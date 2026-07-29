// Package frames is the frame catalogue: the designs a booth offers a customer.
//
// The gap against the incumbent is 99+ templates versus six (design/kiosk.md),
// and templates are content work rather than engineering. So this is a library,
// not an authoring tool: a designer draws a PNG with holes where the photos go,
// an operator uploads it, and everything the booth needs is read back out of
// the file.
//
// # Nothing about the frame is typed in twice
//
// The sheet size comes from the PNG's dimensions and the photo cells come from
// its transparent regions. An operator supplies a name, a group, and optionally
// the dates it should run. There is no form field for a rectangle, because a
// rectangle typed next to a picture that already contains it is a chance to
// disagree with the picture — and the symptom is a face printed off its slot,
// discovered on paper.
//
// # Why the artwork lives in SQLite
//
// A frame is tens of kilobytes and a full catalogue is single-digit megabytes,
// which is smaller than the loyalty ledger will be within a year. Object
// storage would add a bucket, a credential to rotate, and a second thing that
// can be up or down while the database is the other; a BLOB adds a column. It
// also keeps the artwork inside the same backup and the same transaction as the
// row describing it, so a restored database can never reference a frame whose
// bytes went missing.
package frames

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/store"
)

var (
	// ErrNotPNG means the upload did not decode as a PNG.
	ErrNotPNG = errors.New("frames: not a PNG")
	// ErrOpaque means the PNG has no transparency at all, so there is nowhere
	// for a photo to show through.
	ErrOpaque = errors.New("frames: the artwork has no transparent area")
	// ErrNoCells means it has transparency, but nothing large enough — or
	// anything like compact enough — to be a photo slot.
	ErrNoCells = errors.New("frames: no photo slots found in the artwork")
	// ErrSheetSize means the image is not one of the printable sheet sizes.
	ErrSheetSize = errors.New("frames: not a printable sheet size")
	// ErrNoName means the frame was submitted without one.
	ErrNoName = errors.New("frames: a name is required")
	// ErrDuplicate means another frame already claims that id.
	ErrDuplicate = errors.New("frames: a frame with that name already exists")
	// ErrNoFrame means no frame has that id.
	ErrNoFrame = errors.New("frames: no such frame")
	// ErrBadWindow means the season ends before it starts.
	ErrBadWindow = errors.New("frames: the season ends before it begins")
)

// Layout names the sheet a frame prints on. The strings match
// agent/internal/printer, because they travel to the booth in the manifest and
// are what its template loader parses.
type Layout string

const (
	Strip2x6 Layout = "strip2x6"
	R4       Layout = "4r"
	Sheet6x8 Layout = "6x8"
)

// sheets maps a printable size at 300 dpi to its layout.
//
// The upload's dimensions choose the layout rather than a dropdown doing it.
// That removes the mismatch outright: a 600×1800 file tagged "4r" would compose
// a strip's artwork onto a postcard, stretched, and nothing before the printed
// sheet would notice.
var sheets = map[[2]int]Layout{
	{600, 1800}:  Strip2x6,
	{1200, 1800}: R4,
	{1800, 2400}: Sheet6x8,
}

// SheetSizes lists the accepted dimensions, for an error message that says what
// would have worked rather than only that this did not.
func SheetSizes() string {
	return "600×1800 (strip 2×6in), 1200×1800 (4R), 1800×2400 (6×8in), all at 300 dpi"
}

// Frame is one design in the catalogue.
type Frame struct {
	// ID is a slug derived from the name. It becomes a directory name on the
	// booth PC, which is why validSlug is strict about what may be in it.
	ID     string
	Name   string
	Group  string
	Layout Layout
	Cells  []Cell
	Width  int
	Height int

	// SHA256 of the artwork, hex. The booth syncs on this: unchanged hash means
	// it already has the bytes and can skip the download.
	SHA256 string
	Bytes  int

	Published bool

	// ActiveFrom and ActiveUntil bound the season. Zero means unbounded at that
	// end, so a frame with neither set runs whenever it is published.
	ActiveFrom  time.Time
	ActiveUntil time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Live reports whether the booth should be offering this frame at t.
func (f Frame) Live(t time.Time) bool {
	switch {
	case !f.Published:
		return false
	case !f.ActiveFrom.IsZero() && t.Before(f.ActiveFrom):
		return false
	case !f.ActiveUntil.IsZero() && !t.Before(f.ActiveUntil):
		return false
	}
	return true
}

type Catalogue struct{ db *sql.DB }

func New(db *sql.DB) *Catalogue { return &Catalogue{db: db} }

// NewFrame is what an operator supplies. Everything else about a frame is read
// out of the artwork.
type NewFrame struct {
	Name        string
	Group       string
	PNG         []byte
	ActiveFrom  time.Time
	ActiveUntil time.Time
}

// Create validates the artwork, reads its geometry, and stores it unpublished.
//
// Unpublished on purpose. Detection is inference from a picture, and the one
// thing that catches a wrong inference is a person looking at the slots drawn
// over the frame — so a new upload cannot reach a customer until someone has
// had the chance to look.
func (c *Catalogue) Create(ctx context.Context, n NewFrame) (Frame, error) {
	name := strings.TrimSpace(n.Name)
	if name == "" {
		return Frame{}, ErrNoName
	}
	id := slug(name)
	if id == "" {
		return Frame{}, ErrNoName
	}
	if !n.ActiveFrom.IsZero() && !n.ActiveUntil.IsZero() && !n.ActiveFrom.Before(n.ActiveUntil) {
		return Frame{}, ErrBadWindow
	}

	w, h, cells, err := Detect(n.PNG)
	if err != nil {
		return Frame{}, err
	}
	layout, ok := sheets[[2]int{w, h}]
	if !ok {
		return Frame{}, fmt.Errorf("%w: %d×%d", ErrSheetSize, w, h)
	}

	cellsJSON, err := json.Marshal(cells)
	if err != nil {
		return Frame{}, err
	}
	sum := sha256.Sum256(n.PNG)

	f := Frame{
		ID: id, Name: name, Group: strings.TrimSpace(n.Group),
		Layout: layout, Cells: cells, Width: w, Height: h,
		SHA256: hex.EncodeToString(sum[:]), Bytes: len(n.PNG),
		ActiveFrom: n.ActiveFrom, ActiveUntil: n.ActiveUntil,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	_, err = c.db.ExecContext(ctx,
		`INSERT INTO frames
		   (id, name, group_name, layout, cells, png, sha256, width, height,
		    published, active_from, active_until, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		f.ID, f.Name, f.Group, string(f.Layout), string(cellsJSON), n.PNG, f.SHA256,
		f.Width, f.Height, nullTime(f.ActiveFrom), nullTime(f.ActiveUntil),
		f.CreatedAt.Unix(), f.UpdatedAt.Unix())
	if store.IsConstraint(err) {
		return Frame{}, ErrDuplicate
	}
	if err != nil {
		return Frame{}, fmt.Errorf("frames: insert: %w", err)
	}
	return f, nil
}

// List returns every frame, newest first. The console shows all of them —
// unpublished and out of season included, since those are exactly the ones an
// operator has come to do something about.
func (c *Catalogue) List(ctx context.Context) ([]Frame, error) {
	return c.query(ctx, `SELECT `+columns+` FROM frames ORDER BY created_at DESC`)
}

// Live returns the frames a booth should be offering at t, in a stable order.
func (c *Catalogue) Live(ctx context.Context, t time.Time) ([]Frame, error) {
	// Filtered in SQL rather than in Go so that a booth polling every few
	// minutes does not pull the whole catalogue to discard most of it.
	return c.query(ctx,
		`SELECT `+columns+` FROM frames
		  WHERE published = 1
		    AND (active_from  IS NULL OR active_from  <= ?)
		    AND (active_until IS NULL OR active_until >  ?)
		  ORDER BY group_name, name`, t.Unix(), t.Unix())
}

func (c *Catalogue) Get(ctx context.Context, id string) (Frame, error) {
	out, err := c.query(ctx, `SELECT `+columns+` FROM frames WHERE id = ?`, id)
	if err != nil {
		return Frame{}, err
	}
	if len(out) == 0 {
		return Frame{}, ErrNoFrame
	}
	return out[0], nil
}

// Artwork returns the stored PNG. Kept off Frame so that listing the catalogue
// does not read every frame's bytes into memory to render a table of names.
func (c *Catalogue) Artwork(ctx context.Context, id string) ([]byte, string, error) {
	var png []byte
	var sum string
	err := c.db.QueryRowContext(ctx, `SELECT png, sha256 FROM frames WHERE id = ?`, id).Scan(&png, &sum)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNoFrame
	}
	if err != nil {
		return nil, "", fmt.Errorf("frames: artwork: %w", err)
	}
	return png, sum, nil
}

// SetPublished is the switch between "in the catalogue" and "on the booth".
func (c *Catalogue) SetPublished(ctx context.Context, id string, published bool) error {
	return c.touch(ctx, `UPDATE frames SET published = ?, updated_at = ? WHERE id = ?`,
		published, time.Now().UTC().Unix(), id)
}

// SetSeason changes the date window. Zero times clear that end of it.
func (c *Catalogue) SetSeason(ctx context.Context, id string, from, until time.Time) error {
	if !from.IsZero() && !until.IsZero() && !from.Before(until) {
		return ErrBadWindow
	}
	return c.touch(ctx, `UPDATE frames SET active_from = ?, active_until = ?, updated_at = ? WHERE id = ?`,
		nullTime(from), nullTime(until), time.Now().UTC().Unix(), id)
}

// Delete removes a frame outright.
//
// A real delete rather than a soft one: a frame carries no history worth
// keeping — no session references it, because the booth records the template id
// it printed with as a string. Unpublishing is the reversible action, and it is
// the one an operator reaches for; delete is for the file that was wrong.
func (c *Catalogue) Delete(ctx context.Context, id string) error {
	res, err := c.db.ExecContext(ctx, `DELETE FROM frames WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("frames: delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoFrame
	}
	return nil
}

const columns = `id, name, group_name, layout, cells, sha256, width, height,
                 published, active_from, active_until, created_at, updated_at,
                 length(png)`

func (c *Catalogue) query(ctx context.Context, q string, args ...any) ([]Frame, error) {
	rows, err := c.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("frames: query: %w", err)
	}
	defer rows.Close()

	var out []Frame
	for rows.Next() {
		var f Frame
		var layout, cellsJSON string
		var from, until sql.NullInt64
		var created, updated int64
		if err := rows.Scan(&f.ID, &f.Name, &f.Group, &layout, &cellsJSON, &f.SHA256,
			&f.Width, &f.Height, &f.Published, &from, &until, &created, &updated, &f.Bytes); err != nil {
			return nil, fmt.Errorf("frames: scan: %w", err)
		}
		if err := json.Unmarshal([]byte(cellsJSON), &f.Cells); err != nil {
			return nil, fmt.Errorf("frames: cells for %s: %w", f.ID, err)
		}
		f.Layout = Layout(layout)
		f.ActiveFrom, f.ActiveUntil = fromNull(from), fromNull(until)
		f.CreatedAt, f.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		out = append(out, f)
	}
	return out, rows.Err()
}

func (c *Catalogue) touch(ctx context.Context, q string, args ...any) error {
	res, err := c.db.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("frames: update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoFrame
	}
	return nil
}

// slug turns a name into an id that is safe as a directory name on the booth.
//
// Restrictive rather than clever. This string is joined onto a path by the
// agent, so anything outside [a-z0-9-] is dropped rather than escaped — there
// is no encoding of "../" that survives this, which is the point.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			// One dash for any run of separators, and never a leading one.
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

func fromNull(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return time.Unix(n.Int64, 0).UTC()
}
