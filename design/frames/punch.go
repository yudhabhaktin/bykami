// Command punch combines a frame's art pass with its mask pass.
//
// Chrome renders both at the same geometry: the art pass is the frame as it
// should print, the mask pass is white everywhere the frame is opaque and black
// inside the cells. Taking alpha from the mask's luminance keeps the holes
// antialiased, which a rectangle punched by hand would not be — the cells have
// rounded corners, and a hard-edged hole would show a stair-stepped photo edge
// against the ink ring.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
)

func main() {
	if len(os.Args) != 4 {
		log.Fatal("usage: punch <art.png> <mask.png> <out.png>")
	}
	art := load(os.Args[1])
	mask := load(os.Args[2])

	if art.Bounds() != mask.Bounds() {
		log.Fatalf("art %v and mask %v are different sizes", art.Bounds(), mask.Bounds())
	}

	b := art.Bounds()
	out := image.NewNRGBA(b)
	var opaque, clear int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			mr, mg, mb, _ := mask.At(x, y).RGBA()
			// Rec. 601 luma. The mask is greyscale apart from antialiasing, so
			// the weighting barely matters, but averaging channels would tint
			// the edge alpha if the renderer ever subpixel-antialiases.
			a := (299*mr + 587*mg + 114*mb) / 1000 >> 8

			r, g, bb, _ := art.At(x, y).RGBA()
			out.Set(x, y, color.NRGBA{uint8(r >> 8), uint8(g >> 8), uint8(bb >> 8), uint8(a)})

			switch {
			case a == 255:
				opaque++
			case a == 0:
				clear++
			}
		}
	}

	f, err := os.Create(os.Args[3])
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, out); err != nil {
		log.Fatal(err)
	}

	total := b.Dx() * b.Dy()
	fmt.Printf("%s: %dx%d, %.1f%% opaque, %.1f%% transparent, %d antialiased\n",
		os.Args[3], b.Dx(), b.Dy(),
		100*float64(opaque)/float64(total), 100*float64(clear)/float64(total),
		total-opaque-clear)
}

func load(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		log.Fatalf("%s: %v", path, err)
	}
	return img
}
