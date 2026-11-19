package jobs

import (
	"encoding/json"
	"os"
	"testing"

	"titao/internal/psx"
)

func TestStrictReceiptRejectsUnequalOrExcessiveStretch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		both    bool
		stretch float64
	}{{"different views", false, .98}, {"excessive", true, 1.2}, {"negative", true, -1}} {
		t.Run(tc.name, func(t *testing.T) {
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
			meta.Placements[1].StretchY = tc.stretch
			if tc.both {
				meta.Placements[0].StretchY = tc.stretch
			}
			b, err = json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(req.MetadataPath(0), b, 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := renderedPlacement(req, 0); err == nil {
				t.Fatal("invalid stretch receipt accepted")
			}
		})
	}
}
