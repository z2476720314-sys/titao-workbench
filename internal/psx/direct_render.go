package psx

import (
	"encoding/json"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"

	"titao/internal/imgutil"
)

func directFrozenRenderJSX(req RenderRequest) (string, error) {
	c := req.Canvas
	if err := c.Validate(); err != nil {
		return "", err
	}
	if req.NoCrop && !c.Verified {
		return "", fmt.Errorf("原图摆放未通过印刷域不裁切校验")
	}
	if len(req.Scales) == 0 || strings.TrimSpace(req.OutDir) == "" {
		return "", fmt.Errorf("原图摆放缺少输出档位或目录")
	}
	im, err := imgutil.Decode(c.SourcePath())
	if err != nil {
		return "", err
	}
	box := image.Rectangle{}
	for y := 0; y < im.Bounds().Dy(); y++ {
		for x := 0; x < im.Bounds().Dx(); x++ {
			_, _, _, a := im.At(x, y).RGBA()
			if a != 0 {
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	if box.Empty() {
		return "", fmt.Errorf("原图没有可见图案")
	}
	if err = os.MkdirAll(req.OutDir, 0755); err != nil {
		return "", err
	}
	source, err := os.ReadFile(c.SourcePath())
	if err != nil {
		return "", err
	}
	localSource := filepath.Join(req.OutDir, c.SourceFile)
	writeCopy := func(from, to string, data []byte) error {
		a, _ := filepath.Abs(from)
		b, _ := filepath.Abs(to)
		if strings.EqualFold(a, b) {
			return nil
		}
		if st, e := os.Stat(to); e == nil {
			if src, e := os.Stat(from); e == nil && os.SameFile(st, src) {
				return fmt.Errorf("原图输出不能覆盖输入")
			}
		}
		return os.WriteFile(to, data, 0600)
	}
	if err = writeCopy(c.SourcePath(), localSource, source); err != nil {
		return "", err
	}
	manifest, err := os.ReadFile(c.Path)
	if err != nil {
		return "", err
	}
	paths := make([]string, len(req.Scales))
	for i := range paths {
		paths[i] = localSource
		if err = writeCopy(c.Path, req.ArtworkPlanPath(i), manifest); err != nil {
			return "", err
		}
		local:=*c;local.Path=req.ArtworkPlanPath(i)
		if err=local.Validate();err!=nil{return "",err}
	}
	m := c.Transform
	config := struct {
		Template, Group, OutDir string
		Paths                   []string
		Box                     [4]float64
		CellSize                int
		Metadata                RenderMetadata
	}{req.TemplateCopy, req.Group, strings.ReplaceAll(req.OutDir, "\\", "/"), paths, [4]float64{m[0]*float64(box.Min.X) + m[4], m[3]*float64(box.Min.Y) + m[5], m[0]*float64(box.Max.X) + m[4], m[3]*float64(box.Max.Y) + m[5]}, req.CellSize, RenderMetadata{ArtifactKind: c.Kind, SourceSHA256: c.SourceSHA256, Transform: c.Transform, CanvasSHA256: c.SHA256, CanvasWidth: c.Width, CanvasHeight: c.Height, CanvasVerified: c.Verified, Placements: []AppliedPlacement{c.Placement}}}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	js := jsxFrozenRender
	js = strings.Replace(js, "if (iw == 946 && ih == 1890) map = 1;", "if (iw == 946 && ih == 1890) map = ih / 1890;", 1)
	// Keep the native extra column inside the normalized print domain, without added padding.
	js = strings.Replace(js, "for (var li = 0; li < inner.layers.length; li++)", "var mapX = iw / 945, mapY = ih / 1890;\n        for (var li = 0; li < inner.layers.length; li++)", 1)
	for _, v := range []struct{ old, new string }{{"(C.Box[2]-C.Box[0])*map/pw", "(C.Box[2]-C.Box[0])*mapX/pw"}, {"(C.Box[3]-C.Box[1])*map/ph", "(C.Box[3]-C.Box[1])*mapY/ph"}, {"C.Box[0]*map-b[0]", "C.Box[0]*mapX-b[0]"}, {"C.Box[1]*map-b[1]", "C.Box[1]*mapY-b[1]"}} {
		js = strings.ReplaceAll(js, v.old, v.new)
	}
	return strings.ReplaceAll(js, "__CONFIG__", string(encoded)), nil
}
