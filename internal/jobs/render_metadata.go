package jobs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"sort"
	"strings"

	"titao/internal/printcanvas"
	"titao/internal/psx"
)

func renderedPlacement(req psx.RenderRequest, index int) (*psx.RenderMetadata, psx.AppliedPlacement, error) {
	ref := psx.AppliedPlacement{Scale: req.Scales[index], OffsetX: req.OffsetX, OffsetY: req.OffsetY}
	b, err := os.ReadFile(req.MetadataPath(index))
	if os.IsNotExist(err) && !req.NoCrop {
		return nil, ref, nil
	}
	if err != nil {
		return nil, ref, fmt.Errorf("无法确认实际可见边界: %w", err)
	}
	var meta psx.RenderMetadata
	if err := json.Unmarshal(b, &meta); err != nil {
		return nil, ref, fmt.Errorf("无效的渲染位置回执")
	}
	if req.NoCrop && !meta.CanvasVerified {
		return nil, ref, fmt.Errorf("渲染未完成印刷画布不裁切校验")
	}
	canvasPath := req.PrintCanvasPath(index)
	if req.Canvas != nil {
		canvasPath = req.Canvas.Path
		if meta.CanvasSHA256 != req.Canvas.SHA256 {
			return nil, ref, fmt.Errorf("模板未使用冻结的公共印刷画布")
		}
	}
	canvasData, err := os.ReadFile(canvasPath)
	if err != nil {
		return nil, ref, fmt.Errorf("印刷画布证据缺失: %w", err)
	}
	canvasHash := sha256.Sum256(canvasData)
	if meta.CanvasSHA256 != hex.EncodeToString(canvasHash[:]) || meta.CanvasWidth != printcanvas.Width || meta.CanvasHeight != printcanvas.Height {
		return nil, ref, fmt.Errorf("印刷画布证据不一致")
	}
	canvasImage, err := png.Decode(bytes.NewReader(canvasData))
	if err != nil || canvasImage.Bounds().Dx() != printcanvas.Width || canvasImage.Bounds().Dy() != printcanvas.Height {
		return nil, ref, fmt.Errorf("印刷画布PNG内容或尺寸无效")
	}
	if len(meta.Placements) == 0 {
		return nil, ref, fmt.Errorf("渲染未返回实际摆放参数")
	}
	{
		f, err := os.Open(req.FlatPath(index))
		if err != nil {
			return nil, ref, err
		}
		cfg, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil {
			return nil, ref, fmt.Errorf("无法检查拼合图尺寸")
		}
		cell := req.CellSize
		if cell <= 0 {
			cell = cfg.Width
		}
		if cfg.Width != cell || cfg.Height%cell != 0 || len(meta.Placements)+len(meta.StaticCells) != cfg.Height/cell {
			return nil, ref, fmt.Errorf("可见边界回执没有覆盖全部导出角度")
		}
		for _, p := range meta.Placements {
			if p.Cell < 0 || p.Cell >= cfg.Height/cell {
				return nil, ref, fmt.Errorf("无效导出角度")
			}
		}
		seenStatic := map[int]bool{}
		for _, static := range meta.StaticCells {
			if static < 0 || static >= cfg.Height/cell || seenStatic[static] {
				return nil, ref, fmt.Errorf("无效静态照片格")
			}
			seenStatic[static] = true
			for _, p := range meta.Placements {
				if p.Cell == static {
					return nil, ref, fmt.Errorf("静态照片格与投影格重复")
				}
			}
		}
	}
	sort.Slice(meta.Placements, func(i, j int) bool { return meta.Placements[i].Cell < meta.Placements[j].Cell })
	seen := map[int]bool{}
	if meta.ReviewQuarterTurns != nil && len(meta.ReviewQuarterTurns) != len(meta.Placements) {
		return nil, ref, fmt.Errorf("评审方向未覆盖全部印刷照片")
	}
	for _, p := range meta.Placements {
		if p.Cell < 0 || seen[p.Cell] || p.Scale <= 0 || math.IsNaN(p.Scale) || math.IsInf(p.Scale, 0) || math.IsNaN(p.OffsetX) || math.IsInf(p.OffsetX, 0) || math.IsNaN(p.OffsetY) || math.IsInf(p.OffsetY, 0) || !p.ValidStretch() {
			return nil, ref, fmt.Errorf("无效的实际摆放参数")
		}
		seen[p.Cell] = true
		if meta.ReviewQuarterTurns != nil {
			turns, ok := meta.ReviewQuarterTurns[p.Cell]
			if !ok || turns < 0 || turns > 3 {
				return nil, ref, fmt.Errorf("无效照片评审方向")
			}
		}
		{
			first := meta.Placements[0]
			if math.Abs(p.Scale-first.Scale) > 1e-9 || math.Abs(p.OffsetX-first.OffsetX) > 1e-9 || math.Abs(p.OffsetY-first.OffsetY) > 1e-9 || math.Abs(p.VerticalStretch()-first.VerticalStretch()) > 1e-9 {
				return nil, ref, fmt.Errorf("同组各角度图案位置不一致，拒绝作为同一手机壳导出")
			}
		}
	}
	ref = meta.Placements[0]
	for _, p := range meta.Placements {
		if p.Cell == 2 {
			ref = p
			break
		}
	}
	return &meta, ref, nil
}

