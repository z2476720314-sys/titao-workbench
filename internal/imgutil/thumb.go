package imgutil

import (
	"bytes"
	"encoding/base64"
	"image/jpeg"
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
