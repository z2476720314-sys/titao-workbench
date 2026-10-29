package photocraft

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"titao/internal/psx"
)

func TestPlacementPreservesFitAndNoCrop(t *testing.T) {
	for _, tc := range []struct {
		fit    string
		scale  float64
		noCrop bool
		x, y   float64
	}{
		{"auto", 1, true, 1, 1}, {"contain", 1.3, true, 1, 1}, {"cover", 1, true, 2, 2}, {"stretch", 1, true, 1, 2}, {"none", 0.8, false, 0.8, 0.8},
	} {
		x, y, err := placementScale(psx.RenderRequest{Fit: tc.fit, NoCrop: tc.noCrop}, tc.scale, 100, 200, 100, 100)
		if err != nil || x != tc.x || y != tc.y {
			t.Fatalf("%+v got (%v,%v,%v)", tc, x, y, err)
		}
	}
}

func TestContainCentersAndBoundsOffsets(t *testing.T) {
	x, y := placementCenter(psx.RenderRequest{Fit: "auto", NoCrop: true}, 100, 200, 100, 160)
	if x != 50 || y != 100 {
		t.Fatalf("center anchor: (%v,%v)", x, y)
	}
	var bottomReq psx.RenderRequest
	if err := json.Unmarshal([]byte(`{"Fit":"auto","NoCrop":true,"AlignBottom":true}`), &bottomReq); err != nil {
		t.Fatal(err)
	}
	x, y = placementCenter(bottomReq, 100, 200, 100, 160)
	if x != 50 || y != 120 {
		t.Fatalf("bottom content must meet canvas bottom: (%v,%v)", x, y)
	}
	x, y = placementCenter(psx.RenderRequest{Fit: "auto", NoCrop: true, OffsetX: 0.3, OffsetY: 0.2}, 100, 200, 100, 160)
	if x != 50 || y != 120 {
		t.Fatalf("no_crop must limit offsets: (%v,%v)", x, y)
	}
}

func TestCoverFineTuningCannotExposeEmptyBorders(t *testing.T) {
	for _, size := range [][2]float64{{120, 240}, {100, 200}} {
		x, y := placementCenter(psx.RenderRequest{Fit: "cover", OffsetX: .3, OffsetY: .2}, 100, 200, size[0], size[1])
		if x-size[0]/2 > 0 || x+size[0]/2 < 100 || y-size[1]/2 > 0 || y+size[1]/2 < 200 {
			t.Fatalf("cover exposed an edge: center=(%v,%v) size=%v", x, y, size)
		}
	}
}

func TestObjectsDeduplicateByContentsNotFileName(t *testing.T) {
	layers := []layer{{ID: 1, Name: "插画", Kind: "Group", Children: []layer{
		{ID: 2, Kind: "Smart Object", SmartContents: &contents{ID: 7}},
		{ID: 3, Kind: "Smart Object", SmartContents: &contents{ID: 7}},
		{ID: 4, Kind: "Smart Object", SmartContents: &contents{ID: 8}},
	}}}
	got, err := findObjects(layers, "插画")
	if err != nil || len(got) != 2 || got[0].ID != 2 || got[1].ID != 4 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := findObjects(layers, "不存在"); err == nil {
		t.Fatal("must reject absent group")
	}
	if countObjects(layers[0].Children) != 3 {
		t.Fatal("shared instances must each count as a placement")
	}
}

func TestToolResponseErrorsDoNotBecomeSuccess(t *testing.T) {
	for _, tc := range []struct {
		reply string
		fail  bool
	}{
		{`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"document\":0}"}],"isError":false}}`, false},
		{`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"permission denied"}],"isError":true}}`, true},
	} {
		var sent bytes.Buffer
		c := &client{enc: json.NewEncoder(&sent), dec: json.NewDecoder(strings.NewReader(tc.reply))}
		var out struct{ Document int }
		err := c.call("doc_new", map[string]any{}, &out)
		if (err != nil) != tc.fail {
			t.Fatalf("err=%v fail=%v", err, tc.fail)
		}
		if !strings.Contains(sent.String(), `"method":"tools/call"`) {
			t.Fatal(sent.String())
		}
	}
}
