// Package photocraft renders existing layered templates through PhotoCraft's local headless MCP engine.
package photocraft

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"image"
	"titao/internal/imgutil"
	"titao/internal/printcanvas"
	"titao/internal/psx"
)

type Session struct {
	Path             string
	mu               sync.Mutex
	orientationCache map[orientationCacheKey]map[int]int
}

func New(path string) *Session {
	if path == "" {
		path = filepath.Join(os.Getenv("ProgramFiles"), "PhotoCraft", "photocraft-cli.exe")
	}
	return &Session{Path: path}
}

func (s *Session) Probe() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Path, "--version")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("检测 PhotoCraft: %w", err)
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 || fields[0] != "photocraft-cli" {
		return "", fmt.Errorf("程序不是可识别的 PhotoCraft CLI")
	}
	return fields[1], nil
}

// Each render owns a private headless process; desktop documents are never touched.
func (s *Session) CloseAllDocs() error { return nil }
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orientationCache = nil
	return nil
}

type client struct {
	enc *json.Encoder
	dec *json.Decoder
	id  int
}

func (s *Session) connect(root string) (*client, func(), error) {
	return s.connectAt(root, root)
}

func (s *Session) connectAt(root, processRoot string) (*client, func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	cmd := exec.CommandContext(ctx, s.Path, "mcp", "--automation-read-root", root, "--automation-write-root", root)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if processRoot != "" {
		cmd.Env = privateTempEnv(processRoot)
		cmd.Dir = processRoot
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		cancel()
		return nil, nil, err
	}
	if err = cmd.Start(); err != nil {
		in.Close()
		cancel()
		return nil, nil, err
	}
	if processRoot != "" {
		_ = recordProcess(processRoot, cmd, "started", nil)
	}
	wait := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if processRoot != "" {
			_ = recordProcess(processRoot, cmd, "exited", err)
		}
		wait <- err
	}()
	close := func() {
		in.Close()
		select {
		case <-wait:
		case <-time.After(2 * time.Second):
			cancel()
			<-wait
		}
		cancel()
	}
	c := &client{enc: json.NewEncoder(in), dec: json.NewDecoder(out)}
	var result json.RawMessage
	if err = c.rpc("initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "titao", "version": "1"}}, &result); err != nil {
		close()
		return nil, nil, fmt.Errorf("PhotoCraft 初始化: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	if err = c.enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		close()
		return nil, nil, err
	}
	return c, close, nil
}

func (c *client) rpc(method string, params any, out any) error {
	c.id++
	if err := c.enc.Encode(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params}); err != nil {
		return err
	}
	var reply struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := c.dec.Decode(&reply); err != nil {
		return err
	}
	if reply.ID != c.id {
		return fmt.Errorf("PhotoCraft 响应编号不匹配")
	}
	if reply.Error != nil {
		return fmt.Errorf("PhotoCraft: %s", reply.Error.Message)
	}
	return json.Unmarshal(reply.Result, out)
}

func (c *client) call(name string, args any, out any) error {
	var r struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := c.rpc("tools/call", map[string]any{"name": name, "arguments": args}, &r); err != nil {
		return err
	}
	for _, part := range r.Content {
		if part.Type == "text" {
			if r.IsError {
				return fmt.Errorf("PhotoCraft %s: %s", name, part.Text)
			}
			if out == nil {
				return nil
			}
			return json.Unmarshal([]byte(part.Text), out)
		}
	}
	return fmt.Errorf("PhotoCraft %s 未返回结果", name)
}

func (c *client) command(id string, params any, out any) error {
	return c.call("command_run", map[string]any{"id": id, "params": params}, out)
}

