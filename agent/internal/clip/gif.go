package clip

import (
	"errors"
	"fmt"
	"image"
	"image/color/palette"
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
// The costs are real and accepted. GIF is 256 colours and it is large — see
// LongEdge and FPS for where that is bought back. An MP4 would be a tenth of
// the size at better quality, and cannot be produced here: the booth binary is
// cross-compiled from macOS with GOOS=windows, so cgo is unavailable and with
// it every H.264 encoder worth having.
const (
	// LongEdge is the delivered animation's maximum dimension.
	//
	// Small, deliberately, and the number was measured rather than guessed.
	// This travels over Indonesian mobile data exactly as the stills do, and
	// unlike a still it carries fifty frames: every pixel added here is paid
	// for fifty times. On synthetic worst-case noise a five-second clip encodes
	// to roughly 1.5 MB at 400, 3.7 MB at 480 and 7.4 MB at 640 — real footage
	// compresses better than that, but the shape of the curve is the point.
	//
	// 400 is still sharp in a phone's photo roll, which is where this ends up.
	LongEdge = 400

	// FPS is the playback rate. Ten is smooth enough to read as motion rather
	// than as a slideshow, and half the file of twenty.
	//
	// It must divide 100 evenly: GIF measures frame delay in hundredths of a
	// second, so a rate that does not divide cleanly is silently rounded and the
	// clip runs at a length nobody chose.
	FPS = 10
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

// Render encodes frames into an animated GIF at dest.
//
// frames are paths to the JPEGs the kiosk posted, in playback order. Frames
// that will not decode are skipped rather than failing the render: one corrupt
// upload out of fifty is a clip a hundredth of a second shorter, and refusing
// the whole thing would hand the customer nothing over a defect they cannot
// see.
func Render(frames []string, dest string, opts Options) error {
	opts = opts.withDefaults()

	if len(frames) < 2 {
		return ErrTooShort
	}

	out := &gif.GIF{
		// Zero means loop forever, which is what a Live Photo does and what
		// every phone's photo roll expects of an animation this short.
		LoopCount: 0,
	}

	// Fixed by the first frame that decodes, and every frame after it is drawn
	// to exactly this. Uniform by construction rather than by trusting that a
	// camera never changes resolution mid-session: frames of differing sizes
	// encode into a GIF that jumps.
	var canvas image.Rectangle

	for _, path := range frames {
		img, err := readFrame(path)
		if err != nil {
			continue
		}

		if canvas.Empty() {
			// Through the same rule the stills use, so a frame already smaller
			// than the bound is not upscaled into bytes carrying no detail.
			canvas = derive.Resize(img, opts.LongEdge).Bounds()
		}

		// Scaled, then dithered down to the palette. Floyd-Steinberg rather
		// than nearest-colour because 256 fixed colours across a face is
		// visible banding, and a face is the entire subject.
		//
		// ApproxBiLinear and not the CatmullRom the stills use: this output is
		// about to lose all but 256 of its colours and gain dither noise, so a
		// sharper resampler buys nothing a customer could see and costs about
		// half a second per clip — measured, on a job that runs fifteen times a
		// session.
		//
		// Both intermediates are dropped every iteration on purpose: the booth
		// shares 2 GiB with the operator API under a GOMEMLIMIT, and holding
		// fifty full-size frames to quantise at the end is how this feature
		// would take the API down with it.
		scaled := image.NewRGBA(canvas)
		xdraw.ApproxBiLinear.Scale(scaled, canvas, img, img.Bounds(), draw.Src, nil)

		p := image.NewPaletted(canvas, palette.Plan9)
		draw.FloydSteinberg.Draw(p, canvas, scaled, canvas.Min)

		out.Image = append(out.Image, p)
		out.Delay = append(out.Delay, 100/opts.FPS)
	}

	if len(out.Image) < 2 {
		return ErrTooShort
	}
	return write(out, dest)
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
