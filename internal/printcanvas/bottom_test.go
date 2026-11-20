package printcanvas

import (
	"image"
	"image/color"
	"math"
	"path/filepath"
	"testing"

	"titao/internal/psx"
)

func TestBottomIgnoresOnlyTrailingFaintFringe(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 200, 1890))
	im.SetNRGBA(0, 0, color.NRGBA{R: 100, A: 3})
	im.SetNRGBA(199, 1889, color.NRGBA{R: 100, A: 7})
	im.SetNRGBA(30, 1267, color.NRGBA{R: 100, A: 8})
	previous, err := Plan(patternFile(t, im), filepath.Join(t.TempDir(), "plan.json"), psx.RenderRequest{Fit: psx.FitAuto, NoCrop: true, IgnoreCutAnchors: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	bottom, err := MoveToBottom(previous, filepath.Join(t.TempDir(), "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bottom.Transform != [6]float64{1, 0, 0, 1, 372.5, 622} || bottom.SourceHeight != 1268 || bottom.SourceWidth != 200 || bottom.CropBox != [4]int{0, 0, 200, 1268} || bottom.OriginalSHA256 != previous.OriginalSHA256 {
		t.Fatalf("visible artwork did not move intact: %+v", bottom)
	}
}

func TestBottomTranslationPreservesExistingStretchAndSource(t *testing.T) {
	previous, err := Plan(patternFile(t, solid(300, 600)), filepath.Join(t.TempDir(), "plan.json"), psx.RenderRequest{Fit: psx.FitAuto, NoCrop: true, IgnoreCutAnchors: true, OffsetX: .1, StretchY: 1.05}, .8)
	if err != nil {
		t.Fatal(err)
	}
	before := *previous
	bottom, err := MoveToBottom(previous, filepath.Join(t.TempDir(), "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if bottom.Transform[i] != previous.Transform[i] {
			t.Fatalf("scale or x changed: %v -> %v", previous.Transform, bottom.Transform)
		}
	}
	if math.Abs(bottom.Transform[5]+float64(bottom.SourceHeight)*bottom.Transform[3]-float64(Height)) > 1e-9 || bottom.Placement.Scale != .8 || bottom.Placement.VerticalStretch() != 1.05 || bottom.SourceSHA256 != previous.SourceSHA256 || bottom.OriginalSHA256 != previous.OriginalSHA256 || bottom.CropBox != previous.CropBox || *previous != before {
		t.Fatalf("bottom changed previous artwork: %+v -> %+v", previous, bottom)
	}
	if err := previous.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := bottom.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := MoveToBottom(previous, filepath.Join(filepath.Dir(previous.Path), "overwrite.json")); err == nil {
		t.Fatal("allowed previous artifact directory to be overwritten")
	}
}