type contents struct {
	ID        int `json:"id"`
	Instances int `json:"instances"`
}
type layer struct {
	ID            int       `json:"id"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	Visible       bool      `json:"visible"`
	Opacity       float64   `json:"opacity"`
	Blend         string    `json:"blend"`
	Children      []layer   `json:"children"`
	SmartContents *contents `json:"smartContents"`
	Bounds        []float64 `json:"bounds"`
}
type document struct {
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Layers []layer `json:"layers"`
}

func findObjects(layers []layer, group string) ([]layer, error) {
	var objects []layer
	seen := map[int]bool{}
	var collect func([]layer)
	collect = func(ls []layer) {
		for _, l := range ls {
			if l.Kind == "Smart Object" && l.SmartContents != nil && !seen[l.SmartContents.ID] {
				seen[l.SmartContents.ID] = true
				objects = append(objects, l)
			}
			collect(l.Children)
		}
	}
	var find func([]layer)
	find = func(ls []layer) {
		for _, l := range ls {
			if l.Kind == "Group" && l.Name == group {
				collect(l.Children)
			} else {
				find(l.Children)
			}
		}
	}
	find(layers)
	if len(objects) == 0 {
		return nil, fmt.Errorf("图层组 %q 中没有可编辑的嵌入智能对象", group)
	}
	return objects, nil
}

func countObjects(layers []layer) int {
	n := 0
	for _, l := range layers {
		if l.Kind == "Smart Object" && l.SmartContents != nil {
			n++
		}
		n += countObjects(l.Children)
	}
	return n
}

func (s *Session) Render(req psx.RenderRequest) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(req.Scales) == 0 {
		return "", fmt.Errorf("套图至少需要一个档位")
	}
	for _, scale := range req.Scales {
		if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
			return "", fmt.Errorf("套图倍率无效")
		}
	}
	if req.Canvas == nil || req.Canvas.IsDirect() {
		return s.renderDirect(req)
	}
	if req.Canvas != nil {
		if err := printcanvas.Validate(req.Canvas); err != nil {
			return "", fmt.Errorf("共享原图版位无效: %w", err)
		}
	}
	inputs := []string{req.TemplateCopy}
	canvasInput := ""
	if req.Canvas != nil {
		canvasInput = req.Canvas.Path
		inputs = append(inputs, canvasInput)
	} else {
		inputs = append(inputs, req.Pattern)
	}
	localTemplate := filepath.Join(req.OutDir, "render-template"+filepath.Ext(req.TemplateCopy))
	if err := checkOutputPath(localTemplate, inputs, req.TemplateCopy); err != nil {
		return "", err
	}
	if err := checkOutputPath(filepath.Join(req.OutDir, "photocraft-processes.jsonl"), inputs, ""); err != nil {
		return "", err
	}
	for i := range req.Scales {
		if err := checkOutputPath(req.PrintCanvasPath(i), inputs, canvasInput); err != nil {
			return "", err
		}
		paths := []string{req.FlatPath(i), req.MetadataPath(i), req.MetadataPath(i) + ".tmp"}
		if req.DiagnosticAlpha {
			paths = append(paths, req.DiagnosticFlatPath(i), filepath.Join(req.OutDir, fmt.Sprintf("diagnostic_canvas_%d.png", i)))
		}
		for _, path := range paths {
			if err := checkOutputPath(path, inputs, ""); err != nil {
				return "", err
			}
		}
	}
	if err := stageFile(req.TemplateCopy, localTemplate); err != nil {
		return "", err
	}
	req.TemplateCopy = localTemplate
	c, close, err := s.connect(req.OutDir)
	if err != nil {
		return "", err
	}
	defer close()
	if err = c.call("doc_open", map[string]any{"path": req.TemplateCopy}, nil); err != nil {
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
	sourceSizes := map[int][2]int{}
	seen := map[int]bool{}
	for _, l := range objects {
		if seen[l.SmartContents.ID] {
			continue
		}
		seen[l.SmartContents.ID] = true
		if err = c.command("layer.smartObjects.editContents", map[string]any{"layer": l.ID}, nil); err != nil {
			return "", err
		}
		var inner document
		if err = c.call("doc_inspect", map[string]any{}, &inner); err != nil {
			return "", err
		}
		if !validProjection(inner.Width, inner.Height) {
			return "", fmt.Errorf("智能对象画布 %dx%d 没有已知的整画布映射", inner.Width, inner.Height)
		}
		surfaces = append(surfaces, surface{l.ID, inner.Width, inner.Height})
		sourceSizes[l.SmartContents.ID] = [2]int{inner.Width, inner.Height}
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
	var reviewTurns map[int]int
	if req.DiagnosticAlpha {
		reviewTurns, err = s.reviewQuarterTurns(c, req.TemplateCopy, req.Group, req.OutDir, doc, objects, sourceSizes, cellSize)
		if err != nil {
			return "", err
		}
	}
	for i := range req.Scales {
		for _, surface := range surfaces {
			if surface.width == printcanvas.Width && surface.height == printcanvas.Height {
				continue
			}
			prefixes := []string{"mapped_canvas"}
			if req.DiagnosticAlpha {
				prefixes = append(prefixes, "mapped_diagnostic_canvas")
			}
			for _, prefix := range prefixes {
				path := filepath.Join(req.OutDir, fmt.Sprintf("%s_%d_%dx%d.png", prefix, i, surface.width, surface.height))
				if err := checkOutputPath(path, inputs, ""); err != nil {
					return "", err
				}
			}
		}
	}
	var baseline image.Image
	if len(static) > 0 {
		path := filepath.Join(req.OutDir, "template-baseline.png")
		if err := checkOutputPath(path, inputs, ""); err != nil {
			return "", err
		}
		if err = c.call("doc_save", map[string]any{"index": 0, "path": path}, nil); err != nil {
			return "", err
		}
		baseline, err = imgutil.Decode(path)
		if err != nil {
			return "", err
		}
	}
	for i := range req.Scales {
		canvas := req.Canvas
		if err = printcanvas.Validate(canvas); err != nil {
			return "", fmt.Errorf("共享原图版位无效: %w", err)
		}
		if canvas.Width != printcanvas.Width || canvas.Height != printcanvas.Height {
			return "", fmt.Errorf("共享原图版位必须为 %dx%d", printcanvas.Width, printcanvas.Height)
		}
		local := req.PrintCanvasPath(i)
		if err = stageFile(canvas.Path, local); err != nil {
			return "", err
		}
		frozen := *canvas
		frozen.Path = local
		if err = printcanvas.Validate(&frozen); err != nil {
			return "", err
		}
		replace := func(source, prefix string) error {
			paths := map[[2]int]string{}
			for _, v := range surfaces {
				size := [2]int{v.width, v.height}
				path := paths[size]
				if path == "" {
					var err error
					path, err = mappedCanvas(source, filepath.Join(req.OutDir, fmt.Sprintf("%s_%d_%dx%d.png", prefix, i, v.width, v.height)), v.width, v.height)
					if err != nil {
						return err
					}
					paths[size] = path
				}
				if err := c.replaceCanvas(v.layer, path); err != nil {
					return err
				}
			}
			return nil
		}
		export := func(path string) error {
			if err := c.call("doc_save", map[string]any{"index": 0, "path": path}, nil); err != nil {
				return err
			}
			im, err := imgutil.Decode(path)
			if err != nil {
				return fmt.Errorf("PhotoCraft 未导出有效拼合图: %w", err)
			}
			if im.Bounds().Dx() != doc.Width || im.Bounds().Dy() != doc.Height {
				return fmt.Errorf("PhotoCraft 拼合图尺寸与模板不符")
			}
			if baseline != nil {
				return verifyStaticCells(baseline, im, static, cellSize)
			}
			return nil
		}
		if err = replace(local, "mapped_canvas"); err != nil {
			return "", err
		}
		if err = export(req.FlatPath(i)); err != nil {
			return "", err
		}
		if req.DiagnosticAlpha {
			im, err := imgutil.Decode(local)
			if err != nil {
				return "", err
			}
			alpha := filepath.Join(req.OutDir, fmt.Sprintf("diagnostic_canvas_%d.png", i))
			if err = savePNG(alpha, diagnosticAlpha(im)); err != nil {
				return "", err
			}
			if err = replace(alpha, "mapped_diagnostic_canvas"); err != nil {
				return "", err
			}
			if err = c.exportTemplateDiagnostic(req.TemplateCopy, doc.Layers, func() error { return export(req.DiagnosticFlatPath(i)) }); err != nil {
				return "", err
			}
			if err = replace(local, "mapped_canvas"); err != nil {
				return "", err
			}
		}
		metadata := psx.RenderMetadata{CanvasSHA256: canvas.SHA256, CanvasWidth: canvas.Width, CanvasHeight: canvas.Height, CanvasVerified: canvas.Verified, StaticCells: static, FrameVerified: false}
		metadata.ReviewQuarterTurns = reviewTurns
		for _, cell := range cells {
			p := canvas.Placement
			p.Cell = cell
			metadata.Placements = append(metadata.Placements, p)
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
	}
	return fmt.Sprintf("PhotoCraft: %d 个共享源投影至 %d 个印刷照片格，%d 个静态照片格，%d 档公共版位", len(surfaces), len(cells), len(static), len(req.Scales)), nil
}

func (s *Session) ProbeTemplate(path string) (*psx.TemplateInfo, error) {
	// Inspection must not create logs or scratch files in the user's template folder.
	c, close, err := s.connectAt(filepath.Dir(path), "")
	if err != nil {
		return nil, err
	}
	defer close()
	if err = c.call("doc_open", map[string]any{"path": path}, nil); err != nil {
		return nil, err
	}
	var doc document
	if err = c.call("doc_inspect", map[string]any{}, &doc); err != nil {
		return nil, err
	}
	info := &psx.TemplateInfo{Name: filepath.Base(path), Width: doc.Width, Height: doc.Height, CellSize: doc.Width, Guides: []float64{}, Groups: []string{}, Layers: []psx.LayerInfo{}, ArtGroups: []psx.ArtGroup{}}
	if doc.Width > 0 {
		info.Rows = doc.Height / doc.Width
	}
	for _, l := range doc.Layers {
		info.Layers = append(info.Layers, psx.LayerInfo{Name: l.Name, Kind: l.Kind, Visible: l.Visible, Opacity: l.Opacity * 100, Blend: l.Blend, Children: len(l.Children)})
		if l.Kind == "Group" {
			info.Groups = append(info.Groups, l.Name)
			if objects, err := findObjects([]layer{l}, l.Name); err == nil {
				a := psx.ArtGroup{Group: l.Name, Count: countObjects(l.Children), PSBs: []string{}}
				for _, v := range objects {
					a.PSBs = append(a.PSBs, v.Name)
				}
				info.ArtGroups = append(info.ArtGroups, a)
			}
		}
	}
	return info, nil
}
