// Command mkicon draws the page's mascot (a console tile with an amber knob
// that has a face, `#mascot` in internal/web/static/index.html) into a
// Windows .ico for the tray. Run it from the repository root after changing
// the mascot:
//
//	go run ./scripts/mkicon
//
// It draws the shapes itself, with 8×8 supersampling, so it needs nothing
// beyond the standard library.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image/color"
	"math"
	"os"
)

const out = "internal/tray/blendeck.ico"

// The mascot's colors (the page's --amber, --amber-ink, --mint, --blush).
var (
	tile     = rgba(0x2b303a, 1)
	tileEdge = rgba(0x3d434f, 1)
	amber    = rgba(0xf6b649, 1)
	ink      = rgba(0x21180a, 1)
	blush    = rgba(0xff8fa3, 0.6)
	mint     = rgba(0x72e3bd, 1)
	ledOff   = rgba(0x4a5160, 1)
)

// The SVG's viewBox is 48×48; the tile only fills x 4–44, y 8–42, so the
// icon crops to a 42-unit square around it and small sizes stay legible.
const cropX, cropY, cropSize = 3, 4, 42

type shape struct {
	inside func(x, y float64) bool
	col    color.NRGBA
}

func shapes(px int) []shape {
	s := []shape{
		{roundRect(4, 8, 40, 34, 11), tileEdge},
		{roundRect(4.75, 8.75, 38.5, 32.5, 10.25), tile},
		{circle(24, 22, 11), amber},
	}
	if px >= 32 { // the knob's notch and dashes are noise when tiny
		s = append(s,
			shape{ring(24, 22, 10.5, 11), rgba(0xffffff, 0.28)},
			shape{roundRect(23, 11.6, 2, 3.6, 1), ink},
		)
	}
	s = append(s,
		shape{ellipse(20, 22, 1.7, 2.1), ink},
		shape{ellipse(28, 22, 1.7, 2.1), ink},
		shape{circle(17.4, 25.4, 1.7), blush},
		shape{circle(30.6, 25.4, 1.7), blush},
		shape{smile(), ink},
		shape{roundRect(12, 35, 8, 3, 1.5), mint},
		shape{roundRect(28, 35, 8, 3, 1.5), ledOff},
	)
	return s
}

func main() {
	sizes := []int{16, 20, 24, 32, 40, 48, 64}
	var images [][]byte
	for _, n := range sizes {
		images = append(images, dib(render(n), n))
	}
	var b bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&b, le, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, n := range sizes {
		dim := byte(n)
		if n >= 256 {
			dim = 0
		}
		_ = binary.Write(&b, le, struct {
			W, H, Colors, Reserved byte
			Planes, Bits           uint16
			Size, Offset           uint32
		}{dim, dim, 0, 0, 1, 32, uint32(len(images[i])), uint32(offset)})
		offset += len(images[i])
	}
	for _, img := range images {
		b.Write(img)
	}
	if err := os.WriteFile(out, b.Bytes(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d bytes)\n", out, b.Len())
}

// render returns n×n straight-alpha pixels, top row first.
func render(n int) []color.NRGBA {
	const ss = 8
	sh := shapes(n)
	px := make([]color.NRGBA, n*n)
	scale := float64(cropSize) / float64(n)
	for py := 0; py < n; py++ {
		for pxl := 0; pxl < n; pxl++ {
			var r, g, b, a float64 // premultiplied sums
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := cropX + (float64(pxl)+(float64(sx)+0.5)/ss)*scale
					y := cropY + (float64(py)+(float64(sy)+0.5)/ss)*scale
					var cr, cg, cb, ca float64
					for _, s := range sh {
						if !s.inside(x, y) {
							continue
						}
						sa := float64(s.col.A) / 255
						cr = float64(s.col.R)/255*sa + cr*(1-sa)
						cg = float64(s.col.G)/255*sa + cg*(1-sa)
						cb = float64(s.col.B)/255*sa + cb*(1-sa)
						ca = sa + ca*(1-sa)
					}
					r, g, b, a = r+cr, g+cg, b+cb, a+ca
				}
			}
			const k = ss * ss
			r, g, b, a = r/k, g/k, b/k, a/k
			c := color.NRGBA{}
			if a > 0 {
				c = color.NRGBA{to8(r / a), to8(g / a), to8(b / a), to8(a)}
			}
			px[py*n+pxl] = c
		}
	}
	return px
}

