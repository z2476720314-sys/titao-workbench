// Package photocraft renders existing layered templates through PhotoCraft's local headless MCP engine.
package photocraft

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"titao/internal/imgutil"
	"titao/internal/psx"
)

type Session struct {
	Path      string
	mu        sync.Mutex
	cacheRoot string
	prepared  map[string]*preparedTemplate
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
	if s.cacheRoot == "" {
		return nil
	}
	err := os.RemoveAll(s.cacheRoot)
	if err == nil {
		s.cacheRoot = ""
		s.prepared = nil
	}
	return err
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

func placementScale(req psx.RenderRequest, t float64, cw, ch, pw, ph int) (float64, float64, error) {
	if cw <= 0 || ch <= 0 || pw <= 0 || ph <= 0 || t <= 0 || math.IsNaN(t) || math.IsInf(t, 0) {
		return 0, 0, fmt.Errorf("无效画布或倍率")
	}
	x, y := float64(cw)/float64(pw), float64(ch)/float64(ph)
	s0 := math.Min(x, y)
	switch req.Fit {
	case psx.FitStretch:
		x, y = x*t, y*t
	case psx.FitNone:
		x, y = t, t
	case psx.FitCover:
		x, y = math.Max(x, y)*t, math.Max(x, y)*t
	default:
		x, y = s0*t, s0*t
	}
	if req.AnchorX == 2 && req.ContentBounds != [4]float64{} {
		x = float64(cw) / (float64(pw) * (req.ContentBounds[2] - req.ContentBounds[0]))
		y = x
	}
	if req.NoCrop && req.ContentBounds != [4]float64{} && req.Fit != psx.FitStretch {
		b := req.ContentBounds
		limit := math.Min(float64(cw)/(float64(pw)*(b[2]-b[0])), float64(ch)/(float64(ph)*(b[3]-b[1])))
		if req.AnchorX == 2 {
			if x > limit+1e-9 {
				return 0, 0, fmt.Errorf("图案比例无法同时满足左右贴边与内容不裁切")
			}
			x, y = math.Min(x, limit), math.Min(y, limit)
		} else {
			x, y = math.Min(math.Min(x/t, limit)*t, limit), math.Min(math.Min(y/t, limit)*t, limit)
		}
		return x, y, nil
	}
	if req.NoCrop && req.Fit != psx.FitCover && req.Fit != psx.FitStretch && (x > s0 || y > s0) {
		x, y = s0, s0
	}
	return x, y, nil
}

func placementCenter(req psx.RenderRequest, cw, ch int, w, h float64) (float64, float64) {
	x, y := float64(cw)/2, float64(ch)/2
	if req.AlignBottom && (req.Fit == "" || req.Fit == psx.FitAuto || req.Fit == psx.FitContain) {
		y = float64(ch) - h/2
	}
	x += float64(cw) * req.OffsetX
	y += float64(ch) * req.OffsetY
	if req.ContentBounds != [4]float64{} && req.Fit != psx.FitStretch {
		b := req.ContentBounds
		if req.AnchorX < 0 {
			x = w/2 - w*b[0]
		}
		if req.AnchorX > 0 {
			x = float64(cw) + w/2 - w*b[2]
		}
		if req.AnchorY < 0 {
			y = h/2 - h*b[1]
		}
		if req.AnchorY > 0 {
			y = float64(ch) + h/2 - h*b[3]
		}
		if req.NoCrop {
			x = math.Max(w/2-w*b[0], math.Min(float64(cw)+w/2-w*b[2], x))
			y = math.Max(h/2-h*b[1], math.Min(float64(ch)+h/2-h*b[3], y))
		}
		return x, y
	}
	if req.Fit == psx.FitCover {
		if w >= float64(cw) {
			x = math.Max(float64(cw)-w/2, math.Min(w/2, x))
		}
		if h >= float64(ch) {
			y = math.Max(float64(ch)-h/2, math.Min(h/2, y))
		}
	}
	if req.NoCrop && req.Fit != psx.FitCover && req.Fit != psx.FitStretch {
		x = math.Max(w/2, math.Min(float64(cw)-w/2, x))
		y = math.Max(h/2, math.Min(float64(ch)-h/2, y))
	}
	return x, y
}

// legacyPlacement reports the applied transform in the selected fit's unanchored scale basis.
func legacyPlacement(req psx.RenderRequest, desired float64, cw, ch, pw, ph int, sx, sy, x, y float64) (psx.AppliedPlacement, error) {
	base := req
	base.NoCrop, base.AnchorX, base.AnchorY = false, 0, 0
	bx, by, err := placementScale(base, 1, cw, ch, pw, ph)
	if err != nil {
		return psx.AppliedPlacement{}, err
	}
	sx, sy = sx/bx, sy/by
	p := psx.AppliedPlacement{Scale: sx, OffsetX: x/float64(cw) - 0.5, OffsetY: y/float64(ch) - 0.5}
	if sx <= 0 || math.IsNaN(sx) || math.IsInf(sx, 0) || math.IsNaN(sy) || math.IsInf(sy, 0) || math.Abs(sx-sy) > 1e-9*math.Max(sx, sy) || math.IsNaN(p.OffsetX) || math.IsInf(p.OffsetX, 0) || math.IsNaN(p.OffsetY) || math.IsInf(p.OffsetY, 0) {
		return p, fmt.Errorf("PhotoCraft legacy 无法用当前 fit 的单一倍率表达实际摆放")
	}
	p.Constrained = math.Abs(p.Scale-desired) > 1e-9 || math.Abs(p.OffsetX-req.OffsetX) > 1e-9 || math.Abs(p.OffsetY-req.OffsetY) > 1e-9
	return p, nil
}

func legacyMetadata(req psx.RenderRequest, doc document, applied map[int]psx.AppliedPlacement) (psx.RenderMetadata, error) {
	metadata := psx.RenderMetadata{FrameVerified: false}
	objects, err := visibleObjects(doc.Layers, req.Group)
	if err != nil {
		return metadata, err
	}
	cellSize := req.CellSize
	if cellSize == 0 {
		cellSize = doc.Width
	}
	if cellSize <= 0 || doc.Height <= 0 || doc.Height%cellSize != 0 {
		return metadata, fmt.Errorf("PhotoCraft legacy 照片格尺寸无效")
	}
	seen := map[int]bool{}
	for _, l := range objects {
		if len(l.Bounds) != 4 || l.Bounds[2] <= 0 || l.Bounds[3] <= 0 {
			return metadata, fmt.Errorf("PhotoCraft legacy 智能对象缺少有效外层范围")
		}
		for _, bound := range l.Bounds {
			if math.IsNaN(bound) || math.IsInf(bound, 0) {
				return metadata, fmt.Errorf("PhotoCraft legacy 智能对象外层范围无效")
			}
		}
		cell := int(math.Floor((l.Bounds[1] + l.Bounds[3]/2) / float64(cellSize)))
		p, ok := applied[l.SmartContents.ID]
		if !ok || cell < 0 || cell >= doc.Height/cellSize || seen[cell] {
			return metadata, fmt.Errorf("PhotoCraft legacy 实际摆放无法与照片格一一对应")
		}
		seen[cell] = true
		p.Cell = cell
		metadata.Placements = append(metadata.Placements, p)
	}
	return metadata, nil
}

func (s *Session) Render(req psx.RenderRequest) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.NoCrop {
		return s.renderVisible(req)
	}
	return s.renderLegacy(req)
}

