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

func TestTransparentMarginsAllowMovingVisibleContent(t *testing.T) {
	var req psx.RenderRequest
	if err := json.Unmarshal([]byte(`{"Fit":"cover","NoCrop":true,"OffsetY":0.2,"ContentBounds":[0,0.1,1,0.75]}`), &req); err != nil {
		t.Fatal(err)
	}
	x, y := placementCenter(req, 100, 200, 100, 200)
	if x != 50 || y != 140 {
		t.Fatalf("transparent bottom margin must allow down: (%v,%v)", x, y)
	}
	req.OffsetY = 0.5
	_, y = placementCenter(req, 100, 200, 100, 200)
	if y != 150 {
		t.Fatalf("visible content must stop at bottom: %v", y)
	}
	sx, sy, err := placementScale(req, 0.8, 100, 200, 100, 200)
	if err != nil || sx != 0.8 || sy != 0.8 {
		t.Fatalf("camera avoidance must permit proportional shrinking: %v %v %v", sx, sy, err)
	}
	sx, sy, err = placementScale(req, 1.3, 100, 200, 100, 200)
	if err != nil || sx != 1 || sy != 1 {
		t.Fatalf("no_crop must protect visible edges even with cover: %v %v %v", sx, sy, err)
	}
	if err := json.Unmarshal([]byte(`{"Fit":"cover","NoCrop":true,"ContentBounds":[0.1,0.1,0.9,0.9]}`), &req); err != nil {
		t.Fatal(err)
	}
	sx, sy, err = placementScale(req, 0.9, 100, 200, 200, 200)
	if err != nil || sx != 0.5625 || sy != 0.5625 {
		t.Fatalf("shrinking must apply to the effective no-crop baseline: %v %v %v", sx, sy, err)
	}
}

func TestCutEdgeStaysOnShellAfterShrinking(t *testing.T) {
	var req psx.RenderRequest
	if err := json.Unmarshal([]byte(`{"Fit":"cover","NoCrop":true,"ContentBounds":[0,0.1,1,0.75],"AnchorX":-1,"AnchorY":1}`), &req); err != nil {
		t.Fatal(err)
	}
	x, y := placementCenter(req, 100, 200, 80, 160)
	if x != 40 || y != 160 {
		t.Fatalf("cut left/bottom edges must remain attached after shrink: (%v,%v)", x, y)
	}
	req.AnchorX = 2
	sx, sy, err := placementScale(req, .8, 100, 200, 100, 200)
	if err != nil || sx != 1 || sy != 1 {
		t.Fatalf("opposing cut sides must keep full width: %v,%v,%v", sx, sy, err)
	}
	req.ContentBounds = [4]float64{0, .1, 1, .9}
	if _, _, err := placementScale(req, 1, 100, 200, 100, 300); err == nil {
		t.Fatal("incompatible full-width and no-crop constraints must be explicit")
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
