package photocraft

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"titao/internal/fileutil"
	"titao/internal/imgutil"
	"titao/internal/psx"
)

func diagnosticClient(t *testing.T, failures map[int]string) (*client, *bytes.Buffer) {
	t.Helper()
	var replies strings.Builder
	for id := 1; id <= 12; id++ {
		message, fail := failures[id]
		if !fail {
			message = "{}"
		}
		data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"isError": fail, "content": []map[string]string{{"type": "text", "text": message}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		replies.Write(data)
		replies.WriteByte('\n')
	}
	requests := new(bytes.Buffer)
	return &client{enc: json.NewEncoder(requests), dec: json.NewDecoder(strings.NewReader(replies.String()))}, requests
}

type diagnosticRequest struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func diagnosticRequests(t *testing.T, requests *bytes.Buffer) []diagnosticRequest {
	t.Helper()
	var out []diagnosticRequest
	dec := json.NewDecoder(requests)
	for {
		var call struct {
			Method string            `json:"method"`
			Params diagnosticRequest `json:"params"`
		}
		if err := dec.Decode(&call); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if call.Method != "tools/call" {
			t.Fatalf("unexpected MCP method: %s", call.Method)
		}
		out = append(out, call.Params)
	}
	return out
}

func diagnosticVisibility(id int, visible bool) diagnosticRequest {
	return diagnosticRequest{"command_run", map[string]any{"id": "layer.setProps", "params": map[string]any{"layer": float64(id), "visible": visible}}}
}

func TestDiagnosticOcclusionOnlyHidesExactVisibleTopLevelPixelLayers(t *testing.T) {
	layers := []layer{
		{ID: 1, Name: "手指", Kind: "Pixel", Visible: true, Blend: "Normal", Opacity: .5},
		{ID: 2, Name: "遮挡", Kind: "Pixel", Visible: true},
		{ID: 3, Name: "手指", Kind: "Pixel", Visible: false},
		{ID: 4, Name: "手指 拷贝", Kind: "Pixel", Visible: true},
		{ID: 5, Name: " 遮挡", Kind: "Pixel", Visible: true},
		{ID: 6, Name: "手指", Kind: "Group", Visible: true, Children: []layer{{ID: 7, Name: "遮挡", Kind: "Pixel", Visible: true}}},
		{ID: 8, Name: "镜头", Kind: "Pixel", Visible: true},
		{ID: 9, Name: "阴影", Kind: "Pixel", Visible: true},
		{ID: 10, Name: "遮挡", Kind: "Smart Object", Visible: true},
	}
	before, _ := json.Marshal(layers)
	c, requests := diagnosticClient(t, nil)
	if err := c.exportDiagnostic(layers, func() error {
		return c.call("doc_save", map[string]any{"index": 0, "path": "diagnostic.png"}, nil)
	}); err != nil {
		t.Fatal(err)
	}
	want := []diagnosticRequest{
		diagnosticVisibility(1, false), diagnosticVisibility(2, false),
		{"doc_save", map[string]any{"index": float64(0), "path": "diagnostic.png"}},
		diagnosticVisibility(2, true), diagnosticVisibility(1, true),
	}
	if got := diagnosticRequests(t, requests); !reflect.DeepEqual(got, want) {
		t.Fatalf("MCP requests=%+v want=%+v", got, want)
	}
	after, _ := json.Marshal(layers)
	if !bytes.Equal(before, after) {
		t.Fatal("diagnostic mutated the inspected template metadata")
	}
}

func TestDiagnosticOcclusionRestoresAfterFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failures map[int]string
		exported bool
	}{
		{"hide", map[int]string{2: "hide failed"}, false},
		{"export", map[int]string{3: "export failed"}, true},
		{"restore", map[int]string{4: "restore failed"}, true},
		{"export and restore", map[int]string{3: "export failed", 4: "restore failed"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, requests := diagnosticClient(t, tc.failures)
			exported := false
			err := c.exportDiagnostic([]layer{{ID: 1, Name: "手指", Kind: "Pixel", Visible: true}, {ID: 2, Name: "遮挡", Kind: "Pixel", Visible: true}}, func() error {
				exported = true
				return c.call("doc_save", map[string]any{"path": "diagnostic.png"}, nil)
			})
			if err == nil || exported != tc.exported {
				t.Fatalf("exported=%v error=%v", exported, err)
			}
			for _, message := range tc.failures {
				if !strings.Contains(err.Error(), message) {
					t.Fatalf("lost %q: %v", message, err)
				}
			}
			got := diagnosticRequests(t, requests)
			wantRestore := []diagnosticRequest{diagnosticVisibility(2, true), diagnosticVisibility(1, true)}
			if len(got) < 4 || !reflect.DeepEqual(got[len(got)-2:], wantRestore) {
				t.Fatalf("not all attempted layers restored: %+v", got)
			}
		})
	}
}

func TestDiagnosticOcclusionNoMatchLeavesLayersUntouched(t *testing.T) {
	for _, layers := range [][]layer{nil, {
		{ID: 1, Name: "手指", Kind: "Pixel", Visible: false},
		{ID: 2, Name: "遮挡", Kind: "Group", Visible: true, Children: []layer{{ID: 3, Name: "手指", Kind: "Pixel", Visible: true}}},
	}} {
		c, requests := diagnosticClient(t, nil)
		called := false
		if err := c.exportDiagnostic(layers, func() error { called = true; return nil }); err != nil {
			t.Fatal(err)
		}
		if !called || requests.Len() != 0 {
			t.Fatalf("exported=%v layer commands=%s", called, requests)
		}
	}
}

