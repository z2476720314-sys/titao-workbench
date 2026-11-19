package photocraft

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"titao/internal/fileutil"
	"titao/internal/imgutil"
	"titao/internal/printcanvas"
	"titao/internal/psx"
)

// Explicit opt-in runs the real installed engine against caller-selected templates.
func TestRealTemplates(t *testing.T) {
	cli, templates, pattern := os.Getenv("PHOTOCRAFT_TEST_CLI"), os.Getenv("PHOTOCRAFT_TEST_TEMPLATES"), os.Getenv("PHOTOCRAFT_TEST_PATTERN")
	if cli == "" || templates == "" || pattern == "" {
		t.Skip("real PhotoCraft inputs not configured")
	}
	paths, err := filepath.Glob(filepath.Join(templates, "*.tif"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("templates: %v (%d)", err, len(paths))
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(before)
			dir := t.TempDir()
			tpl := filepath.Join(dir, "template.tif")
			pat := filepath.Join(dir, "pattern.png")
			if err := fileutil.Copy(path, tpl); err != nil {
				t.Fatal(err)
			}
			if _, err := imgutil.Normalize(pattern, pat, true); err != nil {
				t.Fatal(err)
			}
			s := New(cli)
			info, err := s.ProbeTemplate(tpl)
			if err != nil {
				t.Fatal(err)
			}
			req := psx.RenderRequest{TemplateCopy: tpl, Pattern: pat, Group: "插画", Fit: "auto", NoCrop: true, Scales: []float64{0.9}, OutDir: dir}
			log, err := s.Render(req)
			if err != nil {
				t.Fatal(err)
			}
			im, err := imgutil.Decode(req.FlatPath(0))
			if err != nil {
				t.Fatal(err)
			}
			if im.Bounds().Dx() != info.Width || im.Bounds().Dy() != info.Height {
				t.Fatal("render dimensions changed")
			}
			after, err := os.ReadFile(path)
			if err != nil || sha256.Sum256(after) != hash {
				t.Fatal("original template changed")
			}
			t.Log(log)
		})
	}
}

// A single frozen artifact must project to all four supplied templates, including static stair photos.
func TestRealSharedCanvasTemplates(t *testing.T) {
	cli, templates := os.Getenv("PHOTOCRAFT_TEST_CLI"), os.Getenv("PHOTOCRAFT_TEST_TEMPLATES")
	if cli == "" || templates == "" {
		t.Skip("real PhotoCraft inputs not configured")
	}
	paths, err := filepath.Glob(filepath.Join(templates, "*.tif"))
	if err != nil || len(paths) != 4 {
		t.Fatalf("four real templates required: %d %v", len(paths), err)
	}
	root := t.TempDir()
	if evidence := os.Getenv("PHOTOCRAFT_TEST_EVIDENCE"); evidence != "" {
		root = evidence
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	pattern := filepath.Join(root, "shared-pattern.png")
	im := image.NewNRGBA(image.Rect(0, 0, 315, 630))
	for y := 10; y < 620; y++ {
		for x := 10; x < 305; x++ {
			c := color.NRGBA{30, 210, 60, 255}
			if y < 80 {
				c = color.NRGBA{220, 40, 40, 255}
			}
			if y > 550 {
				c = color.NRGBA{40, 70, 230, 255}
			}
			if x < 25 || x > 289 {
				c = color.NRGBA{80, 100, 120, 16}
			}
			im.SetNRGBA(x, y, c)
		}
	}
	if err := savePNG(pattern, im); err != nil {
		t.Fatal(err)
	}
	policy := psx.RenderRequest{NoCrop: true, Fit: psx.FitAuto}
	canvas, err := printcanvas.Build(pattern, filepath.Join(root, "shared-canvas.png"), policy, .9)
	if err != nil {
		t.Fatal(err)
	}
	for index, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(before)
			dir := filepath.Join(root, fmt.Sprintf("template-%d", index))
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			tpl := filepath.Join(dir, "template.tif")
			if err := fileutil.Copy(path, tpl); err != nil {
				t.Fatal(err)
			}
			req := psx.RenderRequest{TemplateCopy: tpl, Canvas: canvas, Pattern: "not-read.png", Group: "插画", NoCrop: true, Scales: []float64{1}, OutDir: dir, DiagnosticAlpha: true}
			s := New(cli)
			defer s.Close()
			log, err := s.Render(req)
			if err != nil {
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
			if metadata.CanvasSHA256 != canvas.SHA256 || !metadata.CanvasVerified || metadata.FrameVerified {
				t.Fatalf("metadata=%+v", metadata)
			}
			for _, p := range metadata.Placements {
				want := canvas.Placement
				want.Cell = p.Cell
				if p != want {
					t.Fatalf("placement changed per photo: %+v", p)
				}
			}
			if filepath.Base(path) == "阶梯黑色模板.tif" {
				if len(metadata.Placements) != 4 || fmt.Sprint(metadata.StaticCells) != "[2 4]" {
					t.Fatalf("static cells=%+v", metadata)
				}
				base, err := imgutil.Decode(filepath.Join(dir, "template-baseline.png"))
				if err != nil {
					t.Fatal(err)
				}
				flat, err := imgutil.Decode(req.FlatPath(0))
				if err != nil {
					t.Fatal(err)
				}
				if err := verifyStaticCells(base, flat, metadata.StaticCells, 800); err != nil {
					t.Fatal(err)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || sha256.Sum256(after) != hash {
				t.Fatal("original template changed")
			}
			t.Logf("%s SHA256=%s", log, metadata.CanvasSHA256)
		})
	}
}
