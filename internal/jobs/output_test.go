package jobs

import (
	"context"
	"os"
	"strings"
	"testing"

	"titao/internal/cfg"
	"titao/internal/judge"
)

func TestBeginRejectsUnselectedOutputBeforeQueueWrite(t *testing.T) {
	for _, dir := range []string{"", "   "} {
		t.Run("blank_"+dir, func(t *testing.T) {
			e, _, _ := newTestEngine(t)
			e.processFn = func(*Engine, context.Context, Options, cfg.Config, judge.Config, *Task, string, int, int) {}
			err := e.begin(Options{OutputDir: dir}, cfg.Config{}, makeTasks(1), 0, 0)
			if err == nil {
				e.Stop()
				waitFor(t, "test run stops", func() bool { return e.State() == StateIdle })
				t.Fatal("blank output must fail before starting")
			}
			if !strings.Contains(err.Error(), "输出目录") {
				t.Fatal(err)
			}
			if e.State() != StateIdle {
				t.Fatal("validation claimed the engine")
			}
			if _, err := os.Stat(e.queuePath()); !os.IsNotExist(err) {
				t.Fatal("validation wrote a queue")
			}
		})
	}
}
