package imgutil

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeBottomContentUsesOriginalCanvas(t *testing.T) {
	for _, tc := range []struct {
		name string
		box  image.Rectangle
		want bool
	}{
		{"floating logo", image.Rect(20, 30, 80, 70), false},
		{"bottom ground", image.Rect(10, 50, 90, 100), true},
		{"transparent last row", image.Rect(10, 50, 90, 99), false},
		{"full background", image.Rect(0, 0, 100, 100), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			im := image.NewNRGBA(image.Rect(0, 0, 100, 100))
			draw.Draw(im, tc.box, image.NewUniform(color.NRGBA{40, 80, 120, 255}), image.Point{}, draw.Src)
			im.SetNRGBA(99, 99, color.NRGBA{255, 0, 0, 1})
			dir := t.TempDir()
			src := filepath.Join(dir, "source.png")
			f, err := os.Create(src)
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, im); err != nil {
				t.Fatal(err)
			}
			f.Close()
			res, err := Normalize(src, filepath.Join(dir, "normalized.png"), true)
			if err != nil {
				t.Fatal(err)
			}
			if res.BottomContent != tc.want {
				t.Fatalf("BottomContent=%v want=%t", res.BottomContent, tc.want)
			}
		})
	}
}
