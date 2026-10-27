package imgutil

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestExtendTopPreservesOriginalPixels(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 10, 15))
	for y := 0; y < 15; y++ {
		for x := 0; x < 10; x++ {
			im.SetNRGBA(x, y, color.NRGBA{uint8(y * 10), uint8(x * 20), 40, 255})
		}
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "source.png")
	dst := filepath.Join(dir, "extended.png")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	png.Encode(f, im)
	f.Close()
	extra, err := ExtendBackgroundTop(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if extra != 5 {
		t.Fatalf("extension=%d", extra)
	}
	out, err := Decode(dst)
	if err != nil {
		t.Fatal(err)
	}
	if out.Bounds().Dx() != 10 || out.Bounds().Dy() != 20 {
		t.Fatal("wrong canvas")
	}
	result := toNRGBA(out)
	for y := 0; y < 15; y++ {
		for x := 0; x < 10; x++ {
			if result.NRGBAAt(x, y+extra) != im.NRGBAAt(x, y) {
				t.Fatalf("original pixels changed at %d,%d", x, y)
			}
		}
	}
	if result.NRGBAAt(4, 4) != im.NRGBAAt(4, 0) || result.NRGBAAt(4, 0) != im.NRGBAAt(4, 4) {
		t.Fatal("top edge is not continuous")
	}
}

func TestWideStripCannotAllocateHugeBackground(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "wide.png")
	dst := filepath.Join(dir, "out.png")
	im := image.NewNRGBA(image.Rect(0, 0, 65536, 1))
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	png.Encode(f, im)
	f.Close()
	if _, err := ExtendBackgroundTop(src, dst); err == nil {
		t.Fatal("unbounded expanded image accepted")
	}
}
