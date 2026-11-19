package jobs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"titao/internal/cfg"
	"titao/internal/judge"
	"titao/internal/photocraft"
)

// Opt-in replay of reported renders; never writes the source queue or production outputs.
func TestLiveReportedTuning(t *testing.T) {
	queue, config, output := os.Getenv("TITAO_TUNING_QUEUE"), os.Getenv("TITAO_PRECHECK_CONFIG"), os.Getenv("TITAO_TUNING_OUTPUT")
	if queue == "" || config == "" || output == "" {
		t.Skip("reported tuning QA not configured")
	}
	q, err := LoadQueue(queue)
	if err != nil || q == nil {
		t.Fatal("unable to load QA queue")
	}
	settings := cfg.Default()
	b, err := os.ReadFile(config)
	if err != nil || json.Unmarshal(b, &settings) != nil {
		t.Fatal("unable to load QA settings")
	}
	if os.Getenv("TITAO_TUNING_GEOMETRY_ONLY") == "1" {
		settings.AutoTuneOn = false
	}
	if filepath.Clean(output) == filepath.Clean(q.Options.OutputDir) {
		t.Fatal("QA cannot overwrite production output")
	}
	if err := os.MkdirAll(filepath.Join(output, "qa-state"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := cfg.NewStore(filepath.Join(output, "qa-state"))
	if err != nil {
		t.Fatal(err)
	}
	ps := photocraft.New(settings.PhotoCraftCLI)
	defer ps.Close()
	log, err := os.Create(filepath.Join(output, "task-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	enc := json.NewEncoder(log)
	e := New(ps, store, func(ev Event) {
		_ = enc.Encode(ev)
		if ev.Type == "log" {
			t.Log(ev.Message)
		}
	})
	jc := judge.Config{BaseURL: settings.BaseURL, APIKey: settings.APIKey, Model: settings.Model, MaxTokens: settings.MaxTokens, Timeout: time.Duration(settings.TimeoutSec) * time.Second, Temperature: settings.Temperature, ImageMaxSide: settings.ImageMaxSide, Thinking: settings.Thinking}
	if settings.TuneModel != "" {
		jc.Model = settings.TuneModel
	}
	if settings.TuneAPIKey != "" {
		jc.APIKey = settings.TuneAPIKey
	}
	opt := q.Options
	opt.OutputDir = output
	var results []*Task
	for _, task := range q.ToTasks() {
		if task.Phase != PhaseDone {
			continue
		}
		task.Phase, task.Message, task.JudgeNote, task.Error = PhasePending, "", "", ""
		task.Files, task.TuneHistory, task.TuneRounds = nil, nil, 0
		e.processOne(context.Background(), opt, settings, jc, task, output, len(results)+1, 4)
		results = append(results, task)
		b, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(output, "reported-run.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
		expectConflict := false
		for _, id := range strings.Split(os.Getenv("TITAO_TUNING_EXPECT_CONFLICT"), ",") {
			if strings.TrimSpace(id) == strconv.Itoa(task.ID) {
				expectConflict = true
			}
		}
		if expectConflict {
			if task.Phase != PhaseFailed || !strings.HasPrefix(task.JudgeNote, "约束冲突：") || len(task.Files) == 0 {
				t.Errorf("expected hard-edge conflict: %s %s", task.Phase, task.JudgeNote)
			}
		} else if task.Phase != PhaseDone {
			t.Errorf("%s: %s %s", task.PatternName, task.Phase, task.JudgeNote)
		}
	}
	if len(results) == 0 {
		t.Fatal("no reported exports found")
	}
}
