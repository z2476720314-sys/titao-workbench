package jobs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"titao/internal/cfg"
	"titao/internal/judge"
	"titao/internal/precheck"
)

func TestLiveRejectedImagesNeverRender(t *testing.T) {
	config, casesJSON := os.Getenv("TITAO_PRECHECK_CONFIG"), os.Getenv("TITAO_PRECHECK_CASES")
	if config == "" || casesJSON == "" {
		t.Skip("live precheck inputs not configured")
	}
	b, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Default()
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal("invalid config")
	}
	c.PrecheckOn = true
	c.PrecheckSkip = true
	var cases []struct {
		Path string `json:"path"`
		OK   bool   `json:"ok"`
	}
	if err := json.Unmarshal([]byte(casesJSON), &cases); err != nil {
		t.Fatal(err)
	}
	e, store, _ := newTestEngine(t)
	defer func() {
		if e.pre != nil {
			e.pre.Close()
		}
	}()
	var tasks []*Task
	var legacy []byte
	for _, tc := range cases {
		if tc.OK {
			continue
		}
		st, err := os.Stat(tc.Path)
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, &Task{ID: len(tasks) + 1, Pattern: tc.Path, PatternName: filepath.Base(tc.Path), Template: "not-rendered.tif", TemplateName: "not-rendered"})
		r, _ := json.Marshal(precheck.Record{Path: tc.Path, Size: st.Size(), MTime: st.ModTime().Unix(), OK: true, Reason: "legacy accepted"})
		legacy = append(legacy, r...)
		legacy = append(legacy, '\n')
	}
	if len(tasks) == 0 {
		t.Fatal("no rejected cases")
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(store.Path()), "precheck.jsonl"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	var rendered atomic.Int32
	e.processFn = func(engine *Engine, _ context.Context, _ Options, _ cfg.Config, _ judge.Config, task *Task, _ string, _, _ int) {
		rendered.Add(1)
		st, _ := os.Stat(task.Pattern)
		r, _ := engine.pre.Lookup(task.Pattern, st.Size(), st.ModTime().Unix())
		t.Logf("unexpected accept: %s reason=%s", task.PatternName, r.Reason)
	}
	done := make(chan Event, 1)
	e.emitFn = func(ev Event) {
		if ev.Type == "done" {
			done <- ev
		}
	}
	out := filepath.Join(t.TempDir(), "output")
	if err := e.begin(Options{OutputDir: out}, c, tasks, 0, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-done:
		if ev.Skipped != len(tasks) || ev.Failed != 0 || rendered.Load() != 0 {
			t.Fatalf("skipped=%d failed=%d renders=%d", ev.Skipped, ev.Failed, rendered.Load())
		}
	case <-time.After(2 * time.Minute):
		e.Stop()
		t.Fatal("precheck timed out")
	}
	for _, task := range e.Tasks() {
		if task.Phase != PhaseSkipped || len(task.Files) != 0 {
			t.Fatalf("bad task reached output: %+v", task)
		}
		t.Logf("skipped %s: %s", task.PatternName, task.Message)
	}
}
