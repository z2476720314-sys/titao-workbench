// Package imgutil 负责图案规范化与成品切格。
//
// 关键点：Go 标准库写 PNG 时不携带 dpi 元数据，正好解决了一个实测陷阱——
// 946px@300dpi 的 PNG 置入 72dpi 的 PS 文档时会按物理尺寸换算成 227px，
// 图案莫名缩小 4 倍。所以图案一律经本包重存一遍再交给 Photoshop。
package imgutil

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"

	_ "golang.org/x/image/tiff" // 注册 TIFF 解码器
)

// PatternResult 是图案规范化的结果。
type PatternResult struct {
	// Path 是可直接交给 Photoshop 的图案路径。
	Path string
	// Width / Height 是规范化后的像素尺寸。
	Width, Height int
	// Trimmed 表示是否裁掉了透明留白。
	Trimmed bool
	// OrigBBox 是裁掉的透明区域（原图坐标系），未裁时为 nil。
	OrigBBox *Rect
	// OrigWidth / OrigHeight 是原图尺寸。
	OrigWidth, OrigHeight int
	// BottomContent is measured before trimming so floating logos stay centered.
	BottomContent  bool
	FullBackground bool
}

// Rect 是矩形区域。
type Rect struct {
	X0, Y0, X1, Y1 int
}

func (r Rect) String() string {
	return fmt.Sprintf("(%d,%d)-(%d,%d)", r.X0, r.Y0, r.X1, r.Y1)
}

// ErrUnsupportedFormat 表示图案格式不被支持。
var ErrUnsupportedFormat = errors.New("不支持的图案格式")

// Decode 读取 png/jpg/gif/tif 图片。
func Decode(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, format, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("解码 %s（识别格式 %q）: %w", filepath.Base(path), format, err)
	}
	return img, nil
}

// Normalize 把图案转成 RGBA PNG，可选裁掉透明留白，并强制去掉 dpi 元数据。
//
// 去掉 dpi 是必须的：PS 文档是 72dpi，若 PNG 带 300dpi，置入时会按物理尺寸
// 换算，图案会莫名缩小 4 倍。
func Normalize(src, dst string, trim bool) (PatternResult, error) {
	img, err := Decode(src)
	if err != nil {
		return PatternResult{}, err
	}

	b := img.Bounds()
	res := PatternResult{
		Path:       dst,
		OrigWidth:  b.Dx(),
		OrigHeight: b.Dy(),
	}

	// 统一转成 NRGBA（带 alpha），便于裁边和后续处理
	rgba := toNRGBA(img)
	solidPixels := 0
	for y := rgba.Bounds().Min.Y; y < rgba.Bounds().Max.Y; y++ {
		for x := rgba.Bounds().Min.X; x < rgba.Bounds().Max.X; x++ {
			if rgba.NRGBAAt(x, y).A >= 250 {
				solidPixels++
			}
		}
	}
	// Allow only sparse antialiased edge pixels in an otherwise opaque background.
	totalPixels := rgba.Bounds().Dx() * rgba.Bounds().Dy()
	res.FullBackground = solidPixels >= totalPixels-totalPixels/200
	band := max(1, (rgba.Bounds().Dy()+99)/100)
	bottomPixels := 0
	for y := rgba.Bounds().Max.Y - band; y < rgba.Bounds().Max.Y; y++ {
		for x := rgba.Bounds().Min.X; x < rgba.Bounds().Max.X; x++ {
			if rgba.NRGBAAt(x, y).A >= 8 {
				bottomPixels++
			}
		}
	}
	res.BottomContent = bottomPixels >= max(1, (rgba.Bounds().Dx()*band+99)/100)

	if trim {
		if box, ok := alphaBounds(rgba); ok && box != b {
			rgba = cropNRGBA(rgba, box)
			res.Trimmed = true
			res.OrigBBox = &Rect{X0: box.Min.X, Y0: box.Min.Y, X1: box.Max.X, Y1: box.Max.Y}
		}
	}

	res.Width, res.Height = rgba.Bounds().Dx(), rgba.Bounds().Dy()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return res, err
	}
	f, err := os.Create(dst)
	if err != nil {
		return res, err
	}
	defer f.Close()
	// png.Encode 不写 dpi，正是我们要的
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(f, rgba); err != nil {
		return res, fmt.Errorf("写入规范化图案: %w", err)
	}
	return res, nil
}

// toNRGBA 把任意图片转成 *image.NRGBA。
func toNRGBA(img image.Image) *image.NRGBA {
	if n, ok := img.(*image.NRGBA); ok {
		return n
	}
	b := img.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, g, bl, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8(r >> 8)
			dst.Pix[i+1] = uint8(g >> 8)
			dst.Pix[i+2] = uint8(bl >> 8)
			dst.Pix[i+3] = uint8(a >> 8)
		}
	}
	return dst
}

// alphaBounds ignores distant near-invisible residue when locating the visible artwork.
func alphaBounds(img *image.NRGBA) (image.Rectangle, bool) {
	raw, ok := alphaBoundsAtLeast(img, 1)
	if !ok {
		return raw, false
	}
	visible, ok := alphaBoundsAtLeast(img, 8)
	if !ok || visible == raw {
		return raw, true
	}
	// Keep nearby antialiasing/glow; faint-only designs retain their original alpha bounds.
	pad := max(1, min(16, min(img.Bounds().Dx(), img.Bounds().Dy())/100))
	candidate := visible.Inset(-pad).Intersect(raw)
	total, outside := 0, 0
	for y := raw.Min.Y; y < raw.Max.Y; y++ {
		for x := raw.Min.X; x < raw.Max.X; x++ {
			if img.NRGBAAt(x, y).A != 0 {
				total++
				if !image.Pt(x, y).In(candidate) {
					outside++
				}
			}
		}
	}
	// Only discard sparse residue; a substantial faint composition is intentional content.
	if outside > max(1, total/100) {
		return raw, true
	}
	return candidate, true
}

