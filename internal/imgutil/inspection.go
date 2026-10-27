package imgutil

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/draw"
	"image/jpeg"
)

// PrecheckViews keeps a whole-image overview plus native-width end details for tall/wide inputs.
func PrecheckViews(path string, maxSide int) ([]string, error) {
	im, err := Decode(path)
	if err != nil {
		return nil, err
	}
	im = FillWhite(im)
	b := im.Bounds()
	regions := []image.Rectangle{b}
	if max(b.Dx(), b.Dy()) > maxSide && b.Dx() != b.Dy() {
		n := min(b.Dx(), b.Dy())
		regions = append(regions, image.Rect(b.Min.X, b.Min.Y, b.Min.X+n, b.Min.Y+n), image.Rect(b.Max.X-n, b.Max.Y-n, b.Max.X, b.Max.Y))
	}
	if min(b.Dx(), b.Dy()) >= 2 {
		mx, my := b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()/2
		regions = append(regions, image.Rect(b.Min.X, b.Min.Y, mx, my), image.Rect(mx, b.Min.Y, b.Max.X, my), image.Rect(b.Min.X, my, mx, b.Max.Y), image.Rect(mx, my, b.Max.X, b.Max.Y))
	}
	urls := make([]string, 0, len(regions))
	for _, r := range regions {
		crop := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
		draw.Draw(crop, crop.Bounds(), im, r.Min, draw.Src)
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, downscale(crop, maxSide), &jpeg.Options{Quality: 95}); err != nil {
			return nil, err
		}
		urls = append(urls, "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(buf.Bytes()))
	}
	return urls, nil
}
