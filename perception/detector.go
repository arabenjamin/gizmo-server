// Package perception finds faces in the robot's camera frames. It used to run
// on the Pi; it runs here so the robot stays a device with no agency.
package perception

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"

	pigo "github.com/esimov/pigo/core"
)

// facefinder is Pigo's upstream face cascade (MIT, github.com/esimov/pigo).
//
//go:embed facefinder
var facefinder []byte

// Face is a detection in image pixels. X/Y is the CENTRE of the face.
type Face struct {
	X    int     `json:"x"`
	Y    int     `json:"y"`
	Size int     `json:"size"`
	Q    float32 `json:"q"`
}

// Params tune the cascade search. The defaults are the values benchmarked on
// the robot over real frames (2026-09-17): min 50 / max 280 / shift 0.10 /
// scale 1.15 ran 2.4x faster than Pigo's defaults with identical detections,
// while coarser settings missed faces. Pigo emits every candidate, including
// junk (one real face at Q=20 came with Q=1.3 and Q=2.6 noise), so QMin
// filters by quality.
type Params struct {
	MinSize, MaxSize int
	Shift, Scale     float64
	QMin             float32
	IoU              float64 // clustering overlap threshold
}

var DefaultParams = Params{MinSize: 50, MaxSize: 280, Shift: 0.1, Scale: 1.15, QMin: 5.0, IoU: 0.2}

type Detector struct {
	p       Params
	cascade *pigo.Pigo
}

func NewDetector(p Params) (*Detector, error) {
	c, err := pigo.NewPigo().Unpack(facefinder)
	if err != nil {
		return nil, fmt.Errorf("unpack face cascade: %w", err)
	}
	return &Detector{p: p, cascade: c}, nil
}

// DecodeGray decodes a JPEG into a tightly packed grayscale image.
func DecodeGray(jpegBytes []byte) (*image.Gray, error) {
	img, err := jpeg.Decode(bytes.NewReader(jpegBytes))
	if err != nil {
		return nil, err
	}
	if g, ok := img.(*image.Gray); ok && g.Stride == g.Rect.Dx() && g.Rect.Min == (image.Point{}) {
		return g, nil
	}
	b := img.Bounds()
	g := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(g, g.Rect, img, b.Min, draw.Src)
	return g, nil
}

// Detect returns faces above the quality threshold, largest first.
func (d *Detector) Detect(g *image.Gray) []Face {
	w, h := g.Rect.Dx(), g.Rect.Dy()
	maxSize := d.p.MaxSize
	if h < maxSize {
		maxSize = h
	}
	dets := d.cascade.RunCascade(pigo.CascadeParams{
		MinSize:     d.p.MinSize,
		MaxSize:     maxSize,
		ShiftFactor: d.p.Shift,
		ScaleFactor: d.p.Scale,
		ImageParams: pigo.ImageParams{Pixels: g.Pix, Rows: h, Cols: w, Dim: g.Stride},
	}, 0.0)
	dets = d.cascade.ClusterDetections(dets, d.p.IoU)

	var faces []Face
	for _, det := range dets {
		if det.Q > d.p.QMin {
			// Pigo's Row/Col is the detection centre (its own CLI draws the
			// box at Col-Scale/2); adding Scale/2 biased every target by half
			// a face, a bug the robot once had.
			faces = append(faces, Face{X: det.Col, Y: det.Row, Size: det.Scale, Q: det.Q})
		}
	}
	// Largest first: Pigo returns clusters in no meaningful order, and the
	// biggest face is the nearest and most stable target.
	for i := 1; i < len(faces); i++ {
		for j := i; j > 0 && faces[j].Size > faces[j-1].Size; j-- {
			faces[j], faces[j-1] = faces[j-1], faces[j]
		}
	}
	return faces
}

// Annotate draws a box around each face, in place, for the operator's view.
func Annotate(g *image.Gray, faces []Face) {
	for i, f := range faces {
		shade := uint8(255) // brightest box = the one being tracked
		if i > 0 {
			shade = 160
		}
		half := f.Size / 2
		box(g, f.X-half, f.Y-half, f.X+half, f.Y+half, shade)
		box(g, f.X-half+1, f.Y-half+1, f.X+half-1, f.Y+half-1, 0)
	}
}

func box(g *image.Gray, x0, y0, x1, y1 int, v uint8) {
	set := func(x, y int) {
		if (image.Point{x, y}).In(g.Rect) {
			g.Pix[g.PixOffset(x, y)] = v
		}
	}
	for x := x0; x <= x1; x++ {
		set(x, y0)
		set(x, y1)
	}
	for y := y0; y <= y1; y++ {
		set(x0, y)
		set(x1, y)
	}
}
