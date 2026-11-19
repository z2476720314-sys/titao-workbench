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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"titao/internal/cfg"
	"titao/internal/judge"
	"titao/internal/printcanvas"
	"titao/internal/psx"
)

func patternGroupInputs(t *testing.T) ([]*Task, string) {
	t.Helper()
	dir := t.TempDir()
	pattern := filepath.Join(dir, "pattern.png")
	im := image.NewNRGBA(image.Rect(0, 0, 40, 90))
	draw.Draw(im, image.Rect(5, 15, 35, 75), image.NewUniform(color.NRGBA{R: 180, G: 70, A: 255}), image.Point{}, draw.Src)
	f, err := os.Create(pattern)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
	f.Close()
	var tasks []*Task
	for i, name := range []string{"template-a", "template-b"} {
		tpl := filepath.Join(dir, name+".tif")
		if err := os.WriteFile(tpl, []byte("fixture template"), 0600); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, &Task{ID: i + 1, Pattern: pattern, PatternName: "pattern", Template: tpl, TemplateName: name, Phase: PhasePending})
	}
	return tasks, filepath.Join(dir, "output")
}

func TestPatternTuningRerendersEveryTemplateWithOneCanvasVersion(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ps := &reviewPhotoshop{}
	e.ps = ps
	tasks, output := patternGroupInputs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		b, _ := json.Marshal(body["messages"])
		reply := `{"good":true,"camera_clear":true,"score":95,"problem":"无","dx":0,"dy":0,"scale_ratio":1}`
		if len(ps.requests) == 2 && strings.Contains(string(b), "template-b") {
			reply = `{"good":false,"camera_clear":false,"score":50,"problem":"主脸被挡","dx":0.05,"dy":0.1,"scale_ratio":0.9}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}}})
	}))
	defer srv.Close()
	opt := Options{Auto: true, NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90, MaxRounds: 3}
	e.processPattern(context.Background(), opt, cfg.Default(), judge.Config{BaseURL: srv.URL, Model: "fixture", MaxTokens: 512, Timeout: 3 * time.Second}, tasks, output, 1, 2)
	if len(ps.requests) != 4 {
		t.Fatalf("expected both templates in both rounds: renders=%d", len(ps.requests))
	}
	for _, task := range tasks {
		if task.Phase != PhaseDone || task.TuneRounds != 2 || len(task.Files) != 2 {
			t.Fatalf("group did not finish coherently: %+v", task)
		}
	}
	if tasks[0].CanvasSHA256 == "" || tasks[0].CanvasSHA256 != tasks[1].CanvasSHA256 || tasks[0].CanvasPath != tasks[1].CanvasPath {
		t.Fatalf("different final canvases: %+v %+v", tasks[0], tasks[1])
	}
	for i := 0; i < 4; i += 2 {
		if ps.requests[i].Canvas == nil || ps.requests[i+1].Canvas == nil || ps.requests[i].Canvas.SHA256 != ps.requests[i+1].Canvas.SHA256 {
			t.Fatal("one round used multiple canvas versions")
		}
	}
	if ps.requests[0].Canvas.SHA256 == ps.requests[2].Canvas.SHA256 {
		t.Fatal("tuning did not change the shared canvas")
	}
	final := *ps.requests[2].Canvas
	final.Path = tasks[0].CanvasPath
	if err := printcanvas.Validate(&final); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedPatternNoOpNeverBecomesSuccessfulExport(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ps := &reviewPhotoshop{}
	e.ps = ps
	tasks, output := patternGroupInputs(t)
	tasks = tasks[:1]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"good":false,"camera_clear":false,"score":48,"problem":"主脸被挡且无安全解","dx":0,"dy":0,"scale_ratio":1}`}}}})
	}))
	defer srv.Close()
	e.processPattern(context.Background(), Options{Auto: true, NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90, MaxRounds: 3}, cfg.Default(), judge.Config{BaseURL: srv.URL, Model: "fixture", MaxTokens: 512, Timeout: time.Second}, tasks, output, 1, 1)
	if tasks[0].Phase != PhaseFailed || e.done != 0 || len(ps.requests) != 1 || len(tasks[0].Files) != 2 {
		t.Fatalf("low-score no-op passed or repeated: renders=%d task=%+v", len(ps.requests), tasks[0])
	}
}

