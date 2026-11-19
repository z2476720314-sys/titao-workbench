package jobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"titao/internal/cfg"
	"titao/internal/judge"
)

// 同一图案的多个模板共用一套印刷画布与摆放参数，所以只该评审第一个模板：
// 每个模板再问一遍等于把同一个问题问 N 遍，每遍都是一次 35-47s 的视觉请求。
// 其余模板直接复用第一个模板的结论，但仍然各自渲染出图。
func TestSharedPatternReviewsOnlyFirstTemplateOnce(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ps := &reviewPhotoshop{}
	e.ps = ps
	tasks, output := patternGroupInputs(t)

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"content": `{"good":true,"camera_clear":true,"score":96,"problem":"无","dx":0,"dy":0,"scale_ratio":1,"stretch_y":1}`},
		}}})
	}))
	defer srv.Close()

	opt := Options{Auto: true, NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90, MaxRounds: 1}
	jc := judge.Config{BaseURL: srv.URL, Model: "fixture", MaxTokens: 512, Timeout: 5 * time.Second}
	e.processPattern(context.Background(), opt, cfg.Default(), jc, tasks, output, 1, 1)

	if got := calls.Load(); got != 1 {
		t.Fatalf("%d 个模板只应评审第一个一次，实际 %d 次", len(tasks), got)
	}
	for _, task := range tasks {
		if task.Phase != PhaseDone {
			t.Fatalf("模板 %s 未完成（复用结论应照样出图）: %s", task.TemplateName, task.Error)
		}
		if len(task.Files) == 0 {
			t.Fatalf("模板 %s 没有产物", task.TemplateName)
		}
	}
	// 复用结论不等于少渲染：每个模板都要真出图
	if got := len(ps.requests); got != len(tasks) {
		t.Fatalf("每个模板都应渲染一次，实际渲染 %d 次，模板 %d 个", got, len(tasks))
	}
}

// 复用只发生在「同一图案」组内：不同图案不能被判为同组而共用结论。
func TestDifferentPatternsAreNotSameGroup(t *testing.T) {
	if samePattern(&Task{PatternName: "a"}, &Task{PatternName: "b"}) {
		t.Fatal("不同图案不应被判为同组")
	}
	if !samePattern(&Task{PatternName: "a"}, &Task{PatternName: "a"}) {
		t.Fatal("同一图案应判为同组")
	}
}
