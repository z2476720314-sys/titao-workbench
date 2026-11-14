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
	"titao/internal/tuner"
)

// 假 Photoshop 仍写真实拼合图片，覆盖引擎中的预览、选档、微调与导出路径。
type reviewPhotoshop struct {
	requests         []psx.RenderRequest
	anchorUnmet      bool
	anchorImpossible bool
	cells            int
}

func (p *reviewPhotoshop) CloseAllDocs() error { return nil }

func (p *reviewPhotoshop) Render(req psx.RenderRequest) (string, error) {
	p.requests = append(p.requests, req)
	cells := p.cells
	if cells <= 0 {
		cells = 2
	}
	for i := range req.Scales {
		im := image.NewRGBA(image.Rect(0, 0, 80, 80*cells))
		for y := 0; y < 80*cells; y++ {
			for x := 0; x < 80; x++ {
				im.Set(x, y, color.RGBA{uint8(80 + len(p.requests)*10), 100, 120, 255})
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
		meta := psx.RenderMetadata{FrameVerified: true}
		for cell := 0; cell < cells; cell++ {
			meta.Placements = append(meta.Placements, psx.AppliedPlacement{Cell: cell, Scale: req.Scales[i], OffsetX: req.OffsetX, OffsetY: req.OffsetY, AnchorUnmet: cell == 0 && p.anchorUnmet, AnchorImpossible: cell == 0 && p.anchorImpossible})
		}
		b, err := json.Marshal(meta)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(req.MetadataPath(i), b, 0600); err != nil {
			return "", err
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

func TestModelCannotApproveUnmetCutEdge(t *testing.T) {
	checkModelReview(t, false, true)
}

func checkModelReview(t *testing.T, finalFailure bool, anchorUnmet ...bool) {
	t.Helper()
	e, _, _ := newTestEngine(t)
	ps := &reviewPhotoshop{}
	if len(anchorUnmet) > 0 {
		ps.anchorUnmet = anchorUnmet[0]
	}
	e.ps = ps
	dir := t.TempDir()
	pattern := filepath.Join(dir, "pattern.png")
	f, err := os.Create(pattern)
	if err != nil {
		t.Fatal(err)
	}
	im := image.NewRGBA(image.Rect(0, 0, 40, 90))
	draw.Draw(im, image.Rect(0, 10, 40, 80), image.NewUniform(color.RGBA{255, 100, 20, 255}), image.Point{}, draw.Src)
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
		content := `{"good":false,"score":70,"problem":"主要内容偏位","direction":"right","step":0.1}`
		if len(requests) == 2 {
			content = `{"good":true,"score":91,"problem":"无","direction":"none","step":0.05}`
		}
		if finalFailure && len(requests) >= 2 {
			content = "invalid response"
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
	want := []float64{1}
	if fmt.Sprint(ps.requests[0].Scales) != fmt.Sprint(want) {
		t.Errorf("actual candidates = %v; want %v", ps.requests[0].Scales, want)
	}
	wantScale := 1.0
	if task.ChosenScale != wantScale {
		t.Errorf("chosen scale = %v; want %v", task.ChosenScale, wantScale)
	}
	wantCalls := 2
	if finalFailure {
		wantCalls = 3
	}
	if len(requests) != wantCalls {
		t.Fatalf("model calls = %d; final render must also be reviewed", len(requests))
	}
	for i, body := range requests {
		var content []any
		for _, message := range body["messages"].([]any) {
			m := message.(map[string]any)
			if m["role"] == "user" {
				if parts, ok := m["content"].([]any); ok {
					content = parts
					break
				}
			}
		}
		if len(content) == 0 {
			t.Fatal("review lost the image message")
		}
		text := content[0].(map[string]any)["text"].(string)
		if !strings.Contains(text, "原图") || !strings.Contains(text, "no_crop=true") {
			t.Errorf("request %d missing source/policy context", i)
		}
		if !strings.Contains(text, "anchor_x=2") || !strings.Contains(text, "本轮请求") {
			t.Errorf("request %d missing applied anchor or requested placement context", i)
		}
		wantOffset := "offset_x=0"
		if i > 0 {
			wantOffset = "offset_x=0.1"
		}
		if !strings.Contains(text, wantOffset) || !strings.Contains(text, "offset_y=0") || strings.Contains(text, "scale=0.000") {
			t.Errorf("request %d has incorrect actual parameters", i)
		}
		wantImages := 3 // 原图 + 两个独立成品角度。
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
	if ps.anchorUnmet && (!strings.Contains(task.Message, "人工复核") || task.TuneHistory[len(task.TuneHistory)-1].Good) {
		t.Fatalf("model overrode an unmet cut edge: %+v", task)
	}
}

func TestReviewWarningSurvivesQueue(t *testing.T) {
	in := &Task{ID: 1, Phase: PhaseDone, Message: "生成 2 张（评审未通过，请人工复核）", JudgeNote: "评审未完成，请人工复核", TuneRounds: 3}
	in.TuneHistory = []tuner.Round{{Index: 1, CameraClear: false}, {Index: 2, CameraClear: true, Applied: []psx.AppliedPlacement{{Cell: 2, Scale: .95, AnchorUnmet: true}}}}
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
	if len(out.TuneHistory) != 2 || out.TuneHistory[0].CameraClear || !out.TuneHistory[1].CameraClear || len(out.TuneHistory[1].Applied) != 1 || out.TuneHistory[1].Applied[0] != in.TuneHistory[1].Applied[0] {
		t.Fatalf("actual per-angle review history lost: %+v", out.TuneHistory)
	}
}

func TestFallbackPrefersClearTextOverHigherOverallScore(t *testing.T) {
	e, _, _ := newTestEngine(t)
	e.ps = &reviewPhotoshop{anchorUnmet: true}
	dir := t.TempDir()
	pattern, template := filepath.Join(dir, "pattern.png"), filepath.Join(dir, "template.tif")
	f, err := os.Create(pattern)
	if err != nil {
		t.Fatal(err)
	}
	im := image.NewRGBA(image.Rect(0, 0, 40, 90))
	draw.Draw(im, im.Bounds().Inset(5), image.NewUniform(color.RGBA{200, 80, 30, 255}), image.Point{}, draw.Src)
	if err = png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(template, []byte("fixture template"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		reply := `{"good":false,"score":90,"problem":"标题被镜头挡住","camera_clear":false,"dx":0,"dy":0.1,"scale_ratio":0.95}`
		if calls > 1 {
			reply = `{"good":false,"score":70,"problem":"文字已避让，截边仍悬空","camera_clear":true,"dx":0,"dy":0,"scale_ratio":1}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}}})
	}))
	defer srv.Close()
	def := cfg.Default()
	def.MaxRounds = 2
	opt := Options{Scales: []float64{1}, Auto: true, NoCrop: true, Fit: "auto", CellSize: 80, JPGQuality: 90}
	task := &Task{ID: 1, Pattern: pattern, PatternName: "pattern", Template: template, TemplateName: "template"}
	e.processOne(context.Background(), opt, def, judge.Config{BaseURL: srv.URL, APIKey: "test", Model: "test", MaxTokens: 160, Timeout: time.Second}, task, filepath.Join(dir, "out"), 1, 1)
	if task.Phase != PhaseDone || task.ChosenScale != .95 || !strings.Contains(task.JudgeNote, "70") || !strings.Contains(task.Message, "人工复核") {
		t.Fatalf("clear candidate was discarded for a higher total score: %+v", task)
	}
}

func TestImpossibleAnchorsStopBeforeModelReview(t *testing.T) {
	for _, tc := range []struct {
		auto   bool
		fit    string
		scales []float64
	}{{true, "auto", []float64{1}}, {false, "auto", []float64{1}}, {true, "cover", []float64{.7, .9}}} {
		t.Run(fmt.Sprint(tc.auto, tc.fit), func(t *testing.T) {
			e, _, _ := newTestEngine(t)
			editor := &reviewPhotoshop{anchorUnmet: true, anchorImpossible: true}
			e.ps = editor
			dir := t.TempDir()
			pattern, template := filepath.Join(dir, "pattern.png"), filepath.Join(dir, "template.tif")
			f, err := os.Create(pattern)
			if err != nil {
				t.Fatal(err)
			}
			im := image.NewRGBA(image.Rect(0, 0, 40, 90))
			draw.Draw(im, im.Bounds(), image.NewUniform(color.RGBA{180, 80, 20, 255}), image.Point{}, draw.Src)
			if err = png.Encode(f, im); err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(template, []byte("fixture template"), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"good":true,"score":95,"problem":"无","direction":"none","step":0.1}`}}}})
			}))
			defer srv.Close()
			opt := Options{Scales: tc.scales, Auto: tc.auto, NoCrop: true, Fit: tc.fit, CellSize: 80, JPGQuality: 90}
			task := &Task{ID: 1, Pattern: pattern, PatternName: "pattern", Template: template, TemplateName: "template"}
			e.processOne(context.Background(), opt, cfg.Default(), judge.Config{BaseURL: srv.URL, APIKey: "test", Model: "test", MaxTokens: 100, Timeout: time.Second}, task, filepath.Join(dir, "out"), 1, 1)
			if calls != 0 || len(editor.requests) != 1 || task.Phase != PhaseDone || !strings.Contains(task.Message, "人工复核") || !strings.Contains(task.JudgeNote, "约束冲突") || len(task.TuneHistory) != 1 || task.TuneHistory[0].Score != 0 {
				t.Fatalf("impossible constraint retried or passed: calls=%d task=%+v", calls, task)
			}
		})
	}
}

