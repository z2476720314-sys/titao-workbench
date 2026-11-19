package photocraft

import (
	"encoding/json"
	"testing"
	"titao/internal/psx"
)

func canonicalMetadata(t *testing.T, data []byte) []byte {
	t.Helper()
	var m psx.RenderMetadata
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.ReviewQuarterTurns != nil {
		if len(m.ReviewQuarterTurns) != len(m.Placements) {
			t.Fatal("review orientation does not cover printed photos")
		}
		for _, p := range m.Placements {
			n, ok := m.ReviewQuarterTurns[p.Cell]
			if !ok || n < 0 || n > 3 {
				t.Fatal("invalid review orientation")
			}
		}
		for _, s := range m.StaticCells {
			if _, ok := m.ReviewQuarterTurns[s]; ok {
				t.Fatal("static scene pretends to project canvas")
			}
		}
	}
	m.ReviewQuarterTurns = nil
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
