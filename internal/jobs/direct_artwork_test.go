package jobs

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"titao/internal/cfg"
	"titao/internal/judge"
	"titao/internal/psx"
)

func TestBatchPublishesDirectArtworkManifestWithoutPaddingPNG(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ps := &reviewPhotoshop{}
	e.ps = ps
	tasks, output := patternGroupInputs(t)
	e.processPattern(context.Background(), Options{NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90}, cfg.Default(), judge.Config{}, tasks, output, 1, 2)
	if len(ps.requests) != 2 || tasks[0].Phase != PhaseDone {
		t.Fatalf("direct batch failed: %+v", tasks[0])
	}
	if filepath.Ext(tasks[0].CanvasPath) != ".json" {
		t.Fatalf("production still publishes a padded PNG: %s", tasks[0].CanvasPath)
	}
	data, err := os.ReadFile(tasks[0].CanvasPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["kind"] != "direct_artwork_v1" {
		t.Fatalf("not direct source evidence: %s", data)
	}
	plan, err := psx.LoadArtworkPlan(tasks[0].CanvasPath)
	if err != nil {
		t.Fatalf("published source or plan depended on deleted work: %v", err)
	}
	if plan.SourceWidth != 30 || plan.SourceHeight != 60 {
		t.Fatalf("batch retained outer transparent margins: %+v", plan)
	}
}

func TestKeepWholeDirectContentRetainsUnanchoredPlacement(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ps := &reviewPhotoshop{}
	e.ps = ps
	tasks, out := patternGroupInputs(t)
	im := image.NewNRGBA(image.Rect(0, 0, 40, 90))
	draw.Draw(im, im.Bounds(), image.NewUniform(color.NRGBA{R: 255, A: 255}), image.Point{}, draw.Src)
	f, err := os.Create(tasks[0].Pattern)
	if err != nil {
		t.Fatal(err)
	}
	if err = png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		task.KeepWhole = true
	}
	e.processPattern(context.Background(), Options{NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90}, cfg.Default(), judge.Config{}, tasks, out, 1, 2)
	if len(ps.requests) != 2 || tasks[0].Phase != PhaseDone {
		t.Fatalf("KeepWhole batch failed: %+v", tasks[0])
	}
	if p := ps.requests[0].Canvas.Placement; math.Abs(p.OffsetY) > 1e-12 || math.Abs(p.OffsetX) > 1e-12 {
		t.Fatalf("KeepWhole reintroduced cut/bottom anchors: %+v", p)
	}
}