func TestRunUsesOnePatternExecutorAcrossSelectedTemplates(t *testing.T) {
	_, store, rec := newTestEngine(t)
	ps := &reviewPhotoshop{}
	e := New(ps, store, func(ev Event) { rec.add(ev) })
	tasks, output := patternGroupInputs(t)
	opt := Options{Patterns: []string{tasks[0].Pattern}, Templates: []string{tasks[0].Template, tasks[1].Template}, OutputDir: output, NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90}
	startAndWait(t, e, opt)
	got := e.Tasks()
	if len(got) != 2 || got[0].CanvasPath == "" || got[0].CanvasPath != got[1].CanvasPath || got[0].CanvasSHA256 != got[1].CanvasSHA256 {
		t.Fatalf("scheduler still used independent template processors: %+v", got)
	}
	if got[0].Phase != PhaseDone || got[1].Phase != PhaseDone {
		t.Fatalf("group failed: %+v", got)
	}
}

func TestFailedPatternSiblingsAreNeverLoggedAsCompleted(t *testing.T) {
	_, store, rec := newTestEngine(t)
	e := New(&reviewPhotoshop{}, store, rec.add)
	tasks, output := patternGroupInputs(t)
	e.groupFn = func(e *Engine, _ context.Context, _ Options, _ cfg.Config, _ judge.Config, tasks []*Task, _ string, _, _ int) {
		for _, task := range tasks {
			e.failTask(task, os.ErrInvalid)
		}
	}
	startAndWait(t, e, Options{Patterns: []string{tasks[0].Pattern}, Templates: []string{tasks[0].Template, tasks[1].Template}, OutputDir: output})
	for _, ev := range rec.snapshot() {
		if ev.Type == "log" && strings.Contains(ev.Message, "跳过已完成") {
			t.Fatalf("failed sibling reported completed: %s", ev.Message)
		}
	}
}

