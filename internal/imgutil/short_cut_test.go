package imgutil

import (
	"image"
	"image/color"
	"image/draw"
	"os"
	"path/filepath"
	"testing"
)

func TestShortStableCutAtOriginalSideIsAnchored(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 1000, 2000))
	im.SetNRGBA(500, 0, color.NRGBA{A: 8})
	im.SetNRGBA(500, 1999, color.NRGBA{A: 8})
	draw.Draw(im, image.Rect(0, 800, 40, 850), image.NewUniform(color.NRGBA{R: 200, A: 255}), image.Point{}, draw.Src)
	draw.Draw(im, image.Rect(960, 800, 1000, 1100), image.NewUniform(color.NRGBA{R: 200, A: 255}), image.Point{}, draw.Src)
	if x, _, _ := cutEdgeAnchors(im); x != 2 {
		t.Fatalf("short real side cut lost: x=%d", x)
	}
}

func TestSmallCompleteCircleAtEdgeDoesNotBecomeCut(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 1000, 2000))
	for y := 970; y <= 1030; y++ {
		for x := 0; x <= 60; x++ {
			if (x-30)*(x-30)+(y-1000)*(y-1000) <= 900 {
				im.SetNRGBA(x, y, color.NRGBA{A: 255})
			}
		}
	}
	im.SetNRGBA(990, 1900, color.NRGBA{A: 8})
	if x, y, _ := cutEdgeAnchors(im); x != 0 || y != 0 {
		t.Fatalf("natural tangent got cut anchors: %d,%d", x, y)
	}
}

func TestReported943OriginalCutEdges(t *testing.T) {
	path := os.Getenv("TITAO_943_SOURCE")
	if path == "" {
		t.Skip("reported source not configured")
	}
	pr, err := Normalize(path, filepath.Join(t.TempDir(), "content.png"), true)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Width != 945 || pr.Height != 1488 || pr.AnchorX != 2 {
		t.Fatalf("source-content/side cuts wrong: %+v", pr)
	}
}
