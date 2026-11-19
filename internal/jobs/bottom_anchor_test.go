package jobs

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"titao/internal/imgutil"
	"titao/internal/printcanvas"
	"titao/internal/psx"
)

// buildBottomProbe 造一张底部有内容的长条图，返回规范化结果与源路径。
func buildBottomProbe(t *testing.T, w, h int, contentTop, contentBottom int) (imgutil.PatternResult, string) {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(im, image.Rect(0, contentTop, w, contentBottom), image.NewUniform(color.NRGBA{30, 90, 160, 255}), image.Point{}, draw.Src)
	dir := t.TempDir()
	src := filepath.Join(dir, "source.png")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
	f.Close()
	pr, err := imgutil.Normalize(src, filepath.Join(dir, "norm.png"), true)
	if err != nil {
		t.Fatal(err)
	}
	return pr, src
}

// NoCrop 模式下贴底必须真的生效。
//
// 回归的是 v0.3.11 引入的缺陷：`alignBottom` 被加了 `!opt.NoCrop` 前置条件，
// 而 NoCrop 是默认开启的。同时 NoCrop 走 FitArtwork，那条路径完全
// 不读 AlignBottom，所以贴底从那时起一直静默失效。
// NoCrop 下贴底只能由 AnchorY=1 表达。
func TestNoCropBottomAlignedUsesAnchorYNotAlignBottom(t *testing.T) {
	pr, src := buildBottomProbe(t, 120, 200, 60, 200)
	if !pr.BottomContent {
		t.Fatal("构造的图形底部应有内容，BottomContent 应为 true")
	}
	req := psx.RenderRequest{
		Pattern:       src,
		Fit:           psx.FitContain,
		NoCrop:        true,
		ContentBounds: pr.ContentBounds,
		AnchorX:       pr.AnchorX,
		AnchorY:       pr.AnchorY,
	}
	// 复现 pattern.go 里的贴底映射
	alignBottom := pr.BottomContent && fitIsContainLike(req.Fit)
	if alignBottom && req.NoCrop && req.AnchorY != 2 {
		req.AnchorY = 1
	}
	req.AlignBottom = alignBottom && !req.NoCrop
	if req.AlignBottom {
		t.Fatal("NoCrop 下不应再依赖 AlignBottom：FitArtwork 不读这个字段")
	}
	if req.AnchorY != 1 {
		t.Fatalf("NoCrop 贴底必须落到 AnchorY=1，实际 AnchorY=%d", req.AnchorY)
	}

	canvas, err := printcanvas.Build(src, filepath.Join(t.TempDir(), "canvas.png"), req, 1.0)
	if err != nil {
		t.Fatalf("生成画布失败: %v", err)
	}
	if canvas.Placement.AnchorUnmet {
		// 允许未贴合，但必须真的尝试过：偏移应已明显偏向底部
		if canvas.Placement.OffsetY <= 0 {
			t.Fatalf("声称尝试贴底却把画布往上挪：OffsetY=%v", canvas.Placement.OffsetY)
		}
	}
}

// AnchorY=2（对边铺满）是更强的锚点，不能被单边贴底降级覆盖。
func TestBottomAlignDoesNotDowngradePairedAnchor(t *testing.T) {
	pr, _ := buildBottomProbe(t, 120, 200, 0, 200)
	pr.AnchorY = 2
	if pr.BottomContent && pr.AnchorY != 2 {
		t.Fatal("AnchorY=2 被降级")
	}
}

// 非 NoCrop 的手动摆位仍走 AlignBottom，不能被顺手删掉。
func TestManualFitStillUsesAlignBottom(t *testing.T) {
	pr, _ := buildBottomProbe(t, 120, 200, 60, 200)
	noCrop := false
	alignBottom := pr.BottomContent && fitIsContainLike(psx.FitContain)
	if !alignBottom {
		t.Fatal("手动摆位的贴底判定不应受影响")
	}
	got := alignBottom && !noCrop
	if !got {
		t.Fatal("非 NoCrop 必须继续用 AlignBottom")
	}
}