func TestStrictReviewReceivesAllSevenAngles(t *testing.T) {
	e, _, _ := newTestEngine(t)
	e.ps = &reviewPhotoshop{cells: 7}
	dir := t.TempDir()
	pattern, template := filepath.Join(dir, "pattern.png"), filepath.Join(dir, "template.tif")
	f, err := os.Create(pattern)
	if err != nil {
		t.Fatal(err)
	}
	im := image.NewRGBA(image.Rect(0, 0, 40, 90))
	draw.Draw(im, im.Bounds(), image.NewUniform(color.RGBA{180, 80, 20, 255}), image.Point{}, draw.Src)
	if err = png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(template, []byte("fixture template"), 0600); err != nil {
		t.Fatal(err)
	}
	images := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for _, m := range body.Messages {
			if m.Role != "user" {
				continue
			}
			var parts []struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(m.Content, &parts)
			for _, p := range parts {
				if p.Type == "image_url" {
					images++
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"good":true,"score":95,"problem":"无","direction":"none","step":0.1}`}}}})
	}))
	defer srv.Close()
	opt := Options{Scales: []float64{1}, Auto: true, NoCrop: true, Fit: "auto", CellSize: 80, JPGQuality: 90, MaxRounds: 1}
	task := &Task{ID: 1, Pattern: pattern, PatternName: "pattern", Template: template, TemplateName: "template"}
	e.processOne(context.Background(), opt, cfg.Default(), judge.Config{BaseURL: srv.URL, APIKey: "test", Model: "test", MaxTokens: 100, Timeout: time.Second}, task, filepath.Join(dir, "out"), 1, 1)
	if task.Phase != PhaseDone || images != 8 {
		t.Fatalf("review images=%d want original+7; task=%+v", images, task)
	}
}
