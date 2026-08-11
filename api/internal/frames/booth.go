package frames

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// What a booth says it is offering, which is not the same question as what is
// in the catalogue.
//
// The catalogue above is the half an operator controls. A booth's actual set is
// that, plus the designs compiled into the agent binary, plus anything in its
// local -templates directory — added together rather than chosen between. The
// console used to show only the catalogue, so the seven built-in designs every
// customer picks from appeared nowhere in it, and a published frame that a
// booth had not managed to download looked identical to one it was showing.
//
// This is the booth's own answer, cached. It is never consulted to decide
// anything: nothing here feeds the manifest, the season, or what a booth is
// sent next. It exists so that a person can see what is on the machine.

var (
	// ErrNoArtwork means no stored PNG has that hash.
	ErrNoArtwork = errors.New("frames: no artwork with that hash")
	// ErrArtworkMismatch means the uploaded bytes are not what the hash says.
	ErrArtworkMismatch = errors.New("frames: artwork does not match its hash")
	// ErrBadOutlet means the booth named itself something that is not a slug.
	ErrBadOutlet = errors.New("frames: outlet is not a valid id")
	// ErrBadDesign means a reported design is not describable.
	ErrBadDesign = errors.New("frames: booth reported a design that makes no sense")
)

// Design is one frame a booth is offering, as the booth describes it.
type Design struct {
	ID     string
	Name   string
	Layout Layout
	Cells  []Cell

	// SHA256 of the overlay artwork, hex, or empty for a design that draws
	// none — a plain photo template has no frame to show.
	SHA256 string
}

// valid bounds what a booth may say about a design.
//
// The id is not held to the slug rule the catalogue applies, deliberately. A
// design dropped into a booth's own -templates folder takes its directory name
// as its id, and a folder called "Wisuda 2026" is a perfectly working template
// on that machine — refusing it here would throw away the whole report, and
// with it the ten designs that are fine. Nothing on this side joins the id onto
// a path; it is escaped into HTML and bound into SQL, so length is the only
// thing that has to be true of it.
func (d Design) valid() error {
	switch {
	case d.ID == "" || len(d.ID) > 100:
		return fmt.Errorf("%w: id %q", ErrBadDesign, d.ID)
	case len(d.Name) > 200:
		return fmt.Errorf("%w: %q has an unreasonable name", ErrBadDesign, d.ID)
	case d.Layout != Strip2x6 && d.Layout != R4 && d.Layout != Sheet6x8:
		// Checked here so an unknown layout is a 400 naming the design rather
		// than the schema's CHECK failing as an unexplained 500.
		return fmt.Errorf("%w: %q has layout %q", ErrBadDesign, d.ID, d.Layout)
	case d.SHA256 != "" && len(d.SHA256) != 64:
		return fmt.Errorf("%w: %q has a hash that is not a SHA-256", ErrBadDesign, d.ID)
	}
	return nil
}

// Booth is one booth's report.
type Booth struct {
	Outlet     string
	ReportedAt time.Time
	Designs    []Design
}

// Booths stores what booths report. Separate from Catalogue because the two
// answer different questions and only one of them is authoritative: a row here
// is hearsay from a machine in a shop, and treating it as catalogue state is
// how a booth would end up able to publish its own frames.
type Booths struct{ db *sql.DB }

func NewBooths(db *sql.DB) *Booths { return &Booths{db: db} }

// Report replaces what this booth is known to offer, and returns the hashes of
// the artwork the server does not have yet.
//
// Replaces rather than merges: the report is the whole set, so a design missing
// from it is a design the booth no longer offers, and merging would leave the
// console showing a frame that was taken off the machine months ago.
func (b *Booths) Report(ctx context.Context, outlet string, designs []Design) ([]string, error) {
	if !validOutlet(outlet) {
		return nil, fmt.Errorf("%w: %q", ErrBadOutlet, outlet)
	}
	for _, d := range designs {
		if err := d.valid(); err != nil {
			return nil, err
		}
	}

	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("frames: booth report: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO booth_reports (outlet, reported_at) VALUES (?, ?)
		 ON CONFLICT(outlet) DO UPDATE SET reported_at = excluded.reported_at`,
		outlet, time.Now().UTC().Unix()); err != nil {
		return nil, fmt.Errorf("frames: booth report: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM booth_designs WHERE outlet = ?`, outlet); err != nil {
		return nil, fmt.Errorf("frames: booth designs: %w", err)
	}

	for i, d := range designs {
		cells, err := json.Marshal(d.Cells)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO booth_designs (outlet, id, name, layout, cells, sha256, position)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			outlet, d.ID, d.Name, string(d.Layout), string(cells), d.SHA256, i); err != nil {
			return nil, fmt.Errorf("frames: booth design %q: %w", d.ID, err)
		}
	}

	// Artwork no booth still lists. Left behind otherwise, because nothing else
	// ever deletes from a BLOB table — and the rows that accumulate are exactly
	// the designs somebody replaced, which is the common case over a year.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM booth_artwork
		  WHERE sha256 NOT IN (SELECT sha256 FROM booth_designs)`); err != nil {
		return nil, fmt.Errorf("frames: prune booth artwork: %w", err)
	}

	// Asked inside the same transaction as the write, so the answer describes
	// the set that was just stored rather than one a concurrent report changed.
	want, err := missingArtwork(ctx, tx, designs)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("frames: booth report: %w", err)
	}
	return want, nil
}

// missingArtwork returns the hashes not already held, in report order and
// without repeats.
//
// The catalogue counts as held: a synced frame's bytes are already in `frames`
// under the same hash, so asking a booth on a shop's internet connection to
// upload them back would be a download the server made it pay for twice.
func missingArtwork(ctx context.Context, tx *sql.Tx, designs []Design) ([]string, error) {
	var want []string
	seen := make(map[string]bool, len(designs))
	for _, d := range designs {
		if d.SHA256 == "" || seen[d.SHA256] {
			continue
		}
		seen[d.SHA256] = true

		var have bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM booth_artwork WHERE sha256 = ?)
			     OR EXISTS (SELECT 1 FROM frames        WHERE sha256 = ?)`,
			d.SHA256, d.SHA256).Scan(&have); err != nil {
			return nil, fmt.Errorf("frames: artwork check: %w", err)
		}
		if !have {
			want = append(want, d.SHA256)
		}
	}
	return want, nil
}

