package photocraft

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"titao/internal/psx"
)

func saveOrientationFixture(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	z := zip.NewWriter(f)
	entry, err := z.Create("manifest.json")
	if err == nil {
		manifest := `{"format_version":1,"document":{"size":{"width":100,"height":300},"layers":[{"id":10,"content":{"kind":"smart","contents_id":7,"transform":{"m":[1,0,0,1,0,0]}}},{"id":11,"content":{"kind":"smart","contents_id":8,"transform":{"m":[0,-1,1,0,0,0]}}},{"id":12,"content":{"kind":"smart","contents_id":7,"transform":{"m":[-1,0,0,-1,0,0]}}}]}}`
		if os.Getenv("PHOTOCRAFT_FIXTURE_ORIENTATION") == "invalid" {
			manifest = `{}`
		}
		_, err = entry.Write([]byte(manifest))
	}
	zipErr, fileErr := z.Close(), f.Close()
	if err != nil {
		return err
	}
	if zipErr != nil {
		return zipErr
	}
	return fileErr
}

func orientationManifest(content string) string {
	return `{"format_version":1,"document":{"size":{"width":100,"height":300},"layers":[{"id":20,"content":{"kind":"group","children":[{"id":11,"content":` + content + `}]}}]}}`
}

func writeOrientationProject(t *testing.T, manifests ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "orientation.pcraft")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for _, manifest := range manifests {
		entry, err := z.Create("manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(manifest)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func orientationInputs() (document, []layer, map[int][2]int) {
	return document{Width: 100, Height: 300}, []layer{{ID: 11, SmartContents: &contents{ID: 7}, Bounds: []float64{0, 100, 100, 100}}}, map[int][2]int{7: {945, 1890}}
}

// Ignoring native transforms would incorrectly leave the upside-down source at zero turns.
func TestReviewOrientationUsesNativeAffineDirection(t *testing.T) {
	for _, tc := range []struct {
		name, matrix string
		want         int
	}{
		{"down", `[1,0,0,1,0,0]`, 0},
		{"left", `[0,1,-1,0,0,0]`, 3},
		{"up", `[-1,0,0,-1,0,0]`, 2},
		{"right", `[0,-1,1,0,0,0]`, 1},
		{"slanted down", `[1,0,0.2,1,5,7]`, 0},
		{"slanted up", `[-1,0,-0.2,-1,5,7]`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeOrientationProject(t, orientationManifest(`{"kind":"smart","contents_id":7,"transform":{"m":`+tc.matrix+`},"perspective":null,"warp":null}`))
			doc, objects, sizes := orientationInputs()
			got, err := readReviewQuarterTurns(path, doc, objects, sizes, 100)
			if err != nil || !reflect.DeepEqual(got, map[int]int{1: tc.want}) {
				t.Fatalf("quarter turns=%v want cell 1=%d error=%v", got, tc.want, err)
			}
		})
	}
}

// This native matrix comes from the black template's layer 27; its affine fallback points down.
func TestReviewOrientationPrefersActualPerspectiveOverAffine(t *testing.T) {
	content := `{"kind":"smart","contents_id":7,"transform":{"m":[0.5153647938495534,0,0,0.28400387252643344,241.04435136963994,2510.958372652002]},"perspective":[-0.5129235142125954,0.17155851716991113,728.0640815574678,0.038818997486344646,0.7666124154778867,3040.2111900495934,0.000010127927176415286,0.00041682903730716443,1],"warp":null}`
	doc, objects, sizes := orientationInputs()
	got, err := readReviewQuarterTurns(writeOrientationProject(t, orientationManifest(content)), doc, objects, sizes, 100)
	if err != nil || !reflect.DeepEqual(got, map[int]int{1: 2}) {
		t.Fatalf("actual perspective direction=%v error=%v", got, err)
	}
}

