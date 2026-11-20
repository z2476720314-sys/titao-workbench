package printcanvas

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"testing"

	"titao/internal/imgutil"
	"titao/internal/psx"
)

func TestPlanPreservesOriginalBytesAndBindsSourceAndTransform(t *testing.T) {
	source := patternFile(t, solid(300, 600))
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	one, err := Plan(source, filepath.Join(t.TempDir(), "plan.json"), psx.RenderRequest{NoCrop: true, IgnoreCutAnchors: true}, .5)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Plan(source, filepath.Join(t.TempDir(), "plan.json"), psx.RenderRequest{NoCrop: true, OffsetX: .1, IgnoreCutAnchors: true}, .5)
	if err != nil {
		t.Fatal(err)
	}
	if one.SHA256 == two.SHA256 || one.SourceSHA256 != two.SourceSHA256 {
		t.Fatal("source and placement identity were conflated")
	}
	if one.SourceWidth != 300 || one.SourceHeight != 600 || one.Kind != psx.DirectArtwork {
		t.Fatalf("invalid direct plan: %+v", one)
	}
	snapshot, err := os.ReadFile(one.SourcePath())
	if err != nil || !bytes.Equal(snapshot, original) {
		t.Fatal("source pixels/container were redrawn")
	}
	if im, _, err := image.DecodeConfig(bytes.NewReader(snapshot)); err != nil || im.Width != 300 || im.Height != 600 {
		t.Fatal("source was padded")
	}
	final := filepath.Join(t.TempDir(), "plan.json")
	data, _ := os.ReadFile(one.Path)
	if err := os.WriteFile(final, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(final), one.SourceFile), snapshot, 0600); err != nil {
		t.Fatal(err)
	}
	moved, err := psx.LoadArtworkPlan(final)
	if err != nil || moved.SHA256 != one.SHA256 {
		t.Fatalf("relocated plan invalid: %v", err)
	}
	if err := os.WriteFile(moved.SourcePath(), append(snapshot, 0), 0600); err != nil {
		t.Fatal(err)
	}
	if moved.Validate() == nil {
		t.Fatal("modified source accepted")
	}
	one.Transform[4]++
	if one.Validate() == nil {
		t.Fatal("modified transform accepted against unchanged manifest")
	}
}

func TestDirectAutoStartsAtOriginalPixelSize(t *testing.T) {
	source := patternFile(t, solid(300, 600))
	for _, noCrop := range []bool{false, true} {
		c, err := Plan(source, filepath.Join(t.TempDir(), "plan.json"), psx.RenderRequest{NoCrop: noCrop, Fit: psx.FitAuto, IgnoreCutAnchors: true}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(c.Transform[0]-1) > 1e-12 || math.Abs(c.Transform[3]-1) > 1e-12 {
			t.Fatalf("auto silently rescaled source: %+v", c)
		}
	}
}

func TestDirectPlanCropsOnlyOuterZeroAlphaAndPreservesPrecision(t *testing.T) {
	im := image.NewNRGBA64(image.Rect(0, 0, 9, 11))
	im.SetNRGBA64(2, 3, color.NRGBA64{R: 60000, G: 12345, B: 4567, A: 1})
	im.SetNRGBA64(6, 8, color.NRGBA64{R: 65535, G: 23456, B: 34567, A: 32768})
	im.SetNRGBA64(4, 5, color.NRGBA64{R: 54321, G: 1234, B: 5678, A: 0})
	source := patternFile(t, im)
	plan, err := Plan(source, filepath.Join(t.TempDir(), "plan.json"), psx.RenderRequest{Fit: psx.FitAuto, NoCrop: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if plan.SourceWidth != 5 || plan.SourceHeight != 6 {
		t.Fatalf("outer transparent pixels retained: %dx%d", plan.SourceWidth, plan.SourceHeight)
	}
	got, err := imgutil.Decode(plan.SourcePath())
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 6; y++ {
		for x := 0; x < 5; x++ {
			want := im.NRGBA64At(x+2, y+3)
			actual := color.NRGBA64Model.Convert(got.At(x, y))
			if actual != want {
				t.Fatalf("source RGBA changed at %d,%d: got %v want %v", x, y, actual, want)
			}
		}
	}
}

func TestContentPNGKeepsStraightAlphaForNonNRGBAFallback(t *testing.T) {
	im := image.NewRGBA(image.Rect(0, 0, 3, 3))
	im.SetRGBA(1, 1, color.RGBA{R: 128, A: 128})
	// A wrapper deliberately lacks SubImage, exercising the generic image path.
	wrapped := struct{ image.Image }{im}
	data, box, err := contentPNG(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if box != image.Rect(1, 1, 2, 2) || color.NRGBA64Model.Convert(got.At(0, 0)) != (color.NRGBA64{R: 65535, A: 32896}) {
		t.Fatalf("premultiplied RGB darkened: %v %v", box, got.At(0, 0))
	}
}
