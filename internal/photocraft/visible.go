package photocraft

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"
	"titao/internal/fileutil"
	"titao/internal/imgutil"
	"titao/internal/printcanvas"
	"titao/internal/psx"
)

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

// Replacement updates every instance of the original source, retaining its template transforms.
func (c *client) replaceCanvas(layer int, canvas string) error {
	return c.call("doc_replace_smart_object", map[string]any{"index": 0, "layer": layer, "path": canvas}, nil)
}

func (c *client) exportDiagnostic(layers []layer, export func() error) (err error) {
	var hidden []int
	defer func() {
		for i := len(hidden) - 1; i >= 0; i-- {
			if restore := c.command("layer.setProps", map[string]any{"layer": hidden[i], "visible": true}, nil); restore != nil {
				err = errors.Join(err, fmt.Errorf("恢复诊断遮挡层 %d: %w", hidden[i], restore))
			}
		}
	}()
	for _, l := range layers {
		if !l.Visible || l.Kind != "Pixel" || (l.Name != "手指" && l.Name != "遮挡") {
			continue
		}
		// A failed reply can follow an applied change, so restore every attempted layer.
		hidden = append(hidden, l.ID)
		if err = c.command("layer.setProps", map[string]any{"layer": l.ID, "visible": false}, nil); err != nil {
			return err
		}
	}
	return export()
}

func (c *client) exportTemplateDiagnostic(template string, layers []layer, export func() error) error {
	f, err := os.Open(template)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, readErr := io.Copy(h, f)
	err = errors.Join(readErr, f.Close())
	if err != nil {
		return err
	}
	// Only these verified scene masks are known to exclude the camera itself.
	switch hex.EncodeToString(h.Sum(nil)) {
	case "e8f9f5c750e16d69a939a20152a545c6f738facbb07c542f5b7fd566a174d2b4", "13bdbb7283b43599ef2bdee183db913d117d507f987715e3e514c421aeadef1b", "e1c9d81dd4f6097cff4b35934cdc48d56e013b9c0be6b4b8729dc95a93ed21c9", "b79d884a97e87b701efd0a131ee67a9142f2783faf9f297475242dc294a2926d":
		return c.exportDiagnostic(layers, export)
	default:
		return export()
	}
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

func projectionCells(req psx.RenderRequest, doc document, objects []layer) ([]int, []int, error) {
	cellSize := req.CellSize
	if cellSize == 0 {
		cellSize = doc.Width
	}
	if doc.Width <= 0 || cellSize <= 0 || doc.Height <= 0 || doc.Height%cellSize != 0 {
		return nil, nil, fmt.Errorf("模板照片格尺寸无效")
	}
	seen := map[int]bool{}
	var cells []int
	for _, l := range objects {
		if len(l.Bounds) != 4 || l.Bounds[2] <= 0 || l.Bounds[3] <= 0 {
			return nil, nil, fmt.Errorf("智能对象缺少有效外层范围")
		}
		for _, b := range l.Bounds {
			if math.IsNaN(b) || math.IsInf(b, 0) {
				return nil, nil, fmt.Errorf("智能对象外层范围无效")
			}
		}
		cell := int(math.Floor((l.Bounds[1] + l.Bounds[3]/2) / float64(cellSize)))
		if cell < 0 || cell >= doc.Height/cellSize || seen[cell] {
			return nil, nil, fmt.Errorf("智能对象与照片格不能一一对应")
		}
		seen[cell] = true
		cells = append(cells, cell)
	}
	sort.Ints(cells)
	var static []int
	for cell := 0; cell < doc.Height/cellSize; cell++ {
		if !seen[cell] {
			static = append(static, cell)
		}
	}
	if len(static) > 0 {
		// Only the visually inspected stair template has registered static side/front photos.
		if doc.Height/cellSize != 6 || len(cells) != 4 || len(static) != 2 || static[0] != 2 || static[1] != 4 {
			return nil, nil, fmt.Errorf("模板含未登记的非印刷照片格: %v", static)
		}
		f, err := os.Open(req.TemplateCopy)
		if err != nil {
			return nil, nil, err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return nil, nil, err
		}
		if hex.EncodeToString(h.Sum(nil)) != "13bdbb7283b43599ef2bdee183db913d117d507f987715e3e514c421aeadef1b" {
			return nil, nil, fmt.Errorf("静态照片格模板尚未核验，不能仅凭布局登记")
		}
	}
	return cells, static, nil
}

func savePNG(path string, im image.Image) error {
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

func stageFile(source, target string) error {
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return err
	}
	if targetInfo, err := os.Stat(target); err == nil && os.SameFile(sourceInfo, targetInfo) {
		return nil
	}
	return fileutil.Copy(source, target)
}

func checkOutputPath(target string, inputs []string, copying string) error {
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	targetInfo, _ := os.Stat(target)
	for _, input := range inputs {
		if input == copying {
			continue
		}
		inputAbs, err := filepath.Abs(input)
		if err != nil {
			return err
		}
		same := strings.EqualFold(targetAbs, inputAbs)
		if !same && targetInfo != nil {
			if inputInfo, err := os.Stat(input); err == nil {
				same = os.SameFile(targetInfo, inputInfo)
			}
		}
		if same {
			return fmt.Errorf("输出路径不能覆盖输入文件: %s", filepath.Base(input))
		}
	}
	return nil
}

func validProjection(width, height int) bool {
	return width > 0 && height > 0 && ((width == 946 && height == 1890) || int64(width)*int64(printcanvas.Height) == int64(height)*int64(printcanvas.Width))
}

func mappedCanvas(source, output string, width, height int) (string, error) {
	if !validProjection(width, height) {
		return "", fmt.Errorf("智能对象画布 %dx%d 没有已知的整画布映射", width, height)
	}
	if width == printcanvas.Width && height == printcanvas.Height {
		return source, nil
	}
	im, err := imgutil.Decode(source)
	if err != nil {
		return "", err
	}
	if im.Bounds().Dx() != printcanvas.Width || im.Bounds().Dy() != printcanvas.Height {
		return "", fmt.Errorf("共享印刷画布尺寸无效")
	}
	out := image.NewNRGBA(image.Rect(0, 0, width, height))
	if width == 946 && height == 1890 {
		// White template's extra column is transparent padding on the right; do not resample.
		draw.Draw(out, im.Bounds(), im, im.Bounds().Min, draw.Src)
	} else {
		xdraw.CatmullRom.Scale(out, out.Bounds(), im, im.Bounds(), draw.Src, nil)
	}
	if err := savePNG(output, out); err != nil {
		return "", err
	}
	return output, nil
}

func verifyStaticCells(baseline, output image.Image, cells []int, cellSize int) error {
	if baseline.Bounds() != output.Bounds() {
		return fmt.Errorf("投影改变了模板拼合图尺寸")
	}
	b := baseline.Bounds()
	for _, cell := range cells {
		for y := b.Min.Y + cell*cellSize; y < b.Min.Y+(cell+1)*cellSize; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if color.NRGBAModel.Convert(baseline.At(x, y)) != color.NRGBAModel.Convert(output.At(x, y)) {
					return fmt.Errorf("静态照片格 %d 的原始像素发生变化", cell)
				}
			}
		}
	}
	return nil
}
