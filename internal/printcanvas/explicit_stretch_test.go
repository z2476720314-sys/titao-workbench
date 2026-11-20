package printcanvas

import (
	"image"
	"math"
	"path/filepath"
	"testing"

	"titao/internal/psx"
)

func TestManualPlacementAppliesExplicitVerticalStretch(t *testing.T) {
	for _, fit := range []string{psx.FitContain, psx.FitCover, psx.FitStretch, psx.FitNone} {
		t.Run(fit, func(t *testing.T) {
			source := patternFile(t, solid(945, 1800))
			req := psx.RenderRequest{Fit: fit}
			base, err := Build(source, filepath.Join(t.TempDir(), "base.png"), req, .8)
			if err != nil {
				t.Fatal(err)
			}
			req.StretchY = 1.05
			stretched, err := Build(source, filepath.Join(t.TempDir(), "stretched.png"), req, .8)
			if err != nil {
				t.Fatal(err)
			}
			before, after := alphaBox(canvasImage(t, base)), alphaBox(canvasImage(t, stretched))
			if base.SHA256 == stretched.SHA256 || after.Dy() <= before.Dy() || math.Abs(float64(after.Dx()-before.Dx())) > 1 || math.Abs(stretched.Placement.VerticalStretch()-1.05) > 1e-12 || stretched.Placement.Scale != .8 {
				t.Fatalf("explicit vertical stretch lost or changed horizontal scale: before=%v after=%v placement=%+v", before, after, stretched.Placement)
			}
			if err := Validate(stretched); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManualPairedSidesRetainExplicitVerticalStretch(t *testing.T) {
	req := psx.RenderRequest{Fit: psx.FitContain, AnchorX: 2, ContentBounds: [4]float64{0, 0, 1, 1}, StretchY: 1.05}
	canvas, err := Build(patternFile(t, solid(945, 1800)), filepath.Join(t.TempDir(), "canvas.png"), req, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := alphaBox(canvasImage(t, canvas)); got != image.Rect(0, 0, 945, 1890) || canvas.Placement.VerticalStretch() != 1.05 {
		t.Fatalf("paired sides discarded explicit stretch: bounds=%v placement=%+v", got, canvas.Placement)
	}
}

func TestNoCropPairedSidesPreserveSuccessfulExplicitTopFill(t *testing.T) {
	req := psx.RenderRequest{NoCrop: true, PreferOffsets: true, AnchorX: 2, AnchorY: 1, StretchY: 1.05}
	canvas, err := Build(patternFile(t, solid(945, 1800)), filepath.Join(t.TempDir(), "canvas.png"), req, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := alphaBox(canvasImage(t, canvas)); got != image.Rect(0, 0, 945, 1890) || canvas.Placement.VerticalStretch() != 1.05 || canvas.Placement.AnchorUnmet || !canvas.Verified {
		t.Fatalf("automatic anchor compensation replaced successful explicit top fill: bounds=%v placement=%+v", got, canvas.Placement)
	}
}
