package perception

import (
	"image"
	"os"
	"testing"
)

func TestDetectsTheFaceInPigosSample(t *testing.T) {
	raw, err := os.ReadFile("testdata/sample.jpg") // Pigo's own test image (MIT)
	if err != nil {
		t.Fatal(err)
	}
	g, err := DecodeGray(raw)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDetector(DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	faces := d.Detect(g)
	if len(faces) != 1 {
		t.Fatalf("found %d faces, want 1: %+v", len(faces), faces)
	}
	f := faces[0]
	// One centred portrait: centre near (160, 200), a large face.
	if abs(f.X-160) > 30 || abs(f.Y-200) > 40 || f.Size < 120 {
		t.Errorf("face = %+v, want centre near (160,200) and size >= 120", f)
	}
}

func TestNoFacesInABlankFrame(t *testing.T) {
	d, _ := NewDetector(DefaultParams)
	g := image.NewGray(image.Rect(0, 0, 640, 480))
	for i := range g.Pix {
		g.Pix[i] = 128
	}
	if faces := d.Detect(g); len(faces) != 0 {
		t.Errorf("blank frame produced %+v", faces)
	}
}

func TestAnnotateClipsAtEdges(t *testing.T) {
	g := image.NewGray(image.Rect(0, 0, 64, 48))
	// A face hanging off every edge must not panic or write out of bounds.
	Annotate(g, []Face{{X: 0, Y: 0, Size: 200}, {X: 63, Y: 47, Size: 30}})
	if g.Pix[g.PixOffset(0, 0)] != 0 && g.Pix[g.PixOffset(0, 0)] != 255 {
		t.Error("expected corner pixel to be drawn or untouched")
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