func alphaBoundsAtLeast(img *image.NRGBA, minimum uint8) (image.Rectangle, bool) {
	b := img.Bounds()
	minX, minY := b.Max.X, b.Max.Y
	maxX, maxY := b.Min.X-1, b.Min.Y-1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := img.Pix[y*img.Stride:]
		for x := b.Min.X; x < b.Max.X; x++ {
			if row[x*4+3] < minimum {
				continue
			}
			if x < minX {
				minX = x
			}
			if x > maxX {
				maxX = x
			}
			if y < minY {
				minY = y
			}
			if y > maxY {
				maxY = y
			}
		}
	}
	if maxX < minX || maxY < minY {
		return image.Rectangle{}, false
	}
	return image.Rect(minX, minY, maxX+1, maxY+1), true
}

func cropNRGBA(src *image.NRGBA, r image.Rectangle) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := 0; y < r.Dy(); y++ {
		copy(dst.Pix[y*dst.Stride:y*dst.Stride+r.Dx()*4],
			src.Pix[(r.Min.Y+y)*src.Stride+r.Min.X*4:(r.Min.Y+y)*src.Stride+r.Max.X*4])
	}
	return dst
}

// SplitCells 把整幅拼合图按 cellSize 正方形切格并保存为 JPG。
//
// 返回生成的 JPG 路径。命名规则：<prefix>_<模板名>_NN.jpg。
func SplitCells(src, outDir, prefix, templateName string, cellSize, quality int) ([]string, error) {
	img, err := Decode(src)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	cols, rows := b.Dx()/cellSize, b.Dy()/cellSize
	if cols == 0 || rows == 0 {
		return nil, fmt.Errorf("拼合图 %dx%d 小于一格 %d", b.Dx(), b.Dy(), cellSize)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}

	made := make([]string, 0, rows)
	for r := 0; r < rows; r++ {
		cell := image.NewRGBA(image.Rect(0, 0, cellSize, cellSize))
		for y := 0; y < cellSize; y++ {
			for x := 0; x < cellSize; x++ {
				cell.Set(x, y, img.At(b.Min.X+x, b.Min.Y+r*cellSize+y))
			}
		}
		name := fmt.Sprintf("%s_%s_%02d.jpg", prefix, templateName, r+1)
		path := filepath.Join(outDir, name)
		f, err := os.Create(path)
		if err != nil {
			return made, err
		}
		err = jpeg.Encode(f, cell, &jpeg.Options{Quality: quality})
		f.Close()
		if err != nil {
			return made, fmt.Errorf("写入 %s: %w", name, err)
		}
		made = append(made, path)
	}
	return made, nil
}

// FirstCellJPEG 裁出整幅图的第一格（去白边后缩放到 maxSide），编码为 JPEG。
//
// 为什么要裁：800x4800 的整条图缩到 640 时每格只剩约 107px，视觉模型看不清，
// 判断会趋同。只送第一格能让模型看到真实细节。
func FirstCellJPEG(src string, cellSize, maxSide int, quality int) ([]byte, error) {
	img, err := Decode(src)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	w, h := cellSize, cellSize
	if b.Dx() < w {
		w = b.Dx()
	}
	if b.Dy() < h {
		h = b.Dy()
	}

	// 先取第一格，再找非白区域裁边
	cell := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			cell.Set(x, y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	box, ok := nonWhiteBounds(cell, 12)
	if ok {
		const pad = 12
		x0 := max(0, box.Min.X-pad)
		y0 := max(0, box.Min.Y-pad)
		x1 := min(w, box.Max.X+pad)
		y1 := min(h, box.Max.Y+pad)
		cropped := image.NewRGBA(image.Rect(0, 0, x1-x0, y1-y0))
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				cropped.Set(x-x0, y-y0, cell.At(x, y))
			}
		}
		cell = cropped
	}

	scaled := downscale(cell, maxSide)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, scaled, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// nonWhiteBounds 找出与纯白差异超过阈值的像素包围盒。
func nonWhiteBounds(img image.Image, threshold uint8) (image.Rectangle, bool) {
	b := img.Bounds()
	minX, minY := b.Max.X, b.Max.Y
	maxX, maxY := b.Min.X-1, b.Min.Y-1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			rr, gg, bb := uint8(r>>8), uint8(g>>8), uint8(bl>>8)
			if 255-rr <= threshold && 255-gg <= threshold && 255-bb <= threshold {
				continue
			}
			if x < minX {
				minX = x
			}
			if x > maxX {
				maxX = x
			}
			if y < minY {
				minY = y
			}
			if y > maxY {
				maxY = y
			}
		}
	}
	if maxX < minX || maxY < minY {
		return image.Rectangle{}, false
	}
	return image.Rect(minX, minY, maxX+1, maxY+1), true
}

// downscale 等比缩放到最长边不超过 maxSide。已足够小时原样返回。
func downscale(src image.Image, maxSide int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxSide && h <= maxSide {
		return src
	}
	scale := float64(maxSide) / float64(max(w, h))
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy := b.Min.Y + y*h/nh
		for x := 0; x < nw; x++ {
			sx := b.Min.X + x*w/nw
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

// FillWhite 把带 alpha 的图铺到白底上，便于视觉模型判断（避免透明区域显示成黑）。
func FillWhite(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Over)
	return dst
}
