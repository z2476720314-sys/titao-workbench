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

func TestUnsafeCanonicalAnchorsCannotReachSuccessfulReview(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ps := &reviewPhotoshop{}
	e.ps = ps
	tasks, output := patternGroupInputs(t)
	im := image.NewNRGBA(image.Rect(0, 0, 120, 400))
	draw.Draw(im, image.Rect(0, 160, 120, 240), image.NewUniform(color.NRGBA{R: 150, A: 255}), image.Point{}, draw.Src)
	f, err := os.Create(tasks[0].Pattern)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, im); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := `{"good":true,"camera_clear":true,"score":95,"problem":"无","dx":0,"dy":0,"scale_ratio":1,"stretch_y":1}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}}})
	}))
	defer srv.Close()
	e.processPattern(context.Background(), Options{Auto: true, NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90, MaxRounds: 1}, cfg.Default(), judge.Config{BaseURL: srv.URL, Model: "fixture", MaxTokens: 512, Timeout: time.Second}, tasks, output, 1, 2)
	if len(ps.requests) != 0 {
		t.Fatal("unsafe axis correction must fail before projection")
	}
	for _, task := range tasks {
		if task.Phase != PhaseFailed || !strings.Contains(task.Error, "超出允许范围") || len(task.Files) != 0 {
			t.Fatalf("unsafe anchor correction was silently accepted: %+v", task)
		}
	}
	if e.done != 0 || e.failed != 2 {
		t.Fatalf("unmet anchors counted as successful: done=%d failed=%d", e.done, e.failed)
	}
}

func TestUnsafeSecondRoundPreservesFirstFullBleedOutput(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ps := &reviewPhotoshop{}
	e.ps = ps
	tasks, output := patternGroupInputs(t)
	im := image.NewNRGBA(image.Rect(0, 0, 945, 1890))
	draw.Draw(im, im.Bounds(), image.NewUniform(color.NRGBA{R: 150, A: 255}), image.Point{}, draw.Src)
	f, err := os.Create(tasks[0].Pattern)
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(f, im)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("encode pattern: %v, close: %v", err, closeErr)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := `{"good":false,"camera_clear":true,"score":90,"problem":"缩小下移避开人脸并保留主体","dx":0,"dy":0.16,"scale_ratio":0.75,"stretch_y":1}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}}})
	}))
	defer srv.Close()
	e.processPattern(context.Background(), Options{Auto: true, NoCrop: true, Fit: "auto", CellSize: 80, JPGQuality: 90, MaxRounds: 3}, cfg.Default(), judge.Config{BaseURL: srv.URL, Model: "fixture", MaxTokens: 512, Timeout: 3 * time.Second}, tasks, output, 1, 2)
	if len(ps.requests) != 2 {
		t.Fatalf("expected one shared rendered version for both templates, renders=%d", len(ps.requests))
	}
	for _, task := range tasks {
		if task.Phase != PhaseFailed || len(task.Files) != 2 || len(task.TuneHistory) != 1 || task.TuneHistory[0].Good || !task.TuneHistory[0].CameraClear || !strings.Contains(task.JudgeNote, "超出允许范围") || filepath.Dir(task.OutputDir) != filepath.Join(output, "_待复核") {
			t.Fatalf("lost the earlier result or falsified the review: %+v", task)
		}
		plan, err := psx.LoadArtworkPlan(task.CanvasPath)
		if err != nil || plan.SHA256 != ps.requests[0].Canvas.SHA256 || plan.Placement.AnchorUnmet || plan.Transform != [6]float64{1, 0, 0, 1, 0, 0} {
			t.Fatalf("published the wrong placement: plan=%+v err=%v", plan, err)
		}
	}
}

func TestBottomRetryPreservesAlreadyAttachedTopCut(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ps := &reviewPhotoshop{}
	e.ps = ps
	tasks, output := patternGroupInputs(t)
	im := image.NewNRGBA(image.Rect(0, 0, 945, 1890))
	pigment := image.NewUniform(color.NRGBA{R: 120, A: 255})
	draw.Draw(im, image.Rect(230, 0, 690, 20), pigment, image.Point{}, draw.Src)
	draw.Draw(im, image.Rect(0, 1600, 945, 1888), pigment, image.Point{}, draw.Src)
	im.SetNRGBA(490, 1889, color.NRGBA{A: 7})
	f, err := os.Create(tasks[0].Pattern)
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(f, im)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("encode: %v; close: %v", err, closeErr)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reply := `{"good":false,"camera_clear":false,"score":60,"problem":"顶部主字被挡","dx":0,"dy":0.1,"scale_ratio":1,"stretch_y":1}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}}})
	}))
	defer srv.Close()
	e.processPattern(context.Background(), Options{Auto: true, NoCrop: true, Fit: "auto", CellSize: 80, JPGQuality: 90, MaxRounds: 3}, cfg.Default(), judge.Config{BaseURL: srv.URL, Model: "fixture", MaxTokens: 512, Timeout: 3 * time.Second}, tasks, output, 1, 2)
	if len(ps.requests) != 2 || len(tasks[0].Files) != 2 || !strings.Contains(tasks[0].JudgeNote, "顶部直切边约束冲突") {
		t.Fatalf("already attached top cut was moved: %d renders; %+v", len(ps.requests), tasks[0])
	}
}
