package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"titao/internal/cfg"
	"titao/internal/judge"
)

func preparedPipeline(t *testing.T) (*Engine, Options, string, string, *PreparedArtwork) {
	t.Helper()
	profile, source, approved, original := preparedFixture(t)
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := cfg.NewStore(profile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Update(func(c *cfg.Config) { c.OutputDir = filepath.Join(profile, "out"); c.PrecheckOn = false }); err != nil {
		t.Fatal(err)
	}
	e := New(nil, store, nil)
	template := filepath.Join(t.TempDir(), "template.tif")
	if err = os.WriteFile(template, []byte("fixture template"), 0600); err != nil {
		t.Fatal(err)
	}
	ref, err := RegisterPreparedArtwork(profile, source, approved, fixtureSHA(original))
	if err != nil {
		t.Fatal(err)
	}
	return e, Options{Patterns: []string{source}, Templates: []string{template}, OutputDir: store.Get().OutputDir, NoCrop: true}, source, approved, ref
}

func TestPreparedPipelinePreservesNameHistoryAndCompletionSkip(t *testing.T) {
	e, opt, source, approved, ref := preparedPipeline(t)
	tasks, err := e.Plan(opt)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("plan: %v %v", tasks, err)
	}
	task := tasks[0]
	if task.Pattern != ref.Path || task.OriginalPattern != source || task.OriginalSHA256 != ref.SourceSHA256 || task.PreparedSHA256 != ref.PreparedSHA256 || task.PatternName != "item" {
		t.Fatalf("lost prepared provenance: %+v", task)
	}
	var calls atomic.Int32
	e.processFn = completeToFiles(&calls, PhaseDone)
	startAndWait(t, e, opt)
	pending, err := e.Plan(opt)
	if err != nil || len(pending) != 0 {
		t.Fatalf("prepared completion not reused: %v %v", pending, err)
	}
	if _, err = RegisterPreparedArtwork(filepath.Dir(e.store.Path()), source, approved, ref.SourceSHA256); err != nil {
		t.Fatal(err)
	}
	pending, err = e.Plan(opt)
	if err != nil || len(pending) != 0 || calls.Load() != 1 {
		t.Fatalf("same approval reran: %v %v calls=%d", pending, err, calls.Load())
	}
	q, err := LoadQueue(e.queuePath())
	if err != nil {
		t.Fatal(err)
	}
	restored := q.ToTasks()[0]
	if restored.OriginalPattern != source || restored.OriginalSHA256 != ref.SourceSHA256 || restored.PreparedSHA256 != ref.PreparedSHA256 {
		t.Fatalf("queue lost actual input: %+v", restored)
	}
	if _, err = RegisterPreparedArtwork(filepath.Dir(e.store.Path()), source, source, ref.SourceSHA256); err != nil {
		t.Fatal(err)
	}
	pending, err = e.Plan(opt)
	if err != nil || len(pending) != 1 || pending[0].Pattern == ref.Path {
		t.Fatalf("new approved input not replanned: %v %v", pending, err)
	}
}

func TestPreparedPipelineValidatesSnapshotBeforeProcessing(t *testing.T) {
	e, opt, source, _, _ := preparedPipeline(t)
	tasks, err := e.Plan(opt)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, []byte("changed source"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	e.processFn = completeToFiles(&calls, PhaseDone)
	if err = e.begin(opt, e.store.Get(), tasks, 0, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "prepared failure", func() bool { return !e.IsRunning() })
	if calls.Load() != 0 || e.Tasks()[0].Phase != PhaseFailed || !strings.Contains(e.Tasks()[0].Error, "原图已变化") {
		t.Fatalf("stale prepared input processed: calls=%d task=%+v", calls.Load(), e.Tasks()[0])
	}
}

func TestPreparedPipelineChecksOriginalAgainBeforeRecordingCompletion(t *testing.T) {
	e, opt, source, _, _ := preparedPipeline(t)
	var calls atomic.Int32
	done := completeToFiles(&calls, PhaseDone)
	e.processFn = func(e *Engine, ctx context.Context, opt Options, c cfg.Config, j judge.Config, task *Task, out string, seq, total int) {
		done(e, ctx, opt, c, j, task, out, seq, total)
		if err := os.WriteFile(source, []byte("source changed during render"), 0600); err != nil {
			t.Error(err)
		}
	}
	startAndWait(t, e, opt)
	if !strings.Contains(e.Tasks()[0].Error, "原图已变化") {
		t.Fatalf("original mutation was not reported: %+v", e.Tasks()[0])
	}
	f, err := loadCompletions(e.completionPath())
	if err != nil || len(f.Records) != 0 {
		t.Fatalf("stale approval recorded complete: %v %v", f, err)
	}
}

func TestPreparedPlanDeduplicatesSameNamedSourceAliases(t *testing.T) {
	e, opt, source, _, _ := preparedPipeline(t)
	other := filepath.Join(t.TempDir(), filepath.Base(source))
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(other, data, 0600); err != nil {
		t.Fatal(err)
	}
	opt.Patterns = append(opt.Patterns, other)
	tasks, err := e.Plan(opt)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("same approved input scheduled twice: %v %v", tasks, err)
	}
}

func TestPreparedRunUsesCapturedApprovalAfterCatalogChanges(t *testing.T) {
	e, opt, source, _, ref := preparedPipeline(t)
	tasks, err := e.Plan(opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = RegisterPreparedArtwork(filepath.Dir(e.store.Path()), source, source, ref.SourceSHA256); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	e.processFn = completeToFiles(&calls, PhaseDone)
	if err = e.begin(opt, e.store.Get(), tasks, 0, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "captured approval", func() bool { return !e.IsRunning() })
	if calls.Load() != 1 || e.Tasks()[0].Pattern != ref.Path || e.Tasks()[0].PreparedSHA256 != ref.PreparedSHA256 || e.Tasks()[0].Error != "" {
		t.Fatalf("in-flight input changed with index: %+v", e.Tasks()[0])
	}
	newTasks, err := e.Plan(opt)
	if err != nil || len(newTasks) != 1 || newTasks[0].Pattern == ref.Path {
		t.Fatalf("next batch did not select latest approval: %v %v", newTasks, err)
	}
}
