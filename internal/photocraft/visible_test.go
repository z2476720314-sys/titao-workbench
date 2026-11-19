package photocraft

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"titao/internal/fileutil"
	"titao/internal/psx"
)

func TestVisibleObjectsKeepInstancesAndExcludeHiddenAncestors(t *testing.T) {
	layers := []layer{{ID: 1, Name: "插画", Kind: "Group", Visible: true, Children: []layer{
		{ID: 2, Kind: "Smart Object", Visible: true, SmartContents: &contents{ID: 7}},
		{ID: 3, Kind: "Smart Object", Visible: false, SmartContents: &contents{ID: 7}},
		{ID: 4, Kind: "Smart Object", Visible: true, SmartContents: &contents{ID: 7}},
		{ID: 5, Kind: "Group", Visible: false, Children: []layer{{ID: 6, Kind: "Smart Object", Visible: true, SmartContents: &contents{ID: 8}}}},
	}}}
	objects, err := visibleObjects(layers, "插画")
	if err != nil || len(objects) != 2 || objects[0].ID != 2 || objects[1].ID != 4 {
		t.Fatalf("visible instances=%+v err=%v", objects, err)
	}
	layers[0].Visible = false
	if _, err := visibleObjects(layers, "插画"); err == nil {
		t.Fatal("hidden group was accepted")
	}
}

func TestDiagnosticAlphaEnhancesWeakPixelsAndPreservesStrongDetails(t *testing.T) {
	im := image.NewNRGBA(image.Rect(10, 20, 18, 21))
	for i, a := range []uint8{0, 7, 8, 16, 127, 128, 200, 255} {
		im.SetNRGBA(10+i, 20, color.NRGBA{uint8(20 + i), uint8(60 + i), uint8(100 + i), a})
	}
	mask := diagnosticAlpha(im)
	if mask.Bounds() != (image.Rect(0, 0, 8, 1)) {
		t.Fatal(mask.Bounds())
	}
	for i, want := range []color.NRGBA{{}, {}, {255, 0, 255, 255}, {255, 0, 255, 255}, {255, 0, 255, 255}, {25, 65, 105, 128}, {26, 66, 106, 200}, {27, 67, 107, 255}} {
		if got := mask.NRGBAAt(i, 0); got != want {
			t.Fatalf("pixel%d=%v want%v", i, got, want)
		}
	}
	weak := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	weak.SetNRGBA(1, 0, color.NRGBA{30, 40, 50, 1})
	weak.SetNRGBA(2, 0, color.NRGBA{60, 70, 80, 7})
	mask = diagnosticAlpha(weak)
	if mask.NRGBAAt(0, 0).A != 0 || mask.NRGBAAt(1, 0) != (color.NRGBA{255, 0, 255, 255}) || mask.NRGBAAt(2, 0) != (color.NRGBA{255, 0, 255, 255}) {
		t.Fatal("nonzero weak-alpha fallback failed")
	}
}

func TestDiagnosticCanvasReusesOriginalSharedSource(t *testing.T) {
	var replies strings.Builder
	for id := 1; id <= 2; id++ {
		fmt.Fprintf(&replies, "{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"{}\"}]}}\n", id)
	}
	var requests bytes.Buffer
	c := &client{enc: json.NewEncoder(&requests), dec: json.NewDecoder(strings.NewReader(replies.String()))}
	if err := c.replaceCanvas(8, "real-canvas.png"); err != nil {
		t.Fatal(err)
	}
	if err := c.replaceCanvas(8, "diagnostic-canvas.png"); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&requests)
	for _, path := range []string{"real-canvas.png", "diagnostic-canvas.png"} {
		var call struct {
			Params struct {
				Name      string
				Arguments map[string]any
			}
		}
		if err := dec.Decode(&call); err != nil {
			t.Fatal(err)
		}
		if call.Params.Name != "doc_replace_smart_object" || call.Params.Arguments["layer"] != float64(8) || call.Params.Arguments["path"] != path {
			t.Fatalf("source identity or canvas changed: %+v", call)
		}
	}
}

