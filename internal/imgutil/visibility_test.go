package imgutil

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeIgnoresDistantNearlyInvisibleResidue(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	for y := 30; y < 70; y++ {
		for x := 40; x < 60; x++ {
			im.SetNRGBA(x, y, color.NRGBA{200, 100, 50, 255})
		}
	}
	im.SetNRGBA(0, 0, color.NRGBA{255, 0, 0, 1})
	im.SetNRGBA(99, 99, color.NRGBA{255, 0, 0, 1})
	im.SetNRGBA(39, 50, color.NRGBA{200, 100, 50, 4})
	dir := t.TempDir()
	src := filepath.Join(dir, "source.png")
	dst := filepath.Join(dir, "normalized.png")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
	f.Close()
	r, err := Normalize(src, dst, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Width >= 50 || r.Height >= 60 {
		t.Fatalf("residue controls placement bounds: %dx%d", r.Width, r.Height)
	}
	if r.OrigBBox == nil || r.OrigBBox.X0 > 39 {
		t.Fatal("nearby soft edge was removed")
	}
}

func TestNormalizePreservesFaintOnlyArtwork(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 50, 50))
	for y := 10; y < 30; y++ {
		for x := 15; x < 25; x++ {
			im.SetNRGBA(x, y, color.NRGBA{120, 100, 90, 3})
		}
	}
	b, ok := alphaBounds(im)
	if !ok || b != image.Rect(15, 10, 25, 30) {
		t.Fatalf("faint-only artwork lost: %v %v", b, ok)
	}
}

func TestMixedFaintArtworkIsNotReducedToSingleBrightPixel(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			im.SetNRGBA(x, y, color.NRGBA{180, 160, 140, 7})
		}
	}
	im.SetNRGBA(50, 50, color.NRGBA{180, 160, 140, 8})
	box, ok := alphaBounds(im)
	if !ok || box != im.Bounds() {
		t.Fatalf("faint composition cropped: %v", box)
	}
}

func TestFillWhiteDoesNotMultiplyAlphaTwice(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	im.SetNRGBA(0, 0, color.NRGBA{255, 255, 255, 128})
	r, g, b, a := FillWhite(im).At(0, 0).RGBA()
	if r != 65535 || g != 65535 || b != 65535 || a != 65535 {
		t.Fatalf("white gained a dark fringe: %d %d %d %d", r, g, b, a)
	}
}
