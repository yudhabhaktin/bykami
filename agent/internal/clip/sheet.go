package clip

// The moving version of a printed sheet.
//
// A clip is one face for five seconds. This is the whole frame for five
// seconds: every cell playing its own clip at once, inside the artwork the
// designer drew, with the same crop and the same filter the paper got. It is
// the thing the customer is holding, moving — which is why it belongs beside
// the sheet under "Versi bingkai" rather than as a fourteenth thumbnail.
//
// Composed at delivery size and never at 300 dpi. compose.Sheet works from the
// originals because its output goes to a dye-sub printer; this one is going
// down Indonesian mobile data into an <img>, and scaling a 1200x1800 sheet a
// hundred times to throw away seven eighths of it afterwards would cost about
// a minute of a core per print for pixels nobody ever sees.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"path"
	"time"

	xdraw "golang.org/x/image/draw"

	"github.com/bhaktiyudha/bykami/agent/internal/compose"
	"github.com/bhaktiyudha/bykami/agent/internal/store"
)

// SheetLongEdge is the animated sheet's maximum dimension.
//
// The same 400 a single frame gets, reached from the opposite direction and
// kept as its own constant so that changing one does not silently move the
// other: LongEdge is measured against a face filling the frame, this against a
// whole sheet holding six of them.
//
// The temptation is to go bigger, because each cell here is only about a
// quarter of the long edge. The measurement is what refuses: on synthetic
// worst-case motion a five-second six-cell sheet encodes to roughly 1.3 MB at
// 300, 2.1 MB at 400 and 3.1 MB at 500 — and unlike the printed sheet, this one
// is pulled down a phone's mobile connection the instant the page opens.
//
// Those figures came down from 1.7, 2.8 and 4.1 when the encoder moved to a
// palette built from the sheet's own colours. The room that bought was spent on
// the single-photograph animation, which is the one a customer posts, rather
// than here — see LongEdge.
//
// 400 is where the bytes stop buying anything a customer can see. The page
// shows a sheet at min(58vh, 22rem), which is about 350 CSS pixels tall on a
// phone, so 500 is already being scaled down before it is drawn. See
// TestAnimatedSheetStaysUnderTheDeliveryCeiling.
const SheetLongEdge = 400

// SheetFPS is the animated sheet's playback rate, and it is half a frame clip's.
//
// The frames are captured at FPS for the single-photograph animation, where they
// are delivered at 720 on the long edge and the smoothness is visible. A cell on
// a six-slot sheet is about a quarter of 400, and twenty frames a second buys
// almost nothing at that size — while costing exactly double, on a file already
// measured against a ceiling and pulled down mobile data the moment the page
// opens. At the capture rate a five-second sheet lands at 4.5 MB, over it.
//
// RenderSheet takes every other captured frame rather than slowing the playback,
// so the animation still runs for the seconds it was filmed over. A sheet
// playing the same moment at half speed alongside the frames it is made of is
// the defect this is avoiding.
const SheetFPS = 10

// SheetsDir is where animated sheets live, relative to the store root.
//
// The same tree the composed JPEGs go in, which is the whole point: purge
// sweeps sheets/ by file age, so an animation of the customer's faces inherits
// that sweep instead of needing a retention rule of its own to be got right.
// Restated here rather than imported because the print handler builds its path
// from a literal too, and there is a test that the two agree.
const SheetsDir = "sheets"

// SheetGIFPathFor is where one sheet's animation belongs, relative to the store
// root. Named for the sheet clip rather than the print job so that a rebuilt
// render replaces its predecessor instead of accumulating beside it.
func SheetGIFPathFor(sessionID, id string) string {
	return path.Join(SheetsDir, sessionID, id+".gif")
}

// SheetClip is one printed sheet's animation, queued or rendered.
type SheetClip struct {
	ID        string
	JobID     string
	SessionID string

	// What the sheet was composed from. Held here because the print job does
	// not keep them and the composed JPEG cannot give them back — see the
	// migration.
	TemplateID string
	Filter     string
	PhotoIDs   []string

	QueuedAt time.Time

	GIFPath string
	GIFAt   time.Time

	// AbandonedAt is set when the sources are gone and no retry will help.
	AbandonedAt time.Time
}

