package printcanvas

import (
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"titao/internal/imgutil"
	"titao/internal/psx"
)

func patternFile(t *testing.T, im image.Image) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pattern.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func solid(w, h int) *image.NRGBA {
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.SetNRGBA(x, y, color.NRGBA{200, 80, 30, 128})
		}
	}
	return im
}

func canvasImage(t *testing.T, c *psx.PrintCanvas) image.Image {
	t.Helper()
	im, err := imgutil.Decode(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func alphaBox(im image.Image) image.Rectangle {
	box := image.Rectangle{}
	for y := im.Bounds().Min.Y; y < im.Bounds().Max.Y; y++ {
		for x := im.Bounds().Min.X; x < im.Bounds().Max.X; x++ {
			_, _, _, a := im.At(x, y).RGBA()
			if a != 0 {
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return box
}

func TestBuildNoCropProtectsFaintPixelsAndBindsArtifact(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 945, 1890))
	im.SetNRGBA(0, 0, color.NRGBA{255, 0, 0, 1})
	im.SetNRGBA(944, 1889, color.NRGBA{0, 255, 0, 128})
	im.SetNRGBA(300, 500, color.NRGBA{0, 0, 255, 64})
	source := patternFile(t, im)
	before, _ := os.ReadFile(source)
	out := filepath.Join(t.TempDir(), "nested", "canvas.png")
	c, err := Build(source, out, psx.RenderRequest{NoCrop: true, Fit: psx.FitStretch, OffsetX: .4, OffsetY: -.3, ContentBounds: [4]float64{.2, .2, .8, .8}}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if c.Path != out || c.Width != 945 || c.Height != 1890 || !c.Verified || !c.Placement.Constrained || math.Abs(c.Placement.Scale-1) > 1e-9 || math.Abs(c.Placement.OffsetX) > 1e-9 || math.Abs(c.Placement.OffsetY) > 1e-9 {
		t.Fatalf("wrong canonical placement: %+v", c)
	}
	got := canvasImage(t, c)
	for _, p := range []image.Point{{0, 0}, {944, 1889}, {300, 500}, {0, 1}} {
		if a, b := color.NRGBAModel.Convert(got.At(p.X, p.Y)), im.NRGBAAt(p.X, p.Y); a != b {
			t.Errorf("pixel %v changed: %v != %v", p, a, b)
		}
	}
	data, _ := os.ReadFile(out)
	if c.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) || c.SourceSHA256 != fmt.Sprintf("%x", sha256.Sum256(before)) {
		t.Fatal("hash does not bind actual file bytes")
	}
	after, _ := os.ReadFile(source)
	if string(before) != string(after) {
		t.Fatal("source was changed")
	}
	if err := Validate(c); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, append(data, 0), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(c); err == nil {
		t.Fatal("tampered artifact accepted")
	}
}

func TestBuildNoCropAnchorsUniformGeometry(t *testing.T) {
	source := patternFile(t, solid(126, 126))
	c, err := Build(source, filepath.Join(t.TempDir(), "canvas.png"), psx.RenderRequest{NoCrop: true, AnchorX: -1, AnchorY: 1, OffsetX: .4, OffsetY: -.3}, 2.0/3)
	if err != nil {
		t.Fatal(err)
	}
	if got := alphaBox(canvasImage(t, c)); got != image.Rect(0, 1260, 630, 1890) {
		t.Fatalf("straight edges detached or aspect changed: %v", got)
	}
	if c.Placement.AnchorUnmet || c.Placement.AnchorImpossible || !c.Verified {
		t.Fatalf("unexpected placement: %+v", c)
	}
}

func TestBuildNoCropReportsImpossiblePairedAnchorWithoutCropping(t *testing.T) {
	source := patternFile(t, solid(100, 400))
	c, err := Build(source, filepath.Join(t.TempDir(), "canvas.png"), psx.RenderRequest{NoCrop: true, AnchorX: 2}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Verified || !c.Placement.AnchorUnmet || !c.Placement.AnchorImpossible {
		t.Fatalf("anchor failure must remain distinct from no-crop proof: %+v", c)
	}
	if box := alphaBox(canvasImage(t, c)); box.Min.Y != 0 || box.Max.Y != 1890 || box.Dx() > 474 {
		t.Fatalf("portrait distorted or cropped: %v", box)
	}
}

func TestBuildNoCropPrefersRequestedTranslationAndIsDeterministic(t *testing.T) {
	source := patternFile(t, solid(63, 126))
	out := filepath.Join(t.TempDir(), "canvas.png")
	req := psx.RenderRequest{NoCrop: true, PreferOffsets: true, OffsetX: .2, OffsetY: -.1}
	c, err := Build(source, out, req, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Verified || math.Abs(c.Placement.Scale-.6) > 1e-9 || math.Abs(c.Placement.OffsetX-.2) > 1e-9 || math.Abs(c.Placement.OffsetY+.1) > 1e-9 {
		t.Fatalf("requested offset was not realized by uniform shrinking: %+v", c)
	}
	second, err := Build(source, out, req, 1)
	if err != nil {
		t.Fatal(err)
	}
	if second.SHA256 != c.SHA256 || second.SourceSHA256 != c.SourceSHA256 || second.Placement != c.Placement {
		t.Fatal("same composition produced different artifact evidence")
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(out), ".print-canvas-*.png"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary output retained: %v %v", leftovers, err)
	}
}

func TestBuildManualFitsKeepIntentionalCropStretchAndOffsets(t *testing.T) {
	source := patternFile(t, solid(315, 315))
	for _, tc := range []struct {
		fit           string
		scale, ox, oy float64
		box           image.Rectangle
		verified      bool
	}{
		{psx.FitContain, 1, 0, 0, image.Rect(0, 472, 945, 1417), true},
		{psx.FitCover, 1, .3, .2, image.Rect(0, 0, 945, 1890), false},
		{psx.FitStretch, 1, 0, 0, image.Rect(0, 0, 945, 1890), true},
		{psx.FitNone, 1, 0, .1, image.Rect(315, 976, 630, 1291), true},
		{psx.FitContain, 2, 0, 0, image.Rect(0, 0, 945, 1890), false},
	} {
		t.Run(tc.fit+fmt.Sprint(tc.scale), func(t *testing.T) {
			c, err := Build(source, filepath.Join(t.TempDir(), "canvas.png"), psx.RenderRequest{Fit: tc.fit, OffsetX: tc.ox, OffsetY: tc.oy}, tc.scale)
			if err != nil {
				t.Fatal(err)
			}
			if box := alphaBox(canvasImage(t, c)); box != tc.box {
				t.Fatalf("fit lost its geometry: got %v want %v", box, tc.box)
			}
			if c.Verified != tc.verified || c.Placement.Scale != tc.scale {
				t.Fatalf("wrong fit evidence: %+v", c)
			}
			if err := Validate(c); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBuildManualCoverClampsOffsetsAtExactCanvasRatio(t *testing.T) {
	source := patternFile(t, solid(100, 200))
	for _, tc := range []struct{ scale, offset float64 }{{1, 0}, {1.2, .1}} {
		c, err := Build(source, filepath.Join(t.TempDir(), "canvas.png"), psx.RenderRequest{Fit: psx.FitCover, OffsetX: .3, OffsetY: .2}, tc.scale)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(c.Placement.OffsetX-tc.offset) > 1e-12 || math.Abs(c.Placement.OffsetY-tc.offset) > 1e-12 {
			t.Fatalf("cover failed to clamp its exact-ratio bounds: %+v", c.Placement)
		}
		if box := alphaBox(canvasImage(t, c)); box != image.Rect(0, 0, 945, 1890) {
			t.Fatalf("cover exposed a transparent border: %v", box)
		}
	}
}

func TestBuildRejectsInvalidInputsAndProtectsSource(t *testing.T) {
	source := patternFile(t, solid(2, 2))
	for _, tc := range []struct {
		name  string
		req   psx.RenderRequest
		scale float64
	}{
		{"zero", psx.RenderRequest{}, 0}, {"infinite", psx.RenderRequest{}, math.Inf(1)}, {"nan", psx.RenderRequest{}, math.NaN()},
		{"offset", psx.RenderRequest{OffsetX: math.NaN()}, 1}, {"anchor", psx.RenderRequest{AnchorX: 3}, 1},
		{"bounds", psx.RenderRequest{ContentBounds: [4]float64{0, 0, 1, math.Inf(1)}}, 1}, {"inverted", psx.RenderRequest{ContentBounds: [4]float64{.8, 0, .2, 1}}, 1},
		{"fit", psx.RenderRequest{Fit: "bogus"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Build(source, filepath.Join(t.TempDir(), "out.png"), tc.req, tc.scale); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	if _, err := Build(source, source, psx.RenderRequest{NoCrop: true}, 1); err == nil {
		t.Fatal("source overwrite accepted")
	}
	alias := filepath.Join(t.TempDir(), "alias.png")
	if err := os.Link(source, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(source, alias, psx.RenderRequest{}, 1); err == nil {
		t.Fatal("hard-linked source overwrite accepted")
	}
	if _, err := Build(patternFile(t, image.NewNRGBA(image.Rect(0, 0, 2, 2))), filepath.Join(t.TempDir(), "empty.png"), psx.RenderRequest{NoCrop: true}, 1); err == nil {
		t.Fatal("empty artwork accepted")
	}
	bad := filepath.Join(t.TempDir(), "bad.png")
	if err := os.WriteFile(bad, []byte("broken png"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(bad, filepath.Join(t.TempDir(), "out.png"), psx.RenderRequest{}, 1); err == nil {
		t.Fatal("corrupt source accepted")
	}
}

func TestValidateRejectsForgedEvidence(t *testing.T) {
	c, err := Build(patternFile(t, solid(1, 2)), filepath.Join(t.TempDir(), "canvas.png"), psx.RenderRequest{NoCrop: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*psx.PrintCanvas){
		func(c *psx.PrintCanvas) { c.Path = "" }, func(c *psx.PrintCanvas) { c.SHA256 = "" }, func(c *psx.PrintCanvas) { c.SourceSHA256 = "bogus" },
		func(c *psx.PrintCanvas) { c.Width = 946 }, func(c *psx.PrintCanvas) { c.Placement.Scale = 0 }, func(c *psx.PrintCanvas) { c.Placement.OffsetY = math.NaN() },
	} {
		bad := *c
		mutate(&bad)
		if err := Validate(&bad); err == nil {
			t.Fatalf("forged evidence accepted: %+v", bad)
		}
	}
	if err := Validate(nil); err == nil {
		t.Fatal("nil canvas accepted")
	}
	wrong := patternFile(t, solid(1, 2))
	data, _ := os.ReadFile(wrong)
	bad := *c
	bad.Path = wrong
	bad.SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
	if err := Validate(&bad); err == nil {
		t.Fatal("noncanonical decoded dimensions accepted")
	}
	data = []byte("invalid png with a matching digest")
	if err := os.WriteFile(wrong, data, 0600); err != nil {
		t.Fatal(err)
	}
	bad.SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
	if err := Validate(&bad); err == nil {
		t.Fatal("corrupt PNG with matching digest accepted")
	}
	if err := os.Remove(c.Path); err != nil {
		t.Fatal(err)
	}
	if err := Validate(c); err == nil {
		t.Fatal("missing canvas accepted")
	}
}
