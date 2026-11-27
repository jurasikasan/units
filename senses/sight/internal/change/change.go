// Package change tells whether a scene changed between two frames: each frame
// is brought to a small grey grid, and the grids' mean difference is compared
// with a threshold. It is a probe's detector, enough to open an observation;
// it knows nothing of what changed.
package change

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg" // frames come as JPEG
	_ "image/png"  // or PNG
	"math"
)

// Grid size: coarse enough that sensor noise and compression shimmer average
// out, fine enough that someone stepping into a corner of the view shows.
const (
	W = 32
	H = 24
)

// Grid is a frame as W×H mean grey levels, 0–255.
type Grid [W * H]float64

// MediaType names a frame's format from its first bytes: image/jpeg,
// image/png, or "" for anything else.
func MediaType(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	}
	return ""
}

// Of decodes a JPEG or PNG frame into its grid.
func Of(b []byte) (*Grid, error) {
	img, err := Decode(b)
	if err != nil {
		return nil, err
	}
	return OfImage(img)
}

// Decode decodes a JPEG or PNG frame.
func Decode(b []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("frame: %w", err)
	}
	return img, nil
}

// OfImage brings a picture to its grid. A full-range YCbCr picture (a
// JPEG's, or a decoder's yuvj) is read from its Y plane, which is the luma.
func OfImage(img image.Image) (*Grid, error) {
	r := img.Bounds()
	if r.Dx() == 0 || r.Dy() == 0 {
		return nil, fmt.Errorf("frame: empty")
	}
	var g Grid
	var n [W * H]int
	// Every pixel would be exact; a step keeps a large frame cheap and the
	// mean of a cell is no worse for it.
	step := max(1, min(r.Dx()/(W*4), r.Dy()/(H*4)))
	for y := r.Min.Y; y < r.Max.Y; y += step {
		cy := (y - r.Min.Y) * H / r.Dy()
		for x := r.Min.X; x < r.Max.X; x += step {
			cx := (x - r.Min.X) * W / r.Dx()
			if yc, ok := img.(*image.YCbCr); ok {
				g[cy*W+cx] += float64(yc.Y[yc.YOffset(x, y)])
			} else {
				cr, cg, cb, _ := img.At(x, y).RGBA()
				// Rec. 601 luma, from 16-bit channels to 0–255.
				g[cy*W+cx] += (0.299*float64(cr) + 0.587*float64(cg) + 0.114*float64(cb)) / 257
			}
			n[cy*W+cx]++
		}
	}
	for i := range g {
		if n[i] > 0 {
			g[i] /= float64(n[i])
		}
	}
	return &g, nil
}

// Diff is the mean absolute difference of two grids, 0 (the same) to 1.
func Diff(a, b *Grid) float64 {
	var s float64
	for i := range a {
		s += math.Abs(a[i] - b[i])
	}
	return s / float64(len(a)) / 255
}
