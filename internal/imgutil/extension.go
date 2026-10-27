package imgutil

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
)

// ExtendBackgroundTop adds reflected edge rows without changing any existing source pixel.
// shortcut: current phone templates use 2:1 print canvases; inspect inner sizes for other print formats.
func ExtendBackgroundTop(src, dst string) (int, error) {
	im, err := Decode(src)
	if err != nil {
		return 0, err
	}
	a := toNRGBA(im)
	b := a.Bounds()
	extra := 2*b.Dx() - b.Dy()
	if extra <= 0 {
		return 0, nil
	}
	const maxExpandedPixels = 64 << 20
	if uint64(b.Dx()) > uint64(maxExpandedPixels)/uint64(b.Dx())/2 {
		return 0, fmt.Errorf("顶部续铺超过64百万像素上限，未修改素材")
	}
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()+extra))
	rowBytes := b.Dx() * 4
	for y := 0; y < b.Dy(); y++ {
		copy(out.Pix[(y+extra)*out.Stride:(y+extra)*out.Stride+rowBytes], a.Pix[(b.Min.Y+y)*a.Stride+b.Min.X*4:(b.Min.Y+y)*a.Stride+b.Min.X*4+rowBytes])
	}
	for y := 0; y < extra; y++ {
		n := (extra - 1 - y) % (2 * b.Dy())
		if n >= b.Dy() {
			n = 2*b.Dy() - 1 - n
		}
		copy(out.Pix[y*out.Stride:y*out.Stride+rowBytes], a.Pix[(b.Min.Y+n)*a.Stride+b.Min.X*4:(b.Min.Y+n)*a.Stride+b.Min.X*4+rowBytes])
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return 0, err
	}
	f, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	err = png.Encode(f, out)
	closeErr := f.Close()
	if err != nil {
		return 0, err
	}
	if closeErr != nil {
		return 0, closeErr
	}
	return extra, nil
}
