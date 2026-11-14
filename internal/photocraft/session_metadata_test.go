package photocraft

import (
	"encoding/json"
	"image"
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
			switch req.Params.Name {
			case "command_run":
				if a["id"] == "layer.smartObjects.editContents" {
					inner = int(a["params"].(map[string]any)["layer"].(float64))
				}
			case "doc_inspect":
				if inner != 0 {
					width := 100
					if inner == 11 {
						width = 200
					}
					value = document{Width: width, Height: 200}
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

func TestLegacyRenderReportsClampedPerCellPlacementForSharedContents(t *testing.T) {
	t.Setenv("PHOTOCRAFT_LEGACY_MCP_FIXTURE", "1")
	dir := t.TempDir()
	pattern := filepath.Join(dir, "pattern.png")
	f, err := os.Create(pattern)
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(f, image.NewNRGBA(image.Rect(0, 0, 100, 200)))
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	req := psx.RenderRequest{TemplateCopy: filepath.Join(dir, "fixture.tif"), Pattern: pattern, Group: "插画", Fit: psx.FitCover, Scales: []float64{1, 1.2}, OffsetX: .3, OffsetY: .2, OutDir: dir, CellSize: 100}
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
			Event string `json:"event"`
			PID   int    `json:"pid"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil || event.PID <= 0 {
			t.Fatal("private fixture process identity was not recorded")
		}
		if event.Event == "started" {
			started = event.PID
		}
		if event.Event == "exited" {
			exited = event.PID
		}
	}
	if started == 0 || exited != started {
		t.Fatal("private MCP fixture did not exit before Render returned")
	}
	for index, wantOffset := range []float64{0, .1} {
		data, err := os.ReadFile(req.MetadataPath(index))
		if err != nil {
			t.Fatal("legacy rendering must write actual placement metadata: ", err)
		}
		var metadata psx.RenderMetadata
		if err := json.Unmarshal(data, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.FrameVerified || len(metadata.Placements) != 3 {
			t.Fatalf("legacy shared sources need three unverified cell records: %+v", metadata)
		}
		seen := map[int]psx.AppliedPlacement{}
		for _, p := range metadata.Placements {
			seen[p.Cell] = p
		}
		for _, cell := range []int{0, 2} {
			p, ok := seen[cell]
			if !ok || math.Abs(p.Scale-req.Scales[index]) > 1e-12 || math.Abs(p.OffsetX-wantOffset) > 1e-12 || math.Abs(p.OffsetY-wantOffset) > 1e-12 {
				t.Fatalf("shared cell %d reports requested instead of clamped placement: %+v", cell, p)
			}
		}
		if p, ok := seen[1]; !ok || math.Abs(p.OffsetX-wantOffset) > 1e-12 || math.Abs(p.OffsetY-.2) > 1e-12 {
			t.Fatalf("independent cell lost its different actual placement: %+v", p)
		}
	}
}

func TestLegacyPlacementUsesSelectedFitBasisAndReportsAnchorScale(t *testing.T) {
	for _, fit := range []string{psx.FitAuto, psx.FitContain, psx.FitCover, psx.FitNone, psx.FitStretch} {
		req := psx.RenderRequest{Fit: fit}
		sx, sy, err := placementScale(req, .8, 100, 200, 100, 100)
		if err != nil {
			t.Fatal(err)
		}
		x, y := placementCenter(req, 100, 200, 100*sx, 100*sy)
		p, err := legacyPlacement(req, .8, 100, 200, 100, 100, sx, sy, x, y)
		if err != nil || math.Abs(p.Scale-.8) > 1e-12 {
			t.Fatalf("fit %s changed its scale basis: %+v %v", fit, p, err)
		}
	}
	req := psx.RenderRequest{Fit: psx.FitCover, AnchorX: 2, ContentBounds: [4]float64{.1, 0, .9, 1}}
	sx, sy, err := placementScale(req, .8, 100, 200, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	x, y := placementCenter(req, 100, 200, 100*sx, 200*sy)
	p, err := legacyPlacement(req, .8, 100, 200, 100, 200, sx, sy, x, y)
	if err != nil || math.Abs(p.Scale-1.25) > 1e-12 || !p.Constrained {
		t.Fatalf("anchor override was reported as requested scale: %+v %v", p, err)
	}
}

func TestLegacyPlacementReportsAbsoluteCenterForBottomAlignment(t *testing.T) {
	req := psx.RenderRequest{Fit: psx.FitAuto, AlignBottom: true}
	x, y := placementCenter(req, 100, 200, 100, 100)
	p, err := legacyPlacement(req, 1, 100, 200, 100, 100, 1, 1, x, y)
	if err != nil || p.OffsetX != 0 || p.OffsetY != .25 {
		t.Fatalf("bottom anchor center was not reported: %+v %v", p, err)
	}
	// Subsequent rounds consume the actual centered offset, without reapplying the old bottom baseline.
	req.AlignBottom, req.OffsetX, req.OffsetY = false, p.OffsetX, p.OffsetY
	nx, ny := placementCenter(req, 100, 200, 100, 100)
	if nx != x || ny != y {
		t.Fatal("feeding the reported centered offset moved an unchanged placement")
	}
}

func TestLegacyPlacementRejectsUnrepresentableStretchAndInvalidCellMapping(t *testing.T) {
	req := psx.RenderRequest{Fit: psx.FitStretch, AnchorX: 2, ContentBounds: [4]float64{0, 0, 1, 1}}
	sx, sy, err := placementScale(req, .8, 100, 200, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyPlacement(req, .8, 100, 200, 100, 100, sx, sy, 50, 100); err == nil {
		t.Fatal("anisotropic fit cannot pretend to have a single actual scale multiplier")
	}
	for _, bounds := range [][]float64{nil, {0, 0, 100, 0}, {0, math.NaN(), 100, 100}, {0, 300, 100, 100}} {
		doc := document{Width: 100, Height: 300, Layers: []layer{{Kind: "Group", Name: "插画", Visible: true, Children: []layer{{Kind: "Smart Object", Visible: true, SmartContents: &contents{ID: 7}, Bounds: bounds}}}}}
		if _, err := legacyMetadata(psx.RenderRequest{Group: "插画"}, doc, map[int]psx.AppliedPlacement{7: {Scale: 1}}); err == nil {
			t.Fatalf("invalid cell mapping was accepted: %v", bounds)
		}
	}
}