func TestStrictRenderRealCopiedTemplate(t *testing.T) {
	cli, template := os.Getenv("PHOTOCRAFT_VISIBLE_CLI"), os.Getenv("PHOTOCRAFT_VISIBLE_TEMPLATE")
	if cli == "" || template == "" {
		t.Skip("strict real template opt-in not configured")
	}
	original, err := os.ReadFile(template)
	if err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(original)
	root := t.TempDir()
	copy := filepath.Join(root, "source.tif")
	if err = fileutil.Copy(template, copy); err != nil {
		t.Fatal(err)
	}
	s := New(cli)
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	var realHashes, metadataHashes [][32]byte
	for n := 0; n < 2; n++ {
		dir := filepath.Join(root, fmt.Sprintf("render-%d", n))
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		pat := filepath.Join(dir, "pattern.png")
		im := image.NewNRGBA(image.Rect(0, 0, 200, 400))
		for y := 20; y < 380; y++ {
			for x := 10; x < 190; x++ {
				im.SetNRGBA(x, y, color.NRGBA{80, 100, 120, 16})
				if x >= 45 && x < 155 && y >= 85 && y < 315 {
					im.SetNRGBA(x, y, color.NRGBA{15, 240, 30, 255})
				}
			}
		}
		f, err := os.Create(pat)
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, im)
		closed := f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closed != nil {
			t.Fatal(closed)
		}
		req := psx.RenderRequest{TemplateCopy: copy, Pattern: pat, Group: "插画", Fit: "cover", NoCrop: true, Scales: []float64{1, .8}, OutDir: dir, CellSize: 800, AnchorY: 1}
		if n == 1 {
			if err = json.Unmarshal([]byte(`{"DiagnosticAlpha":true}`), &req); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = s.Render(req); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(req.MetadataPath(0))
		if err != nil {
			t.Fatal(err)
		}
		var metadata psx.RenderMetadata
		if err = json.Unmarshal(data, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.FrameVerified || !metadata.CanvasVerified || len(metadata.Placements) != 7 {
			t.Fatalf("metadata=%+v", metadata)
		}
		for i := range req.Scales {
			flat, err := os.ReadFile(req.FlatPath(i))
			if err != nil {
				t.Fatal(err)
			}
			meta, err := os.ReadFile(req.MetadataPath(i))
			if err != nil {
				t.Fatal(err)
			}
			diagnostic := filepath.Join(dir, fmt.Sprintf("diagnostic_flat_%d.png", i))
			if n == 0 {
				realHashes = append(realHashes, sha256.Sum256(flat))
				metadataHashes = append(metadataHashes, sha256.Sum256(meta))
				if _, err = os.Stat(diagnostic); !os.IsNotExist(err) {
					t.Fatal("default rendering created a diagnostic")
				}
			} else {
				if sha256.Sum256(flat) != realHashes[i] || sha256.Sum256(meta) != metadataHashes[i] {
					t.Fatal("diagnostic changed real flat or metadata")
				}
				if _, err = os.Stat(diagnostic); err != nil {
					t.Fatalf("requested diagnostic missing: %v", err)
				}
				f, err := os.Open(diagnostic)
				if err != nil {
					t.Fatal(err)
				}
				diagnosticImage, err := png.Decode(f)
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
				if diagnosticImage.Bounds() != image.Rect(0, 0, 800, 5600) {
					t.Fatal("diagnostic lost one of seven angles")
				}
				realImage, err := png.Decode(bytes.NewReader(flat))
				if err != nil {
					t.Fatal(err)
				}
				var magenta [7]int
				var strong, unchanged [7]int
				for y := 0; y < 5600; y++ {
					for x := 0; x < 800; x++ {
						c := color.NRGBAModel.Convert(diagnosticImage.At(x, y)).(color.NRGBA)
						original := color.NRGBAModel.Convert(realImage.At(x, y)).(color.NRGBA)
						// Template lighting can darken the diagnostic; require a magenta change.
						if int(c.R)-int(c.G) > 40 && int(c.B)-int(c.G) > 40 && int(c.B)-int(original.B) > 30 && c.A > 128 {
							magenta[y/800]++
						}
						if int(original.G)-int(original.R) > 60 && original.G > 140 && original.B < 110 {
							strong[y/800]++
							if c == original {
								unchanged[y/800]++
							}
						}
					}
				}
				t.Logf("candidate %d diagnostic magenta pixels per angle: %v", i, magenta)
				for cell, count := range magenta {
					if count < 10 {
						t.Fatalf("cell%d diagnostic magenta missing: %d", cell, count)
					}
					if strong[cell] < 10 || unchanged[cell]*100 < strong[cell]*90 {
						t.Fatalf("cell%d strong detail changed: original=%d same=%d", cell, strong[cell], unchanged[cell])
					}
				}
			}
		}
		for _, p := range metadata.Placements {
			if p.Scale <= 0 || p.Scale > 1.000001 {
				t.Fatalf("invalid effective placement %+v", p)
			}
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(template)
	if err != nil || sha256.Sum256(after) != before {
		t.Fatal("original template changed")
	}
}

func TestStrictRejectsEmptyScalesBeforeStartingEditor(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "missing-editor.exe"))
	if _, err := s.Render(psx.RenderRequest{NoCrop: true}); err == nil || !strings.Contains(err.Error(), "档位") {
		t.Fatalf("empty scales accepted or editor started: %v", err)
	}
}

func TestPrivateTempEnvAndClosePreserveCallerDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TEMP", "old-temp")
	t.Setenv("TMP", "old-tmp")
	var temp, tmp int
	for _, entry := range privateTempEnv(root) {
		key, value, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "TEMP") {
			temp++
			if value != root {
				t.Fatal(entry)
			}
		}
		if strings.EqualFold(key, "TMP") {
			tmp++
			if value != root {
				t.Fatal(entry)
			}
		}
	}
	if temp != 1 || tmp != 1 {
		t.Fatalf("TEMP=%d TMP=%d", temp, tmp)
	}

	caller := filepath.Join(root, "caller.txt")
	if err := os.WriteFile(caller, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := New("").Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(caller); err != nil {
		t.Fatal("caller data removed")
	}
}
