// Package derive makes the file the customer receives.
//
// Print from the original, deliver a derivative. The 200D produces 24 MP JPEGs
// at 6–10 MB, so a 30-frame session is 180–300 MB of originals — hostile to
// send to a phone on Indonesian mobile data and wasteful to store. The same
// session compresses to about 18 MB, which at ten sessions a day and 30-day
// retention is ~5.4 GB rolling: inside R2's free tier, where the uncompressed
// version would be roughly 70 GB and a monthly bill.
//
// Never print from the output of this package. Full-resolution capture is the
// reason the hot-folder design exists at all, and recompressing before the
// printer gives back exactly what it bought.
package derive

import (
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/image/draw"
)

const (
	// LongEdge is the delivered file's maximum dimension. Comfortably above any
	// phone screen and far below what the camera produces.
	LongEdge = 2048

	// Quality is where JPEG stops paying for itself on photographic content.
	Quality = 85
)

// Options exist so the numbers above can be varied in a test without becoming
// configuration the operator can get wrong.
type Options struct {
	LongEdge int
	Quality  int
}

func (o Options) withDefaults() Options {
	if o.LongEdge <= 0 {
		o.LongEdge = LongEdge
	}
	if o.Quality <= 0 {
		o.Quality = Quality
	}
	return o
}

// File reads src, writes the derivative to dest, and returns its dimensions.
//
// Pure Go — image/jpeg plus x/image/draw — because the booth binary is
// cross-compiled from macOS with GOOS=windows and cgo would make that
// impossible without a Windows toolchain. A few hundred milliseconds per frame,
// run in the background during a 15–25 minute session.
func File(src, dest string, opts Options) (image.Point, error) {
	opts = opts.withDefaults()

	in, err := os.Open(src)
	if err != nil {
		return image.Point{}, err
	}
	defer in.Close()

	img, err := jpeg.Decode(in)
	if err != nil {
		return image.Point{}, fmt.Errorf("derive: decode %s: %w", filepath.Base(src), err)
	}

	out := Resize(img, opts.LongEdge)

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return image.Point{}, err
	}

	// Written under a temporary name and renamed into place. A crash mid-encode
	// would otherwise leave a truncated JPEG at the path a row already points
	// at, which is a corrupt delivery rather than a missing one.
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return image.Point{}, err
	}

	// Encoding from a decoded image drops every APP segment the source carried,
	// which is how EXIF goes away: encode/jpeg writes only JFIF plus the frame.
	// That is smaller, and it keeps the camera's serial number out of every
	// file a customer shares publicly.
	err = jpeg.Encode(f, out, &jpeg.Options{Quality: opts.Quality})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return image.Point{}, fmt.Errorf("derive: encode: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return image.Point{}, err
	}
	return out.Bounds().Size(), nil
}

// Resize scales img so its long edge is at most longEdge, preserving aspect
// ratio. An image already inside the bound is returned untouched — upscaling a
// small frame would add bytes and no detail.
func Resize(img image.Image, longEdge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= longEdge && h <= longEdge {
		return img
	}

	if w >= h {
		h = int(float64(h) * float64(longEdge) / float64(w))
		w = longEdge
	} else {
		w = int(float64(w) * float64(longEdge) / float64(h))
		h = longEdge
	}
	// A rounded-away dimension of zero would panic in the scaler. Only reachable
	// for absurdly thin inputs, but the scaler is not the place to find out.
	w, h = max(w, 1), max(h, 1)

	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// CatmullRom over ApproxBiLinear: this runs a handful of times per session
	// on frames a customer will look at closely, so the sharper result is worth
	// the milliseconds.
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// HasEXIF reports whether a JPEG carries an APP1 segment.
//
// Exists for the test that guards the promise above. EXIF removal is a privacy
// property, and a privacy property that nothing checks is a comment.
func HasEXIF(r io.Reader) (bool, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return false, err
	}
	// JPEG is a sequence of marker segments after SOI. Walk them until the
	// start of scan, which is where entropy-coded data begins and markers stop
	// being addressable this way.
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xFF {
			return false, nil
		}
		marker := b[i+1]
		if marker == 0xDA || marker == 0xD9 { // SOS, EOI
			return false, nil
		}
		if marker == 0xE1 { // APP1 — where EXIF lives
			return true, nil
		}
		i += 2 + int(b[i+2])<<8 + int(b[i+3])
	}
	return false, nil
}