// SheetSource is one cell's material for an animated sheet.
type SheetSource struct {
	// Frames are that cell's clip, as paths to the JPEGs the kiosk posted, in
	// playback order. Empty means the cell does not move — a burst that never
	// arrived, or a booth with motion off — and the cell holds its still for
	// the whole run rather than going blank.
	Frames []string

	// Still is the photograph, drawn under the animation. It is what a cell
	// with no clip shows, and what any single frame that will not decode falls
	// back to, so a corrupt upload costs a hundredth of a second rather than
	// putting a white hole in the middle of the customer's frame.
	Still string
}

// RenderSheet composes an animated GIF of the whole sheet at dest.
//
// cells are in the template's cell order, which is the print's order — a
// moving version whose faces are arranged differently from the paper is worse
// than none.
func RenderSheet(tpl compose.Template, cells []SheetSource, filter compose.Filter, dest string, opts Options) error {
	opts = opts.withSheetDefaults()

	if len(cells) != len(tpl.Cells) {
		return fmt.Errorf("%w: %d sources for %d cells", compose.ErrCellCount, len(cells), len(tpl.Cells))
	}

	w, h, err := compose.SheetSize(tpl.Layout)
	if err != nil {
		return err
	}

	// Never enlarged, the same rule the stills use: a sheet already smaller
	// than the bound would only gain bytes carrying no detail.
	scale := min(float64(opts.LongEdge)/float64(max(w, h)), 1)
	bounds := image.Rect(0, 0, int(float64(w)*scale), int(float64(h)*scale))

	// The shortest clip on the sheet is the length of the animation.
	//
	// Not the longest: a cell that ran out would have to freeze while the
	// others kept moving, and one frozen face among five moving ones reads as
	// a bug in the booth rather than as a short clip. Every burst is the same
	// five seconds anyway, so this trims hundredths.
	n := 0
	for _, c := range cells {
		if len(c.Frames) == 0 {
			continue
		}
		if n == 0 || len(c.Frames) < n {
			n = len(c.Frames)
		}
	}
	if n < 2 {
		// Nothing on this sheet moves. A GIF of six frozen faces is a JPEG
		// costing thirty times as much, and the page already has that JPEG.
		return ErrTooShort
	}

	// Every stride-th captured frame, which is what lets the sheet play at its
	// own rate over the same seconds the burst was filmed across. See SheetFPS.
	stride := max(1, FPS/opts.FPS)
	steps := n / stride
	if steps < 2 {
		return ErrTooShort
	}

	// base holds everything that does not change: the background, and every
	// cell's still. Built once and memcpy'd per frame, which is what keeps the
	// per-frame work proportional to the cells that actually move.
	base := image.NewRGBA(bounds)
	// White, not transparent, for the same reason compose.Sheet paints it:
	// an unpainted region quantises to black.
	draw.Draw(base, bounds, image.White, image.Point{}, draw.Src)

	if tpl.Background != "" {
		bg, err := templateImage(tpl, "background")
		if err != nil {
			return err
		}
		xdraw.CatmullRom.Scale(base, bounds, bg, bg.Bounds(), draw.Src, nil)
	}

	rects := make([]compose.Cell, len(tpl.Cells))
	for i, c := range tpl.Cells {
		rects[i] = compose.Cell{
			X: int(float64(c.X) * scale),
			Y: int(float64(c.Y) * scale),
			W: int(float64(c.W) * scale),
			H: int(float64(c.H) * scale),
		}
	}

	// Every cell's still, moving ones included — for them it is the floor the
	// animation is painted over, so a frame that will not decode shows the
	// photograph instead of the background.
	for i, c := range cells {
		if c.Still == "" {
			continue
		}
		img, err := readFrame(c.Still)
		if err != nil {
			// The still is a fallback; losing it costs this cell its floor and
			// nothing else, and the clip frames are about to cover it anyway.
			continue
		}
		compose.DrawCover(base, rects[i], img)
		filter.Apply(base, cellRect(rects[i]))
	}

	// Scaled once rather than per frame: it is the same artwork a hundred times,
	// and CatmullRom over a full sheet is not cheap.
	var overlay image.Image
	if tpl.Overlay != "" {
		over, err := templateImage(tpl, "overlay")
		if err != nil {
			return err
		}
		o := image.NewRGBA(bounds)
		xdraw.CatmullRom.Scale(o, bounds, over, over.Bounds(), draw.Src, nil)
		overlay = o
	}

	// One scratch frame for the whole run. EncodeSeq dithers each frame into a
	// paletted image of its own before asking for the next, so handing back the
	// same buffer every time is safe — and holding a hundred sheets instead would
	// be most of the booth's memory budget.
	work := image.NewRGBA(bounds)

	return EncodeSeq(steps, func(i int) (image.Image, error) {
		copy(work.Pix, base.Pix)

		for ci, c := range cells {
			if len(c.Frames) == 0 {
				continue
			}
			img, err := readFrame(c.Frames[i*stride])
			if err != nil {
				continue
			}
			compose.DrawCover(work, rects[ci], img)
			// After the draw and over the cell alone, exactly as the print
			// does it, so the frame artwork keeps the colours it was drawn in.
			filter.Apply(work, cellRect(rects[ci]))
		}

		if overlay != nil {
			draw.Draw(work, bounds, overlay, image.Point{}, draw.Over)
		}
		return work, nil
	}, dest, opts)
}

