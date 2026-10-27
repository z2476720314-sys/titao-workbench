package jobs

import (
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"titao/internal/cfg"
	"titao/internal/imgutil"
	"titao/internal/photocraft"
)

// Opt-in full-pipeline QA keeps credentials in memory and all state in the selected output directory.
func TestLiveFullMockups(t *testing.T) {
	manifest, config := os.Getenv("TITAO_FULL_QA"), os.Getenv("TITAO_PRECHECK_CONFIG")
	if manifest == "" || config == "" {
		t.Skip("full mockup QA inputs not configured")
	}
	b, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	settings := cfg.Default()
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatal("invalid runtime settings")
	}
	if model := os.Getenv("TITAO_PRECHECK_MODEL"); model != "" {
		settings.Model = model
	}
	var cases struct {
		Patterns []struct {
			Path   string `json:"path"`
			Reject bool   `json:"reject"`
		} `json:"patterns"`
		Templates []string `json:"templates"`
		Output    string   `json:"output"`
	}
	b, err = os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases.Patterns) == 0 || len(cases.Templates) == 0 || cases.Output == "" {
		t.Fatal("empty QA plan")
	}
	var paths []string
	rejected := map[string]bool{}
	for _, p := range cases.Patterns {
		paths = append(paths, p.Path)
		rejected[p.Path] = p.Reject
	}
	runtimeDir := filepath.Join(cases.Output, "qa-state")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := cfg.NewStore(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	ps := photocraft.New(settings.PhotoCraftCLI)
	defer ps.Close()
	version, err := ps.Probe()
	if err != nil {
		t.Fatal(err)
	}
	j, err := os.Create(filepath.Join(cases.Output, "task-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	enc := json.NewEncoder(j)
	done := make(chan Event, 1)
	e := New(ps, store, func(ev Event) {
		enc.Encode(ev)
		if ev.Type == "task" && ev.Task != nil && (ev.Task.Phase == PhaseDone || ev.Task.Phase == PhaseFailed || ev.Task.Phase == PhaseSkipped) {
			t.Logf("task=%d phase=%s template=%s files=%d fill_top=%t %s", ev.Task.ID, ev.Task.Phase, ev.Task.TemplateName, len(ev.Task.Files), ev.Task.FillTop, ev.Task.Message)
		}
		if ev.Type == "done" {
			done <- ev
		}
	})
	defer func() {
		if e.pre != nil {
			e.pre.Close()
		}
	}()
	opt := Options{Patterns: paths, Templates: cases.Templates, OutputDir: cases.Output, Group: settings.Group, Fit: settings.Fit, Scales: settings.Candidates, Auto: true, TrimAlpha: settings.TrimAlpha, NoCrop: settings.NoCropOn(), CellSize: settings.CellSize, JPGQuality: settings.JPGQuality, MaxRounds: settings.MaxRounds}
	tasks, err := Plan(opt, settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.begin(opt, settings, tasks, 0, 0); err != nil {
		t.Fatal(err)
	}
	var final Event
	select {
	case final = <-done:
	case <-time.After(25 * time.Minute):
		e.Stop()
		t.Fatal("QA timeout")
	}
	tasks = e.Tasks()
	files := 0
	for _, task := range tasks {
		if rejected[task.Pattern] {
			if task.Phase != PhaseSkipped || len(task.Files) != 0 {
				t.Errorf("rejected pattern was not skipped: %d %s", task.ID, task.Phase)
			}
		} else if task.Phase != PhaseDone {
			t.Errorf("task not exported: %d %s %s", task.ID, task.Phase, task.Error)
		}
		for _, p := range task.Files {
			f, err := os.Open(p)
			if err != nil {
				t.Error(err)
				continue
			}
			meta, _, err := image.DecodeConfig(f)
			f.Close()
			if err != nil {
				t.Error(err)
				continue
			}
			if meta.Width != settings.CellSize || meta.Height != settings.CellSize {
				t.Error("output dimensions changed")
			}
			if _, err := imgutil.Decode(p); err != nil {
				t.Error(err)
			}
			files++
		}
	}
	report := map[string]any{"model": settings.Model, "engine_version": version, "Done": final.Done, "Failed": final.Failed, "Skipped": final.Skipped, "Files": files, "tasks": tasks}
	b, err = json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cases.Output, "full-run.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("SUMMARY groups=%d done=%d failed=%d skipped=%d files=%d", len(tasks), final.Done, final.Failed, final.Skipped, files)
	wantSkipped := 0
	for _, p := range cases.Patterns {
		if p.Reject {
			wantSkipped += len(cases.Templates)
		}
	}
	if final.Done != len(tasks)-wantSkipped || final.Failed != 0 || final.Skipped != wantSkipped {
		t.Fatal("not all QA groups exported")
	}
}
