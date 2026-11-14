package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"titao/internal/cfg"
)

// The v0.3.10 fingerprint serialization is a migration fixture, before final-frame validation.
func legacyFrameKey(t *testing.T, opt Options, def cfg.Config, task *Task) string {
	t.Helper()
	opt = effectiveJobOptions(opt, def)
	pat, err := completionInputOf(task.Pattern)
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := completionInputOf(task.Template)
	if err != nil {
		t.Fatal(err)
	}
	out, err := normalizedCompletionPath(filepath.Join(opt.OutputDir, task.PatternName))
	if err != nil {
		t.Fatal(err)
	}
	on, rounds := effectiveReviewSettings(opt, def)
	data, err := json.Marshal(struct {
		Pattern, Template                             completionInput
		Output, PatternName, TemplateName, Group, Fit string
		Scales                                        []float64
		Auto, ReviewOn                                bool
		MaxRounds                                     int
		TrimAlpha, NoCrop                             bool
		OffsetX, OffsetY                              float64
		CellSize, JPGQuality                          int
	}{pat, tpl, out, task.PatternName, task.TemplateName, opt.Group, opt.Fit, opt.Scales, opt.Auto, on, rounds, opt.TrimAlpha, opt.NoCrop, opt.OffsetX, opt.OffsetY, opt.CellSize, opt.JPGQuality})
	if err != nil {
		t.Fatal(err)
	}
	return fixtureSHA(data)
}

func TestStrictFrameDoesNotReuseUnverifiedLegacyCompletions(t *testing.T) {
	for _, strict := range []bool{false, true} {
		for _, recordedKey := range []bool{false, true} {
			t.Run(fmt.Sprint(strict, recordedKey), func(t *testing.T) {
				e, _, _ := newTestEngine(t)
				opt := completionInputs(t, e)
				opt.Patterns = opt.Patterns[:1]
				opt.Templates = opt.Templates[:1]
				opt.NoCrop = strict
				tasks, err := Plan(opt, e.store.Get())
				if err != nil {
					t.Fatal(err)
				}
				task := tasks[0]
				oldTime := time.Now().Add(-time.Minute)
				for _, p := range []string{task.Pattern, task.Template} {
					if err = os.Chtimes(p, oldTime, oldTime); err != nil {
						t.Fatal(err)
					}
				}
				file := filepath.Join(opt.OutputDir, "old.jpg")
				if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(file, []byte("legacy exported image"), 0600); err != nil {
					t.Fatal(err)
				}
				task.Phase, task.Files = PhaseDone, []string{file}
				oldKey := legacyFrameKey(t, opt, e.store.Get(), task)
				if recordedKey {
					task.CompletionKey = oldKey
				}
				if err = SaveQueue(e.queuePath(), NewQueue(opt, tasks)); err != nil {
					t.Fatal(err)
				}
				rec, err := completionRecordOf(task)
				if err != nil {
					t.Fatal(err)
				}
				if err = saveCompletions(e.completionPath(), &completionFile{Version: 1, Records: map[string]completionRecord{oldKey: rec}}); err != nil {
					t.Fatal(err)
				}
				pending, err := e.Plan(opt)
				if err != nil {
					t.Fatal(err)
				}
				want := 0
				if strict {
					want = 1
				}
				if len(pending) != want {
					t.Fatalf("legacy strict=%t key=%t pending=%d want%d", strict, recordedKey, len(pending), want)
				}
				current, err := loadCompletions(e.completionPath())
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := current.Records[oldKey]; !ok || len(current.Records) != 1 {
					t.Fatalf("legacy history deleted or re-labelled verified: %+v", current)
				}
			})
		}
	}
}
