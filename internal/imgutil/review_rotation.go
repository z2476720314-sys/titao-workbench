package imgutil

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
)

// ReviewUprightJPEG changes only the review copy; product photos keep their pose.
func ReviewUprightJPEG(data []byte, turns int) ([]byte, error) {
	if turns < 0 || turns > 3 {
		return nil, fmt.Errorf("invalid review rotation")
	}
	if turns == 0 {
		return data, nil
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 || uint64(config.Width) > uint64(64<<20)/uint64(config.Height) {
		return nil, fmt.Errorf("review image is too large")
	}
	im, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	rotated := rotateReviewQuarter(im, turns)
	var output bytes.Buffer
	if err := jpeg.Encode(&output, rotated, &jpeg.Options{Quality: 95}); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func rotateReviewQuarter(im image.Image, turns int) *image.NRGBA {
	b := im.Bounds()
	w, h := b.Dx(), b.Dy()
	ow, oh := w, h
	if turns%2 == 1 {
		ow, oh = h, w
	}
	out := image.NewNRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := x, y
			switch turns {
			case 1:
				dx, dy = h-1-y, x
			case 2:
				dx, dy = w-1-x, h-1-y
			case 3:
				dx, dy = y, w-1-x
			}
			out.Set(dx, dy, im.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}
