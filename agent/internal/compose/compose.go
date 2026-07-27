// Package compose turns chosen frames into the sheet that goes to the printer.
//
// Two rules from design/kiosk.md decide everything here.
//
// The first is that templates beat filters. The gap against the incumbent is
// 99+ templates versus six, templates are what customers actually choose from,
// and on a franchise they are the shared asset every outlet consumes. That is
// content work, not engineering — so a template here is a PNG someone drew,
// plus a small manifest saying where the photos go. There is no authoring tool
// and no editor, because booth packages already advertise "free desain frame"
// and the flat files exist.
//
// The second is that print quality is not negotiable. This package composes at
// 300 dpi from the originals and never from the delivered derivative.
package compose

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	xdraw "golang.org/x/image/draw"

	"github.com/bhaktiyudha/bykami/agent/internal/printer"
)

// DPI is the target every geometry below is expressed at.
const DPI = 300

var (
	ErrCellCount     = errors.New("compose: wrong number of photos for this template")
	ErrNoTemplate    = errors.New("compose: no such template")
	ErrBadManifest   = errors.New("compose: invalid template manifest")
	ErrUnknownLayout = errors.New("compose: unknown layout")
)

// Cell is where one photo goes, in pixels at 300 dpi with the sheet's top-left
// as the origin.
type Cell struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Template is a frame design: a manifest and, optionally, artwork drawn over
// the photos.
type Template struct {
	// ID is the directory name, and what the kiosk sends back when a customer
	// picks one.
	ID string `json:"-"`

	Name   string         `json:"name"`
	Layout printer.Layout `json:"layout"`
	Cells  []Cell         `json:"cells"`

	// Overlay is a PNG with transparency laid over the photos — the frame
	// artwork, the logo, and any footer text, all of which the designer already
	// drew. Optional: a template with none is a plain photo, which is what pas
	// foto wants.
	Overlay string `json:"overlay"`

	// Background is drawn under the photos, so a template can have a coloured
	// border without the overlay needing to be opaque anywhere.
	Background string `json:"background"`

	// files is the template's own directory, and nothing above it. An fs.FS
	// rather than a path so that the built-in templates embedded in the binary
	// and the ones dropped into a folder on the booth PC are the same thing to
	// everything below.
	files fs.FS
}

// SheetSize returns the composed sheet's pixel dimensions.
func SheetSize(l printer.Layout) (int, int, error) {
	spec, ok := printer.SpecFor(l)
	if !ok {
		return 0, 0, fmt.Errorf("%w: %q", ErrUnknownLayout, l)
	}
	return spec.WidthPx, spec.HeightPx, nil
}

//go:embed templates
var builtin embed.FS

// Builtin returns the templates compiled into the binary.
//
// One artifact, one version, exactly as the kiosk UI is embedded in the agent:
// a booth that starts with no designs at all has nothing to offer a customer,
// and "copy the templates folder too" is a deployment step that will be missed.
func Builtin() ([]Template, error) {
	sub, err := fs.Sub(builtin, "templates")
	if err != nil {
		return nil, err
	}
	return Load(sub)
}

