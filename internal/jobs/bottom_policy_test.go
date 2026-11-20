package jobs

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"titao/internal/cfg"
	"titao/internal/judge"
)

func TestOcclusionTriesBottomTranslationOnce(t *testing.T) {
	for _, clear := range []bool{true, false} {
		t.Run(map[bool]string{true: "clear", false: "residual"}[clear], func(t *testing.T) {
			e, _, _ := newTestEngine(t)
			ps := &reviewPhotoshop{}
			e.ps = ps
			tasks, output := patternGroupInputs(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reply := `{"good":false,"camera_clear":false,"score":70,"problem":"主标题被镜头挡住","dx":0.1,"dy":0.1,"scale_ratio":0.55,"stretch_y":1.1}`
				if len(ps.requests) > 2 {
					if clear {
						reply = `{"good":true,"camera_clear":true,"score":95,"problem":"无","dx":0,"dy":0,"scale_ratio":1,"stretch_y":1}`
					} else {
						reply = `{"good":false,"camera_clear":false,"score":40,"problem":"标题仍被镜头挡住","dx":0.1,"dy":0.1,"scale_ratio":0.55,"stretch_y":1.1}`
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}}})
			}))
			defer srv.Close()
			e.processPattern(context.Background(), Options{Auto: true, NoCrop: true, Fit: "auto", MaxRounds: 4, CellSize: 80, JPGQuality: 90}, cfg.Default(), judge.Config{BaseURL: srv.URL, Model: "fixture", MaxTokens: 512, Timeout: time.Second}, tasks, output, 1, 2)
			if len(ps.requests) != 4 {
				t.Fatalf("expected initial and bottom round for both templates, renders=%d", len(ps.requests))
			}
			first, bottom := ps.requests[0].Canvas, ps.requests[2].Canvas
			for i := 0; i < 5; i++ {
				if bottom.Transform[i] != first.Transform[i] {
					t.Fatalf("bottom changed scale, shape or x: %v -> %v", first.Transform, bottom.Transform)
				}
			}
			if bottom.Transform[5] <= first.Transform[5] || math.Abs(bottom.Transform[5]+float64(bottom.SourceHeight)*bottom.Transform[3]-float64(bottom.Height)) > 1e-6 {
				t.Fatalf("not translated to bottom: %v", bottom.Transform)
			}
			for _, task := range tasks {
				if task.CanvasSHA256 != bottom.SHA256 || task.TuneRounds != 2 || len(task.Files) != 2 || task.TuneHistory[1].CameraClear != clear || task.TuneHistory[1].Good != clear {
					t.Fatalf("bottom result lost or review falsified: %+v", task)
				}
				if !clear && (task.Phase != PhaseFailed || !strings.Contains(task.JudgeNote, "贴底后仍有遮挡") || !strings.Contains(task.OutputDir, "_待复核")) {
					t.Fatalf("residual occlusion silently passed or resumed: %+v", task)
				}
			}
		})
	}
}

func TestHighScoreDoesNotSkipRequestedAdjustment(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ps := &reviewPhotoshop{}
	e.ps = ps
	tasks, output := patternGroupInputs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := `{"good":false,"camera_clear":true,"score":95,"problem":"底部切边未贴合","dx":0,"dy":0.1,"scale_ratio":1,"stretch_y":1}`
		if len(ps.requests) > 2 {
			reply = `{"good":true,"camera_clear":true,"score":95,"problem":"无","dx":0,"dy":0,"scale_ratio":1,"stretch_y":1}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}}})
	}))
	defer srv.Close()
	e.processPattern(context.Background(), Options{Auto: true, NoCrop: true, Fit: "auto", MaxRounds: 3, CellSize: 80, JPGQuality: 90}, cfg.Default(), judge.Config{BaseURL: srv.URL, Model: "fixture", MaxTokens: 512, Timeout: time.Second}, tasks, output, 1, 2)
	if len(ps.requests) != 4 || tasks[0].Phase != PhaseDone || tasks[0].TuneHistory[0].Good || !tasks[0].TuneHistory[1].Good {
		t.Fatalf("high score skipped false/adjustment: renders=%d task=%+v", len(ps.requests), tasks[0])
	}
}

func TestAutoLayoutPreservesConfiguredOffsets(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ps := &reviewPhotoshop{}
	e.ps = ps
	tasks, output := patternGroupInputs(t)
	settings := cfg.Default()
	settings.AutoTuneOn = false
	e.processPattern(context.Background(), Options{Auto: true, NoCrop: true, Fit: "auto", OffsetX: .15, OffsetY: .2, CellSize: 80, JPGQuality: 90}, settings, judge.Config{}, tasks, output, 1, 2)
	if len(ps.requests) != 2 || math.Abs(ps.requests[0].Canvas.Placement.OffsetX-.15) > 1e-6 || math.Abs(ps.requests[0].Canvas.Placement.OffsetY-.2) > 1e-6 {
		t.Fatalf("Auto ignored explicit offsets: %+v", ps.requests)
	}
}
