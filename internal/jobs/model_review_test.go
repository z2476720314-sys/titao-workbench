package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
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

// 假 Photoshop 仍写真实拼合图片，覆盖引擎中的预览、选档、微调与导出路径。
type reviewPhotoshop struct{ requests []psx.RenderRequest }

func (p *reviewPhotoshop) CloseAllDocs() error { return nil }

func (p *reviewPhotoshop) Render(req psx.RenderRequest) (string, error) {
	p.requests = append(p.requests, req)
	for i := range req.Scales {
		im := image.NewRGBA(image.Rect(0, 0, 80, 160))
		for y := 0; y < 160; y++ {
			for x := 0; x < 80; x++ {
				im.Set(x, y, color.RGBA{80, 100, 120, 255})
			}
		}
		f, err := os.Create(req.FlatPath(i))
		if err != nil {
			return "", err
		}
		err = jpeg.Encode(f, im, nil)
		closeErr := f.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return "", nil
}

func TestModelReviewUsesActualScalesReferenceAndFinalRound(t *testing.T) {
	checkModelReview(t, false)
}

func TestReviewFailurePreservesPreviousCandidate(t *testing.T) {
	checkModelReview(t, true)
}

func checkModelReview(t *testing.T, finalFailure bool) {
	t.Helper()
	e, _, _ := newTestEngine(t)
	ps := &reviewPhotoshop{}
	e.ps = ps
	dir := t.TempDir()
	pattern := filepath.Join(dir, "pattern.png")
	f, err := os.Create(pattern)
	if err != nil {
		t.Fatal(err)
	}
	im := image.NewRGBA(image.Rect(0, 0, 40, 70))
	draw.Draw(im, im.Bounds(), image.NewUniform(color.RGBA{255, 100, 20, 255}), image.Point{}, draw.Src)
	if err := png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
	f.Close()
	tpl := filepath.Join(dir, "template.tif")
	if err := os.WriteFile(tpl, []byte("fake template"), 0600); err != nil {
		t.Fatal(err)
	}
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests = append(requests, body)
		content := `{"pick":5,"reason":"大档更合适"}`
		if len(requests) == 2 {
			content = `{"good":false,"score":70,"problem":"偏大","direction":"smaller","step":0.1}`
		}
		if len(requests) == 3 {
			content = `{"good":true,"score":91,"problem":"无","direction":"none","step":0.05}`
			if finalFailure {
				content = "invalid response"
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	defer srv.Close()
	def := cfg.Default()
	def.MaxRounds = 2
	task := &Task{ID: 1, Pattern: pattern, PatternName: "pattern", Template: tpl, TemplateName: "template"}
	opt := Options{Scales: cfg.DefaultCandidates(), Auto: true, NoCrop: true, Fit: "auto", CellSize: 80, JPGQuality: 90, OffsetX: 0.02, OffsetY: 0.01}
	jc := judge.Config{BaseURL: srv.URL, APIKey: "test", Model: "test", MaxTokens: 100, Timeout: time.Second}
	e.processOne(context.Background(), opt, def, jc, task, filepath.Join(dir, "out"), 1, 1)
	if task.Phase != PhaseDone {
		t.Fatalf("task = %+v", task)
	}
	if len(ps.requests) != 2 {
		t.Fatalf("render count = %d", len(ps.requests))
	}
	want := []float64{0.654, 0.731, 0.808, 0.885, 1}
	if fmt.Sprint(ps.requests[0].Scales) != fmt.Sprint(want) {
		t.Errorf("actual candidates = %v; want %v", ps.requests[0].Scales, want)
	}
	wantScale := 0.9
	if finalFailure {
		wantScale = 1
	}
	if task.ChosenScale != wantScale {
		t.Errorf("chosen scale = %v; want %v", task.ChosenScale, wantScale)
	}
	if len(requests) != 3 {
		t.Fatalf("model calls = %d; final render must also be reviewed", len(requests))
	}
	for i, body := range requests {
		content := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
		text := content[0].(map[string]any)["text"].(string)
		if !strings.Contains(text, "原图") || !strings.Contains(text, "no_crop=true") {
			t.Errorf("request %d missing source/policy context", i)
		}
		if !strings.Contains(text, "offset_x=0.020") || !strings.Contains(text, "offset_y=0.010") || strings.Contains(text, "scale=0.000") {
			t.Errorf("request %d has incorrect actual parameters", i)
		}
		wantImages := 2
		if i == 0 {
			wantImages = 6
		}
		images := 0
		for _, c := range content {
			if c.(map[string]any)["type"] == "image_url" {
				images++
			}
		}
		if images != wantImages {
			t.Errorf("request %d images=%d want=%d", i, images, wantImages)
		}
	}
	if !finalFailure && !strings.Contains(task.JudgeNote, "91") {
		t.Errorf("final review missing: %q", task.JudgeNote)
	}
	if finalFailure && (!strings.Contains(task.JudgeNote, "70") || !strings.Contains(task.Message, "人工复核")) {
		t.Fatalf("failed review state=%+v", task)
	}
}

func TestReviewWarningSurvivesQueue(t *testing.T) {
	in := &Task{ID: 1, Phase: PhaseDone, Message: "生成 2 张（评审未通过，请人工复核）", JudgeNote: "评审未完成，请人工复核", TuneRounds: 3}
	path := filepath.Join(t.TempDir(), "queue.json")
	if err := SaveQueue(path, NewQueue(Options{}, []*Task{in})); err != nil {
		t.Fatal(err)
	}
	q, err := LoadQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	out := q.ToTasks()[0]
	if out.Message != in.Message || out.JudgeNote != in.JudgeNote || out.TuneRounds != 3 {
		t.Fatalf("review state lost: %+v", out)
	}
}
