package imgutil

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// makePattern 造一张 40x60 的 RGBA，中间 20x20 实心红块，其余全透明。
func makePattern(t *testing.T, path string) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 40, 60))
	for y := 20; y < 40; y++ {
		for x := 10; x < 30; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// TestNormalizeTrimsAlpha 守住「裁掉透明留白后尺寸 = alpha bbox」。
func TestNormalizeTrimsAlpha(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.png")
	makePattern(t, src)
	dst := filepath.Join(dir, "out.png")

	r, err := Normalize(src, dst, true)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if r.Width != 20 || r.Height != 20 {
		t.Errorf("裁后尺寸 = %dx%d，期望 20x20", r.Width, r.Height)
	}
	if !r.Trimmed {
		t.Error("Trimmed 应为 true")
	}
	if r.OrigWidth != 40 || r.OrigHeight != 60 {
		t.Errorf("原尺寸 = %dx%d，期望 40x60", r.OrigWidth, r.OrigHeight)
	}
}

// TestNormalizeKeepSize 守住 --no-trim 时保留原尺寸。
func TestNormalizeKeepSize(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.png")
	makePattern(t, src)
	dst := filepath.Join(dir, "out.png")

	r, err := Normalize(src, dst, false)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if r.Width != 40 || r.Height != 60 {
		t.Errorf("尺寸 = %dx%d，期望 40x60", r.Width, r.Height)
	}
	if r.Trimmed {
		t.Error("Trimmed 应为 false")
	}
}

// TestNormalizeStripsDPI 守住「300dpi 的 PNG 置入 PS 后按物理尺寸缩小 4 倍」这个坑：
// Normalize 始终经 Go 重存，产物不能再带 dpi 元数据。
func TestNormalizeStripsDPI(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.png")
	makePattern(t, src)
	dst := filepath.Join(dir, "out.png")

	if _, err := Normalize(src, dst, false); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	img, err := Decode(dst)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 40 || b.Dy() != 60 {
		t.Errorf("重存后尺寸 = %dx%d，像素被改变了", b.Dx(), b.Dy())
	}
	// Go 的 png 编码器不写 pHYs 块；用原始字节确认没有 "pHYs"
	raw, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+4 <= len(raw); i++ {
		if string(raw[i:i+4]) == "pHYs" {
			t.Fatal("产物仍带 pHYs(dpi) 块，置入 PS 会被按物理尺寸缩放")
		}
	}
}

// TestSplitCells 守住按格切分、命名与张数。
func TestSplitCells(t *testing.T) {
	dir := t.TempDir()
	// 造 3 格宽的拼合图，每格 8x8，格与格颜色不同
	const cell, rows = 8, 3
	img := image.NewNRGBA(image.Rect(0, 0, cell, cell*rows))
	for r := 0; r < rows; r++ {
		for y := 0; y < cell; y++ {
			for x := 0; x < cell; x++ {
				img.SetNRGBA(x, r*cell+y, color.NRGBA{R: uint8(40 * (r + 1)), A: 255})
			}
		}
	}
	src := filepath.Join(dir, "flat.png")
	f, _ := os.Create(src)
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()

	outDir := filepath.Join(dir, "out")
	files, err := SplitCells(src, outDir, "图案A", "模板B", cell, 90)
	if err != nil {
		t.Fatalf("SplitCells: %v", err)
	}
	if len(files) != rows {
		t.Fatalf("切出 %d 张，期望 %d 张", len(files), rows)
	}
	for i, p := range files {
		want := filepath.Join(outDir, "图案A_模板B_0"+string(rune('1'+i))+".jpg")
		if p != want {
			t.Errorf("第 %d 张路径 = %s，期望 %s", i+1, p, want)
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("文件不存在: %s", p)
		}
	}
}

// TestSplitCellsTooSmall 守住拼合图小于一格时的明确报错，而不是产出 0 张静默成功。
func TestSplitCellsTooSmall(t *testing.T) {
	dir := t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	src := filepath.Join(dir, "tiny.png")
	f, _ := os.Create(src)
	png.Encode(f, img)
	f.Close()

	if _, err := SplitCells(src, filepath.Join(dir, "out"), "a", "b", 800, 90); err == nil {
		t.Fatal("期望报错，实际返回 nil")
	}
}

// TestFirstCellJPEGCropsFirstCell 守住只取第一格（送模型的是第一格而不是整条长图）。
func TestFirstCellJPEGCropsFirstCell(t *testing.T) {
	dir := t.TempDir()
	const cell = 40
	img := image.NewNRGBA(image.Rect(0, 0, cell, cell*3))
	// 第一格画满黑色，后面两格全白
	for y := 0; y < cell; y++ {
		for x := 0; x < cell; x++ {
			img.SetNRGBA(x, y, color.NRGBA{A: 255})
			img.SetNRGBA(x, cell+y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
			img.SetNRGBA(x, 2*cell+y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	src := filepath.Join(dir, "flat.png")
	f, _ := os.Create(src)
	png.Encode(f, img)
	f.Close()

	buf, err := FirstCellJPEG(src, cell, 640, 90)
	if err != nil {
		t.Fatalf("FirstCellJPEG: %v", err)
	}
	if len(buf) == 0 {
		t.Fatal("返回空 JPEG")
	}
	dec, _, err := image.Decode(bytesReader(buf))
	if err != nil {
		t.Fatalf("解码产物: %v", err)
	}
	// 第一格是黑块 → 产物应当整体偏暗；若误取了后面的白格会接近全白
	b := dec.Bounds()
	var sum uint32
	var n uint32
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, _, _, _ := dec.At(x, y).RGBA()
			sum += r >> 8
			n++
		}
	}
	if n == 0 {
		t.Fatal("产物为空图")
	}
	if avg := sum / n; avg > 128 {
		t.Errorf("产物平均亮度 %d 偏白，疑似取错了格子", avg)
	}
}
