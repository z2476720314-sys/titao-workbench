package jobs

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"titao/internal/cfg"
	"titao/internal/judge"
	"titao/internal/psx"
)

func TestWholeImagePolicySurvivesPlanningQueueAndRendersWithoutEdgeAnchors(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ps := &reviewPhotoshop{}
	e.ps = ps
	dir := t.TempDir()
	source := filepath.Join(dir, "scene.png")
	im := image.NewNRGBA(image.Rect(0, 0, 40, 80))
	draw.Draw(im, im.Bounds(), image.NewUniform(color.NRGBA{R: 150, G: 80, B: 20, A: 255}), image.Point{}, draw.Src)
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
	f.Close()
	profile := filepath.Dir(e.store.Path())
	if _, err = RegisterPreparedArtwork(profile, source, source, ""); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(profile, "prepared-artwork.json")
	setPolicy := func(keep bool) {
		t.Helper()
		b, err := os.ReadFile(index)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err = json.Unmarshal(b, &document); err != nil {
			t.Fatal(err)
		}
		for _, v := range document["entries"].(map[string]any) {
			v.(map[string]any)["keep_whole"] = keep
		}
		b, err = json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(index, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	setPolicy(true)
	template := filepath.Join(dir, "template.tif")
	if err = os.WriteFile(template, []byte("fixture template"), 0600); err != nil {
		t.Fatal(err)
	}
	opt := Options{Patterns: []string{source}, Templates: []string{template}, OutputDir: filepath.Join(dir, "out"), Fit: "auto", Auto: true, Scales: []float64{1.4}, CellSize: 80, JPGQuality: 90, MaxRounds: 1}
	planned, err := e.Plan(opt)
	if err != nil || len(planned) != 1 {
		t.Fatalf("plan: %v %d", err, len(planned))
	}
	policyJSON, err := json.Marshal(planned[0])
	if err != nil || !strings.Contains(string(policyJSON), `"keepWhole":true`) {
		t.Fatal("approved whole-image policy was not captured")
	}
	q := NewQueue(opt, planned)
	queuePath := filepath.Join(dir, "saved-queue.json")
	if err = SaveQueue(queuePath, q); err != nil {
		t.Fatal(err)
	}
	q, err = LoadQueue(queuePath)
	if err != nil {
		t.Fatal(err)
	}
	restored := q.ToTasks()[0]
	policyJSON, _ = json.Marshal(restored)
	if !strings.Contains(string(policyJSON), `"keepWhole":true`) {
		t.Fatal("queue lost whole-image policy")
	}
	keyBefore, err := completionKey(Options{NoCrop: true}, e.store.Get(), restored)
	if err != nil {
		t.Fatal(err)
	}
	setPolicy(false)
	other, err := e.Plan(opt)
	if err != nil || len(other) != 1 {
		t.Fatal("could not plan updated policy")
	}
	keyAfter, err := completionKey(Options{NoCrop: true}, e.store.Get(), other[0])
	if err != nil {
		t.Fatal(err)
	}
	if keyBefore == keyAfter {
		t.Fatal("whole-image policy change reused another placement's completion key")
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		messages, _ := json.Marshal(body["messages"])
		for _, want := range []string{"用户已选择保留整幅画面", "不强制贴壳边", "no_crop=true"} {
			if !strings.Contains(string(messages), want) {
				t.Errorf("missing whole-image instruction %q", want)
			}
		}
		if strings.Contains(string(messages), "anchor_x=2") {
			t.Error("whole-image task was given paired cut-edge instructions")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"good":true,"camera_clear":true,"score":96,"problem":"无","dx":0,"dy":0,"scale_ratio":1}`}}}})
	}))
	defer server.Close()
	def := cfg.Default()
	jc := judge.Config{BaseURL: server.URL, Model: "fixture", Timeout: time.Second, MaxTokens: 100}
	e.processOne(context.Background(), opt, def, jc, restored, opt.OutputDir, 1, 1)
	if restored.Phase != PhaseDone || calls != 1 || len(ps.requests) != 1 {
		t.Fatalf("whole-image task failed: phase=%s model=%d renders=%d", restored.Phase, calls, len(ps.requests))
	}
	req := ps.requests[0]
	if !req.NoCrop || req.Fit != psx.FitContain || req.AnchorX != 0 || req.AnchorY != 0 || req.AlignBottom || req.ContentBounds != [4]float64{0, 0, 1, 1} {
		t.Fatalf("unsafe whole-image render contract: %+v", req)
	}
	if err = ValidatePreparedInput(profile, source, restored.OriginalSHA256, restored.Pattern, restored.PreparedSHA256); err != nil {
		t.Fatal(err)
	}
}