// StoreArtwork keeps a booth's PNG under its hash.
//
// The hash is verified here rather than by the caller, because this is the one
// place it cannot be forgotten: a mismatch means a truncated upload or a proxy
// that rewrote the body, and storing it would put bytes under a name that is
// not theirs — which is the same check framesync makes in the other direction.
func (b *Booths) StoreArtwork(ctx context.Context, want string, png []byte) error {
	sum := sha256.Sum256(png)
	if hex.EncodeToString(sum[:]) != want {
		return ErrArtworkMismatch
	}
	// Nothing to update: the hash is the content, so a second upload of the
	// same bytes has nothing new to say.
	_, err := b.db.ExecContext(ctx,
		`INSERT INTO booth_artwork (sha256, png, stored_at) VALUES (?, ?, ?)
		 ON CONFLICT(sha256) DO NOTHING`,
		want, png, time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("frames: store booth artwork: %w", err)
	}
	return nil
}

// Artwork returns the PNG with that hash, from either table.
//
// Either, because a booth's set mixes the two: a synced frame's bytes are in
// the catalogue and a built-in design's are only ever here. The console asks by
// hash and does not have to know which it is looking at.
func (b *Booths) Artwork(ctx context.Context, sum string) ([]byte, error) {
	var png []byte
	err := b.db.QueryRowContext(ctx,
		`SELECT png FROM booth_artwork WHERE sha256 = ?
		 UNION ALL
		 SELECT png FROM frames        WHERE sha256 = ?
		 LIMIT 1`, sum, sum).Scan(&png)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoArtwork
	}
	if err != nil {
		return nil, fmt.Errorf("frames: booth artwork: %w", err)
	}
	return png, nil
}

// All returns every booth's report, most recently heard from first.
func (b *Booths) All(ctx context.Context) ([]Booth, error) {
	rows, err := b.db.QueryContext(ctx,
		`SELECT r.outlet, r.reported_at, d.id, d.name, d.layout, d.cells, d.sha256
		   FROM booth_reports r
		   LEFT JOIN booth_designs d ON d.outlet = r.outlet
		  ORDER BY r.reported_at DESC, r.outlet, d.position`)
	if err != nil {
		return nil, fmt.Errorf("frames: booths: %w", err)
	}
	defer rows.Close()

	var out []Booth
	for rows.Next() {
		var outlet string
		var reported int64
		// LEFT JOIN, so a booth that reported an empty set is one row of NULLs
		// rather than no rows. That booth exists and has nothing to offer, which
		// is a thing worth being able to see.
		var id, name, layout, cells, sum sql.NullString
		if err := rows.Scan(&outlet, &reported, &id, &name, &layout, &cells, &sum); err != nil {
			return nil, fmt.Errorf("frames: booths: scan: %w", err)
		}

		if len(out) == 0 || out[len(out)-1].Outlet != outlet {
			out = append(out, Booth{Outlet: outlet, ReportedAt: time.Unix(reported, 0).UTC()})
		}
		if !id.Valid {
			continue
		}
		d := Design{ID: id.String, Name: name.String, Layout: Layout(layout.String), SHA256: sum.String}
		if err := json.Unmarshal([]byte(cells.String), &d.Cells); err != nil {
			return nil, fmt.Errorf("frames: booth cells for %s/%s: %w", outlet, d.ID, err)
		}
		last := &out[len(out)-1]
		last.Designs = append(last.Designs, d)
	}
	return out, rows.Err()
}

// validOutlet is the slug rule, checked where the name arrives from a booth.
// Restated rather than shared with slug() above, which normalises rather than
// judges: silently rewriting a booth's own name for it would make the console
// list an outlet nobody configured.
func validOutlet(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}
