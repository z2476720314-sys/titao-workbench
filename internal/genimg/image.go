package genimg

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // 注册 PNG 解码器（生图接口常见返回格式）
	"os"

	xdraw "golang.org/x/image/draw"

	"titao/internal/imgutil"
)

// decodeBytes 解码内存里的图片字节。支持 png / jpeg / gif。
func decodeBytes(raw []byte) (image.Image, error) {
	img, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("解码图片（识别格式 %q）: %w", format, err)
	}
	return img, nil
}

// scaleDown 等比缩放到最长边不超过 maxSide。已足够小时原样返回。
//
// 用 CatmullRom 而不是最近邻：送视觉模型的图缩小后仍要能看清图案细节。
func scaleDown(src image.Image, maxSide int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if maxSide <= 0 || (w <= maxSide && h <= maxSide) {
		return src
	}
	long := w
	if h > long {
		long = h
	}
	scale := float64(maxSide) / float64(long)
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	return dst
}

// ToJPEG 把任意图片字节铺白底、缩放并编码为 JPEG。
//
// 铺白底是必须的：带透明通道的 PNG 直接编码成 JPEG 会让透明区域变黑，
// 视觉模型会把黑块当成手机壳上的图案。
func ToJPEG(raw []byte, maxSide, quality int) ([]byte, error) {
	img, err := decodeBytes(raw)
	if err != nil {
		return nil, err
	}
	if quality < 1 || quality > 100 {
		quality = 92
	}
	filled := imgutil.FillWhite(img)
	scaled := scaleDown(filled, maxSide)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("编码 JPEG: %w", err)
	}
	return buf.Bytes(), nil
}

// FileJPEG 读取图片文件并转成 JPEG 字节。
func FileJPEG(path string, maxSide, quality int) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ToJPEG(raw, maxSide, quality)
}

// SaveJPEG 把图片字节转成 JPEG 写到 dst。
//
// 接口返回的通常是 PNG，但上架要 JPG，所以统一转码；同时在内存里完成
// 尺寸校验，避免写出一张 0 字节的坏图。
func SaveJPEG(raw []byte, dst string, quality int) (int, int, error) {
	img, err := decodeBytes(raw)
	if err != nil {
		return 0, 0, err
	}
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return 0, 0, fmt.Errorf("生成图尺寸为 0")
	}
	if quality < 1 || quality > 100 {
		quality = 95
	}
	f, err := os.Create(dst)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	if err := jpeg.Encode(f, imgutil.FillWhite(img), &jpeg.Options{Quality: quality}); err != nil {
		return 0, 0, fmt.Errorf("写入 %s: %w", dst, err)
	}
	return b.Dx(), b.Dy(), nil
}

// DetectMIME 按魔数判断图片类型，用于日志与落盘决策。
func DetectMIME(raw []byte) string {
	switch {
	case len(raw) >= 8 && bytes.Equal(raw[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png"
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		return "image/jpeg"
	case len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}