func cellRect(c compose.Cell) image.Rectangle {
	return image.Rect(c.X, c.Y, c.X+c.W, c.Y+c.H)
}

// templateImage decodes one of a template's two declared assets. Only the names
// the manifest itself declares can be opened — see compose.Template.Asset.
func templateImage(tpl compose.Template, kind string) (image.Image, error) {
	f, ctype, err := tpl.Asset(kind)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	switch ctype {
	case "image/png":
		return png.Decode(f)
	case "image/jpeg":
		return jpeg.Decode(f)
	}
	return nil, fmt.Errorf("clip: template %s is %s", kind, ctype)
}

// RecordSheet queues one print job's animation.
//
// ErrDuplicate when the job already has one, which is not a failure: the print
// handler composes before it queues, so a client retrying a request whose
// response it never saw is the ordinary way this happens.
func (s *Store) RecordSheet(ctx context.Context, sc SheetClip) (SheetClip, error) {
	sc.ID = newID()
	sc.QueuedAt = s.now()

	ids, err := json.Marshal(sc.PhotoIDs)
	if err != nil {
		return SheetClip{}, fmt.Errorf("clip: record sheet: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO sheet_clips (id, job_id, session_id, template_id, filter, photo_ids, queued_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sc.ID, sc.JobID, sc.SessionID, sc.TemplateID, sc.Filter, string(ids), sc.QueuedAt.Unix(),
	)
	switch {
	case store.IsConstraint(err):
		return SheetClip{}, ErrDuplicate
	case err != nil:
		return SheetClip{}, fmt.Errorf("clip: record sheet: %w", err)
	}
	return sc, nil
}

func (s *Store) GetSheet(ctx context.Context, id string) (SheetClip, error) {
	sc, err := scanSheet(s.db.QueryRowContext(ctx,
		`SELECT `+sheetColumns+` FROM sheet_clips WHERE id = ?`, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return SheetClip{}, ErrNotFound
	case err != nil:
		return SheetClip{}, fmt.Errorf("clip: get sheet: %w", err)
	}
	return sc, nil
}

// RenderedSheets returns a session's finished sheet animations, keyed by the
// print job each one belongs to — which is how the download page, walking the
// sheets it already lists, asks of each whether it moves.
func (s *Store) RenderedSheets(ctx context.Context, sessionID string) (map[string]SheetClip, error) {
	scs, err := s.querySheets(ctx,
		`SELECT `+sheetColumns+` FROM sheet_clips
		  WHERE session_id = ? AND gif_path IS NOT NULL`, sessionID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]SheetClip, len(scs))
	for _, sc := range scs {
		out[sc.JobID] = sc
	}
	return out, nil
}

// RenderedSheetLike finds an animation this session has already built from the
// very same template, filter and frames.
//
// It exists because a reprint composes the identical sheet under a new job, and
// most packages include more than one print — so without this the ordinary
// session renders the same animation two or three times. The rows stay separate
// and share a file: retention sweeps that file by age, which leaves both
// pointing at nothing at the same moment, which is what the download page
// already stats for.
//
// The photo ids are compared as the JSON they are stored as, which is exact
// because the order is part of the value: the same faces in different cells is
// a different sheet and must render again.
func (s *Store) RenderedSheetLike(ctx context.Context, sc SheetClip) (SheetClip, bool, error) {
	ids, err := json.Marshal(sc.PhotoIDs)
	if err != nil {
		return SheetClip{}, false, fmt.Errorf("clip: rendered sheet like: %w", err)
	}

	out, err := s.querySheets(ctx,
		`SELECT `+sheetColumns+` FROM sheet_clips
		  WHERE session_id = ? AND template_id = ? AND filter = ? AND photo_ids = ?
		    AND gif_path IS NOT NULL AND id <> ?
		  ORDER BY gif_at
		  LIMIT 1`,
		sc.SessionID, sc.TemplateID, sc.Filter, string(ids), sc.ID)
	if err != nil {
		return SheetClip{}, false, err
	}
	if len(out) == 0 {
		return SheetClip{}, false, nil
	}
	return out[0], true, nil
}

// UnrenderedSheets returns queued sheet animations, oldest first.
func (s *Store) UnrenderedSheets(ctx context.Context, limit int) ([]SheetClip, error) {
	return s.querySheets(ctx,
		`SELECT `+sheetColumns+` FROM sheet_clips
		  WHERE gif_path IS NULL AND abandoned_at IS NULL
		  ORDER BY queued_at, rowid
		  LIMIT ?`, limit)
}

// SetSheetGIF records the rendered animation.
func (s *Store) SetSheetGIF(ctx context.Context, id, path string) error {
	return s.touch(ctx, id, `UPDATE sheet_clips SET gif_path = ?, gif_at = ? WHERE id = ?`,
		path, s.now().Unix(), id)
}

// AbandonSheet records that this one will never render: the frames it was to be
// built from reached retention first. It is what keeps the queue finite across
// the restarts the booth's own updater performs.
func (s *Store) AbandonSheet(ctx context.Context, id string) error {
	return s.touch(ctx, id, `UPDATE sheet_clips SET abandoned_at = ? WHERE id = ?`,
		s.now().Unix(), id)
}

func (s *Store) querySheets(ctx context.Context, q string, args ...any) ([]SheetClip, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("clip: query sheets: %w", err)
	}
	defer rows.Close()

	var out []SheetClip
	for rows.Next() {
		sc, err := scanSheet(rows)
		if err != nil {
			return nil, fmt.Errorf("clip: scan sheet: %w", err)
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

const sheetColumns = `id, job_id, session_id, template_id, filter, photo_ids, ` +
	`queued_at, gif_path, gif_at, abandoned_at`

func scanSheet(row scanner) (SheetClip, error) {
	var (
		sc                 SheetClip
		photoIDs           string
		gifPath            sql.NullString
		gifAt, abandonedAt sql.NullInt64
		queuedAt           int64
	)
	if err := row.Scan(
		&sc.ID, &sc.JobID, &sc.SessionID, &sc.TemplateID, &sc.Filter, &photoIDs,
		&queuedAt, &gifPath, &gifAt, &abandonedAt,
	); err != nil {
		return SheetClip{}, err
	}

	if err := json.Unmarshal([]byte(photoIDs), &sc.PhotoIDs); err != nil {
		return SheetClip{}, fmt.Errorf("clip: sheet %s has unreadable photo ids: %w", sc.ID, err)
	}

	sc.GIFPath = gifPath.String
	sc.QueuedAt = time.Unix(queuedAt, 0)
	if gifAt.Valid {
		sc.GIFAt = time.Unix(gifAt.Int64, 0)
	}
	if abandonedAt.Valid {
		sc.AbandonedAt = time.Unix(abandonedAt.Int64, 0)
	}
	return sc, nil
}
