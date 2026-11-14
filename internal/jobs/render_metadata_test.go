package jobs

import (
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
