package imgutil

import (
	"bytes"
	"fmt"
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
	if got.AnchorX != 2 || got.AnchorY != 0 {
		t.Fatalf("only edges adjacent to the original canvas need shell anchors: %+v", got)
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

func TestCutEdgeAnchorsTolerateSparsePixelsBeforeStraightEdges(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 945, 1456))
	draw.Draw(im, image.Rect(5, 5, 938, 1450), image.NewUniform(color.NRGBA{R: 180, A: 255}), image.Point{}, draw.Src)
	for _, p := range []image.Point{{3, 100}, {4, 101}, {943, 200}, {942, 201}, {100, 3}, {101, 4}, {200, 1455}, {201, 1454}} {
		im.SetNRGBA(p.X, p.Y, color.NRGBA{R: 180, A: 8})
	}
	before := append([]byte(nil), im.Pix...)
	x, y := cutEdgeAnchors(im)
	if x != 2 || y != 1 {
		t.Fatalf("near-edge alpha8 pixels hid sustained cut edges: %d,%d", x, y)
	}
	if !bytes.Equal(before, im.Pix) {
		t.Fatal("anchor detection changed artwork pixels")
	}
}

func TestCutEdgeAnchorsIgnoreCirclesAcrossSizesAndCanvasOffsets(t *testing.T) {
	for _, radius := range []int{20, 100, 500} {
		for _, offset := range []image.Point{{0, 0}, {700, 1000}} {
			for _, halfPixel := range []int{0, 1} {
				t.Run(fmt.Sprintf("r%d_offset%v_half%d", radius, offset, halfPixel), func(t *testing.T) {
					im := image.NewNRGBA(image.Rect(0, 0, 2*radius+5+offset.X, 2*radius+5+offset.Y))
					cx, cy := offset.X+radius+2, offset.Y+radius+2
					for y := cy - radius - 1; y <= cy+radius+1; y++ {
						for x := cx - radius - 1; x <= cx+radius+1; x++ {
							dx, dy := 2*(x-cx)-halfPixel, 2*(y-cy)-halfPixel
							if dx*dx+dy*dy <= 4*radius*radius {
								im.SetNRGBA(x, y, color.NRGBA{R: 180, A: 255})
							}
						}
					}
					if x, y := cutEdgeAnchors(im); x != 0 || y != 0 {
						t.Fatalf("complete circular contour acquired anchors: %d,%d", x, y)
					}
				})
			}
		}
	}
}

func TestCutEdgeAnchorsRequireOriginalCanvasAdjacency(t *testing.T) {
	for _, tc := range []struct {
		name string
		rect image.Rectangle
		x, y int
	}{
		{"internal-paper", image.Rect(40, 40, 160, 160), 0, 0},
		{"left-cut", image.Rect(0, 40, 120, 160), -1, 0},
		{"right-cut", image.Rect(80, 40, 200, 160), 1, 0},
		{"top-cut", image.Rect(40, 0, 160, 120), 0, -1},
		{"bottom-cut", image.Rect(40, 80, 160, 200), 0, 1},
		{"sides-and-bottom-cut", image.Rect(0, 40, 200, 200), 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			im := image.NewNRGBA(image.Rect(0, 0, 200, 200))
			draw.Draw(im, tc.rect, image.NewUniform(color.NRGBA{R: 180, A: 255}), image.Point{}, draw.Src)
			if x, y := cutEdgeAnchors(im); x != tc.x || y != tc.y {
				t.Fatalf("canvas adjacency ignored: got (%d,%d), want (%d,%d)", x, y, tc.x, tc.y)
			}
			dir := t.TempDir()
			src := filepath.Join(dir, "source.png")
			var encoded bytes.Buffer
			if err := png.Encode(&encoded, im); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(src, encoded.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			for _, trim := range []bool{false, true} {
				got, err := Normalize(src, filepath.Join(dir, fmt.Sprintf("normalized-%t.png", trim)), trim)
				if err != nil || got.AnchorX != tc.x || got.AnchorY != tc.y {
					t.Fatalf("trim=%t changed source cut semantics: %+v, %v", trim, got, err)
				}
			}
		})
	}
}

func TestNormalizeDoesNotInventCutAnchorsWhenTrimmingInternalPaper(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "internal-paper.png")
	im := image.NewNRGBA(image.Rect(0, 0, 200, 240))
	draw.Draw(im, image.Rect(40, 60, 160, 200), image.NewUniform(color.NRGBA{R: 180, A: 255}), image.Point{}, draw.Src)
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(f, im)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, trim := range []bool{false, true} {
		got, err := Normalize(src, filepath.Join(dir, fmt.Sprintf("normalized-%t.png", trim)), trim)
		if err != nil {
			t.Fatal(err)
		}
		if got.AnchorX != 0 || got.AnchorY != 0 || got.Trimmed != trim {
			t.Fatalf("transparent trim invented source-canvas cut edges: %+v", got)
		}
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
