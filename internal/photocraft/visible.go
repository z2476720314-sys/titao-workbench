package photocraft

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"titao/internal/fileutil"
	"titao/internal/imgutil"
	"titao/internal/psx"
)

type visibleSurface struct {
	Name                string
	Width, Height, Cell int
}
type preparedTemplate struct {
	Path     string
	Surfaces []visibleSurface
	Areas    []psx.VisibleArea
	CellSize int
}

type placedCanvas struct {
	Surface     visibleSurface
	Layer       int
	Scale, X, Y float64
}

func diagnosticAlpha(im image.Image) *image.NRGBA {
	b := im.Bounds()
	threshold := uint32(8 * 257)
	strong := false
	for y := b.Min.Y; y < b.Max.Y && !strong; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := im.At(x, y).RGBA()
			if a >= threshold {
				strong = true
				break
			}
		}
	}
	if !strong {
		threshold = 1
	}
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := im.At(x, y).RGBA()
			if a >= 128*257 {
				out.Set(x-b.Min.X, y-b.Min.Y, im.At(x, y))
			} else if a >= threshold {
				out.SetNRGBA(x-b.Min.X, y-b.Min.Y, color.NRGBA{255, 0, 255, 255})
			}
		}
	}
	return out
}

func (c *client) replaceCanvas(placed placedCanvas, pattern, canvas string) error {
	var created struct {
		Document int `json:"document"`
	}
	if err := c.call("doc_new", map[string]any{"width": placed.Surface.Width, "height": placed.Surface.Height, "background": "transparent"}, &created); err != nil {
		return err
	}
	if err := c.call("doc_place", map[string]any{"path": pattern, "fit": false, "scale": placed.Scale * 100, "center": []float64{placed.X, placed.Y}}, nil); err != nil {
		return err
	}
	if err := c.call("doc_save", map[string]any{"path": canvas}, nil); err != nil {
		return err
	}
	if err := c.call("doc_close", map[string]any{"index": created.Document}, nil); err != nil {
		return err
	}
	return c.call("doc_replace_smart_object", map[string]any{"index": 0, "layer": placed.Layer, "path": canvas}, nil)
}

func privateTempEnv(root string) []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "TEMP") && !strings.EqualFold(key, "TMP") {
			env = append(env, entry)
		}
	}
	return append(env, "TEMP="+root, "TMP="+root)
}

