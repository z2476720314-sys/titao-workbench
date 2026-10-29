package jobs

import (
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"titao/internal/cfg"
)

func TestPrecheckAndTuningUseSeparateModels(t *testing.T) {
	e, store, rec := newTestEngine(t)
	editor := &reviewPhotoshop{}
	e.ps = editor
	dir := t.TempDir()
	pattern, template := filepath.Join(dir, "pattern.png"), filepath.Join(dir, "template.tif")
	f, err := os.Create(pattern)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 40, 70))); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.WriteFile(template, []byte("template"), 0600); err != nil {
		t.Fatal(err)
	}
	var models []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		model, _ := body["model"].(string)
		models = append(models, model)
		wantAuth := "Bearer dummy"
		if model == "deepseek-v4.1-flash" {
			wantAuth = "Bearer placeholder"
		}
		if r.Header.Get("Authorization") != wantAuth {
			t.Error("model used the other stage's credential")
		}
		if model == "gpt-6-luna" && body["reasoning_effort"] != "none" {
			t.Error("precheck reasoning must remain disabled")
		}
		if model == "deepseek-v4.1-flash" && body["thinking"].(map[string]any)["type"] != "disabled" {
			t.Error("tuning reasoning must remain disabled")
		}
		content := `{"watermark":false,"lens":false,"case_edge":false,"case_photo":false,"reason":"完整图案","rounded_panels":false,"uncertain":false,"evidence":"正常图案","good":true,"score":90,"problem":"无","direction":"none","step":0.1,"pick":1}`
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	defer srv.Close()
	settings := store.Get()
	if err := json.Unmarshal([]byte(`{"model":"gpt-6-luna","tune_model":"deepseek-v4.1-flash","tune_api_key":"placeholder","precheck_on":true}`), &settings); err != nil {
		t.Fatal(err)
	}
	settings.BaseURL = srv.URL
	settings.APIKey = "dummy"
	task := &Task{ID: 1, Pattern: pattern, PatternName: "pattern", Template: template, TemplateName: "template", Phase: PhasePending}
	opt := Options{Auto: true, NoCrop: true, Fit: "auto", CellSize: 80, JPGQuality: 90, Scales: cfg.DefaultCandidates()}
	if err := e.begin(opt, settings, []*Task{task}, 0, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "completed routed task", func() bool { return rec.doneEvent() != nil })
	if e.pre != nil {
		e.pre.Close()
	}
	if task.Phase != PhaseDone {
		t.Fatalf("task=%s %s", task.Phase, task.Message)
	}
	if len(models) < 2 || models[0] != "gpt-6-luna" || models[len(models)-1] != "deepseek-v4.1-flash" {
		t.Fatalf("model routing=%v", models)
	}
	for _, model := range models[:len(models)-1] {
		if model != "gpt-6-luna" {
			t.Fatalf("non-precheck request before review: %v", models)
		}
	}
	if len(editor.requests) != 1 || len(editor.requests[0].Scales) != 1 || editor.requests[0].Scales[0] != 1 || editor.requests[0].OffsetX != 0 || editor.requests[0].OffsetY != 0 {
		t.Fatalf("default must render original size at center once: %+v", editor.requests)
	}
}
