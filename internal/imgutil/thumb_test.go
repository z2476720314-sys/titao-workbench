package imgutil

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeReferencePNG(t *testing.T, im image.Image) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reference.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(f, im)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func decodeReferenceURL(t *testing.T, url string) image.Image {
	t.Helper()
	_, encoded, ok := strings.Cut(url, ",")
	if !ok {
		t.Fatal("missing image data URL")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	im, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func darkReferencePixels(im image.Image, bounds image.Rectangle) int {
	count := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := im.At(x, y).RGBA()
			if max(r, g, b) < 128*257 {
				count++
			}
		}
	}
	return count
}

func TestReviewReferenceOrdinaryAndTransparentReuseExactThumbnail(t *testing.T) {
	for _, kind := range []string{"opaque", "soft-edge", "transparent"} {
		t.Run(kind, func(t *testing.T) {
			im := image.NewNRGBA(image.Rect(0, 0, 200, 200))
			if kind == "opaque" {
				draw.Draw(im, im.Bounds(), image.NewUniform(color.NRGBA{R: 120, A: 255}), image.Point{}, draw.Src)
			}
			if kind == "soft-edge" {
				draw.Draw(im, image.Rect(59, 59, 141, 141), image.NewUniform(color.NRGBA{R: 220, A: 32}), image.Point{}, draw.Src)
				draw.Draw(im, image.Rect(60, 60, 140, 140), image.NewUniform(color.NRGBA{R: 120, A: 255}), image.Point{}, draw.Src)
				im.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 1})
			}
			path := writeReferencePNG(t, im)
			for _, size := range []int{64, 512} {
				old, err := ThumbDataURL(path, size)
				if err != nil {
					t.Fatal(err)
				}
				got, guide, err := ReviewReferenceDataURL(path, size)
				if err != nil || guide || got != old {
					t.Fatalf("ordinary %s reference changed bytes or added a panel: guide=%t error=%v", kind, guide, err)
				}
				if kind == "transparent" {
					out := decodeReferenceURL(t, got)
					if darkReferencePixels(out, out.Bounds()) != 0 {
						t.Fatal("transparent input gained diagnostic content")
					}
				}
			}
		})
	}
}

func TestReviewReferenceMakesWhiteFaintOutlineVisibleWithoutChangingInput(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 256, 384))
	for x := 33; x < 221; x++ {
		im.SetNRGBA(x, 80, color.NRGBA{255, 255, 255, 16})
	}
	draw.Draw(im, image.Rect(96, 160, 160, 352), image.NewUniform(color.NRGBA{R: 180, A: 255}), image.Point{}, draw.Src)
	path := writeReferencePNG(t, im)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	url, guide, err := ReviewReferenceDataURL(path, 800)
	if err != nil || !guide {
		t.Fatalf("faint outline lacks alpha guide: %t %v", guide, err)
	}
	out := decodeReferenceURL(t, url)
	b := out.Bounds()
	if max(b.Dx(), b.Dy()) > 800 {
		t.Fatal("combined reference exceeded maxSide")
	}
	if darkReferencePixels(out, image.Rect(0, 0, b.Dx()/2, 20)) == 0 || darkReferencePixels(out, image.Rect(b.Dx()/2, 0, b.Max.X, 20)) == 0 {
		t.Fatal("art and alpha guide panels need visible labels")
	}
	upperLeft := image.Rect(0, b.Dy()/6, b.Dx()/2, b.Dy()/3)
	upperRight := image.Rect(b.Dx()/2, b.Dy()/6, b.Max.X, b.Dy()/3)
	if darkReferencePixels(out, upperLeft) != 0 || darkReferencePixels(out, upperRight) == 0 {
		t.Fatal("white art reference or high-contrast faint outline is incorrect")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("review reference generation modified the source")
	}
}

func TestReviewReferencePreservesThinOutlineDuringDownsampling(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 1024, 1024))
	im.SetNRGBA(17, 257, color.NRGBA{255, 255, 255, 8})
	draw.Draw(im, image.Rect(600, 700, 900, 950), image.NewUniform(color.NRGBA{R: 120, A: 255}), image.Point{}, draw.Src)
	path := writeReferencePNG(t, im)
	for _, size := range []int{64, 128} {
		url, guide, err := ReviewReferenceDataURL(path, size)
		if err != nil || !guide {
			t.Fatalf("thin alpha guide missing: %t %v", guide, err)
		}
		out := decodeReferenceURL(t, url)
		b := out.Bounds()
		if max(b.Dx(), b.Dy()) > size {
			t.Fatal("reference exceeded maxSide")
		}
		if darkReferencePixels(out, image.Rect(b.Dx()/2, b.Dy()/3, b.Max.X, 3*b.Dy()/5)) == 0 {
			t.Fatalf("single-pixel alpha line vanished at maxSide=%d", size)
		}
	}
}

func TestReviewReferenceShowsFaintOnlyNonzeroAlpha(t *testing.T) {
	for _, alpha := range []uint8{1, 7, 16, 127} {
		im := image.NewNRGBA(image.Rect(0, 0, 64, 128))
		draw.Draw(im, image.Rect(20, 20, 40, 100), image.NewUniform(color.NRGBA{255, 255, 255, alpha}), image.Point{}, draw.Src)
		url, guide, err := ReviewReferenceDataURL(writeReferencePNG(t, im), 256)
		if err != nil || !guide {
			t.Fatalf("faint-only alpha %d was hidden: guide=%t error=%v", alpha, guide, err)
		}
		out := decodeReferenceURL(t, url)
		b := out.Bounds()
		if darkReferencePixels(out, image.Rect(b.Dx()/2, b.Dy()/4, b.Max.X, 3*b.Dy()/4)) == 0 {
			t.Fatalf("alpha %d has no diagnostic silhouette", alpha)
		}
	}
}

func TestReviewReferenceReportsUnreadableInput(t *testing.T) {
	url, guide, err := ReviewReferenceDataURL(filepath.Join(t.TempDir(), "absent.png"), 512)
	if err == nil || url != "" || guide {
		t.Fatal("unreadable reference was presented as usable")
	}
}

func TestReviewReferenceLandscapeKeepsReadableLabels(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 1024, 128))
	im.SetNRGBA(17, 40, color.NRGBA{255, 255, 255, 8})
	draw.Draw(im, image.Rect(512, 80, 900, 110), image.NewUniform(color.NRGBA{255, 255, 255, 255}), image.Point{}, draw.Src)
	url, guide, err := ReviewReferenceDataURL(writeReferencePNG(t, im), 800)
	if err != nil || !guide {
		t.Fatalf("landscape guide unavailable: %t %v", guide, err)
	}
	out := decodeReferenceURL(t, url)
	first, last := 20, -1
	for y := 0; y < min(20, out.Bounds().Dy()); y++ {
		if darkReferencePixels(out, image.Rect(0, y, out.Bounds().Dx()/2, y+1)) > 0 {
			first, last = min(first, y), max(last, y)
		}
	}
	if last-first+1 < 8 {
		t.Fatal("landscape layout shrank the label below readable size")
	}
}