func recordProcess(root string, cmd *exec.Cmd, event string, runErr error) error {
	row := map[string]any{"event": event, "pid": cmd.Process.Pid, "parentPID": os.Getpid(), "at": time.Now().UTC().Format(time.RFC3339Nano), "executable": cmd.Path, "args": cmd.Args[1:], "purpose": "private PhotoCraft rendering"}
	if runErr != nil {
		row["error"] = runErr.Error()
	}
	data, err := json.Marshal(row)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, "photocraft-processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

func visibleObjects(layers []layer, group string) ([]layer, error) {
	var out []layer
	var collect func([]layer)
	collect = func(ls []layer) {
		for _, l := range ls {
			if !l.Visible {
				continue
			}
			if l.Kind == "Smart Object" && l.SmartContents != nil {
				out = append(out, l)
			}
			collect(l.Children)
		}
	}
	var find func([]layer)
	find = func(ls []layer) {
		for _, l := range ls {
			if !l.Visible {
				continue
			}
			if l.Kind == "Group" && l.Name == group {
				collect(l.Children)
			} else {
				find(l.Children)
			}
		}
	}
	find(layers)
	if len(out) == 0 {
		return nil, fmt.Errorf("图层组 %q 中没有可见智能对象", group)
	}
	return out, nil
}

func uniqueCommands(input, output string, objects []layer) ([]string, []string, error) {
	if filepath.Clean(input) == filepath.Clean(output) {
		return nil, nil, fmt.Errorf("独立化输出不能覆盖输入模板")
	}
	args := []string{"run", input}
	var names []string
	add := func(id string, params any) {
		data, _ := json.Marshal(params)
		args = append(args, "--cmd", id, "--params", string(data))
	}
	for i, l := range objects {
		name := fmt.Sprintf("__titao_visible_%d_%d__", i, l.ID)
		names = append(names, name)
		add("layer.smartObjects.newSmartObjectViaCopy", map[string]any{"layer": l.ID})
		add("layer.setProps", map[string]any{"name": name})
		add("layer.setProps", map[string]any{"layer": l.ID, "visible": false})
	}
	return append(args, "--out", output), names, nil
}

func preparedKey(req psx.RenderRequest) (string, error) {
	f, err := os.Open(req.TemplateCopy)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	fmt.Fprintf(h, "\x00%s\x00%d", req.Group, req.CellSize)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func strictPlacement(req psx.RenderRequest, area psx.VisibleArea, hull []psx.Point, cw, ch, pw, ph int, desired float64) (psx.AppliedPlacement, float64, float64, float64, error) {
	applied, err := psx.FitArtwork(area, hull, cw, ch, pw, ph, desired, req.OffsetX, req.OffsetY, req.AnchorX, req.AnchorY, req.PreferOffsets)
	if err != nil {
		return applied, 0, 0, 0, err
	}
	scale := math.Min(float64(cw)/float64(pw), float64(ch)/float64(ph)) * applied.Scale
	return applied, scale, float64(cw) * (.5 + applied.OffsetX), float64(ch) * (.5 + applied.OffsetY), nil
}

func saveUV(path string, width, height int) error {
	if width < 2 || height < 2 {
		return fmt.Errorf("UV画布尺寸无效")
	}
	im := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			im.SetNRGBA(x, y, color.NRGBA{uint8(x * 255 / (width - 1)), uint8(y * 255 / (height - 1)), 128, 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	err = png.Encode(f, im)
	closed := f.Close()
	if err != nil {
		return err
	}
	return closed
}

func (s *Session) runUnique(root string, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Env = privateTempEnv(root)
	cmd.Dir = root
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = recordProcess(root, cmd, "started", nil)
	err := cmd.Wait()
	_ = recordProcess(root, cmd, "exited", err)
	if err != nil {
		return fmt.Errorf("PhotoCraft 智能对象独立化: %w (%s)", err, strings.TrimSpace(output.String()))
	}
	return nil
}

func (s *Session) prepareVisible(req psx.RenderRequest) (_ *preparedTemplate, err error) {
	key, err := preparedKey(req)
	if err != nil {
		return nil, err
	}
	if cached := s.prepared[key]; cached != nil {
		return cached, nil
	}
	if s.cacheRoot == "" {
		s.cacheRoot, err = os.MkdirTemp(filepath.Dir(req.OutDir), "titao-visible-")
		if err != nil {
			return nil, err
		}
		s.prepared = map[string]*preparedTemplate{}
	}
	root, err := os.MkdirTemp(s.cacheRoot, "template-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(root)
		}
	}()
	template := filepath.Join(root, "source"+filepath.Ext(req.TemplateCopy))
	if err = fileutil.Copy(req.TemplateCopy, template); err != nil {
		return nil, err
	}
	c, close, err := s.connect(root)
	if err != nil {
		return nil, err
	}
	var doc document
	if err = c.call("doc_open", map[string]any{"path": template}, nil); err == nil {
		err = c.call("doc_inspect", map[string]any{}, &doc)
	}
	if err != nil {
		close()
		return nil, err
	}
	objects, err := visibleObjects(doc.Layers, req.Group)
	if err != nil {
		close()
		return nil, err
	}
	cellSize := req.CellSize
	if cellSize == 0 {
		cellSize = doc.Width
	}
	if cellSize <= 0 || doc.Height%cellSize != 0 {
		close()
		return nil, fmt.Errorf("模板照片格尺寸无效")
	}
	dimensions := map[int][2]int{}
	for _, l := range objects {
		if _, ok := dimensions[l.SmartContents.ID]; ok {
			continue
		}
		if err = c.command("layer.smartObjects.editContents", map[string]any{"layer": l.ID}, nil); err != nil {
			break
		}
		var inner document
		if err = c.call("doc_inspect", map[string]any{}, &inner); err != nil {
			break
		}
		dimensions[l.SmartContents.ID] = [2]int{inner.Width, inner.Height}
		if err = c.call("doc_close", map[string]any{}, nil); err != nil {
			break
		}
		if err = c.call("doc_select", map[string]any{"index": 0}, nil); err != nil {
			break
		}
	}
	close()
	if err != nil {
		return nil, err
	}
	prepared := &preparedTemplate{Path: filepath.Join(root, "prepared.psd"), CellSize: cellSize}
	args, names, err := uniqueCommands(template, prepared.Path, objects)
	if err != nil {
		return nil, err
	}
	if err = s.runUnique(root, args); err != nil {
		return nil, err
	}
	seenCells := map[int]bool{}
	for i, l := range objects {
		if len(l.Bounds) != 4 || l.Bounds[2] <= 0 || l.Bounds[3] <= 0 {
			return nil, fmt.Errorf("智能对象缺少有效外层范围")
		}
		cell := int(math.Floor((l.Bounds[1] + l.Bounds[3]/2) / float64(cellSize)))
		if cell < 0 || cell >= doc.Height/cellSize || seenCells[cell] {
			return nil, fmt.Errorf("智能对象与照片格不能一一对应")
		}
		seenCells[cell] = true
		d := dimensions[l.SmartContents.ID]
		prepared.Surfaces = append(prepared.Surfaces, visibleSurface{names[i], d[0], d[1], cell})
	}
	c, close, err = s.connect(root)
	if err != nil {
		return nil, err
	}
	defer close()
	if err = c.call("doc_open", map[string]any{"path": prepared.Path}, nil); err != nil {
		return nil, err
	}
	var independent document
	if err = c.call("doc_inspect", map[string]any{}, &independent); err != nil {
		return nil, err
	}
	visible, err := visibleObjects(independent.Layers, req.Group)
	if err != nil {
		return nil, err
	}
	if len(visible) != len(prepared.Surfaces) {
		return nil, fmt.Errorf("独立智能对象数量不一致")
	}
	byName := map[string]layer{}
	for _, l := range visible {
		if l.SmartContents.Instances != 1 {
			return nil, fmt.Errorf("可见智能对象仍共享内容")
		}
		byName[l.Name] = l
	}
	for _, surface := range prepared.Surfaces {
		l, ok := byName[surface.Name]
		if !ok {
			return nil, fmt.Errorf("独立智能对象名称缺失")
		}
		uv := filepath.Join(root, fmt.Sprintf("uv_%dx%d.png", surface.Width, surface.Height))
		if _, e := os.Stat(uv); os.IsNotExist(e) {
			if err = saveUV(uv, surface.Width, surface.Height); err != nil {
				return nil, err
			}
		}
		if err = c.call("doc_replace_smart_object", map[string]any{"index": 0, "layer": l.ID, "path": uv}, nil); err != nil {
			return nil, err
		}
	}
	if err = isolateVisibleGroup(c, independent.Layers, req.Group); err != nil {
		return nil, err
	}
	calibration := filepath.Join(root, "visible.png")
	if err = c.call("doc_save", map[string]any{"index": 0, "path": calibration}, nil); err != nil {
		return nil, err
	}
	im, err := imgutil.Decode(calibration)
	if err != nil {
		return nil, err
	}
	prepared.Areas, err = psx.CalibrateVisibleAreas(im, cellSize)
	if err != nil {
		return nil, fmt.Errorf("照片画面可见区域校准失败: %w", err)
	}
	if len(prepared.Areas) != len(prepared.Surfaces) {
		return nil, fmt.Errorf("可见区域与智能对象数量不一致")
	}
	sort.Slice(prepared.Surfaces, func(i, j int) bool { return prepared.Surfaces[i].Cell < prepared.Surfaces[j].Cell })
	s.prepared[key] = prepared
	_ = os.Remove(template)
	return prepared, nil
}

func isolateVisibleGroup(c *client, layers []layer, group string) error {
	var contains func(layer) bool
	contains = func(l layer) bool {
		if !l.Visible {
			return false
		}
		if l.Kind == "Group" && l.Name == group {
			return true
		}
		for _, child := range l.Children {
			if contains(child) {
				return true
			}
		}
		return false
	}
	var isolate func([]layer) error
	isolate = func(ls []layer) error {
		for _, l := range ls {
			if !contains(l) {
				if l.Visible {
					if err := c.command("layer.setProps", map[string]any{"layer": l.ID, "visible": false}, nil); err != nil {
						return err
					}
				}
				continue
			}
			if l.Kind != "Group" || l.Name != group {
				if err := isolate(l.Children); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return isolate(layers)
}

func (s *Session) renderVisible(req psx.RenderRequest) (string, error) {
	if len(req.Scales) == 0 {
		return "", fmt.Errorf("严格套图至少需要一个档位")
	}
	prepared, err := s.prepareVisible(req)
	if err != nil {
		return "", err
	}
	local := filepath.Join(req.OutDir, "prepared-visible.psd")
	if err = fileutil.Copy(prepared.Path, local); err != nil {
		return "", err
	}
	c, close, err := s.connect(req.OutDir)
	if err != nil {
		return "", err
	}
	defer close()
	if err = c.call("doc_open", map[string]any{"path": local}, nil); err != nil {
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
	byName := map[string]layer{}
	for _, l := range objects {
		byName[l.Name] = l
	}
	pat, err := imgutil.Decode(req.Pattern)
	if err != nil {
		return "", err
	}
	hull := psx.ArtworkHull(pat)
	pb := pat.Bounds()
	var alphaPath string
	if req.DiagnosticAlpha {
		alphaPath = filepath.Join(req.OutDir, "diagnostic-alpha.png")
		f, err := os.Create(alphaPath)
		if err != nil {
			return "", err
		}
		err = png.Encode(f, diagnosticAlpha(pat))
		closed := f.Close()
		if err != nil {
			return "", err
		}
		if closed != nil {
			return "", closed
		}
	}
	for i, desired := range req.Scales {
		metadata := psx.RenderMetadata{FrameVerified: true}
		var placed []placedCanvas
		for _, surface := range prepared.Surfaces {
			l, ok := byName[surface.Name]
			if !ok || l.SmartContents.Instances != 1 {
				return "", fmt.Errorf("独立智能对象缺失或重新共享")
			}
			var area *psx.VisibleArea
			for j := range prepared.Areas {
				if prepared.Areas[j].Cell == surface.Cell {
					area = &prepared.Areas[j]
					break
				}
			}
			if area == nil {
				return "", fmt.Errorf("照片格 %d 未校准", surface.Cell+1)
			}
			applied, scale, x, y, err := strictPlacement(req, *area, hull, surface.Width, surface.Height, pb.Dx(), pb.Dy(), desired)
			if err != nil {
				return "", fmt.Errorf("照片格 %d 无裁切摆放失败: %w", surface.Cell+1, err)
			}
			placement := placedCanvas{Surface: surface, Layer: l.ID, Scale: scale, X: x, Y: y}
			canvas := filepath.Join(req.OutDir, fmt.Sprintf("canvas_cell_%d.png", surface.Cell))
			if err = c.replaceCanvas(placement, req.Pattern, canvas); err != nil {
				return "", err
			}
			if req.DiagnosticAlpha {
				placed = append(placed, placement)
			}
			metadata.Placements = append(metadata.Placements, applied)
		}
		if err = c.call("doc_save", map[string]any{"index": 0, "path": req.FlatPath(i)}, nil); err != nil {
			return "", err
		}
		if st, err := os.Stat(req.FlatPath(i)); err != nil || st.Size() == 0 {
			return "", fmt.Errorf("PhotoCraft 未导出有效拼合图")
		}
		data, err := json.Marshal(metadata)
		if err != nil {
			return "", err
		}
		tmp := req.MetadataPath(i) + ".tmp"
		if err = os.WriteFile(tmp, data, 0600); err != nil {
			return "", err
		}
		if err = os.Rename(tmp, req.MetadataPath(i)); err != nil {
			_ = os.Remove(tmp)
			return "", err
		}
		if req.DiagnosticAlpha {
			for _, placement := range placed {
				canvas := filepath.Join(req.OutDir, fmt.Sprintf("diagnostic_canvas_cell_%d.png", placement.Surface.Cell))
				if err = c.replaceCanvas(placement, alphaPath, canvas); err != nil {
					return "", err
				}
			}
			if err = c.call("doc_save", map[string]any{"index": 0, "path": req.DiagnosticFlatPath(i)}, nil); err != nil {
				return "", err
			}
			if st, err := os.Stat(req.DiagnosticFlatPath(i)); err != nil || st.Size() == 0 {
				return "", fmt.Errorf("PhotoCraft 未导出有效alpha诊断图")
			}
		}
	}
	return fmt.Sprintf("PhotoCraft: %d 个独立角度，%d 档候选，照片画面边界已校准验证", len(prepared.Surfaces), len(req.Scales)), nil
}