// LoadDir reads templates from a directory on disk, which is how an outlet adds
// its own artwork without a rebuild.
func LoadDir(dir string) ([]Template, error) {
	if dir == "" {
		return nil, nil
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return Load(os.DirFS(dir))
}

// Load reads every template in fsys.
//
// A directory of directories, each with a template.json. Adding a design is
// dropping in a folder — no build step, no migration, and no code change, which
// is what makes templates content work rather than engineering.
func Load(fsys fs.FS) ([]Template, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("compose: read templates: %w", err)
	}

	var out []Template
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub, err := fs.Sub(fsys, e.Name())
		if err != nil {
			return out, err
		}
		t, err := LoadOne(sub, e.Name())
		if err != nil {
			// Returned with what loaded so far, so one broken design is a
			// reported problem rather than a booth with no templates at all.
			return out, fmt.Errorf("compose: template %q: %w", e.Name(), err)
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// LoadOne reads a single template directory. id is what the kiosk sends back
// when a customer picks it.
func LoadOne(fsys fs.FS, id string) (Template, error) {
	b, err := fs.ReadFile(fsys, "template.json")
	if err != nil {
		return Template{}, err
	}

	var t Template
	dec := json.NewDecoder(strings.NewReader(string(b)))
	// A typo in a manifest is otherwise a field that silently does nothing, and
	// the symptom is a photo in the wrong place on a printed sheet.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return Template{}, fmt.Errorf("%w: %w", ErrBadManifest, err)
	}

	t.ID = id
	t.files = fsys
	if err := t.validate(); err != nil {
		return Template{}, err
	}
	return t, nil
}

func (t Template) validate() error {
	w, h, err := SheetSize(t.Layout)
	if err != nil {
		return err
	}
	if t.Name == "" {
		return fmt.Errorf("%w: name is required", ErrBadManifest)
	}
	if len(t.Cells) == 0 {
		return fmt.Errorf("%w: at least one cell is required", ErrBadManifest)
	}
	for i, c := range t.Cells {
		switch {
		case c.W <= 0 || c.H <= 0:
			return fmt.Errorf("%w: cell %d has no size", ErrBadManifest, i)
		case c.X < 0 || c.Y < 0 || c.X+c.W > w || c.Y+c.H > h:
			// A cell off the edge is a photo half-printed. Caught when the
			// template is loaded rather than when a customer prints it.
			return fmt.Errorf("%w: cell %d falls outside the %dx%d sheet", ErrBadManifest, i, w, h)
		}
	}
	return nil
}

// Sheet composes photos into a printable JPEG at dest and returns its size.
//
// photos are paths to the ORIGINALS. Composing from the delivered derivative
// would hand back exactly what full-resolution capture bought — see the
// resolution table in design/kiosk.md.
func (t Template) Sheet(photos []string, dest string) (image.Point, error) {
	if len(photos) != len(t.Cells) {
		return image.Point{}, fmt.Errorf("%w: %d photos for %d cells", ErrCellCount, len(photos), len(t.Cells))
	}

	w, h, err := SheetSize(t.Layout)
	if err != nil {
		return image.Point{}, err
	}

	sheet := image.NewRGBA(image.Rect(0, 0, w, h))
	// White, not transparent. A dye-sub printer has no notion of an alpha
	// channel, and JPEG has no notion of one either — an unpainted region would
	// encode as black.
	draw.Draw(sheet, sheet.Bounds(), image.White, image.Point{}, draw.Src)

	if t.Background != "" {
		bg, err := t.readImage(t.Background)
		if err != nil {
			return image.Point{}, err
		}
		xdraw.CatmullRom.Scale(sheet, sheet.Bounds(), bg, bg.Bounds(), draw.Src, nil)
	}

	for i, p := range photos {
		img, err := readJPEG(p)
		if err != nil {
			return image.Point{}, err
		}
		drawCover(sheet, t.Cells[i], img)
	}

	if t.Overlay != "" {
		over, err := t.readImage(t.Overlay)
		if err != nil {
			return image.Point{}, err
		}
		// draw.Over, so the designer's transparency means what it looks like.
		xdraw.CatmullRom.Scale(sheet, sheet.Bounds(), over, over.Bounds(), draw.Over, nil)
	}

	if err := writeJPEG(sheet, dest); err != nil {
		return image.Point{}, err
	}
	return sheet.Bounds().Size(), nil
}

// drawCover scales a photo to fill its cell and centre-crops the overflow.
//
// Fill rather than fit: a letterboxed photo in a designed frame looks like a
// mistake, and the customer framed the shot expecting the whole cell. Cropping
// the long dimension is what every photobooth does.
func drawCover(dst *image.RGBA, c Cell, src image.Image) {
	sb := src.Bounds()
	scale := max(float64(c.W)/float64(sb.Dx()), float64(c.H)/float64(sb.Dy()))

	// The source rectangle that, scaled, exactly covers the cell — centred, so
	// a face in the middle of the frame stays in the middle of the cell.
	srcW := int(float64(c.W) / scale)
	srcH := int(float64(c.H) / scale)
	offX := sb.Min.X + (sb.Dx()-srcW)/2
	offY := sb.Min.Y + (sb.Dy()-srcH)/2

	xdraw.CatmullRom.Scale(dst,
		image.Rect(c.X, c.Y, c.X+c.W, c.Y+c.H),
		src,
		image.Rect(offX, offY, offX+srcW, offY+srcH),
		draw.Src, nil)
}

func (t Template) readImage(name string) (image.Image, error) {
	// The template's own directory and nothing above it. fs.FS rejects an
	// absolute or parent-relative name outright, which is the check that would
	// otherwise have to be remembered here.
	f, err := t.files.Open(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadManifest, err)
	}
	defer f.Close()

	switch strings.ToLower(path.Ext(name)) {
	case ".png":
		return png.Decode(f)
	case ".jpg", ".jpeg":
		return jpeg.Decode(f)
	default:
		return nil, fmt.Errorf("%w: %q is not a PNG or JPEG", ErrBadManifest, name)
	}
}

func readJPEG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("compose: decode %s: %w", filepath.Base(path), err)
	}
	return img, nil
}

func writeJPEG(img image.Image, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	// Quality 95: this is the file the printer consumes, and it is the last
	// place in the chain where quality can still be lost.
	err = jpeg.Encode(f, img, &jpeg.Options{Quality: 95})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("compose: encode sheet: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