func sameRenderedPlacement(a, b *psx.RenderMetadata) bool {
	if a == nil || b == nil || len(a.Placements) != len(b.Placements) {
		return false
	}
	for i, p := range a.Placements {
		q := b.Placements[i]
		if p.Cell != q.Cell || math.Abs(p.Scale-q.Scale) > 1e-6 || math.Abs(p.OffsetX-q.OffsetX) > 1e-6 || math.Abs(p.OffsetY-q.OffsetY) > 1e-6 || math.Abs(p.VerticalStretch()-q.VerticalStretch()) > 1e-6 {
			return false
		}
	}
	return true
}

func placementSummary(meta *psx.RenderMetadata, req psx.RenderRequest, index int) string {
	if meta == nil {
		return ""
	}
	parts := make([]string, 0, len(meta.Placements))
	for _, p := range meta.Placements {
		suffix := ""
		if p.AnchorUnmet {
			suffix = "[截断边未贴边]"
		} else if req.AnchorX != 0 || req.AnchorY != 0 {
			suffix = "[截断边贴合已校验]"
		}
		parts = append(parts, fmt.Sprintf("%02d:x%.3f偏移(%+.3f,%+.3f)%s", p.Cell+1, p.Scale, p.OffsetX, p.OffsetY, suffix))
	}
	return fmt.Sprintf("本轮请求：x%.4f偏移(%+.4f,%+.4f)。", req.Scales[index], req.OffsetX, req.OffsetY) + "各角实际摆放：" + strings.Join(parts, "；") + "。截断边贴合已校验表示程序按实际壳面及像素容差确认锚点满足，不能把照片中的透明物理边框、曲面透视或原图自然轮廓当作未贴边而要求放大。只有标记截断边未贴边的角度尚未满足锚点，无法同时满足时须报告冲突；镜头对主字主脸的遮挡仍须独立检查。增量以03号（不足三角时首角）实际参数为参考；请求位移若未达到，表示非透明内容或贴边已限制该方向，再移同样距离不会生效，应联合缩小释放空间。浅色残留也属于受保护内容，不能只按肉眼空白估计余量。边界合法性由渲染器执行，不得以裁内容满足锚点。"
}

func anchorsMet(meta *psx.RenderMetadata) bool {
	if meta != nil {
		for _, p := range meta.Placements {
			if p.AnchorUnmet {
				return false
			}
		}
	}
	return true
}

func anchorConflict(meta *psx.RenderMetadata) string {
	var cells []string
	if meta != nil {
		for _, p := range meta.Placements {
			if p.AnchorImpossible {
				cells = append(cells, fmt.Sprintf("%02d", p.Cell+1))
			}
		}
	}
	if len(cells) == 0 {
		return ""
	}
	return "约束冲突：当前校准下角度" + strings.Join(cells, "、") + "无法共用同一印刷位置并同时保全内容与双侧贴边；停止模型微调"
}
