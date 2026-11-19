package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"titao/internal/psx"
)

func TestStrictPlacementCannotUseUnverifiedOrMissingReceipt(t *testing.T) {
	req := psx.RenderRequest{NoCrop: true, Scales: []float64{1}, OutDir: t.TempDir()}
	if _, _, err := renderedPlacement(req, 0); err == nil {
		t.Fatal("missing safety receipt accepted")
	}
	os.WriteFile(filepath.Join(req.OutDir, "placement_0.json"), []byte(`{"frame_verified":false,"placements":[{"Cell":0,"Scale":1}]}`), 0600)
	if _, _, err := renderedPlacement(req, 0); err == nil {
		t.Fatal("unverified visible area accepted")
	}
}

func TestStrictReceiptRejectsDifferentPositionsWithinOneCase(t *testing.T) {
	req := psx.RenderRequest{NoCrop: true, Scales: []float64{1}, OutDir: t.TempDir(), CellSize: 80}
	if _, err := (&reviewPhotoshop{}).Render(req); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(req.MetadataPath(0))
	if err != nil {
		t.Fatal(err)
	}
	var meta psx.RenderMetadata
	if err := json.Unmarshal(b, &meta); err != nil {
		t.Fatal(err)
	}
	meta.Placements[1].OffsetY = .1
	b, _ = json.Marshal(meta)
	if err := os.WriteFile(req.MetadataPath(0), b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := renderedPlacement(req, 0); err == nil {
		t.Fatal("one phone case exported with different print positions")
	}
}