func (s *Session) renderLegacy(req psx.RenderRequest) (string, error) {
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
	objects, err := findObjects(doc.Layers, req.Group)
	if err != nil {
		return "", err
	}
	type surface struct{ ID, ContentsID, W, H int }
	surfaces := make([]surface, 0, len(objects))
	for _, l := range objects {
		if err = c.command("layer.smartObjects.editContents", map[string]any{"layer": l.ID}, nil); err != nil {
			return "", err
		}
		var inner document
		if err = c.call("doc_inspect", map[string]any{}, &inner); err != nil {
			return "", err
		}
		surfaces = append(surfaces, surface{l.ID, l.SmartContents.ID, inner.Width, inner.Height})
		if err = c.call("doc_close", map[string]any{}, nil); err != nil {
			return "", err
		}
		if err = c.call("doc_select", map[string]any{"index": 0}, nil); err != nil {
			return "", err
		}
	}
	pat, err := imgutil.Decode(req.Pattern)
	if err != nil {
		return "", err
	}
	pb := pat.Bounds()
	for i, t := range req.Scales {
		applied := map[int]psx.AppliedPlacement{}
		for _, v := range surfaces {
			sx, sy, err := placementScale(req, t, v.W, v.H, pb.Dx(), pb.Dy())
			if err != nil {
				return "", err
			}
			x, y := placementCenter(req, v.W, v.H, float64(pb.Dx())*sx, float64(pb.Dy())*sy)
			p, err := legacyPlacement(req, t, v.W, v.H, pb.Dx(), pb.Dy(), sx, sy, x, y)
			if err != nil {
				return "", err
			}
			applied[v.ContentsID] = p
			var created struct {
				Document int `json:"document"`
			}
			if err = c.call("doc_new", map[string]any{"width": v.W, "height": v.H, "background": "transparent"}, &created); err != nil {
				return "", err
			}
			var placed struct {
				Layer int `json:"layer"`
			}
			if err = c.call("doc_place", map[string]any{"path": req.Pattern, "fit": false, "scale": sx * 100, "center": []float64{x, y}}, &placed); err != nil {
				return "", err
			}
			if sx != sy {
				if err = c.command("edit.transform", map[string]any{"layer": placed.Layer, "scaleY": sy / sx * 100}, nil); err != nil {
					return "", err
				}
			}
			canvas := filepath.Join(req.OutDir, fmt.Sprintf("canvas_%d.png", v.ID))
			if err = c.call("doc_save", map[string]any{"path": canvas}, nil); err != nil {
				return "", err
			}
			if err = c.call("doc_close", map[string]any{"index": created.Document}, nil); err != nil {
				return "", err
			}
			if err = c.call("doc_replace_smart_object", map[string]any{"index": 0, "layer": v.ID, "path": canvas}, nil); err != nil {
				return "", err
			}
		}
		if err = c.call("doc_save", map[string]any{"index": 0, "path": req.FlatPath(i)}, nil); err != nil {
			return "", err
		}
		if f, err := os.Open(req.FlatPath(i)); err != nil {
			return "", err
		} else {
			b := make([]byte, 8)
			_, err = io.ReadFull(f, b)
			f.Close()
			if err != nil {
				return "", fmt.Errorf("PhotoCraft 导出为空: %w", err)
			}
		}
		metadata, err := legacyMetadata(req, doc, applied)
		if err != nil {
			return "", err
		}
		data, err := json.Marshal(metadata)
		if err != nil {
			return "", err
		}
		tmp := req.MetadataPath(i) + ".tmp"
		if err := os.WriteFile(tmp, data, 0600); err != nil {
			return "", err
		}
		if err := os.Rename(tmp, req.MetadataPath(i)); err != nil {
			_ = os.Remove(tmp)
			return "", err
		}
	}
	return fmt.Sprintf("PhotoCraft: %d 个独立智能对象，%d 档候选", len(surfaces), len(req.Scales)), nil
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
