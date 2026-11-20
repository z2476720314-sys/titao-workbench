package photocraft

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"titao/internal/psx"
)

// The test executable doubles as a private MCP fixture; no installed editor is launched.
func TestMain(m *testing.M) {
	if os.Getenv("PHOTOCRAFT_LEGACY_MCP_FIXTURE") == "1" && len(os.Args) > 1 && os.Args[1] == "mcp" {
		os.Exit(serveLegacyMCPFixture())
	}
	os.Exit(m.Run())
}

func serveLegacyMCPFixture() int {
	dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	inner := 0
	for {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := dec.Decode(&req); err != nil {
			if err == io.EOF {
				return 0
			}
			return 2
		}
		if req.ID == 0 {
			continue
		}
		var result any = map[string]any{}
		if req.Method == "tools/call" {
			var value any = map[string]any{}
			a := req.Params.Arguments
			if path := os.Getenv("PHOTOCRAFT_FIXTURE_CALLS"); path != "" {
				f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					return 7
				}
				err = json.NewEncoder(f).Encode(map[string]any{"Name": req.Params.Name, "Arguments": a})
				f.Close()
				if err != nil {
					return 8
				}
			}
			switch req.Params.Name {
			case "command_run":
				if a["id"] == "layer.smartObjects.editContents" {
					inner = int(a["params"].(map[string]any)["layer"].(float64))
				}
			case "doc_inspect":
				if inner != 0 {
					width, height := 945, 1890
					if inner == 11 {
						switch os.Getenv("PHOTOCRAFT_FIXTURE_MAPPING") {
						case "white":
							width = 946
						case "scaled":
							width, height = 1890, 3780
						case "invalid":
							width = 1000
						}
					}
					value = document{Width: width, Height: height}
				} else {
					value = document{Width: 100, Height: 300, Layers: []layer{{Name: "插画", Kind: "Group", Visible: true, Children: []layer{
						{ID: 10, Kind: "Smart Object", Visible: true, SmartContents: &contents{ID: 7}, Bounds: []float64{0, 0, 100, 100}},
						{ID: 11, Kind: "Smart Object", Visible: true, SmartContents: &contents{ID: 8}, Bounds: []float64{0, 100, 100, 100}},
						{ID: 12, Kind: "Smart Object", Visible: true, SmartContents: &contents{ID: 7}, Bounds: []float64{0, 200, 100, 100}},
					}}}}
				}
			case "doc_close", "doc_select":
				inner = 0
			case "doc_new":
				value = map[string]int{"document": 5}
			case "doc_place":
				value = map[string]int{"layer": 20}
			case "doc_save":
				if filepath.Ext(a["path"].(string)) == ".pcraft" {
					if err := saveOrientationFixture(a["path"].(string)); err != nil {
						return 9
					}
					break
				}
				f, err := os.Create(a["path"].(string))
				if err != nil {
					return 3
				}
				err = png.Encode(f, image.NewNRGBA(image.Rect(0, 0, 100, 300)))
				f.Close()
				if err != nil {
					return 4
				}
			}
			data, err := json.Marshal(value)
			if err != nil {
				return 5
			}
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": string(data)}}}
		}
		if enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}) != nil {
			return 6
		}
	}
}

func TestStandaloneRenderDirectCandidatesReportPairedCutEdgeConstraints(t *testing.T) {
	t.Setenv("PHOTOCRAFT_LEGACY_MCP_FIXTURE", "1")
	dir := t.TempDir()
	pattern := filepath.Join(dir, "pattern.png")
	if err := os.WriteFile(filepath.Join(dir, "fixture.tif"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	im := image.NewNRGBA(image.Rect(0, 0, 100, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 100; x++ {
			im.SetNRGBA(x, y, color.NRGBA{80, 120, 160, 255})
		}
	}
	if err := savePNG(pattern, im); err != nil {
		t.Fatal(err)
	}
	req := psx.RenderRequest{TemplateCopy: filepath.Join(dir, "fixture.tif"), Pattern: pattern, Group: "插画", Fit: psx.FitCover, Scales: []float64{1, 1.1}, OffsetX: .3, OffsetY: .2, OutDir: dir, CellSize: 100}
	if _, err := New(os.Args[0]).Render(req); err != nil {
		t.Fatal(err)
	}
	processLog, err := os.ReadFile(filepath.Join(dir, "photocraft-processes.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	started, exited := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(processLog)), "\n") {
		var event struct {
			Event string
			PID   int
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil || event.PID <= 0 {
			t.Fatal("process identity not recorded")
		}
		if event.Event == "started" {
			started = event.PID
		}
		if event.Event == "exited" {
			exited = event.PID
		}
	}
	if started == 0 || started != exited {
		t.Fatal("private MCP did not exit before Render returned")
	}
	for index := range req.Scales {
		data, err := os.ReadFile(req.MetadataPath(index))
		if err != nil {
			t.Fatal(err)
		}
		var metadata psx.RenderMetadata
		if err := json.Unmarshal(data, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.FrameVerified || len(metadata.Placements) != 3 || metadata.CanvasSHA256 == "" || metadata.CanvasWidth != 945 || metadata.CanvasHeight != 1890 {
			t.Fatalf("metadata=%+v", metadata)
		}
		for cell, p := range metadata.Placements {
			if p.Cell != cell || p.Scale != 1 || math.Abs(p.VerticalStretch()-req.Scales[index]) > 1e-12 || math.Abs(p.OffsetX) > 1e-12 || math.Abs(p.OffsetY-(1-req.Scales[index])/2) > 1e-12 || !p.Constrained {
				t.Fatalf("shared cell lost actual canonical placement: %+v", p)
			}
		}
		artifact, err := os.ReadFile(req.ArtworkPlanPath(index))
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(artifact)) != metadata.CanvasSHA256 {
			t.Fatal("metadata not bound to artifact")
		}
	}
}
