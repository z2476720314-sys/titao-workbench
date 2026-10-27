package imgutil

import (
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrecheckViewsRetainTopAndBottomDetails(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 100, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 100; x++ {
			c := color.NRGBA{0, 255, 0, 255}
			if y < 100 {
				c = color.NRGBA{255, 0, 0, 255}
			}
			if y >= 200 {
				c = color.NRGBA{0, 0, 255, 255}
			}
			im.SetNRGBA(x, y, c)
		}
	}
	p := filepath.Join(t.TempDir(), "source.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	png.Encode(f, im)
	f.Close()
	views, err := PrecheckViews(p, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 7 {
		t.Fatalf("views=%d", len(views))
	}
	for i := 1; i < 3; i++ {
		b, err := base64.StdEncoding.DecodeString(strings.SplitN(views[i], ",", 2)[1])
		if err != nil {
			t.Fatal(err)
		}
		pic, _, err := image.Decode(strings.NewReader(string(b)))
		if err != nil {
			t.Fatal(err)
		}
		if pic.Bounds().Dx() != 100 || pic.Bounds().Dy() != 100 {
			t.Fatal("detail was shrunk with full canvas")
		}
		r, _, blue, _ := pic.At(50, 50).RGBA()
		if (i == 1 && r <= blue) || (i == 2 && blue <= r) {
			t.Fatal("wrong region used for detail")
		}
	}
}
