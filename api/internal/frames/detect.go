package frames

import (
	"bytes"
	"fmt"
	"image/png"
	"sort"
)

// Cell is where one photo goes, in pixels at 300 dpi with the sheet's top-left
// as the origin. The field names are the manifest the booth reads, so this
// struct is also the wire format — see agent/internal/compose.
type Cell struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Detection thresholds.
//
// Both exist to tell a photo slot from decoration. A designer may punch a
// transparent star or a cut-out corner into a frame, and without these that
// hole becomes a cell the customer is asked to fill with their face.
//
// Decoration is small and slots are large, so size does most of the work here.
const (
	// minCellArea is the fraction of the sheet a region must cover. A photo
	// cell on the smallest sheet is around 20% of it; a decorative cut-out is
	// far under 1%.
	minCellArea = 0.01

	// minRectangularity is region area over bounding-box area, and it rejects a
	// shape that is nowhere near its own box: a border with a transparent
	// middle, a ring, an L. Those score under 0.5 and are not slots.
	//
	// It is deliberately loose enough to admit shapes that are. A heart scores
	// about 0.70 and a circle 0.79, and both are ordinary frame design — the
	// photo fills the bounding box and the artwork, drawn over it, masks it
	// back to the shape the designer cut. Nothing is printed outside the hole,
	// so there is no reason to insist a slot be square.
	//
	// The real guard against decoration is minCellArea above; the second is
	// that an import prints its slots and stays unpublished until somebody
	// looks at them.
	minRectangularity = 0.62
)

// Detect reads a frame PNG and returns the sheet size and the cells its
// transparent regions define.
//
// This is the whole reason an uploaded PNG is a working template. The artwork
// already says where the photos go — that is what the holes in it are — so
// asking an operator to also type four rectangles would be asking them to
// restate the picture, and any typo becomes a face printed off its slot.
//
// A pixel counts as hole when it is anything less than fully opaque, not merely
// when it is fully transparent. The photo has to be under every pixel the frame
// does not completely cover, or the antialiased edge of each hole blends
// against white and prints as a pale halo.
func Detect(data []byte) (width, height int, cells []Cell, err error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, 0, nil, fmt.Errorf("%w: %w", ErrNotPNG, err)
	}

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	hole := make([]bool, w*h)
	holes := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if _, _, _, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA(); a < 0xffff {
				hole[y*w+x] = true
				holes++
			}
		}
	}
	if holes == 0 {
		return w, h, nil, ErrOpaque
	}

	cells = regions(hole, w, h)
	if len(cells) == 0 {
		return w, h, nil, ErrNoCells
	}
	return w, h, cells, nil
}

// regions finds connected transparent areas and returns the bounding boxes of
// those big and compact enough to be photo slots, in reading order.
func regions(hole []bool, w, h int) []Cell {
	seen := make([]bool, len(hole))
	stack := make([]int, 0, 4096)
	minArea := int(minCellArea * float64(w) * float64(h))

	var out []Cell
	for start := range hole {
		if !hole[start] || seen[start] {
			continue
		}

		x0, y0 := start%w, start/w
		x1, y1 := x0, y0
		area := 0

		// Iterative rather than recursive: a photo cell is hundreds of
		// thousands of pixels, which is hundreds of thousands of stack frames.
		stack = append(stack[:0], start)
		seen[start] = true
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			area++

			x, y := p%w, p/w
			x0, x1 = min(x0, x), max(x1, x)
			y0, y1 = min(y0, y), max(y1, y)

			// Four-connected. Eight would bridge two cells that touch only at a
			// diagonal corner pixel, merging them into one oversized slot.
			if x > 0 {
				push(hole, seen, &stack, p-1)
			}
			if x < w-1 {
				push(hole, seen, &stack, p+1)
			}
			if y > 0 {
				push(hole, seen, &stack, p-w)
			}
			if y < h-1 {
				push(hole, seen, &stack, p+w)
			}
		}

		boxArea := (x1 - x0 + 1) * (y1 - y0 + 1)
		if area < minArea || float64(area)/float64(boxArea) < minRectangularity {
			continue
		}
		out = append(out, Cell{X: x0, Y: y0, W: x1 - x0 + 1, H: y1 - y0 + 1})
	}

	return readingOrder(out)
}

func push(hole, seen []bool, stack *[]int, q int) {
	if hole[q] && !seen[q] {
		seen[q] = true
		*stack = append(*stack, q)
	}
}

// readingOrder sorts cells the way a customer reads the strip: down the sheet,
// and left to right within a row.
//
// Rows are banded rather than sorted on y directly, because two cells sitting
// side by side are rarely aligned to the pixel — a two-up layout whose right
// cell starts one pixel lower would otherwise be ordered after the entire row
// below it, and the photos would print in the wrong slots.
func readingOrder(cells []Cell) []Cell {
	sort.Slice(cells, func(i, j int) bool { return cells[i].Y < cells[j].Y })

	out := make([]Cell, 0, len(cells))
	for i := 0; i < len(cells); {
		// Same row while the next cell starts before the current one's middle.
		j := i + 1
		for j < len(cells) && cells[j].Y < cells[i].Y+cells[i].H/2 {
			j++
		}
		row := cells[i:j]
		sort.Slice(row, func(a, b int) bool { return row[a].X < row[b].X })
		out = append(out, row...)
		i = j
	}
	return out
}
