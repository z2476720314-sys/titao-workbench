package photocraft

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"titao/internal/psx"
)

func TestStandalonePlacesOriginalInNativeSmartObjectWithoutReplacingCanvas(t *testing.T) {
	t.Setenv("PHOTOCRAFT_LEGACY_MCP_FIXTURE", "1")
	dir := t.TempDir()
	t.Setenv("PHOTOCRAFT_FIXTURE_CALLS", filepath.Join(dir, "calls.jsonl"))
	c := frozenCanvas(t, dir)
	tpl := filepath.Join(dir, "template.tif")
	if err := os.WriteFile(tpl, []byte("template"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(os.Args[0]).Render(psx.RenderRequest{TemplateCopy: tpl, Pattern: c.Path, Group: "插画", Scales: []float64{1}, NoCrop: true, OutDir: dir, CellSize: 100}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "doc_replace_smart_object") || !strings.Contains(string(data), "doc_place") || !strings.Contains(string(data), "layer.smartObjects.saveContents") {
		t.Fatalf("renderer still replaces padded source instead of placing original: %s", data)
	}
}
