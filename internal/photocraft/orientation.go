package photocraft

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
)

const maxOrientationManifestBytes = 16 << 20

type orientationCacheKey struct {
	templateSHA [32]byte
	group       string
	cellSize    int
}

type orientationLayer struct {
	ID      int `json:"id"`
	Content struct {
		Kind       string             `json:"kind"`
		Children   []orientationLayer `json:"children"`
		ContentsID int                `json:"contents_id"`
		Transform  struct {
			Matrix []float64 `json:"m"`
		} `json:"transform"`
		Perspective []float64       `json:"perspective"`
		Warp        json.RawMessage `json:"warp"`
	} `json:"content"`
}

// The render lock owns this cache; a native export is needed only for the first diagnostic render.
func (s *Session) reviewQuarterTurns(c *client, template, group, outDir string, doc document, objects []layer, sizes map[int][2]int, cellSize int) (map[int]int, error) {
	f, err := os.Open(template)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	_, readErr := io.Copy(h, f)
	err = errors.Join(readErr, f.Close())
	if err != nil {
		return nil, err
	}
	key := orientationCacheKey{group: group, cellSize: cellSize}
	copy(key.templateSHA[:], h.Sum(nil))
	if cached, ok := s.orientationCache[key]; ok {
		return cached, nil
	}
	turns, err := exportReviewQuarterTurns(c, outDir, doc, objects, sizes, cellSize)
	if err != nil {
		return nil, fmt.Errorf("无法确定自动诊断方向: %w", err)
	}
	if s.orientationCache == nil {
		s.orientationCache = map[orientationCacheKey]map[int]int{}
	}
	s.orientationCache[key] = turns
	return turns, nil
}

func exportReviewQuarterTurns(c *client, outDir string, doc document, objects []layer, sizes map[int][2]int, cellSize int) (turns map[int]int, err error) {
	dir, err := os.MkdirTemp(outDir, ".review-orientation-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "orientation.pcraft")
	defer func() {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, fmt.Errorf("清理诊断方向项目: %w", removeErr))
		}
		if removeErr := os.Remove(dir); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("清理诊断方向临时目录: %w", removeErr))
		}
	}()
	if err = c.call("doc_save", map[string]any{"index": 0, "path": path}, nil); err != nil {
		return nil, err
	}
	return readReviewQuarterTurns(path, doc, objects, sizes, cellSize)
}

