package clip

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"os"
	"path/filepath"

	xdraw "golang.org/x/image/draw"

	"github.com/bhaktiyudha/bykami/agent/internal/derive"
)

// GIF, and it is a considered choice rather than a nostalgic one.
//
// The download page has no JavaScript and a Content-Security-Policy of
// `default-src 'none'; img-src 'self'` — see galleryHeaders. An animated GIF
// plays inside a plain <img>, so it is the only moving format that needs
// neither a script nor a wider policy on the one page in this system a stranger
// can reach. It is also the only one a phone reliably saves: long-pressing an
// animated GIF in mobile Safari offers "Add to Photos" and the animation
// survives, which is the entire point of building this.
//
// The remaining cost is size, and most of it is now bought back rather than
// paid. See quantize.go: the palette is chosen from the clip's own pixels, which
// removes the dither noise a fixed palette needs, and the pixels that did not
// change between frames are dropped entirely. An MP4 would still be a tenth of
// the size, and still cannot be produced here: the booth binary is
// cross-compiled from macOS with GOOS=windows, so cgo is unavailable and with
// it every H.264 encoder worth having.
const (
	// LongEdge is the delivered animation's maximum dimension.
	//
	// This is what a customer posts to a story, so it is sized for a phone
	// screen rather than for a thumbnail. It travels over Indonesian mobile
	// data and carries a hundred frames, so not for a phone screen exactly
	// either: 720 on the long edge is sharp held at arm's length, and is what
	// the kiosk grabs at — see CLIP_LONG_EDGE, which must match, or the agent is
	// upscaling frames that never held the detail.
	//
	// The number moved from 400 because the encoder underneath it changed. At
	// the old settings the new encoder is half the size and fifteen decibels
	// better; that surplus is what pays for this.
	LongEdge = 720

	// FPS is the playback rate.
	//
	// Twenty, which is where a clip stops reading as a fast slideshow and
	// starts reading as video. Ten is visibly steppy on the pans and hand
	// movements that fill a photobooth clip, and the difference costs far less
	// than double: consecutive frames at 20fps differ less from each other, and
	// differing less is exactly what the frame differencing is paid on.
	//
	// It must divide 100 evenly: GIF measures frame delay in hundredths of a
	// second, so a rate that does not divide cleanly is silently rounded and the
	// clip runs at a length nobody chose. It must also stay at or below 20,
	// because a browser rounds a delay of one hundredth up to ten and would play
	// a faster clip at a tenth of the rate it asked for.
	FPS = 20

	// Colors is the palette size, and it is 255 rather than 256 because the
	// last entry is spent on the transparency the differencing needs.
	Colors = 255

	// still is how far a pixel may drift from what is already on screen before
	// the frame has to redraw it, per channel.
	//
	// This is the single number that decides what a clip costs, because it
	// decides how much of each frame can be dropped as unchanged. Zero would
	// redraw nearly every pixel of every frame: a booth's camera puts a couple
	// of levels of sensor noise on a wall that is not moving, and JPEG adds its
	// own. Measured across a five-second clip, the delivered file runs 17.4 MB
	// at zero, 6.8 at six, 4.4 at eight and 2.4 at twelve.
	//
	// Eight, which is comfortably above the noise floor and below a step a
	// person can see on a face. Past it the wall starts holding visibly stale
	// patches through a pan, which is the artefact this trades against and the
	// reason the number is not simply as high as the file size would like.
	still = 8
)

// ErrTooShort means there was not enough of a clip to animate.
var ErrTooShort = errors.New("clip: need at least two frames to animate")

type Options struct {
	LongEdge int
	FPS      int
}

func (o Options) withDefaults() Options {
	if o.LongEdge <= 0 {
		o.LongEdge = LongEdge
	}
	if o.FPS <= 0 {
		o.FPS = FPS
	}
	return o
}

// withSheetDefaults is the same, for the animation that carries every cell at
// once. Both the bound and the rate differ, and see SheetFPS for why the rate
// does without the sheet running at a different speed from the frames it is made
// of: it plays fewer of them over the same seconds, rather than the same ones
// more slowly.
func (o Options) withSheetDefaults() Options {
	if o.LongEdge <= 0 {
		o.LongEdge = SheetLongEdge
	}
	if o.FPS <= 0 {
		o.FPS = SheetFPS
	}
	return o.withDefaults()
}

// Render encodes frames into an animated GIF at dest.
//
// frames are paths to the JPEGs the kiosk posted, in playback order. Frames
// that will not decode are skipped rather than failing the render: one corrupt
// upload out of a hundred is a clip a twentieth of a second shorter, and
// refusing the whole thing would hand the customer nothing over a defect they
// cannot see.
func Render(frames []string, dest string, opts Options) error {
	opts = opts.withDefaults()

	if len(frames) < 2 {
		return ErrTooShort
	}

	// Fixed by the first frame that decodes, and every frame after it is drawn
	// to exactly this. Uniform by construction rather than by trusting that a
	// camera never changes resolution mid-session: frames of differing sizes
	// encode into a GIF that jumps.
	var (
		canvas image.Rectangle
		scaled *image.RGBA
	)

	return EncodeSeq(len(frames), func(i int) (image.Image, error) {
		img, err := readFrame(frames[i])
		if err != nil {
			return nil, nil
		}

		if canvas.Empty() {
			// Through the same rule the stills use, so a frame already smaller
			// than the bound is not upscaled into bytes carrying no detail.
			canvas = derive.Resize(img, opts.LongEdge).Bounds()
			scaled = image.NewRGBA(canvas)
		}

		// ApproxBiLinear and not the CatmullRom the stills use: this is a
		// resample of at most the difference between two capture sizes, and a
		// sharper one buys nothing a customer could see for about half a second
		// per clip — measured, on a job that runs fifteen times a session.
		//
		// One scratch buffer for the whole run rather than one per frame: the
		// booth shares 2 GiB with the operator API under a GOMEMLIMIT, and
		// holding a hundred full-size frames to quantise at the end is how this
		// feature would take the API down with it. EncodeSeq is written so
		// that returning the same buffer every call is safe.
		xdraw.ApproxBiLinear.Scale(scaled, canvas, img, img.Bounds(), draw.Src, nil)
		return scaled, nil
	}, dest, opts)
}