// dib is one icon image: a BITMAPINFOHEADER, BGRA rows bottom-up, and the
// (unused, all-zero) AND mask.
func dib(px []color.NRGBA, n int) []byte {
	var b bytes.Buffer
	le := binary.LittleEndian
	maskRow := ((n + 31) / 32) * 4
	_ = binary.Write(&b, le, struct {
		Size                 uint32
		W, H                 int32
		Planes, Bits         uint16
		Compression, ImgSize uint32
		XPPM, YPPM           int32
		Used, Important      uint32
	}{40, int32(n), int32(2 * n), 1, 32, 0, uint32(n*n*4 + maskRow*n), 0, 0, 0, 0})
	for y := n - 1; y >= 0; y-- {
		for x := 0; x < n; x++ {
			c := px[y*n+x]
			b.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	b.Write(make([]byte, maskRow*n))
	return b.Bytes()
}

func roundRect(x, y, w, h, r float64) func(float64, float64) bool {
	return func(px, py float64) bool {
		if px < x || px > x+w || py < y || py > y+h {
			return false
		}
		cx := math.Max(x+r, math.Min(px, x+w-r))
		cy := math.Max(y+r, math.Min(py, y+h-r))
		return math.Hypot(px-cx, py-cy) <= r
	}
}

func circle(cx, cy, r float64) func(float64, float64) bool {
	return func(x, y float64) bool { return math.Hypot(x-cx, y-cy) <= r }
}

func ellipse(cx, cy, rx, ry float64) func(float64, float64) bool {
	return func(x, y float64) bool {
		dx, dy := (x-cx)/rx, (y-cy)/ry
		return dx*dx+dy*dy <= 1
	}
}

// ring is the knob's dashed edge: dashes of 2, gaps of 2.6 along the circle.
func ring(cx, cy, r0, r1 float64) func(float64, float64) bool {
	return func(x, y float64) bool {
		d := math.Hypot(x-cx, y-cy)
		if d < r0 || d > r1 {
			return false
		}
		// SVG starts a circle's dash at 3 o'clock and goes clockwise.
		a := math.Atan2(y-cy, x-cx)
		if a < 0 {
			a += 2 * math.Pi
		}
		return math.Mod(a*11, 4.6) < 2
	}
}

// smile is the path M20.5 26 q3.5 2.6 7 0, stroked 1.7 wide with round caps.
func smile() func(float64, float64) bool {
	const steps = 48
	var pts [steps + 1][2]float64
	for i := range pts {
		t := float64(i) / steps
		// Quadratic Bézier from (20.5,26) via (24,28.6) to (27.5,26).
		pts[i] = [2]float64{
			(1-t)*(1-t)*20.5 + 2*(1-t)*t*24 + t*t*27.5,
			(1-t)*(1-t)*26 + 2*(1-t)*t*28.6 + t*t*26,
		}
	}
	return func(x, y float64) bool {
		for i := 0; i < steps; i++ {
			if segDist(x, y, pts[i], pts[i+1]) <= 0.85 {
				return true
			}
		}
		return false
	}
}

func segDist(x, y float64, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	t := ((x-a[0])*dx + (y-a[1])*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(x-(a[0]+t*dx), y-(a[1]+t*dy))
}

func rgba(hex uint32, a float64) color.NRGBA {
	return color.NRGBA{uint8(hex >> 16), uint8(hex >> 8), uint8(hex), uint8(math.Round(a * 255))}
}

func to8(v float64) uint8 { return uint8(math.Round(math.Max(0, math.Min(1, v)) * 255)) }