func readReviewQuarterTurns(path string, doc document, objects []layer, sizes map[int][2]int, cellSize int) (map[int]int, error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("读取 PhotoCraft 原生项目: %w", err)
	}
	defer z.Close()
	var entry *zip.File
	for _, file := range z.File {
		if file.Name == "manifest.json" {
			if entry != nil {
				return nil, fmt.Errorf("原生项目包含重复 manifest.json")
			}
			entry = file
		}
	}
	if entry == nil || !entry.Mode().IsRegular() || entry.UncompressedSize64 > maxOrientationManifestBytes {
		return nil, fmt.Errorf("原生项目 manifest.json 缺失、无效或过大")
	}
	r, err := entry.Open()
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(r, maxOrientationManifestBytes+1))
	if err := errors.Join(readErr, r.Close()); err != nil {
		return nil, err
	}
	if len(data) > maxOrientationManifestBytes {
		return nil, fmt.Errorf("原生项目 manifest.json 过大")
	}
	var manifest struct {
		Version  int `json:"format_version"`
		Document struct {
			Size struct {
				Width  int `json:"width"`
				Height int `json:"height"`
			} `json:"size"`
			Layers []orientationLayer `json:"layers"`
		} `json:"document"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("解析原生项目 manifest.json: %w", err)
	}
	if manifest.Version != 1 || manifest.Document.Size.Width != doc.Width || manifest.Document.Size.Height != doc.Height || doc.Width <= 0 || doc.Height <= 0 || cellSize <= 0 || doc.Height%cellSize != 0 {
		return nil, fmt.Errorf("原生项目格式或画布尺寸与模板不符")
	}
	byID := map[int]orientationLayer{}
	var collect func([]orientationLayer) error
	collect = func(layers []orientationLayer) error {
		for _, layer := range layers {
			if _, exists := byID[layer.ID]; layer.ID <= 0 || exists {
				return fmt.Errorf("原生项目图层 ID 无效或重复: %d", layer.ID)
			}
			byID[layer.ID] = layer
			if err := collect(layer.Content.Children); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(manifest.Document.Layers); err != nil {
		return nil, err
	}
	if len(objects) == 0 {
		return nil, fmt.Errorf("没有可确定方向的印刷对象")
	}
	turns := map[int]int{}
	for _, object := range objects {
		native, ok := byID[object.ID]
		if !ok || object.SmartContents == nil || native.Content.Kind != "smart" || native.Content.ContentsID != object.SmartContents.ID {
			return nil, fmt.Errorf("印刷图层 %d 缺少匹配的原生变换", object.ID)
		}
		size, ok := sizes[object.SmartContents.ID]
		if !ok || size[0] <= 0 || size[1] <= 0 || len(object.Bounds) != 4 || object.Bounds[2] <= 0 || object.Bounds[3] <= 0 {
			return nil, fmt.Errorf("印刷图层 %d 缺少画布尺寸或照片格范围", object.ID)
		}
		for _, value := range object.Bounds {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("印刷图层 %d 的照片格范围无效", object.ID)
			}
		}
		cell := int(math.Floor((object.Bounds[1] + object.Bounds[3]/2) / float64(cellSize)))
		if _, exists := turns[cell]; cell < 0 || cell >= doc.Height/cellSize || exists {
			return nil, fmt.Errorf("印刷图层 %d 与照片格不能一一对应", object.ID)
		}
		rotation, err := native.reviewQuarterTurns(size)
		if err != nil {
			return nil, fmt.Errorf("印刷图层 %d: %w", object.ID, err)
		}
		turns[cell] = rotation
	}
	return turns, nil
}

func (layer orientationLayer) reviewQuarterTurns(size [2]int) (int, error) {
	content := layer.Content
	if len(content.Warp) != 0 && string(content.Warp) != "null" {
		return 0, fmt.Errorf("尚不支持带 warp 的诊断方向")
	}
	var downX, downY float64
	if content.Perspective != nil {
		h := content.Perspective
		if len(h) != 9 {
			return 0, fmt.Errorf("透视矩阵无效")
		}
		for _, value := range h {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return 0, fmt.Errorf("透视矩阵含非有限数值")
			}
		}
		det := h[0]*(h[4]*h[8]-h[5]*h[7]) - h[1]*(h[3]*h[8]-h[5]*h[6]) + h[2]*(h[3]*h[7]-h[4]*h[6])
		x, y := float64(size[0])/2, float64(size[1])
		topW, bottomW := h[6]*x+h[8], h[6]*x+h[7]*y+h[8]
		if det == 0 || math.IsNaN(det) || math.IsInf(det, 0) || topW == 0 || bottomW == 0 || math.Signbit(topW) != math.Signbit(bottomW) {
			return 0, fmt.Errorf("透视变换退化或穿过无穷远")
		}
		downX = (h[0]*x+h[1]*y+h[2])/bottomW - (h[0]*x+h[2])/topW
		downY = (h[3]*x+h[4]*y+h[5])/bottomW - (h[3]*x+h[5])/topW
	} else {
		m := content.Transform.Matrix
		if len(m) != 6 {
			return 0, fmt.Errorf("缺少有效仿射矩阵")
		}
		for _, value := range m {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return 0, fmt.Errorf("仿射矩阵含非有限数值")
			}
		}
		if determinant := m[0]*m[3] - m[1]*m[2]; determinant == 0 || math.IsNaN(determinant) || math.IsInf(determinant, 0) {
			return 0, fmt.Errorf("仿射变换退化")
		}
		downX, downY = m[2]*float64(size[1]), m[3]*float64(size[1])
	}
	if math.IsNaN(downX) || math.IsNaN(downY) || math.IsInf(downX, 0) || math.IsInf(downY, 0) || math.Hypot(downX, downY) < 1e-9 {
		return 0, fmt.Errorf("画布上下方向退化或无效")
	}
	quarterTurns := int(math.Round((math.Pi/2 - math.Atan2(downY, downX)) / (math.Pi / 2)))
	return (quarterTurns%4 + 4) % 4, nil
}