func TestReviewOrientationRejectsIncompleteOrUnsupportedNativeData(t *testing.T) {
	valid := orientationManifest(`{"kind":"smart","contents_id":7,"transform":{"m":[1,0,0,1,0,0]},"perspective":null,"warp":null}`)
	for _, tc := range []struct{ name, manifest string }{
		{"version", strings.Replace(valid, `"format_version":1`, `"format_version":2`, 1)},
		{"missing version", strings.Replace(valid, `"format_version":1,`, ``, 1)},
		{"wrong document", strings.Replace(valid, `"width":100`, `"width":101`, 1)},
		{"duplicate id", strings.Replace(valid, `"id":20`, `"id":11`, 1)},
		{"missing target", strings.Replace(valid, `"id":11`, `"id":12`, 1)},
		{"wrong contents", strings.Replace(valid, `"contents_id":7`, `"contents_id":8`, 1)},
		{"unknown warp", strings.Replace(valid, `"warp":null`, `"warp":{}`, 1)},
		{"missing transform", strings.Replace(valid, `"transform":{"m":[1,0,0,1,0,0]},`, ``, 1)},
		{"short transform", strings.Replace(valid, `[1,0,0,1,0,0]`, `[1,0,0,1]`, 1)},
		{"collapsed transform", strings.Replace(valid, `[1,0,0,1,0,0]`, `[1,0,0,0,0,0]`, 1)},
		{"nonfinite number", strings.Replace(valid, `[1,0,0,1,0,0]`, `[1e309,0,0,1,0,0]`, 1)},
		{"short perspective", strings.Replace(valid, `"perspective":null`, `"perspective":[1,0,0]`, 1)},
		{"perspective horizon", strings.Replace(valid, `"perspective":null`, `"perspective":[1,0,0,0,1,0,0,-0.001,1]`, 1)},
		{"zero denominator", strings.Replace(valid, `"perspective":null`, `"perspective":[1,0,0,0,1,0,0,0,0]`, 1)},
		{"collapsed perspective", strings.Replace(valid, `"perspective":null`, `"perspective":[1,0,0,0,0,0,0,0,1]`, 1)},
		{"trailing JSON", valid + `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, objects, sizes := orientationInputs()
			if got, err := readReviewQuarterTurns(writeOrientationProject(t, tc.manifest), doc, objects, sizes, 100); err == nil {
				t.Fatalf("unsafe direction accepted: %v", got)
			}
		})
	}
	for _, tc := range []struct {
		name string
		edit func(*document, *[]layer, map[int][2]int)
	}{
		{"missing source size", func(_ *document, _ *[]layer, s map[int][2]int) { delete(s, 7) }},
		{"zero source size", func(_ *document, _ *[]layer, s map[int][2]int) { s[7] = [2]int{0, 1890} }},
		{"missing bounds", func(_ *document, o *[]layer, _ map[int][2]int) { (*o)[0].Bounds = nil }},
		{"out of range cell", func(_ *document, o *[]layer, _ map[int][2]int) { (*o)[0].Bounds[1] = 300 }},
		{"duplicate cell", func(_ *document, o *[]layer, _ map[int][2]int) { *o = append(*o, (*o)[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, objects, sizes := orientationInputs()
			tc.edit(&doc, &objects, sizes)
			if got, err := readReviewQuarterTurns(writeOrientationProject(t, valid), doc, objects, sizes, 100); err == nil {
				t.Fatalf("incomplete direction accepted: %v", got)
			}
		})
	}
}

func TestReviewOrientationBoundsNativeManifestRead(t *testing.T) {
	doc, objects, sizes := orientationInputs()
	valid := orientationManifest(`{"kind":"smart","contents_id":7,"transform":{"m":[1,0,0,1,0,0]}}`)
	for _, manifests := range [][]string{nil, {valid, valid}, {strings.Repeat(" ", 17<<20) + valid}} {
		if got, err := readReviewQuarterTurns(writeOrientationProject(t, manifests...), doc, objects, sizes, 100); err == nil {
			t.Fatalf("unbounded or ambiguous manifest accepted: %v", got)
		}
	}
}

func TestRenderReviewOrientationCachesOnlySuccessfulTemplateContent(t *testing.T) {
	t.Setenv("PHOTOCRAFT_LEGACY_MCP_FIXTURE", "1")
	root := t.TempDir()
	canvas := frozenCanvas(t, root)
	template := filepath.Join(root, "fixture.tif")
	if err := os.WriteFile(template, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(root, "calls.jsonl")
	t.Setenv("PHOTOCRAFT_FIXTURE_CALLS", calls)
	s := New(os.Args[0])
	req := psx.RenderRequest{TemplateCopy: template, Canvas: canvas, Group: "插画", Scales: []float64{1}, OutDir: root, DiagnosticAlpha: true}
	for pass := 0; pass < 3; pass++ {
		if pass == 2 {
			if err := os.WriteFile(template, []byte("changed fixture"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.Render(req); err != nil {
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
		if !reflect.DeepEqual(metadata.ReviewQuarterTurns, map[int]int{0: 0, 1: 1, 2: 2}) {
			t.Fatalf("review rotations lost: %v", metadata.ReviewQuarterTurns)
		}
		if metadata.CanvasSHA256 != canvas.SHA256 || metadata.FrameVerified || len(metadata.Placements) != 3 {
			t.Fatalf("canonical metadata changed: %+v", metadata)
		}
		for _, placement := range metadata.Placements {
			want := canvas.Placement
			want.Cell = placement.Cell
			if placement != want {
				t.Fatalf("canonical placement changed: %+v", placement)
			}
		}
		entries, err := filepath.Glob(filepath.Join(root, ".review-orientation-*"))
		if err != nil || len(entries) != 0 {
			t.Fatalf("private export not cleaned: %v %v", entries, err)
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if got := bytes.Count(data, []byte(`orientation.pcraft`)); got != 2 {
		t.Fatalf("native exports=%d want 2 for two template hashes", got)
	}
	if err := s.Close(); err != nil || len(s.orientationCache) != 0 {
		t.Fatal("Close retained orientation cache", err)
	}
	t.Setenv("PHOTOCRAFT_FIXTURE_ORIENTATION", "invalid")
	for pass := 0; pass < 2; pass++ {
		if _, err := s.Render(req); err == nil || !strings.Contains(err.Error(), "无法确定自动诊断方向") {
			t.Fatalf("invalid native project must fail diagnostic: %v", err)
		}
		if len(s.orientationCache) != 0 {
			t.Fatal("failed orientation cached")
		}
	}
	data, _ = os.ReadFile(calls)
	if got := bytes.Count(data, []byte(`orientation.pcraft`)); got != 4 {
		t.Fatalf("failed exports must retry: native exports=%d want 4 total", got)
	}
	if entries, err := filepath.Glob(filepath.Join(root, ".review-orientation-*")); err != nil || len(entries) != 0 {
		t.Fatalf("failed private export not cleaned: %v %v", entries, err)
	}
}

func TestReviewOrientationCacheIncludesGroupAndCellSize(t *testing.T) {
	t.Setenv("PHOTOCRAFT_LEGACY_MCP_FIXTURE", "1")
	root := t.TempDir()
	template := filepath.Join(root, "template.tif")
	if err := os.WriteFile(template, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(root, "calls.jsonl")
	t.Setenv("PHOTOCRAFT_FIXTURE_CALLS", calls)
	s := New(os.Args[0])
	defer s.Close()
	c, close, err := s.connect(root)
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	doc, objects, _ := orientationInputs()
	objects[0].SmartContents.ID = 8
	sizes := map[int][2]int{8: {945, 1890}}
	for _, entry := range []struct {
		group string
		cell  int
	}{
		{"插画", 100}, {"插画", 100}, {"其它插画", 100}, {"插画", 50},
	} {
		got, err := s.reviewQuarterTurns(c, template, entry.group, root, doc, objects, sizes, entry.cell)
		if err != nil || len(got) != 1 {
			t.Fatalf("cache result=%v error=%v", got, err)
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if got := bytes.Count(data, []byte(`orientation.pcraft`)); got != 3 {
		t.Fatalf("native exports=%d want 3 for distinct group and cell sizes", got)
	}
}

func TestManualRenderDoesNotExportReviewOrientation(t *testing.T) {
	t.Setenv("PHOTOCRAFT_LEGACY_MCP_FIXTURE", "1")
	root := t.TempDir()
	canvas := frozenCanvas(t, root)
	template := filepath.Join(root, "fixture.tif")
	if err := os.WriteFile(template, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(root, "calls.jsonl")
	t.Setenv("PHOTOCRAFT_FIXTURE_CALLS", calls)
	req := psx.RenderRequest{TemplateCopy: template, Canvas: canvas, Group: "插画", Scales: []float64{1}, OutDir: root}
	if _, err := New(os.Args[0]).Render(req); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(calls)
	if bytes.Contains(data, []byte(".pcraft")) {
		t.Fatal("manual projection exported native review project")
	}
	data, err := os.ReadFile(req.MetadataPath(0))
	if err != nil {
		t.Fatal(err)
	}
	var metadata psx.RenderMetadata
	if err := json.Unmarshal(data, &metadata); err != nil || len(metadata.ReviewQuarterTurns) != 0 {
		t.Fatalf("manual projection review data=%v error=%v", metadata.ReviewQuarterTurns, err)
	}
}

// Opt-in checks the supplied four local templates without invoking a model or writing to them.
func TestRealReviewOrientationTemplates(t *testing.T) {
	cli, source := os.Getenv("PHOTOCRAFT_TEST_CLI"), os.Getenv("PHOTOCRAFT_TEST_TEMPLATES")
	if cli == "" || source == "" {
		t.Skip("real PhotoCraft inputs not configured")
	}
	paths, err := filepath.Glob(filepath.Join(source, "*.tif"))
	if err != nil || len(paths) != 4 {
		t.Fatalf("four templates required: %d %v", len(paths), err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			root := t.TempDir()
			local := filepath.Join(root, "template.tif")
			if err := stageFile(path, local); err != nil {
				t.Fatal(err)
			}
			s := New(cli)
			defer s.Close()
			c, close, err := s.connect(root)
			if err != nil {
				t.Fatal(err)
			}
			defer close()
			if err := c.call("doc_open", map[string]any{"path": local}, nil); err != nil {
				t.Fatal(err)
			}
			var doc document
			if err := c.call("doc_inspect", map[string]any{}, &doc); err != nil {
				t.Fatal(err)
			}
			objects, err := visibleObjects(doc.Layers, "插画")
			if err != nil {
				t.Fatal(err)
			}
			sizes := map[int][2]int{}
			for _, object := range objects {
				if _, ok := sizes[object.SmartContents.ID]; ok {
					continue
				}
				if err := c.command("layer.smartObjects.editContents", map[string]any{"layer": object.ID}, nil); err != nil {
					t.Fatal(err)
				}
				var inner document
				if err := c.call("doc_inspect", map[string]any{}, &inner); err != nil {
					t.Fatal(err)
				}
				sizes[object.SmartContents.ID] = [2]int{inner.Width, inner.Height}
				if err := c.call("doc_close", map[string]any{}, nil); err != nil {
					t.Fatal(err)
				}
				if err := c.call("doc_select", map[string]any{"index": 0}, nil); err != nil {
					t.Fatal(err)
				}
			}
			turns, err := s.reviewQuarterTurns(c, local, "插画", root, doc, objects, sizes, doc.Width)
			if err != nil || len(turns) != len(objects) {
				t.Fatalf("turns=%v objects=%d error=%v", turns, len(objects), err)
			}
			if filepath.Base(path) == "黑色IMD模版1.tif" && turns[3] != 2 {
				t.Fatalf("black inverted cell 3=%d want 2", turns[3])
			}
			if filepath.Base(path) == "阶梯黑色模板.tif" {
				for _, cell := range []int{2, 4} {
					if _, ok := turns[cell]; ok {
						t.Fatalf("static cell %d got orientation", cell)
					}
				}
			}
			data, _ := json.Marshal(turns)
			t.Logf("review_quarter_turns=%s", data)
			if evidence := os.Getenv("PHOTOCRAFT_ORIENTATION_EVIDENCE"); evidence != "" {
				if err := os.WriteFile(filepath.Join(evidence, fmt.Sprintf("%s.orientation.json", filepath.Base(path))), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
