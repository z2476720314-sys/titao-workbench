package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"titao/internal/cfg"
	"titao/internal/judge"
)

func completionInputs(t *testing.T, e *Engine) Options {
	t.Helper()
	dir := t.TempDir()
	opt := Options{OutputDir: e.store.Get().OutputDir}
	for _, name := range []string{"a.png", "b.png", "c.png"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("pattern"), 0600); err != nil {
			t.Fatal(err)
		}
		opt.Patterns = append(opt.Patterns, p)
	}
	for _, name := range []string{"a.tif", "b.tif"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("template"), 0600); err != nil {
			t.Fatal(err)
		}
		opt.Templates = append(opt.Templates, p)
	}
	return opt
}

func withPatternLimit(t *testing.T, opt Options, limit int) Options {
	t.Helper()
	data, err := json.Marshal(opt)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["patternLimit"] = limit
	data, _ = json.Marshal(fields)
	if err := json.Unmarshal(data, &opt); err != nil {
		t.Fatal(err)
	}
	return opt
}

func completeToFiles(calls *atomic.Int32, phase Phase) func(*Engine, context.Context, Options, cfg.Config, judge.Config, *Task, string, int, int) {
	return func(e *Engine, _ context.Context, _ Options, _ cfg.Config, _ judge.Config, task *Task, root string, _, _ int) {
		calls.Add(1)
		p := filepath.Join(root, task.PatternName, task.TemplateName+".jpg")
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			e.failTask(task, err)
			return
		}
		if err := os.WriteFile(p, []byte("exported image"), 0600); err != nil {
			e.failTask(task, err)
			return
		}
		e.updateTask(task, func(task *Task) {
			task.Phase, task.Progress = phase, 100
			task.Files = []string{p}
			task.Message, task.JudgeNote = "生成 1 张（请人工复核）", "请人工复核"
		})
		if phase == PhaseDone {
			e.mu.Lock()
			e.done++
			e.mu.Unlock()
		}
	}
}

func startAndWait(t *testing.T, e *Engine, opt Options) {
	t.Helper()
	if err := e.Start(opt); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "batch complete", func() bool { return !e.IsRunning() })
}

func TestPatternLimitCountsPatternsWithAllTemplates(t *testing.T) {
	e, _, _ := newTestEngine(t)
	opt := withPatternLimit(t, completionInputs(t, e), 1)
	opt.PatternName = "single-pattern-only"
	tasks, err := Plan(opt, e.store.Get())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || tasks[0].PatternName != "a" || tasks[1].PatternName != "a" {
		t.Fatalf("one pattern must retain both templates: %+v", tasks)
	}
	all, err := Plan(withPatternLimit(t, opt, 0), e.store.Get())
	if err != nil || len(all) != 6 {
		t.Fatalf("zero limit must include all patterns: %d %v", len(all), err)
	}
	opt = withPatternLimit(t, opt, -1)
	if _, err := Plan(opt, e.store.Get()); err == nil {
		t.Fatal("negative pattern limit accepted")
	}
}

func TestCompletionLimitFinishesRemainingTemplatesBeforeNextPattern(t *testing.T) {
	e, _, _ := newTestEngine(t)
	opt := withPatternLimit(t, completionInputs(t, e), 1)
	first := opt
	first.Patterns, first.Templates = first.Patterns[:1], first.Templates[:1]
	var calls atomic.Int32
	e.processFn = completeToFiles(&calls, PhaseDone)
	startAndWait(t, e, first)
	pending, err := e.Plan(opt)
	if err != nil || len(pending) != 1 || pending[0].PatternName != "a" || pending[0].TemplateName != "b" {
		t.Fatalf("one partially completed pattern must retain only its unfinished template: %+v %v", pending, err)
	}
}

func TestCompletionPersistsAcrossBatchesAndLimitsAfterSkipping(t *testing.T) {
	e, store, _ := newTestEngine(t)
	opt := withPatternLimit(t, completionInputs(t, e), 1)
	var calls atomic.Int32
	e.processFn = completeToFiles(&calls, PhaseDone)
	startAndWait(t, e, opt)
	if calls.Load() != 2 {
		t.Fatalf("first batch calls = %d", calls.Load())
	}
	if err := e.ClearQueue(); err != nil {
		t.Fatal(err)
	}
	e = New(nil, store, nil)
	e.processFn = completeToFiles(&calls, PhaseDone)
	startAndWait(t, e, opt)
	tasks := e.Tasks()
	if len(tasks) != 2 || tasks[0].PatternName != "b" || tasks[1].PatternName != "b" {
		t.Fatalf("second batch must advance to next unfinished pattern: %+v", tasks)
	}
	startAndWait(t, e, opt)
	if e.Tasks()[0].PatternName != "c" {
		t.Fatal("third batch did not advance to c")
	}
	if err := e.Start(opt); err == nil || !strings.Contains(err.Error(), "已完成") {
		t.Fatalf("all completed should report no work: %v", err)
	}
	if calls.Load() != 6 {
		t.Fatalf("completed outputs reran, calls=%d", calls.Load())
	}
}