// EncodeSeq encodes n frames into an animated GIF at dest.
//
// at(i) returns the image for frame i, or nil to leave that frame out — which
// is how a source that will not decode costs a twentieth of a second instead of
// the whole animation. It may hand back the same buffer on every call: each
// frame is quantised into a paletted image of its own before at is asked for the
// next, and nothing here keeps a reference to what it was given.
//
// at is called twice for every frame, once to choose the palette and once to
// write it, so it has to answer the same way both times. Both callers do
// naturally — one re-reads the JPEG from disk, the other repaints the sheet from
// an untouched base — and the alternative is holding every scaled frame in
// memory to quantise at the end, which for a hundred of them is a hundred and
// forty megabytes on a booth that shares two gigabytes with the operator API.
//
// The two callers are a single photograph's clip and a whole sheet's, and they
// differ in everything except this: both arrive as a run of same-sized images
// that has to become 255 colours without banding across a face.
func EncodeSeq(n int, at func(i int) (image.Image, error), dest string, opts Options) error {
	opts = opts.withDefaults()
	if n < 2 {
		return ErrTooShort
	}

	canvas, pal, err := survey(n, at)
	if err != nil {
		return err
	}

	out := &gif.GIF{
		// Zero means loop forever, which is what a Live Photo does and what
		// every phone's photo roll expects of an animation this short.
		LoopCount: 0,
		Config: image.Config{
			// Set explicitly so the encoder writes one global colour table that
			// every frame refers to, rather than repeating 768 bytes of
			// identical palette a hundred times.
			ColorModel: pal,
			Width:      canvas.Dx(),
			Height:     canvas.Dy(),
		},
	}

	q := newQuantizer(canvas, pal)
	scratch := image.NewRGBA(canvas)

	for i := range n {
		img, err := at(i)
		if err != nil {
			return err
		}
		if img == nil {
			continue
		}

		out.Image = append(out.Image, q.frame(rgbaOf(img, scratch)))
		out.Delay = append(out.Delay, 100/opts.FPS)
		// The frame under this one has to survive, because most of this frame is
		// transparent and that frame is what shows through.
		out.Disposal = append(out.Disposal, gif.DisposalNone)
	}

	if len(out.Image) < 2 {
		return ErrTooShort
	}
	return write(out, dest)
}

// survey runs the sequence once to decide what it is: how big the animation
// will be, and which 255 colours it should be made of.
//
// Every fourth frame or so rather than all of them. A palette is a summary of
// what the clip contains, and five seconds of one room does not change enough in
// a twentieth of a second to be worth counting four times — sampling keeps this
// pass to a fraction of the render while choosing the same colours.
func survey(n int, at func(i int) (image.Image, error)) (image.Rectangle, color.Palette, error) {
	var (
		canvas  image.Rectangle
		hist    histogram
		scratch *image.RGBA
		seen    int
	)

	const want = 24
	stride := max(1, n/want)

	for i := range n {
		if i%stride != 0 {
			continue
		}
		img, err := at(i)
		if err != nil {
			return image.Rectangle{}, nil, err
		}
		if img == nil {
			continue
		}

		if canvas.Empty() {
			// Fixed by the first frame that arrives, and every frame after it is
			// read at exactly this size. Both callers already promise uniform
			// frames; this is what makes a broken promise a crop rather than an
			// animation that jumps.
			b := img.Bounds()
			canvas = image.Rect(0, 0, b.Dx(), b.Dy())
			scratch = image.NewRGBA(canvas)
		}
		hist.add(rgbaOf(img, scratch))
		seen++
	}

	if seen == 0 {
		return image.Rectangle{}, nil, ErrTooShort
	}

	// The transparent entry goes last, so a palette index is never silently
	// reinterpreted if the count changes.
	return canvas, append(hist.palettize(Colors), color.RGBA{}), nil
}

// rgbaOf is img as an *image.RGBA the quantiser can read directly.
//
// Both callers already hand over an RGBA of exactly the right size, so this is
// normally the image itself and costs nothing. Anything else is copied into the
// scratch buffer, which is also what clips a frame that arrived the wrong size.
func rgbaOf(img image.Image, scratch *image.RGBA) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok && rgba.Bounds().Size() == scratch.Bounds().Size() {
		return rgba
	}
	draw.Draw(scratch, scratch.Bounds(), img, img.Bounds().Min, draw.Src)
	return scratch
}

func readFrame(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, err := jpeg.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("clip: decode %s: %w", filepath.Base(path), err)
	}
	return img, nil
}

// write encodes to a temporary name and renames into place, the same discipline
// derive.File uses: a crash mid-encode would otherwise leave a truncated GIF at
// the path a row is about to point at, which is a corrupt delivery rather than
// a missing one.
func write(g *gif.GIF, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	err = gif.EncodeAll(f, g)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("clip: encode gif: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
