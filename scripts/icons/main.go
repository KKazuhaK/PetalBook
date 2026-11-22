// Command icons draws the PNG icons of the installable web app from the
// shapes of web/public/favicon.svg, using only the standard library (there is
// no image tool in the build environment). Run it from the repository root:
//
//	go run ./scripts/icons
//
// It writes web/public/icon-192.png, icon-512.png, icon-maskable-512.png and
// apple-touch-icon.png. The SVG is the design; if it changes, update the
// coordinates below and run this again.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// The favicon's coordinate system is 32 x 32.
const (
	viewBox   = 32.0
	cornerR   = 8.0 // rx of the background square
	markCX    = 16.0
	markCY    = 15.0 // vertical centre of the petals (y 6 to 24)
	markReach = 9.0  // farthest point of the mark from (markCX, markCY)
)

type rgb struct{ r, g, b float64 }

var (
	brand  = rgb{0xbe, 0x18, 0x5d} // #be185d
	petalA = rgb{0xfd, 0xf2, 0xf8} // #fdf2f8, the centre petal
	petalB = rgb{0xfb, 0xcf, 0xe8} // #fbcfe8, the two side petals
)

type pt struct{ x, y float64 }

// cubic is one cubic Bezier segment p0 p1 p2 p3.
type cubic [4]pt

// The three paths of favicon.svg with their relative and smooth commands
// resolved to absolute cubic segments.
var (
	centrePetal = []cubic{
		{{16, 6}, {19.5, 9}, {21, 12}, {21, 15}},
		{{21, 15}, {21, 18}, {19, 20}, {16, 20}},
		{{16, 20}, {13, 20}, {11, 18}, {11, 15}},
		{{11, 15}, {11, 12}, {12.5, 9}, {16, 6}},
	}
	leftPetal = []cubic{
		{{8, 17}, {12, 17}, {15, 19}, {16, 24}},
		{{16, 24}, {11, 24}, {8, 22}, {8, 17}},
	}
	rightPetal = []cubic{
		{{24, 17}, {20, 17}, {17, 19}, {16, 24}},
		{{16, 24}, {21, 24}, {24, 22}, {24, 17}},
	}
)

// flatten turns the segments into a polygon.
func flatten(segs []cubic) []pt {
	const steps = 48
	var out []pt
	for _, s := range segs {
		for i := 0; i < steps; i++ {
			t := float64(i) / steps
			u := 1 - t
			out = append(out, pt{
				u*u*u*s[0].x + 3*u*u*t*s[1].x + 3*u*t*t*s[2].x + t*t*t*s[3].x,
				u*u*u*s[0].y + 3*u*u*t*s[1].y + 3*u*t*t*s[2].y + t*t*t*s[3].y,
			})
		}
	}
	return out
}

// inside is the even-odd point-in-polygon test.
func inside(poly []pt, x, y float64) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		a, b := poly[i], poly[j]
		if (a.y > y) != (b.y > y) && x < (b.x-a.x)*(y-a.y)/(b.y-a.y)+a.x {
			in = !in
		}
	}
	return in
}

// inRoundRect tests a square of side viewBox with corner radius r.
func inRoundRect(x, y, r float64) bool {
	if x < 0 || y < 0 || x > viewBox || y > viewBox {
		return false
	}
	cx := math.Min(math.Max(x, r), viewBox-r)
	cy := math.Min(math.Max(y, r), viewBox-r)
	return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
}

type variant struct {
	file string
	size int
	// radius of the background's corners in viewBox units: 0 is full-bleed.
	radius float64
	// reach is how far the mark may extend from the centre, as a fraction of
	// the icon's size; 0 keeps the favicon's own proportions.
	reach float64
}

func render(v variant, petals [3][]pt) *image.NRGBA {
	const ss = 8 // 8 x 8 samples per pixel
	n := v.size
	// Map icon pixels to favicon units. With no reach the whole icon is the
	// 32 x 32 favicon; otherwise the mark is scaled and centred so it fits
	// inside the circle a launcher's mask always leaves visible.
	scale := viewBox / float64(n) // favicon units per pixel
	offX, offY := 0.0, 0.0
	if v.reach > 0 {
		unitsPerPx := markReach / (v.reach * float64(n))
		scale = unitsPerPx
		offX = markCX - float64(n)/2*scale
		offY = markCY - float64(n)/2*scale
	}
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for py := 0; py < n; py++ {
		for px := 0; px < n; px++ {
			var r, g, b, a float64 // premultiplied sums
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					// The background is positioned in the icon's own frame; the mark in the mark's.
					bx := (float64(px) + (float64(sx)+0.5)/ss) * viewBox / float64(n)
					by := (float64(py) + (float64(sy)+0.5)/ss) * viewBox / float64(n)
					mx := offX + (float64(px)+(float64(sx)+0.5)/ss)*scale
					my := offY + (float64(py)+(float64(sy)+0.5)/ss)*scale
					var c rgb
					switch {
					case !inRoundRect(bx, by, v.radius):
						continue
					case inside(petals[0], mx, my):
						c = petalA
					case inside(petals[1], mx, my) || inside(petals[2], mx, my):
						c = petalB
					default:
						c = brand
					}
					r, g, b, a = r+c.r, g+c.g, b+c.b, a+1
				}
			}
			if a == 0 {
				continue
			}
			i := img.PixOffset(px, py)
			img.Pix[i+0] = uint8(math.Round(r / a))
			img.Pix[i+1] = uint8(math.Round(g / a))
			img.Pix[i+2] = uint8(math.Round(b / a))
			img.Pix[i+3] = uint8(math.Round(255 * a / (ss * ss)))
		}
	}
	return img
}

func main() {
	out := flag.String("out", "web/public", "directory to write the icons to")
	flag.Parse()
	petals := [3][]pt{flatten(centrePetal), flatten(leftPetal), flatten(rightPetal)}
	for _, v := range []variant{
		{"icon-192.png", 192, cornerR, 0},
		{"icon-512.png", 512, cornerR, 0},
		// Full bleed, mark inside the central 80% circle (radius 40% of the size).
		{"icon-maskable-512.png", 512, 0, 0.36},
		// iOS rounds the corners itself and paints transparency black.
		{"apple-touch-icon.png", 180, 0, 0},
	} {
		img := render(v, petals)
		f, err := os.Create(filepath.Join(*out, v.file))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		err = png.Encode(f, img)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("wrote", f.Name())
	}
}