func TestCompletionMissingOrEmptyOutputAndChangedInputsRedo(t *testing.T) {
	for _, change := range []string{"missing", "empty", "pattern", "template", "placement", "output"} {
		t.Run(change, func(t *testing.T) {
			e, _, _ := newTestEngine(t)
			opt := completionInputs(t, e)
			opt.Patterns, opt.Templates = opt.Patterns[:1], opt.Templates[:1]
			var calls atomic.Int32
			e.processFn = completeToFiles(&calls, PhaseDone)
			startAndWait(t, e, opt)
			var err error
			switch change {
			case "missing":
				err = os.Remove(e.Tasks()[0].Files[0])
			case "empty":
				err = os.WriteFile(e.Tasks()[0].Files[0], nil, 0600)
			case "pattern":
				err = os.WriteFile(opt.Patterns[0], []byte("changed pattern"), 0600)
			case "template":
				err = os.WriteFile(opt.Templates[0], []byte("changed template"), 0600)
			case "placement":
				opt.OffsetX = 0.2
			case "output":
				opt.OutputDir = filepath.Join(t.TempDir(), "other")
			}
			if err != nil {
				t.Fatal(err)
			}
			startAndWait(t, e, opt)
			if calls.Load() != 2 {
				t.Fatalf("changed %s was not rerun", change)
			}
		})
	}
}

func TestCompletionNeverRecordsOtherPhases(t *testing.T) {
	for _, phase := range []Phase{PhaseFailed, PhaseCanceled, PhasePending, PhaseSkipped} {
		t.Run(string(phase), func(t *testing.T) {
			e, _, _ := newTestEngine(t)
			opt := completionInputs(t, e)
			opt.Patterns, opt.Templates = opt.Patterns[:1], opt.Templates[:1]
			var calls atomic.Int32
			e.processFn = completeToFiles(&calls, phase)
			startAndWait(t, e, opt)
			startAndWait(t, e, opt)
			if calls.Load() != 2 {
				t.Fatalf("phase %s was treated as complete", phase)
			}
		})
	}
}

