package photocraft

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"titao/internal/imgutil"
	"titao/internal/printcanvas"
	"titao/internal/psx"
)

// Explicit opt-in; original inputs are read only, every editor process is private and closed.
func TestDirectArtworkLiveTemplates(t *testing.T) {
	cli, source, templates, out := os.Getenv("PHOTOCRAFT_TEST_CLI"), os.Getenv("PHOTOCRAFT_DIRECT_SOURCE"), os.Getenv("PHOTOCRAFT_DIRECT_TEMPLATES"), os.Getenv("PHOTOCRAFT_DIRECT_OUT")
	if cli == "" || source == "" || templates == "" || out == "" {
		t.Skip("direct artwork live inputs not configured")
	}
	if err := os.MkdirAll(out, 0700); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(bytes)
	pr, err := imgutil.Normalize(source, filepath.Join(out, "analysis.png"), false)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := printcanvas.Plan(source, filepath.Join(out, "plan", "artwork_plan.json"), psx.RenderRequest{NoCrop: true, Fit: psx.FitAuto, AnchorX: pr.AnchorX, AnchorY: pr.AnchorY, ContentBounds: pr.ContentBounds}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Placement.AnchorUnmet || plan.Placement.AnchorImpossible {
		t.Fatalf("cut edges not satisfied: %+v", plan.Placement)
	}
	original, err := imgutil.Decode(source)
	if err != nil {
		t.Fatal(err)
	}
	content, err := imgutil.Decode(plan.SourcePath())
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < plan.SourceHeight; y++ {
		for x := 0; x < plan.SourceWidth; x++ {
			a := color.NRGBA64Model.Convert(original.At(x+plan.CropBox[0], y+plan.CropBox[1]))
			b := color.NRGBA64Model.Convert(content.At(x, y))
			if a != b {
				t.Fatalf("content RGBA changed at %d,%d", x, y)
			}
		}
	}
	data, _ := json.MarshalIndent(map[string]any{"source_sha256": fmt.Sprintf("%x", hash), "detection": pr, "plan": plan}, "", "  ")
	if err = os.WriteFile(filepath.Join(out, "source-plan.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	for i, tpl := range strings.Split(templates, ";") {
		t.Run(filepath.Base(tpl), func(t *testing.T) {
			before, err := os.ReadFile(tpl)
			if err != nil {
				t.Fatal(err)
			}
			tplHash := sha256.Sum256(before)
			dir := filepath.Join(out, fmt.Sprintf("template-%d", i))
			if err = os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			copy := filepath.Join(dir, "input.tif")
			if err = stageFile(tpl, copy); err != nil {
				t.Fatal(err)
			}
			s := New(cli)
			defer s.Close()
			req := psx.RenderRequest{Canvas: plan, Pattern: source, TemplateCopy: copy, Group: "插画", NoCrop: true, Scales: []float64{1}, OutDir: dir, CellSize: 800, DiagnosticAlpha: true}
			log, err := s.Render(req)
			if err != nil {
				t.Fatal(err)
			}
			t.Log(log)
			proof, err := psx.LoadArtworkPlan(req.ArtworkPlanPath(0))
			if err != nil || proof.SHA256 != plan.SHA256 {
				t.Fatalf("local plan invalid: %v", err)
			}
			if _, err := os.Stat(req.PrintCanvasPath(0)); !os.IsNotExist(err) {
				t.Fatal("direct render wrote padding PNG")
			}
			// Inspect the same native placement helper without baking any intermediate PNG.
			c, close, err := s.connect(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer close()
			if err = c.call("doc_open", map[string]any{"path": copy}, nil); err != nil {
				t.Fatal(err)
			}
			var parent document
			if err = c.call("doc_inspect", map[string]any{}, &parent); err != nil {
				t.Fatal(err)
			}
			objects, err := visibleObjects(parent.Layers, "插画")
			if err != nil {
				t.Fatal(err)
			}
			seen := map[int]bool{}
			for _, l := range objects {
				if seen[l.SmartContents.ID] {
					continue
				}
				seen[l.SmartContents.ID] = true
				if err = c.command("layer.smartObjects.editContents", map[string]any{"layer": l.ID}, nil); err != nil {
					t.Fatal(err)
				}
				var native document
				if err = c.call("doc_inspect", map[string]any{}, &native); err != nil {
					t.Fatal(err)
				}
				if err = c.call("doc_close", map[string]any{}, nil); err != nil {
					t.Fatal(err)
				}
				if err = c.call("doc_select", map[string]any{"index": 0}, nil); err != nil {
					t.Fatal(err)
				}
				if err = c.placeArtwork(l.ID, native.Width, native.Height, proof.SourcePath(), proof); err != nil {
					t.Fatal(err)
				}
				if err = c.command("layer.smartObjects.editContents", map[string]any{"layer": l.ID}, nil); err != nil {
					t.Fatal(err)
				}
				var placed document
				if err = c.call("doc_inspect", map[string]any{}, &placed); err != nil {
					t.Fatal(err)
				}
				if placed.Width != native.Width || placed.Height != native.Height || len(placed.Layers) != 1 || placed.Layers[0].Kind != "Smart Object" {
					t.Fatalf("native contents changed shape: %+v", placed)
				}
				b, _ := json.MarshalIndent(placed, "", "  ")
				if err = os.WriteFile(filepath.Join(dir, fmt.Sprintf("native-%d.json", l.SmartContents.ID)), b, 0600); err != nil {
					t.Fatal(err)
				}
				if err = c.call("doc_save", map[string]any{"path": filepath.Join(dir, fmt.Sprintf("native-%d.png", l.SmartContents.ID))}, nil); err != nil {
					t.Fatal(err)
				}
				if err = c.call("doc_close", map[string]any{}, nil); err != nil {
					t.Fatal(err)
				}
				if err = c.call("doc_select", map[string]any{"index": 0}, nil); err != nil {
					t.Fatal(err)
				}
			}
			after, err := os.ReadFile(tpl)
			if err != nil || sha256.Sum256(after) != tplHash {
				t.Fatal("original template changed")
			}
		})
	}
	after, err := os.ReadFile(source)
	if err != nil || sha256.Sum256(after) != hash {
		t.Fatal("original artwork changed")
	}
}
