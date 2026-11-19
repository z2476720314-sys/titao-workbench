package photocraft

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"titao/internal/imgutil"
	"titao/internal/psx"
)

func frozenCanvas(t *testing.T, dir string) *psx.PrintCanvas {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 945, 1890))
	im.SetNRGBA(0, 0, color.NRGBA{90, 120, 150, 7})
	im.SetNRGBA(944, 1889, color.NRGBA{30, 60, 90, 255})
	path := filepath.Join(dir, "frozen.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(f, im)
	if closed := f.Close(); err != nil || closed != nil {
		t.Fatalf("encode: %v %v", err, closed)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return &psx.PrintCanvas{Path: path, SHA256: hex.EncodeToString(sum[:]), SourceSHA256: strings.Repeat("1", 64), Width: 945, Height: 1890, Verified: true, Placement: psx.AppliedPlacement{Scale: .8, OffsetX: -.04, OffsetY: .1, Constrained: true}}
}

func TestFrozenCanvasReplacesSharedSourcesWithoutRecomposing(t *testing.T) {
	t.Setenv("PHOTOCRAFT_LEGACY_MCP_FIXTURE", "1")
	dir := t.TempDir()
	t.Setenv("PHOTOCRAFT_FIXTURE_CALLS", filepath.Join(dir, "calls.jsonl"))
	canvas := frozenCanvas(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "fixture.tif"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	req := psx.RenderRequest{TemplateCopy: filepath.Join(dir, "fixture.tif"), Canvas: canvas, Pattern: "missing-pattern-must-not-be-read.png", Group: "插画", Fit: psx.FitStretch, Scales: []float64{1.4}, OutDir: dir, CellSize: 100}
	if _, err := New(os.Args[0]).Render(req); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(req.MetadataPath(0))
	if err != nil {
		t.Fatal(err)
	}
	var metadata psx.RenderMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.FrameVerified || !metadata.CanvasVerified || metadata.CanvasSHA256 != canvas.SHA256 || metadata.CanvasWidth != 945 || metadata.CanvasHeight != 1890 || len(metadata.Placements) != 3 {
		t.Fatalf("metadata=%+v", metadata)
	}
	for _, p := range metadata.Placements {
		want := canvas.Placement
		want.Cell = p.Cell
		if p != want {
			t.Fatalf("frozen placement changed: %+v", p)
		}
	}
	calls, err := os.ReadFile(filepath.Join(dir, "calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "doc_place") || strings.Contains(string(calls), "doc_new") || strings.Contains(string(calls), "newSmartObjectViaCopy") {
		t.Fatal("frozen canvas was recomposed or shared sources split")
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		var call struct {
			Name      string
			Arguments map[string]any
		}
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatal(err)
		}
		if call.Name == "doc_replace_smart_object" {
			paths = append(paths, call.Arguments["path"].(string))
		}
	}
	if len(paths) != 2 || paths[0] != paths[1] {
		t.Fatalf("shared canonical artifact not reused: %v", paths)
	}
}

func TestFrozenCanvasRejectsDigestMismatchBeforeEditorStarts(t *testing.T) {
	dir := t.TempDir()
	canvas := frozenCanvas(t, dir)
	canvas.SHA256 = strings.Repeat("0", 64)
	_, err := New(filepath.Join(dir, "missing-editor.exe")).Render(psx.RenderRequest{Canvas: canvas, Scales: []float64{1}, OutDir: dir})
	if err == nil || strings.Contains(err.Error(), "missing-editor") {
		t.Fatalf("artifact was not rejected before launch: %v", err)
	}
}

func TestCanvasMappingPadsWhiteWithoutMovingSourcePixels(t *testing.T) {
	dir := t.TempDir()
	canvas := frozenCanvas(t, dir)
	path, err := mappedCanvas(canvas.Path, filepath.Join(dir, "white.png"), 946, 1890)
	if err != nil {
		t.Fatal(err)
	}
	source, err := imgutil.Decode(canvas.Path)
	if err != nil {
		t.Fatal(err)
	}
	white, err := imgutil.Decode(path)
	if err != nil {
		t.Fatal(err)
	}
	if white.Bounds() != image.Rect(0, 0, 946, 1890) {
		t.Fatal(white.Bounds())
	}
	for y := 0; y < 1890; y++ {
		for x := 0; x < 945; x++ {
			if color.NRGBAModel.Convert(source.At(x, y)) != color.NRGBAModel.Convert(white.At(x, y)) {
				t.Fatalf("source pixel moved or resampled at %d,%d", x, y)
			}
		}
		if _, _, _, a := white.At(945, y).RGBA(); a != 0 {
			t.Fatalf("right padding not transparent at row %d", y)
		}
	}
	canonical, err := mappedCanvas(canvas.Path, filepath.Join(dir, "unused.png"), 945, 1890)
	if err != nil || canonical != canvas.Path {
		t.Fatalf("canonical PNG not reused: %s %v", canonical, err)
	}
}

func TestCanvasMappingScalesWholeCanvasAndRejectsUnknownAspect(t *testing.T) {
	dir := t.TempDir()
	canvas := frozenCanvas(t, dir)
	path, err := mappedCanvas(canvas.Path, filepath.Join(dir, "double.png"), 1890, 3780)
	if err != nil {
		t.Fatal(err)
	}
	im, err := imgutil.Decode(path)
	if err != nil {
		t.Fatal(err)
	}
	if im.Bounds() != image.Rect(0, 0, 1890, 3780) {
		t.Fatal(im.Bounds())
	}
	_, _, _, top := im.At(0, 0).RGBA()
	_, _, _, bottom := im.At(1889, 3779).RGBA()
	_, _, _, center := im.At(945, 1890).RGBA()
	if top == 0 || bottom == 0 || center != 0 {
		t.Fatal("whole-canvas corner geometry changed")
	}
	for _, size := range [][2]int{{946, 1891}, {1000, 1890}, {0, 1890}, {945, 0}} {
		if _, err := mappedCanvas(canvas.Path, filepath.Join(dir, "invalid.png"), size[0], size[1]); err == nil {
			t.Fatalf("unknown mapping accepted: %v", size)
		}
	}
}

func TestProjectionCellsRejectInvalidOrUnregisteredMappings(t *testing.T) {
	for _, bounds := range [][]float64{nil, {0, 0, 100, 0}, {0, math.NaN(), 100, 100}, {0, 300, 100, 100}} {
		objects := []layer{{Bounds: bounds}}
		if _, _, err := projectionCells(psx.RenderRequest{}, document{Width: 100, Height: 300}, objects); err == nil {
			t.Fatalf("invalid cells accepted: %v", bounds)
		}
	}
	dir := t.TempDir()
	template := filepath.Join(dir, "unknown.tif")
	if err := os.WriteFile(template, []byte("unverified source"), 0600); err != nil {
		t.Fatal(err)
	}
	var objects []layer
	for _, cell := range []int{0, 1, 3, 5} {
		objects = append(objects, layer{Bounds: []float64{0, float64(cell * 100), 100, 100}})
	}
	if _, _, err := projectionCells(psx.RenderRequest{TemplateCopy: template}, document{Width: 100, Height: 600}, objects); err == nil || !strings.Contains(err.Error(), "尚未核验") {
		t.Fatalf("unverified static template accepted: %v", err)
	}
}

func TestStaticCellsRemainPixelIdenticalAndDetectEitherChangedPhoto(t *testing.T) {
	base := image.NewNRGBA(image.Rect(0, 0, 10, 60))
	for _, cell := range []int{2, 4} {
		base.SetNRGBA(2, cell*10+3, color.NRGBA{80, 120, 160, 255})
	}
	out := image.NewNRGBA(base.Bounds())
	copy(out.Pix, base.Pix)
	out.SetNRGBA(1, 11, color.NRGBA{30, 60, 90, 255})
	if err := verifyStaticCells(base, out, []int{2, 4}, 10); err != nil {
		t.Fatal(err)
	}
	for _, cell := range []int{2, 4} {
		copy(out.Pix, base.Pix)
		out.SetNRGBA(2, cell*10+3, color.NRGBA{80, 120, 161, 255})
		if err := verifyStaticCells(base, out, []int{2, 4}, 10); err == nil {
			t.Fatalf("changed static photo %d accepted", cell)
		}
	}
}

func TestFrozenCanvasUsesSameProjectionForNoCropAndDiagnostics(t *testing.T) {
	t.Setenv("PHOTOCRAFT_LEGACY_MCP_FIXTURE", "1")
	root := t.TempDir()
	canvas := frozenCanvas(t, root)
	var plain []byte
	for _, noCrop := range []bool{false, true} {
		dir := filepath.Join(root, fmt.Sprintf("nocrop-%v", noCrop))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PHOTOCRAFT_FIXTURE_CALLS", filepath.Join(dir, "calls.jsonl"))
		if err := os.WriteFile(filepath.Join(dir, "fixture.tif"), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		req := psx.RenderRequest{Canvas: canvas, TemplateCopy: filepath.Join(dir, "fixture.tif"), Group: "插画", Scales: []float64{1}, OutDir: dir, NoCrop: noCrop, DiagnosticAlpha: noCrop}
		if _, err := New(os.Args[0]).Render(req); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(req.MetadataPath(0))
		if err != nil {
			t.Fatal(err)
		}
		if !noCrop {
			plain = data
			continue
		}
		if string(canonicalMetadata(t, data)) != string(canonicalMetadata(t, plain)) {
			t.Fatal("NoCrop or diagnostic changed frozen composition metadata")
		}
		calls, err := os.ReadFile(filepath.Join(dir, "calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(calls), "doc_replace_smart_object") != 6 {
			t.Fatal("diagnostic did not replace then restore both shared sources")
		}
		if _, err := os.Stat(req.DiagnosticFlatPath(0)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderRejectsUnknownProjectionWithoutFallback(t *testing.T) {
	t.Setenv("PHOTOCRAFT_LEGACY_MCP_FIXTURE", "1")
	t.Setenv("PHOTOCRAFT_FIXTURE_MAPPING", "invalid")
	dir := t.TempDir()
	canvas := frozenCanvas(t, dir)
	template := filepath.Join(dir, "fixture.tif")
	if err := os.WriteFile(template, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	req := psx.RenderRequest{TemplateCopy: template, Canvas: canvas, Group: "插画", Scales: []float64{1}, NoCrop: true, OutDir: dir}
	if _, err := New(os.Args[0]).Render(req); err == nil || !strings.Contains(err.Error(), "整画布映射") {
		t.Fatalf("unregistered projection was not rejected: %v", err)
	}
	if _, err := os.Stat(req.FlatPath(0)); !os.IsNotExist(err) {
		t.Fatalf("failed mapping exported an apparent success: %v", err)
	}
}

func TestRenderRejectsFrozenCanvasCollisionWithMappedOutput(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(fmt.Sprint(alias), func(t *testing.T) {
			t.Setenv("PHOTOCRAFT_LEGACY_MCP_FIXTURE", "1")
			t.Setenv("PHOTOCRAFT_FIXTURE_MAPPING", "white")
			dir := t.TempDir()
			canvas := frozenCanvas(t, dir)
			target := filepath.Join(dir, "mapped_canvas_0_946x1890.png")
			if alias {
				if err := os.Link(canvas.Path, target); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(canvas.Path, target); err != nil {
					t.Fatal(err)
				}
				canvas.Path = target
			}
			before, err := os.ReadFile(canvas.Path)
			if err != nil {
				t.Fatal(err)
			}
			template := filepath.Join(dir, "fixture.tif")
			if err := os.WriteFile(template, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err = New(os.Args[0]).Render(psx.RenderRequest{Canvas: canvas, TemplateCopy: template, Group: "插画", Scales: []float64{1}, OutDir: dir})
			if err == nil || !strings.Contains(err.Error(), "覆盖输入") {
				t.Fatalf("frozen artifact collision not rejected: %v", err)
			}
			after, err := os.ReadFile(canvas.Path)
			if err != nil || string(after) != string(before) {
				t.Fatal("supplied artifact was modified")
			}
		})
	}
}

func TestRenderRejectsPatternCollisionWithFlatOutput(t *testing.T) {
	t.Setenv("PHOTOCRAFT_LEGACY_MCP_FIXTURE", "1")
	dir := t.TempDir()
	canvas := frozenCanvas(t, dir)
	pattern := filepath.Join(dir, "flat_0.png")
	if err := os.Rename(canvas.Path, pattern); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(pattern)
	if err != nil {
		t.Fatal(err)
	}
	template := filepath.Join(dir, "fixture.tif")
	if err := os.WriteFile(template, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = New(os.Args[0]).Render(psx.RenderRequest{Pattern: pattern, TemplateCopy: template, Group: "插画", Scales: []float64{1}, OutDir: dir})
	if err == nil || !strings.Contains(err.Error(), "覆盖输入") {
		t.Fatalf("source/output collision not rejected: %v", err)
	}
	after, err := os.ReadFile(pattern)
	if err != nil || string(after) != string(before) {
		t.Fatal("source pattern was modified")
	}
}