func TestCorruptCompletionBlocksStartWithoutOverwritingQueue(t *testing.T) {
	e, _, _ := newTestEngine(t)
	opt := completionInputs(t, e)
	path := filepath.Join(filepath.Dir(e.store.Path()), "completed.json")
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	q := []byte("preserve current queue")
	if err := os.WriteFile(e.queuePath(), q, 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	e.processFn = completeToFiles(&calls, PhaseDone)
	err := e.Start(opt)
	if err == nil {
		waitFor(t, "finish unexpected run", func() bool { return !e.IsRunning() })
		t.Fatal("corrupt completion file allowed a run")
	}
	data, readErr := os.ReadFile(e.queuePath())
	if readErr != nil || string(data) != string(q) {
		t.Fatalf("queue overwritten: %s, %v", data, readErr)
	}
	if calls.Load() != 0 {
		t.Fatal(fmt.Sprintf("corrupt completion caused %d calls", calls.Load()))
	}
}

func TestLegacyQueueSeedsOnlyUnchangedExportedInputs(t *testing.T) {
	for _, change := range []string{"unchanged", "new_pattern", "new_template", "missing_output", "different_placement", "different_output"} {
		t.Run(change, func(t *testing.T) {
			e, _, _ := newTestEngine(t)
			opt := completionInputs(t, e)
			opt.Patterns, opt.Templates = opt.Patterns[:1], opt.Templates[:1]
			tasks, err := Plan(opt, e.store.Get())
			if err != nil {
				t.Fatal(err)
			}
			inputTime, outputTime := time.Now().Add(-time.Hour), time.Now().Add(-time.Minute)
			for _, p := range []string{opt.Patterns[0], opt.Templates[0]} {
				if err := os.Chtimes(p, inputTime, inputTime); err != nil {
					t.Fatal(err)
				}
			}
			out := filepath.Join(opt.OutputDir, "a", "a_a_01.jpg")
			if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(out, []byte("old exported image"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(out, outputTime, outputTime); err != nil {
				t.Fatal(err)
			}
			tasks[0].Phase, tasks[0].Files, tasks[0].JudgeNote = PhaseDone, []string{out}, "请人工复核"
			if err := SaveQueue(e.queuePath(), NewQueue(opt, tasks)); err != nil {
				t.Fatal(err)
			}
			originalQueue, _ := os.ReadFile(e.queuePath())
			switch change {
			case "new_pattern":
				if err := os.Chtimes(opt.Patterns[0], time.Now(), time.Now()); err != nil {
					t.Fatal(err)
				}
			case "new_template":
				if err := os.Chtimes(opt.Templates[0], time.Now(), time.Now()); err != nil {
					t.Fatal(err)
				}
			case "missing_output":
				if err := os.Remove(out); err != nil {
					t.Fatal(err)
				}
			case "different_placement":
				opt.OffsetY = 0.2
			case "different_output":
				opt.OutputDir = t.TempDir()
			}
			pending, err := e.Plan(opt)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if change == "unchanged" {
				want = 0
			}
			if len(pending) != want {
				t.Fatalf("legacy %s: pending=%d want=%d", change, len(pending), want)
			}
			afterQueue, _ := os.ReadFile(e.queuePath())
			if string(afterQueue) != string(originalQueue) {
				t.Fatal("planning changed real queue state")
			}
			if change == "unchanged" {
				data, err := os.ReadFile(e.completionPath())
				if err != nil || !strings.Contains(string(data), "人工复核") {
					t.Fatalf("warning not retained: %s %v", data, err)
				}
			}
		})
	}
}

func TestCompletionSaveFailureStopsAndReportsUnrecordedOutput(t *testing.T) {
	e, _, rec := newTestEngine(t)
	opt := completionInputs(t, e)
	var calls atomic.Int32
	process := completeToFiles(&calls, PhaseDone)
	e.processFn = func(e *Engine, ctx context.Context, opt Options, def cfg.Config, jc judge.Config, task *Task, root string, seq, total int) {
		process(e, ctx, opt, def, jc, task, root, seq, total)
		if err := os.Mkdir(e.completionPath()+".tmp", 0755); err != nil {
			t.Error(err)
		}
	}
	startAndWait(t, e, opt)
	if calls.Load() != 1 {
		t.Fatalf("save failure did not stop remaining work: %d", calls.Load())
	}
	waitFor(t, "done event", func() bool { return rec.doneEvent() != nil })
	if !strings.Contains(rec.doneEvent().Message, "完成记录保存失败") {
		t.Fatalf("save failure hidden from final status: %+v", rec.doneEvent())
	}
	if _, err := os.Stat(e.completionPath()); !os.IsNotExist(err) {
		t.Fatalf("failed write claimed a saved record: %v", err)
	}
	if _, err := os.Stat(e.Tasks()[0].Files[0]); err != nil {
		t.Fatal("save failure removed exported file")
	}
}

func TestCompletionModelsAndKeysDoNotCauseRerunOrEnterRecords(t *testing.T) {
	e, store, _ := newTestEngine(t)
	opt := completionInputs(t, e)
	opt.Patterns, opt.Templates = opt.Patterns[:1], opt.Templates[:1]
	var calls atomic.Int32
	e.processFn = completeToFiles(&calls, PhaseDone)
	startAndWait(t, e, opt)
	if _, err := store.Update(func(c *cfg.Config) {
		c.Model = "changed-model"
		c.APIKey = "<credential-fixture-1>"
		c.TuneModel = "changed-tune-model"
		c.TuneAPIKey = "<credential-fixture-2>"
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := e.Plan(opt)
	if err != nil || len(pending) != 0 {
		t.Fatalf("model/key change invalidated output: %v %d", err, len(pending))
	}
	data, err := os.ReadFile(e.completionPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"changed-model", "<credential-fixture-1>", "changed-tune-model", "<credential-fixture-2>", "api_key", "base_url"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("completion record includes %s", forbidden)
		}
	}
}

func TestCompletionOutputReplacementDoesNotReuseOldRecord(t *testing.T) {
	for _, change := range []string{"replaced_file", "parameter_round_trip"} {
		t.Run(change, func(t *testing.T) {
			e, _, _ := newTestEngine(t)
			opt := completionInputs(t, e)
			opt.Patterns, opt.Templates = opt.Patterns[:1], opt.Templates[:1]
			var calls atomic.Int32
			e.processFn = completeToFiles(&calls, PhaseDone)
			startAndWait(t, e, opt)
			if change == "replaced_file" {
				if err := os.WriteFile(e.Tasks()[0].Files[0], []byte("replaced image"), 0600); err != nil {
					t.Fatal(err)
				}
				newTime := time.Now().Add(time.Minute)
				if err := os.Chtimes(e.Tasks()[0].Files[0], newTime, newTime); err != nil {
					t.Fatal(err)
				}
			} else {
				changed := opt
				changed.OffsetX = 0.2
				startAndWait(t, e, changed)
			}
			pending, err := e.Plan(opt)
			if err != nil || len(pending) != 1 {
				t.Fatalf("old record accepted overwritten output: pending=%d err=%v", len(pending), err)
			}
		})
	}
}

func TestClearQueuePreservesLegacyCompletions(t *testing.T) {
	e, _, _ := newTestEngine(t)
	opt := completionInputs(t, e)
	opt.Patterns, opt.Templates = opt.Patterns[:1], opt.Templates[:1]
	tasks, err := Plan(opt, e.store.Get())
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "existing.jpg")
	if err := os.WriteFile(out, []byte("old export"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	for _, p := range []string{opt.Patterns[0], opt.Templates[0]} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	tasks[0].Phase, tasks[0].Files = PhaseDone, []string{out}
	if err := SaveQueue(e.queuePath(), NewQueue(opt, tasks)); err != nil {
		t.Fatal(err)
	}
	if err := e.ClearQueue(); err != nil {
		t.Fatal(err)
	}
	pending, err := e.Plan(opt)
	if err != nil || len(pending) != 0 {
		t.Fatalf("clearing queue lost completed history: %d %v", len(pending), err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
}

func TestCompletionEffectiveReviewSettingsInvalidateHistory(t *testing.T) {
	for _, setting := range []string{"enabled", "rounds"} {
		t.Run(setting, func(t *testing.T) {
			e, store, _ := newTestEngine(t)
			opt := completionInputs(t, e)
			opt.Auto = true
			opt.Patterns, opt.Templates = opt.Patterns[:1], opt.Templates[:1]
			if _, err := store.Update(func(c *cfg.Config) { c.AutoTuneOn = setting != "enabled"; c.MaxRounds = 1 }); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			e.processFn = completeToFiles(&calls, PhaseDone)
			startAndWait(t, e, opt)
			if _, err := store.Update(func(c *cfg.Config) { c.AutoTuneOn = true; c.MaxRounds = 2 }); err != nil {
				t.Fatal(err)
			}
			pending, err := e.Plan(opt)
			if err != nil || len(pending) != 1 {
				t.Fatalf("effective review %s change reused old export: %d %v", setting, len(pending), err)
			}
		})
	}
}

func TestCompletionRefreshesPendingInputAfterQueueRestart(t *testing.T) {
	e, store, _ := newTestEngine(t)
	opt := completionInputs(t, e)
	opt.Patterns = opt.Patterns[:1]
	tasks, err := e.Plan(opt)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveQueue(e.queuePath(), NewQueue(opt, tasks)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opt.Patterns[0], []byte("edited before restart"), 0600); err != nil {
		t.Fatal(err)
	}
	e = New(nil, store, nil)
	var calls atomic.Int32
	e.processFn = completeToFiles(&calls, PhaseDone)
	if err := e.Resume(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "restarted batch finished", func() bool { return !e.IsRunning() })
	if calls.Load() != 2 {
		t.Fatalf("pending input change stopped restart after %d calls", calls.Load())
	}
	for _, task := range e.Tasks() {
		if task.Phase != PhaseDone || task.Error != "" {
			t.Fatalf("pre-start edit treated as in-flight change: %+v", task)
		}
	}
}

func TestCompletionInputChangedDuringExecutionIsNotRecorded(t *testing.T) {
	e, _, _ := newTestEngine(t)
	opt := completionInputs(t, e)
	opt.Patterns = opt.Patterns[:1]
	var calls atomic.Int32
	process := completeToFiles(&calls, PhaseDone)
	e.processFn = func(e *Engine, ctx context.Context, opt Options, def cfg.Config, jc judge.Config, task *Task, root string, seq, total int) {
		if err := os.WriteFile(task.Pattern, []byte("edited while processing"), 0600); err != nil {
			t.Error(err)
		}
		process(e, ctx, opt, def, jc, task, root, seq, total)
	}
	startAndWait(t, e, opt)
	if calls.Load() != 1 || !strings.Contains(e.Tasks()[0].Error, "期间") {
		t.Fatalf("in-flight mutation was accepted: %d %+v", calls.Load(), e.Tasks())
	}
	if _, err := os.Stat(e.completionPath()); !os.IsNotExist(err) {
		t.Fatalf("in-flight mutation was saved: %v", err)
	}
}

func TestCompletionRecognizesStrippedOutputDirectory(t *testing.T) {
	for _, change := range []string{"legacy", "stored", "missing", "changed_metadata", "escape"} {
		t.Run(change, func(t *testing.T) {
			e, _, _ := newTestEngine(t)
			opt := completionInputs(t, e)
			opt.Templates = opt.Templates[:1]
			code := "WMTAI1056"
			pattern := filepath.Join(filepath.Dir(opt.Patterns[0]), "名称@"+code+".png")
			if err := os.Rename(opt.Patterns[0], pattern); err != nil {
				t.Fatal(err)
			}
			opt.Patterns = []string{pattern}
			tasks, err := Plan(opt, e.store.Get())
			if err != nil {
				t.Fatal(err)
			}
			old := filepath.Join(opt.OutputDir, tasks[0].PatternName)
			if change == "escape" {
				old = filepath.Join(filepath.Dir(opt.OutputDir), "outside", tasks[0].PatternName)
			}
			if err := os.MkdirAll(old, 0755); err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= 7; i++ {
				p := filepath.Join(old, fmt.Sprintf("product_%02d.jpg", i))
				if err := os.WriteFile(p, []byte("exported image"), 0600); err != nil {
					t.Fatal(err)
				}
				tasks[0].Files = append(tasks[0].Files, p)
			}
			tasks[0].Phase, tasks[0].Message = PhaseDone, "生成7张，请人工复核"
			past := time.Now().Add(-time.Hour)
			for _, p := range []string{pattern, opt.Templates[0]} {
				if err := os.Chtimes(p, past, past); err != nil {
					t.Fatal(err)
				}
			}
			if err := SaveQueue(e.queuePath(), NewQueue(opt, tasks)); err != nil {
				t.Fatal(err)
			}
			original, _ := os.ReadFile(e.queuePath())
			if change == "stored" || change == "changed_metadata" {
				if pending, err := e.Plan(opt); err != nil || len(pending) != 0 {
					t.Fatalf("seed before rename: %d %v", len(pending), err)
				}
			}
			actual := filepath.Join(opt.OutputDir, code)
			if change == "escape" {
				actual = filepath.Join(filepath.Dir(old), code)
			}
			if err := os.Rename(old, actual); err != nil {
				t.Fatal(err)
			}
			if change == "missing" {
				if err := os.Remove(filepath.Join(actual, "product_07.jpg")); err != nil {
					t.Fatal(err)
				}
			}
			if change == "changed_metadata" {
				if err := os.WriteFile(filepath.Join(actual, "product_07.jpg"), []byte("different export"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			pending, err := e.Plan(withPatternLimit(t, opt, 1))
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if change == "missing" || change == "escape" || change == "changed_metadata" {
				want = 1
			}
			if len(pending) != want {
				t.Fatalf("stripped %s pending=%d want=%d", change, len(pending), want)
			}
			q, err := LoadQueue(e.queuePath())
			if err != nil {
				t.Fatal(err)
			}
			restored := RestoreQueueTasks(q, opt.OutputDir)
			if want == 0 {
				if len(restored[0].Files) != 7 || restored[0].OutputDir != actual || filepath.Dir(restored[0].Files[0]) != actual {
					t.Fatalf("restored open path did not follow renamed output: %+v", restored[0])
				}
				key, err := completionKey(opt, e.store.Get(), tasks[0])
				if err != nil {
					t.Fatal(err)
				}
				history, err := loadCompletions(e.completionPath())
				if err != nil {
					t.Fatal(err)
				}
				if !history.Records[key].valid() {
					t.Fatal("renamed paths were not saved in completion history")
				}
			} else if change == "missing" || change == "escape" {
				if restored[0].Files[0] != tasks[0].Files[0] {
					t.Fatal("partial or escaping candidate was accepted")
				}
			}
			after, _ := os.ReadFile(e.queuePath())
			if string(after) != string(original) {
				t.Fatal("resolution modified source queue")
			}
		})
	}
}
