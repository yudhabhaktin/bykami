package compose

import "image"

// Filters are colour matrices, and there is exactly one copy of the numbers.
//
// A filter has to reach the printed sheet, not only the screen. The obvious
// build — a CSS filter on the preview — prints an unfiltered photo, and the
// customer finds out on paper. So the filter is applied here, at compose time,
// from the originals.
//
// That leaves the screen and the printer as two implementations of the same
// effect, which is a drift waiting to happen. The fix is that they are not two
// implementations: a Filter is a 4×5 colour matrix, the same shape as SVG's
// feColorMatrix, and the kiosk reads these very numbers out of /api/state and
// hands them to the browser. Nothing is written down twice, so nothing can
// disagree.
//
// It also means changing a filter is changing twenty numbers in this file, and
// a new one is a new entry — no shader, no image library, no second pipeline.

// Matrix is a colour transform in feColorMatrix order: four rows of five, the
// fifth column an offset applied on a 0–1 scale.
type Matrix [20]float64

// Filter is one look a customer can choose.
type Filter struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Matrix is nil for the identity filter, which is worth a special case:
	// most sheets are printed without one, and this skips a pass over every
	// pixel of every photo to multiply by 1.
	Matrix *Matrix `json:"matrix,omitempty"`
}

// NoFilter is what a session prints with when nobody chose anything.
const NoFilter = "asli"

// Filters is the catalogue, in the order the kiosk shows them.
//
// Six, and the first is "no filter". A row of choices at a booth is a row the
// customer reads standing up with people waiting behind them; twenty looks is
// a menu, not a choice.
var Filters = []Filter{
	{ID: NoFilter, Name: "Asli"},

	// Rec. 709 luminance, which is what CSS grayscale(1) is defined as.
	{ID: "hitam-putih", Name: "Hitam putih", Matrix: &Matrix{
		0.2126, 0.7152, 0.0722, 0, 0,
		0.2126, 0.7152, 0.0722, 0, 0,
		0.2126, 0.7152, 0.0722, 0, 0,
		0, 0, 0, 1, 0,
	}},

	// The sepia matrix from the CSS Filter Effects specification, unmodified.
	{ID: "sepia", Name: "Sepia", Matrix: &Matrix{
		0.393, 0.769, 0.189, 0, 0,
		0.349, 0.686, 0.168, 0, 0,
		0.272, 0.534, 0.131, 0, 0,
		0, 0, 0, 1, 0,
	}},

	// Warm: red up, blue down, with a little cross-channel bleed so skin does
	// not go orange the way a flat red gain makes it.
	{ID: "hangat", Name: "Hangat", Matrix: &Matrix{
		1.10, 0.04, 0.00, 0, 0.01,
		0.02, 1.02, 0.00, 0, 0.00,
		0.00, 0.02, 0.90, 0, 0.00,
		0, 0, 0, 1, 0,
	}},

	// Cool: the mirror of the above. Reads as shade or as an overcast day.
	{ID: "dingin", Name: "Dingin", Matrix: &Matrix{
		0.92, 0.00, 0.02, 0, 0.00,
		0.00, 1.00, 0.02, 0, 0.00,
		0.02, 0.04, 1.12, 0, 0.01,
		0, 0, 0, 1, 0,
	}},

	// Faded film: contrast pulled in and the blacks lifted, which is the whole
	// trick behind the look. The offset is what stops it being a grey wash —
	// scaling alone darkens, lifting alone flattens, and the pair is the look.
	{ID: "pudar", Name: "Pudar", Matrix: &Matrix{
		0.86, 0.00, 0.00, 0, 0.09,
		0.00, 0.86, 0.00, 0, 0.09,
		0.00, 0.00, 0.88, 0, 0.10,
		0, 0, 0, 1, 0,
	}},
}

// FilterByID returns the named filter. An unknown id is the identity filter
// rather than an error: a print request naming a filter this booth does not
// have should produce an unfiltered sheet, not a refusal at the till.
func FilterByID(id string) Filter {
	for _, f := range Filters {
		if f.ID == id {
			return f
		}
	}
	return Filters[0]
}

// apply runs the matrix over one rectangle of the sheet.
//
// The rectangle is the photo's cell, so this touches the customer's photo and
// nothing else. Filtering the whole sheet afterwards would tint the frame
// artwork the designer chose the colours of, and filtering the source before
// scaling would run over every pixel of a 24-megapixel original to produce a
// 540-pixel-wide cell.
func (f Filter) apply(dst *image.RGBA, r image.Rectangle) {
	if f.Matrix == nil {
		return
	}
	m := *f.Matrix
	r = r.Intersect(dst.Bounds())

	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			i := dst.PixOffset(x, y)
			p := dst.Pix[i : i+4 : i+4]
			// The sheet is opaque here — drawCover writes with draw.Src over a
			// white background — so the channels are already unassociated and
			// no un-premultiply is needed.
			cr, cg, cb := float64(p[0])/255, float64(p[1])/255, float64(p[2])/255
			ca := float64(p[3]) / 255

			p[0] = clamp8(m[0]*cr + m[1]*cg + m[2]*cb + m[3]*ca + m[4])
			p[1] = clamp8(m[5]*cr + m[6]*cg + m[7]*cb + m[8]*ca + m[9])
			p[2] = clamp8(m[10]*cr + m[11]*cg + m[12]*cb + m[13]*ca + m[14])
			p[3] = clamp8(m[15]*cr + m[16]*cg + m[17]*cb + m[18]*ca + m[19])
		}
	}
}

func clamp8(v float64) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 1:
		return 255
	}
	return uint8(v*255 + 0.5)
}
