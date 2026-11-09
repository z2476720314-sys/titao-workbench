package imgutil

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeReportsPaddingWithoutChangingPixels(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "source.png"), filepath.Join(dir, "normalized.png")
	im := image.NewNRGBA(image.Rect(0, 0, 100, 200))
	draw.Draw(im, image.Rect(0, 20, 100, 150), image.NewUniform(color.NRGBA{220, 80, 40, 150}), image.Point{}, draw.Src)
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
	f.Close()
	got, err := Normalize(src, dst, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Width != 100 || got.Height != 200 || got.Trimmed || got.ContentBounds != [4]float64{0, .1, 1, .75} {
		t.Fatalf("canvas or visible bounds changed: %+v", got)
	}
	if got.AnchorX != 2 || got.AnchorY != 1 {
		t.Fatalf("straight cut edges need shell anchors: %+v", got)
	}
	out, err := Decode(dst)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 200; y++ {
		for x := 0; x < 100; x++ {
			if color.NRGBAModel.Convert(out.At(x, y)) != im.At(x, y) {
				t.Fatalf("pixel changed at %d,%d", x, y)
			}
		}
	}
}

func TestCutEdgeAnchorsIgnoreNaturalContours(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 100, 200))
	for y := 80; y <= 120; y++ {
		for x := 30; x <= 70; x++ {
			if (x-50)*(x-50)+(y-100)*(y-100) <= 400 {
				im.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
			}
		}
	}
	x, y := cutEdgeAnchors(im)
	if x != 0 || y != 0 {
		t.Fatalf("natural circular outline acquired anchors: %d,%d", x, y)
	}
	im = image.NewNRGBA(image.Rect(0, 0, 100, 200))
	for y := 80; y <= 121; y++ {
		for x := 30; x <= 71; x++ {
			if (2*x-101)*(2*x-101)+(2*y-201)*(2*y-201) <= 1600 {
				im.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
			}
		}
	}
	x, y = cutEdgeAnchors(im)
	if x != 0 || y != 0 {
		t.Fatalf("half-pixel circular outline acquired anchors: %d,%d", x, y)
	}
}

func TestReviewCellsKeepsFirstMiddleLastAnglesReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flat.png")
	im := image.NewRGBA(image.Rect(0, 0, 80, 590)) // 最后30px不属于实际导出格子。
	for i := 0; i < 7; i++ {
		draw.Draw(im, image.Rect(0, i*80, 80, (i+1)*80), image.NewUniform(color.RGBA{uint8(20 + i*30), 60, 80, 255}), image.Point{}, draw.Src)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
	f.Close()
	views, err := ReviewCellsJPEG(path, 80, 80, 95)
	if err != nil || len(views) != 3 {
		t.Fatalf("views=%d err=%v", len(views), err)
	}
	for i, index := range []int{0, 3, 6} {
		view, err := jpeg.Decode(bytes.NewReader(views[i]))
		if err != nil {
			t.Fatal(err)
		}
		c := color.RGBAModel.Convert(view.At(40, 40)).(color.RGBA)
		want := 20 + index*30
		if view.Bounds().Dx() != 80 || view.Bounds().Dy() != 80 || int(c.R) < want-3 || int(c.R) > want+3 {
			t.Fatalf("angle %d lost resolution or pixels: %v %+v", index, view.Bounds(), c)
		}
	}
}