func TestCanvasChangeInvalidatesAllTemplateCompletionRecords(t *testing.T) {
	tasks, output := patternGroupInputs(t)
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	canvas, err := printcanvas.Build(tasks[0].Pattern, filepath.Join(output, "print_canvas.png"), psx.RenderRequest{NoCrop: true, Fit: "auto"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var records []completionRecord
	for _, task := range tasks {
		path := filepath.Join(output, task.TemplateName+".jpg")
		if err := os.WriteFile(path, []byte("old export"), 0600); err != nil {
			t.Fatal(err)
		}
		task.Files = []string{path}
		task.CanvasSHA256, task.CanvasPath = canvas.SHA256, canvas.Path
		record, err := completionRecordOf(task)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	info, err := os.Stat(canvas.Path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(canvas.Path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(canvas.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(canvas.Path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.valid() {
			t.Fatal("changed shared canvas kept old template completion valid")
		}
	}
}

func TestSharedPlannerIncludesCompletedSiblingWhenOneTemplatePending(t *testing.T) {
	_, store, _ := newTestEngine(t)
	e := New(&reviewPhotoshop{}, store, nil)
	tasks, output := patternGroupInputs(t)
	opt := Options{Patterns: []string{tasks[0].Pattern}, Templates: []string{tasks[0].Template, tasks[1].Template}, OutputDir: output, NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90}
	planned, err := e.Plan(opt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	canvas, err := printcanvas.Build(tasks[0].Pattern, filepath.Join(output, "print_canvas.png"), psx.RenderRequest{NoCrop: true, Fit: "auto"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	first := planned[0]
	first.Phase = PhaseDone
	first.CanvasPath, first.CanvasSHA256 = canvas.Path, canvas.SHA256
	path := filepath.Join(output, "first.jpg")
	if err := os.WriteFile(path, []byte("exported image"), 0600); err != nil {
		t.Fatal(err)
	}
	first.Files = []string{path}
	if err := e.recordCompletion(opt, store.Get(), first); err != nil {
		t.Fatal(err)
	}
	pending, err := e.Plan(opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("partial pattern must revalidate all selected templates together: %d", len(pending))
	}
}

func TestGroupRejectsInputMutationBeforePublishing(t *testing.T) {
	for _, kind := range []string{"pattern", "template"} {
		t.Run(kind, func(t *testing.T) {
			e, _, _ := newTestEngine(t)
			ps := &reviewPhotoshop{}
			e.ps = ps
			tasks, output := patternGroupInputs(t)
			finalDir := filepath.Join(output, "pattern")
			if err := os.MkdirAll(finalDir, 0700); err != nil {
				t.Fatal(err)
			}
			oldCanvas := filepath.Join(finalDir, "print_canvas.png")
			if err := os.WriteFile(oldCanvas, []byte("existing output"), 0600); err != nil {
				t.Fatal(err)
			}
			var once sync.Once
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				once.Do(func() {
					path := tasks[0].Pattern
					if kind == "template" {
						path = tasks[0].Template
					}
					if err := os.WriteFile(path, []byte("changed while reviewing"), 0600); err != nil {
						t.Error(err)
					}
				})
				reply := `{"good":true,"camera_clear":true,"score":95,"problem":"无","dx":0,"dy":0,"scale_ratio":1}`
				if len(ps.requests) == 2 {
					reply = `{"good":false,"camera_clear":false,"score":50,"problem":"主脸被挡","dx":0,"dy":0.1,"scale_ratio":0.9}`
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}}})
			}))
			defer srv.Close()
			e.processPattern(context.Background(), Options{Auto: true, NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90, MaxRounds: 2}, cfg.Default(), judge.Config{BaseURL: srv.URL, Model: "fixture", MaxTokens: 512, Timeout: 3 * time.Second}, tasks, output, 1, 2)
			for _, task := range tasks {
				if task.Phase != PhaseFailed {
					t.Fatalf("changed input published: %+v", task)
				}
			}
			data, err := os.ReadFile(oldCanvas)
			if err != nil || string(data) != "existing output" {
				t.Fatalf("old output changed before input validation: %q %v", data, err)
			}
			if len(ps.requests) != 4 {
				t.Fatalf("input snapshot not reused in second round: renders=%d", len(ps.requests))
			}
		})
	}
}

func TestGroupProtectsOtherSelectedSourceFromCanvasPublication(t *testing.T) {
	e, _, _ := newTestEngine(t)
	e.ps = &reviewPhotoshop{}
	tasks, output := patternGroupInputs(t)
	tasks = tasks[:1]
	e.processPattern(context.Background(), Options{NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90}, cfg.Default(), judge.Config{}, tasks, output, 1, 1)
	if tasks[0].Phase != PhaseDone {
		t.Fatalf("initial fixture failed: %+v", tasks[0])
	}
	futureSource := tasks[0].CanvasPath
	before, err := os.ReadFile(futureSource)
	if err != nil {
		t.Fatal(err)
	}
	future := *tasks[0]
	future.ID = 3
	future.Pattern = futureSource
	future.PatternName = "future"
	e.tasks = append(tasks, &future)
	e.processPattern(context.Background(), Options{NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90}, cfg.Default(), judge.Config{}, tasks, output, 1, 2)
	after, err := os.ReadFile(futureSource)
	if err != nil || string(after) != string(before) || tasks[0].Phase != PhaseFailed {
		t.Fatalf("selected input overwritten by canvas: phase=%s error=%v", tasks[0].Phase, err)
	}
}

func TestGroupProtectsPreparedOriginalAtCanvasDestination(t *testing.T) {
	e, store, _ := newTestEngine(t)
	e.ps = &reviewPhotoshop{}
	tasks, output := patternGroupInputs(t)
	tasks = tasks[:1]
	opt := Options{NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90}
	e.processPattern(context.Background(), opt, cfg.Default(), judge.Config{}, tasks, output, 1, 1)
	if tasks[0].Phase != PhaseDone {
		t.Fatalf("initial fixture failed: %+v", tasks[0])
	}
	original := tasks[0].CanvasPath
	before, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterPreparedArtwork(filepath.Dir(store.Path()), original, tasks[0].Pattern, ""); err != nil {
		t.Fatal(err)
	}
	tasks[0].Pattern = original
	tasks[0].OriginalPattern = ""
	tasks[0].OriginalSHA256 = ""
	tasks[0].PreparedSHA256 = ""
	if err := e.prepareTaskInputs(tasks); err != nil {
		t.Fatal(err)
	}
	e.processPattern(context.Background(), opt, cfg.Default(), judge.Config{}, tasks, output, 1, 1)
	after, err := os.ReadFile(original)
	if err != nil || string(after) != string(before) || tasks[0].Phase != PhaseFailed {
		t.Fatalf("prepared original overwritten: phase=%s err=%v", tasks[0].Phase, err)
	}
}

func TestPlanRejectsDifferentPatternsWithSameOutputDirectory(t *testing.T) {
	e, _, _ := newTestEngine(t)
	tasks, output := patternGroupInputs(t)
	other := filepath.Join(t.TempDir(), "pattern.png")
	if err := copyFile(tasks[0].Pattern, other); err != nil {
		t.Fatal(err)
	}
	_, err := e.Plan(Options{Patterns: []string{tasks[0].Pattern, other}, Templates: []string{tasks[0].Template}, OutputDir: output})
	if err == nil {
		t.Fatal("different sources with same output name accepted")
	}
}

// 每轮只把一张「能完整看到壳面正面」的图送审，而不是逐格都发。
//
// 全组共用同一套摆放参数，图案压镜头这件事每格都会发生，只是露出角度不同。
// 逐格送审等于把同一个问题问 7 遍——实测每张 35-47s、prompt 5000+ tokens，
// 是套图耗时的绝对大头。这里验证：3 个模板 → 3 次评审调用（不是 3×格数）。
func TestGroupReviewsOneRepresentativeViewPerTemplate(t *testing.T) {
	e, _, _ := newTestEngine(t)
	e.ps = &reviewPhotoshop{cells: 3}
	tasks, output := patternGroupInputs(t)
	var calls, multiImage atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		images := 0
		for _, message := range body["messages"].([]any) {
			if parts, ok := message.(map[string]any)["content"].([]any); ok {
				for _, part := range parts {
					if part.(map[string]any)["type"] == "image_url" {
						images++
					}
				}
			}
		}
		// 每条请求 = 原图参考 1 张 + 成品 1 张
		if images > 2 {
			multiImage.Add(1)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"good":true,"camera_clear":true,"score":95,"problem":"无","dx":0,"dy":0,"scale_ratio":1}`}}}})
	}))
	defer srv.Close()
	e.processPattern(context.Background(), Options{Auto: true, NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90, MaxRounds: 3}, cfg.Default(), judge.Config{BaseURL: srv.URL, Model: "fixture", MaxTokens: 512, Timeout: 3 * time.Second}, tasks, output, 1, 2)
	if calls.Load() != int32(len(tasks)) {
		t.Fatalf("每个模板应只发一次评审，实际 calls=%d，模板数=%d", calls.Load(), len(tasks))
	}
	if multiImage.Load() != 0 {
		t.Fatalf("不应把多张成品塞进同一条请求，实际 %d 条超限", multiImage.Load())
	}
	for _, task := range tasks {
		if task.Phase != PhaseDone {
			t.Fatalf("评审失败: %+v", task)
		}
	}
}

func TestPublicPhotoFolderContainsOnlyTemplatePhotos(t *testing.T) {
	e, _, _ := newTestEngine(t)
	e.ps = &reviewPhotoshop{}
	tasks, output := patternGroupInputs(t)
	e.processPattern(context.Background(), Options{NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90}, cfg.Default(), judge.Config{}, tasks, output, 1, 2)
	for _, task := range tasks {
		if task.Phase != PhaseDone {
			t.Fatalf("projection failed: %+v", task)
		}
	}
	entries, err := os.ReadDir(tasks[0].OutputDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".jpg" {
			t.Fatalf("internal print artifact could be uploaded as product photo: %s", entry.Name())
		}
	}
}
