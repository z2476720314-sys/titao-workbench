package printcanvas

import (
	"math"
	"path/filepath"
	"testing"

	"titao/internal/psx"
)

func TestCanonicalValidatorsRejectExcessiveStretchEvidence(t *testing.T) {
	c, err := Build(patternFile(t, solid(945, 1890)), filepath.Join(t.TempDir(), "canvas.png"), psx.RenderRequest{NoCrop: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, ratio := range []float64{.96, 1.04, -1, math.NaN(), math.Inf(1)} {
		c.Placement.StretchY = ratio
		if err := Validate(c); err == nil {
			t.Errorf("builder validator accepted stretch %v", ratio)
		}
		if err := c.Validate(); err == nil {
			t.Errorf("adapter validator accepted stretch %v", ratio)
		}
	}
}

func TestPairedSidesAllowOnlySubtleVerticalAdjustment(t *testing.T) {
	c, err := Build(patternFile(t, solid(945, 1890)), filepath.Join(t.TempDir(), "canvas.png"), psx.RenderRequest{NoCrop: true, PreferOffsets: true, AnchorX: 2, OffsetY: .2}, .8)
	if err != nil {
		t.Fatal(err)
	}
	box := alphaBox(canvasImage(t, c))
	if box.Dx() != Width || box.Max.Y != Height || box.Dy() >= Height || box.Dy() < 1833 || c.Placement.AnchorUnmet || !c.Verified {
		t.Fatalf("paired sides blocked subtle safe adjustment or allowed obvious distortion: bounds=%v canvas=%+v", box, c)
	}
}

func TestPairedTopBottomAllowOnlySubtleHorizontalAdjustment(t *testing.T) {
	c, err := Build(patternFile(t, solid(945, 1890)), filepath.Join(t.TempDir(), "canvas.png"), psx.RenderRequest{NoCrop: true, PreferOffsets: true, AnchorY: 2, OffsetX: .2}, .8)
	if err != nil {
		t.Fatal(err)
	}
	box := alphaBox(canvasImage(t, c))
	if box.Dy() != Height || box.Max.X != Width || box.Dx() >= Width || box.Dx() < 916 || c.Placement.AnchorUnmet || !c.Verified {
		t.Fatalf("paired top/bottom blocked subtle safe adjustment or allowed obvious distortion: bounds=%v canvas=%+v", box, c)
	}
}

func TestSubtleStretchSurvivesTheNextTranslationOnlyRound(t *testing.T) {
	source := patternFile(t, solid(945, 1890))
	first, err := Build(source, filepath.Join(t.TempDir(), "first.png"), psx.RenderRequest{NoCrop: true, PreferOffsets: true, AnchorX: 2, OffsetY: .2}, .8)
	if err != nil {
		t.Fatal(err)
	}
	next, err := Build(source, filepath.Join(t.TempDir(), "next.png"), psx.RenderRequest{NoCrop: true, PreferOffsets: true, AnchorX: 2, OffsetY: first.Placement.OffsetY + .01, StretchY: first.Placement.VerticalStretch()}, first.Placement.Scale)
	if err != nil {
		t.Fatal(err)
	}
	if next.Placement.VerticalStretch() != first.Placement.VerticalStretch() || next.SHA256 != first.SHA256 {
		t.Fatalf("translation-only proposal undid the existing safe aspect: first=%+v next=%+v", first, next)
	}
}

func TestFirstRoundTriesSubtleAspectBeforeDeclaringPairedAnchorImpossible(t *testing.T) {
	c, err := Build(patternFile(t, solid(945, 1900)), filepath.Join(t.TempDir(), "canvas.png"), psx.RenderRequest{NoCrop: true, AnchorX: 2}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if c.Placement.AnchorUnmet || c.Placement.AnchorImpossible || c.Placement.VerticalStretch() >= 1 || !c.Placement.ValidStretch() || alphaBox(canvasImage(t, c)).Dx() != Width {
		t.Fatalf("small valid aspect was prematurely declared impossible: %+v", c)
	}
}

func TestSubtleAspectCanSatisfyBothPairedAxes(t *testing.T) {
	c, err := Build(patternFile(t, solid(945, 1900)), filepath.Join(t.TempDir(), "canvas.png"), psx.RenderRequest{NoCrop: true, AnchorX: 2, AnchorY: 2}, 1)
	if err != nil {
		t.Fatal(err)
	}
	box := alphaBox(canvasImage(t, c))
	if c.Placement.AnchorUnmet || !c.Placement.ValidStretch() || box.Dx() != Width || box.Dy() != Height {
		t.Fatalf("valid small aspect did not satisfy both paired axes: bounds=%v placement=%+v", box, c.Placement)
	}
}

func TestBuildRejectsInvalidRequestedAspect(t *testing.T) {
	source := patternFile(t, solid(94, 189))
	for _, ratio := range []float64{.96, 1.04, -1, math.NaN(), math.Inf(1)} {
		if _, err := Build(source, filepath.Join(t.TempDir(), "canvas.png"), psx.RenderRequest{NoCrop: true, StretchY: ratio}, 1); err == nil {
			t.Errorf("invalid input aspect accepted: %v", ratio)
		}
	}
}
