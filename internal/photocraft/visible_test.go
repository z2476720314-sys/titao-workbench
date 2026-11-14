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

func TestDiagnosticCanvasReusesExactPlacement(t *testing.T) {
	var replies strings.Builder
	for id := 1; id <= 10; id++ {
		fmt.Fprintf(&replies, "{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"{\\\"document\\\":1}\"}]}}\n", id)
	}
	var requests bytes.Buffer
	c := &client{enc: json.NewEncoder(&requests), dec: json.NewDecoder(strings.NewReader(replies.String()))}
	placement := placedCanvas{Surface: visibleSurface{Width: 100, Height: 200, Cell: 2}, Layer: 8, Scale: .37, X: 44.25, Y: 103.75}
	if err := c.replaceCanvas(placement, "pattern.png", "real-canvas.png"); err != nil {
		t.Fatal(err)
	}
	if err := c.replaceCanvas(placement, "alpha.png", "diagnostic-canvas.png"); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&requests)
	var places []map[string]any
	for dec.More() {
		var call struct {
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := dec.Decode(&call); err != nil {
			t.Fatal(err)
		}
		if call.Params.Name == "doc_place" {
			places = append(places, call.Params.Arguments)
		}
	}
	if len(places) != 2 {
		t.Fatal("missing actual/diagnostic placement")
	}
	for _, call := range places {
		center := call["center"].([]any)
		if call["scale"] != float64(37) || center[0] != float64(44.25) || center[1] != float64(103.75) || call["fit"] != false {
			t.Fatalf("geometry was recalculated: %v", call)
		}
	}
	if places[0]["path"] != "pattern.png" || places[1]["path"] != "alpha.png" {
		t.Fatal("source/mask placement mixed")
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
	var cached string
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
		if !metadata.FrameVerified || len(metadata.Placements) != 7 {
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
		if n == 0 {
			for _, p := range s.prepared {
				cached = p.Path
			}
		} else {
			if len(s.prepared) != 1 {
				t.Fatal("same template was recalibrated")
			}
			for _, p := range s.prepared {
				if p.Path != cached {
					t.Fatal("prepared cache not reused")
				}
			}
		}
		for _, p := range metadata.Placements {
			if p.Scale <= 0 || p.Scale > 1.000001 {
				t.Fatalf("invalid effective placement %+v", p)
			}
		}
	}
	cacheRoot := s.cacheRoot
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(cacheRoot); !os.IsNotExist(err) {
		t.Fatalf("private calibration cache not removed: %v", err)
	}
	after, err := os.ReadFile(template)
	if err != nil || sha256.Sum256(after) != before {
		t.Fatal("original template changed")
	}
}

func TestMakeUniqueCommandsKeepOriginalsHiddenAndNameEachCopy(t *testing.T) {
	args, names, err := uniqueCommands("source.tif", "prepared.psd", []layer{{ID: 18}, {ID: 20}})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] == names[1] {
		t.Fatalf("copy names=%v", names)
	}
	joined := strings.Join(args, " ")
	if strings.Count(joined, "layer.smartObjects.newSmartObjectViaCopy") != 2 || strings.Count(joined, `"visible":false`) != 2 {
		t.Fatal(joined)
	}
	for _, name := range names {
		if !strings.Contains(joined, name) {
			t.Fatal("independent copy not named")
		}
	}
	if args[0] != "run" || args[1] != "source.tif" || args[len(args)-2] != "--out" || args[len(args)-1] != "prepared.psd" {
		t.Fatal(args)
	}
	if _, _, err := uniqueCommands("same.tif", "same.tif", []layer{{ID: 18}}); err == nil {
		t.Fatal("preparation may overwrite template")
	}
}

func TestPreparedKeyUsesTemplateBytesGroupAndCell(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "template.tif")
	if err := os.WriteFile(file, []byte("template"), 0600); err != nil {
		t.Fatal(err)
	}
	req := psx.RenderRequest{TemplateCopy: file, Group: "插画", CellSize: 800}
	first, err := preparedKey(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"group", "cell", "bytes"} {
		changed := req
		switch change {
		case "group":
			changed.Group = "other"
		case "cell":
			changed.CellSize = 400
		case "bytes":
			if err := os.WriteFile(file, []byte("new template"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		key, err := preparedKey(changed)
		if err != nil || key == first {
			t.Fatalf("%s did not invalidate calibration: %v", change, err)
		}
	}
}

func TestStrictPlacementUsesContainBaseAndReturnsActualCenter(t *testing.T) {
	area := psx.VisibleArea{Cell: 2, Polygon: []psx.Point{{0, 0}, {1, 0}, {1, .9}, {0, .9}}}
	hull := []psx.Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	req := psx.RenderRequest{Fit: "cover", NoCrop: true, OffsetY: .2, AnchorX: 2, AnchorY: 1}
	applied, sx, x, y, err := strictPlacement(req, area, hull, 100, 200, 100, 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Cell != 2 || applied.Scale != 1 || sx != 1 || x != 50 || y > 130.000001 {
		t.Fatalf("actual placement=%+v scale=%v center=%v,%v", applied, sx, x, y)
	}
	data, err := json.Marshal(applied)
	if err != nil || len(data) == 0 {
		t.Fatal(err)
	}
}

func TestStrictRejectsEmptyScalesBeforeStartingEditor(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "missing-editor.exe"))
	if _, err := s.Render(psx.RenderRequest{NoCrop: true}); err == nil || !strings.Contains(err.Error(), "档位") {
		t.Fatalf("empty scales accepted or editor started: %v", err)
	}
}

func TestStrictNaturalContourDoesNotInheritLegacyBottomAnchor(t *testing.T) {
	area := psx.VisibleArea{Polygon: []psx.Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}}}
	hull := []psx.Point{{.5, 0}, {1, .5}, {.5, 1}, {0, .5}}
	req := psx.RenderRequest{NoCrop: true, AlignBottom: true}
	p, _, _, _, err := strictPlacement(req, area, hull, 100, 200, 100, 100, 1)
	if err != nil || p.OffsetY != 0 || p.AnchorUnmet {
		t.Fatalf("natural contour acquired a bottom anchor: %+v %v", p, err)
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
	owned, err := os.MkdirTemp(root, "owned-")
	if err != nil {
		t.Fatal(err)
	}
	caller := filepath.Join(root, "caller.txt")
	if err = os.WriteFile(caller, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	s := New("")
	s.cacheRoot = owned
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(owned); !os.IsNotExist(err) {
		t.Fatal("cache remains")
	}
	if _, err = os.Stat(caller); err != nil {
		t.Fatal("caller data removed")
	}
}
