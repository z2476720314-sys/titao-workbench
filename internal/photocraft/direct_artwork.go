package photocraft

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"titao/internal/imgutil"
	"titao/internal/printcanvas"
	"titao/internal/psx"
)

// The caller holds the render lock; each contents id is edited once, keeping parent projections.
func (s *Session) renderDirect(req psx.RenderRequest) (string, error) {
	if req.Canvas != nil {
		if err := req.Canvas.Validate(); err != nil {
			return "", err
		}
	}
	inputs := []string{req.TemplateCopy, req.Pattern}
	if req.Canvas != nil {
		inputs = append(inputs, req.Canvas.Path, req.Canvas.SourcePath())
	}
	localTemplate := filepath.Join(req.OutDir, "render-template"+filepath.Ext(req.TemplateCopy))
	if err := checkOutputPath(localTemplate, inputs, req.TemplateCopy); err != nil {
		return "", err
	}
	for i := range req.Scales {
		for _, p := range []string{req.FlatPath(i), req.MetadataPath(i), req.ArtworkPlanPath(i)} {
			if err := checkOutputPath(p, inputs, ""); err != nil {
				return "", err
			}
		}
	}
	if err := stageFile(req.TemplateCopy, localTemplate); err != nil {
		return "", err
	}
	c, close, err := s.connect(req.OutDir)
	if err != nil {
		return "", err
	}
	defer close()
	if err = c.call("doc_open", map[string]any{"path": localTemplate}, nil); err != nil {
		return "", err
	}
	var doc document
	if err = c.call("doc_inspect", map[string]any{}, &doc); err != nil {
		return "", err
	}
	objects, err := visibleObjects(doc.Layers, req.Group)
	if err != nil {
		return "", err
	}
	cells, static, err := projectionCells(req, doc, objects)
	if err != nil {
		return "", err
	}
	type surface struct{ layer, width, height int }
	var surfaces []surface
	sizes := map[int][2]int{}
	for _, l := range objects {
		if _, ok := sizes[l.SmartContents.ID]; ok {
			continue
		}
		if err = c.command("layer.smartObjects.editContents", map[string]any{"layer": l.ID}, nil); err != nil {
			return "", err
		}
		var inner document
		if err = c.call("doc_inspect", map[string]any{}, &inner); err != nil {
			return "", err
		}
		if !validProjection(inner.Width, inner.Height) {
			return "", fmt.Errorf("智能对象画布 %dx%d 没有已知映射", inner.Width, inner.Height)
		}
		surfaces = append(surfaces, surface{l.ID, inner.Width, inner.Height})
		sizes[l.SmartContents.ID] = [2]int{inner.Width, inner.Height}
		if err = c.call("doc_close", map[string]any{}, nil); err != nil {
			return "", err
		}
		if err = c.call("doc_select", map[string]any{"index": 0}, nil); err != nil {
			return "", err
		}
	}
	cellSize := req.CellSize
	if cellSize == 0 {
		cellSize = doc.Width
	}
	var turns map[int]int
	if req.DiagnosticAlpha {
		turns, err = s.reviewQuarterTurns(c, localTemplate, req.Group, req.OutDir, doc, objects, sizes, cellSize)
		if err != nil {
			return "", err
		}
	}
	var baselinePath string
	if len(static) > 0 {
		baselinePath = filepath.Join(req.OutDir, "template-baseline.png")
		if err = checkOutputPath(baselinePath, inputs, ""); err != nil {
			return "", err
		}
		if err = c.call("doc_save", map[string]any{"index": 0, "path": baselinePath}, nil); err != nil {
			return "", err
		}
	}
	export := func(path string) error {
		if err := checkOutputPath(path, inputs, ""); err != nil {
			return err
		}
		if err := c.call("doc_save", map[string]any{"index": 0, "path": path}, nil); err != nil {
			return err
		}
		im, err := imgutil.Decode(path)
		if err != nil {
			return err
		}
		if im.Bounds().Dx() != doc.Width || im.Bounds().Dy() != doc.Height {
			return fmt.Errorf("PhotoCraft 拼合图尺寸与模板不符")
		}
		if baselinePath != "" {
			baseline, err := imgutil.Decode(baselinePath)
			if err != nil {
				return err
			}
			return verifyStaticCells(baseline, im, static, cellSize)
		}
		return nil
	}
	for i, scale := range req.Scales {
		plan := req.Canvas
		if plan == nil {
			plan, err = printcanvas.Plan(req.Pattern, req.ArtworkPlanPath(i), req, scale)
			if err != nil {
				return "", err
			}
		}
		if err = plan.Validate(); err != nil {
			return "", err
		}
		if req.NoCrop && !plan.Verified {
			return "", fmt.Errorf("原图摆放未通过印刷域不裁切校验")
		}
		localSource := filepath.Join(req.OutDir, plan.SourceFile)
		if err = checkOutputPath(localSource, inputs, plan.SourcePath()); err != nil {
			return "", err
		}
		if err = stageFile(plan.SourcePath(), localSource); err != nil {
			return "", err
		}
		localPlan := *plan
		localPlan.Path = req.ArtworkPlanPath(i)
		if err = stageFile(plan.Path, localPlan.Path); err != nil {
			return "", err
		}
		if err = localPlan.Validate(); err != nil {
			return "", err
		}
		place := func(path string) error {
			for _, v := range surfaces {
				if err := c.placeArtwork(v.layer, v.width, v.height, path, &localPlan); err != nil {
					return err
				}
			}
			return nil
		}
		if err = place(localSource); err != nil {
			return "", err
		}
		if err = export(req.FlatPath(i)); err != nil {
			return "", err
		}
		if req.DiagnosticAlpha {
			im, err := imgutil.Decode(localSource)
			if err != nil {
				return "", err
			}
			alpha := filepath.Join(req.OutDir, fmt.Sprintf("diagnostic_source_%d.png", i))
			if err = checkOutputPath(alpha, inputs, ""); err != nil {
				return "", err
			}
			if err = savePNG(alpha, diagnosticAlpha(im)); err != nil {
				return "", err
			}
			if err = place(alpha); err != nil {
				return "", err
			}
			if err = c.exportTemplateDiagnostic(localTemplate, doc.Layers, func() error { return export(req.DiagnosticFlatPath(i)) }); err != nil {
				return "", err
			}
			if err = place(localSource); err != nil {
				return "", err
			}
		}
		meta := psx.RenderMetadata{ArtifactKind: plan.Kind, SourceSHA256: plan.SourceSHA256, Transform: plan.Transform, CanvasSHA256: plan.SHA256, CanvasWidth: plan.Width, CanvasHeight: plan.Height, CanvasVerified: plan.Verified, StaticCells: static, ReviewQuarterTurns: turns}
		for _, cell := range cells {
			p := plan.Placement
			p.Cell = cell
			meta.Placements = append(meta.Placements, p)
		}
		data, err := json.Marshal(meta)
		if err != nil {
			return "", err
		}
		if err = os.WriteFile(req.MetadataPath(i), data, 0600); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("PhotoCraft: 原图直接置入 %d 个共享源，%d 个印刷照片格", len(surfaces), len(cells)), nil
}

func (c *client) placeArtwork(layer, width, height int, path string, plan *psx.PrintCanvas) error {
	if err := c.command("layer.smartObjects.editContents", map[string]any{"layer": layer}, nil); err != nil {
		return err
	}
	var inner document
	if err := c.call("doc_inspect", map[string]any{}, &inner); err != nil {
		return err
	}
	if inner.Width != width || inner.Height != height {
		return fmt.Errorf("智能对象原生尺寸发生变化")
	}
	var placed struct {
		Layer int `json:"layer"`
	}
	if err := c.call("doc_place", map[string]any{"path": path, "scale": 100, "fit": false, "center": []float64{float64(plan.SourceWidth) / 2, float64(plan.SourceHeight) / 2}}, &placed); err != nil {
		return err
	}
	if placed.Layer <= 0 {
		return fmt.Errorf("PhotoCraft 未返回置入图层")
	}
	for _, old := range inner.Layers {
		if err := c.command("layer.delete", map[string]any{"layer": old.ID}, nil); err != nil {
			return err
		}
	}
	m := plan.Transform
	mx, my := float64(width)/float64(printcanvas.Width), float64(height)/float64(printcanvas.Height)
	m[0] *= mx
	m[2] *= mx
	m[4] *= mx
	m[1] *= my
	m[3] *= my
	m[5] *= my
	if err := c.command("edit.transform", map[string]any{"layer": placed.Layer, "matrix": m, "interpolation": "bilinear"}, nil); err != nil {
		return err
	}
	if err := c.command("layer.smartObjects.saveContents", map[string]any{}, nil); err != nil {
		return err
	}
	if err := c.call("doc_close", map[string]any{}, nil); err != nil {
		return err
	}
	return c.call("doc_select", map[string]any{"index": 0}, nil)
}