func TestDiagnosticOcclusionPreservesRealFlatAndCanonicalMetadata(t *testing.T) {
	t.Setenv("PHOTOCRAFT_LEGACY_MCP_FIXTURE", "1")
	root := t.TempDir()
	canvas := frozenCanvas(t, root)
	var flatHash, metadataHash [32]byte
	for _, diagnostic := range []bool{false, true} {
		dir := filepath.Join(root, fmt.Sprintf("diagnostic-%v", diagnostic))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		template := filepath.Join(dir, "fixture.tif")
		if err := os.WriteFile(template, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		req := psx.RenderRequest{TemplateCopy: template, Canvas: canvas, Group: "插画", OutDir: dir, Scales: []float64{1}, DiagnosticAlpha: diagnostic}
		if _, err := New(os.Args[0]).Render(req); err != nil {
			t.Fatal(err)
		}
		flat, err := os.ReadFile(req.FlatPath(0))
		if err != nil {
			t.Fatal(err)
		}
		metadata, err := os.ReadFile(req.MetadataPath(0))
		if err != nil {
			t.Fatal(err)
		}
		if !diagnostic {
			flatHash, metadataHash = sha256.Sum256(flat), sha256.Sum256(canonicalMetadata(t, metadata))
		} else if flatHash != sha256.Sum256(flat) || metadataHash != sha256.Sum256(canonicalMetadata(t, metadata)) {
			t.Fatal("diagnostic changed real flat or metadata hashes")
		}
		var decoded psx.RenderMetadata
		if err := json.Unmarshal(metadata, &decoded); err != nil || decoded.CanvasSHA256 != canvas.SHA256 {
			t.Fatalf("canonical canvas hash changed: %+v %v", decoded, err)
		}
	}
}

func TestRealDiagnosticOcclusionPreservesFourTemplates(t *testing.T) {
	cli, templates, output := os.Getenv("PHOTOCRAFT_TEST_CLI"), os.Getenv("PHOTOCRAFT_TEST_TEMPLATES"), os.Getenv("PHOTOCRAFT_OCCLUSION_OUTPUT")
	if cli == "" || templates == "" || output == "" {
		t.Skip("four-template diagnostic comparison not configured")
	}
	paths, err := filepath.Glob(filepath.Join(templates, "*.tif"))
	if err != nil || len(paths) != 4 {
		t.Fatalf("four real templates required: %d %v", len(paths), err)
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	canvas := frozenCanvas(t, t.TempDir())
	if source := os.Getenv("PHOTOCRAFT_TEST_CANVAS"); source != "" {
		canvas.Path = filepath.Join(output, "canonical.png")
		if err := fileutil.Copy(source, canvas.Path); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(canvas.Path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		canvas.SHA256 = hex.EncodeToString(sum[:])
	}
	for index, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			originalHash := sha256.Sum256(original)
			var flatHash, metadataHash [32]byte
			for _, diagnostic := range []bool{false, true} {
				dir := filepath.Join(output, fmt.Sprintf("template-%d-diagnostic-%v", index, diagnostic))
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				req := psx.RenderRequest{TemplateCopy: path, Canvas: canvas, Group: "插画", OutDir: dir, Scales: []float64{1}, DiagnosticAlpha: diagnostic}
				if _, err := New(cli).Render(req); err != nil {
					t.Fatal(err)
				}
				flat, err := os.ReadFile(req.FlatPath(0))
				if err != nil {
					t.Fatal(err)
				}
				metadata, err := os.ReadFile(req.MetadataPath(0))
				if err != nil {
					t.Fatal(err)
				}
				if !diagnostic {
					flatHash, metadataHash = sha256.Sum256(flat), sha256.Sum256(canonicalMetadata(t, metadata))
				} else if flatHash != sha256.Sum256(flat) || metadataHash != sha256.Sum256(canonicalMetadata(t, metadata)) {
					t.Fatal("diagnostic changed real flat or metadata hashes")
				}
				var decoded psx.RenderMetadata
				if err := json.Unmarshal(metadata, &decoded); err != nil || decoded.CanvasSHA256 != canvas.SHA256 {
					t.Fatalf("canonical canvas hash changed: %+v %v", decoded, err)
				}
				if diagnostic {
					flatImage, err := imgutil.Decode(req.DiagnosticFlatPath(0))
					if err != nil {
						t.Fatal(err)
					}
					if _, err := imgutil.SplitCells(req.DiagnosticFlatPath(0), filepath.Join(output, "diagnostic-previews"), filepath.Base(path), "diagnostic", flatImage.Bounds().Dx(), 90); err != nil {
						t.Fatal(err)
					}
					t.Logf("flat SHA256=%x metadata SHA256=%x canonical=%s static=%v", flatHash, metadataHash, decoded.CanvasSHA256, decoded.StaticCells)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || sha256.Sum256(after) != originalHash {
				t.Fatal("original template changed")
			}
		})
	}
}
