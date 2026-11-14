package jobs

import (
	"encoding/json"
	"fmt"
	"image"
	"math"
	"os"
	"sort"
	"strings"

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
	if req.NoCrop && !meta.FrameVerified {
		return nil, ref, fmt.Errorf("渲染未完成最终画面不裁切校验")
	}
	if len(meta.Placements) == 0 {
		return nil, ref, fmt.Errorf("渲染未返回实际摆放参数")
	}
	if req.NoCrop {
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
		if cfg.Width != cell || cfg.Height%cell != 0 || len(meta.Placements) != cfg.Height/cell {
			return nil, ref, fmt.Errorf("可见边界回执没有覆盖全部导出角度")
		}
		for _, p := range meta.Placements {
			if p.Cell < 0 || p.Cell >= cfg.Height/cell {
				return nil, ref, fmt.Errorf("无效导出角度")
			}
		}
	}
	sort.Slice(meta.Placements, func(i, j int) bool { return meta.Placements[i].Cell < meta.Placements[j].Cell })
	seen := map[int]bool{}
	for _, p := range meta.Placements {
		if p.Cell < 0 || seen[p.Cell] || p.Scale <= 0 || math.IsNaN(p.Scale) || math.IsInf(p.Scale, 0) || math.IsNaN(p.OffsetX) || math.IsInf(p.OffsetX, 0) || math.IsNaN(p.OffsetY) || math.IsInf(p.OffsetY, 0) {
			return nil, ref, fmt.Errorf("无效的实际摆放参数")
		}
		seen[p.Cell] = true
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
		if p.Cell != q.Cell || math.Abs(p.Scale-q.Scale) > 1e-6 || math.Abs(p.OffsetX-q.OffsetX) > 1e-6 || math.Abs(p.OffsetY-q.OffsetY) > 1e-6 {
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
		}
		parts = append(parts, fmt.Sprintf("%02d:x%.3f偏移(%+.3f,%+.3f)%s", p.Cell+1, p.Scale, p.OffsetX, p.OffsetY, suffix))
	}
	return fmt.Sprintf("本轮请求：x%.4f偏移(%+.4f,%+.4f)。", req.Scales[index], req.OffsetX, req.OffsetY) + "各角实际摆放：" + strings.Join(parts, "；") + "。标记截断边未贴边的角度尚不合格，无法同时满足时须报告冲突。增量以03号（不足三角时首角）实际参数为参考；请求位移若未达到，表示非透明内容或贴边已限制该方向，再移同样距离不会生效，应联合缩小释放空间。浅色残留也属于受保护内容，不能只按肉眼空白估计余量。边界合法性由渲染器执行，不得以裁内容满足锚点。"
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
	return "约束冲突：角度" + strings.Join(cells, "、") + "在当前安全边界内无法同时保全内容与双侧贴边；停止模型微调"
}
