package imgutil

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// ThumbDataURL 把图片缩成 maxSide 的 JPEG，返回 data URL，供界面直接显示。
func ThumbDataURL(path string, maxSide int) (string, error) {
	img, err := Decode(path)
	if err != nil {
		return "", err
	}
	// 带透明的图铺白底，避免界面里显示成黑块
	filled := FillWhite(img)
	scaled := downscale(filled, maxSide)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: 88}); err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// ReviewReferenceDataURL adds an alpha-only diagnostic when pale content extends beyond the solid body.
func ReviewReferenceDataURL(path string, maxSide int) (url string, alphaGuide bool, err error) {
	img, err := Decode(path)
	if err != nil {
		return "", false, err
	}
	n := toNRGBA(img)
	weak, hasWeak := alphaBoundsAtLeast(n, 8)
	strong, hasStrong := alphaBoundsAtLeast(n, 128)
	minimum := uint8(8)
	if !hasWeak {
		_, hasWeak = alphaBoundsAtLeast(n, 1)
		minimum = 1
	}
	pad := max(1, min(16, min(n.Bounds().Dx(), n.Bounds().Dy())/100))
	if !hasWeak || (hasStrong && weak.In(strong.Inset(-pad))) {
		url, err := ThumbDataURL(path, maxSide)
		return url, false, err
	}
	if maxSide < 2 {
		return "", false, fmt.Errorf("alpha参考图最大边长至少为2")
	}
	const header, gap = 20, 4
	b := n.Bounds()
	artLimit := min(max(1, maxSide-header), max(1, (maxSide-gap)/2)*max(b.Dx(), b.Dy())/b.Dx())
	art := downscale(FillWhite(img), max(1, artLimit))
	aw, ah := art.Bounds().Dx(), art.Bounds().Dy()
	panelWidth := max(84, aw)
	left := (panelWidth - aw) / 2
	canvas := image.NewRGBA(image.Rect(0, 0, 2*panelWidth+gap, ah+header))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(left, header, left+aw, header+ah), art, art.Bounds().Min, draw.Src)
	label := font.Drawer{Dst: canvas, Src: image.NewUniform(color.Black), Face: basicfont.Face7x13, Dot: fixed.P(4, 14)}
	label.DrawString("Art")
	label.Dot = fixed.P(panelWidth+gap+4, 14)
	label.DrawString("Alpha guide")
	out := downscale(canvas, maxSide).(*image.RGBA)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if n.NRGBAAt(x, y).A < minimum {
				continue
			}
			// Project every occupied source pixel after resizing so narrow contours cannot be skipped.
			dx := (panelWidth + gap + left + (x-b.Min.X)*aw/b.Dx()) * out.Bounds().Dx() / canvas.Bounds().Dx()
			dy := (header + (y-b.Min.Y)*ah/b.Dy()) * out.Bounds().Dy() / canvas.Bounds().Dy()
			out.SetRGBA(dx, dy, color.RGBA{A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 88}); err != nil {
		return "", false, err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), true, nil
}
