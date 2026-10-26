package photocraft

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"titao/internal/fileutil"
	"titao/internal/imgutil"
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
