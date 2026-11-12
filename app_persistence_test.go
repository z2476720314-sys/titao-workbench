package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"titao/internal/cfg"
	"titao/internal/jobs"
)

type workspaceSaver interface {
	SaveWorkspace([]string, []string, string) (int64, error)
}

func TestGetConfigRestoresLegacyWorkspaceWithoutRewritingAndRespectsClearedPaths(t *testing.T) {
	dir := t.TempDir()
	store, err := cfg.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(func(c *cfg.Config) {
		c.PatternDir, c.TemplateDir, c.OutputDir = "stale-pattern-default", "stale-template-default", "stale-output-default"
	}); err != nil {
		t.Fatal(err)
	}
	opt := jobs.Options{Patterns: []string{"last-pattern-file", "last-pattern-directory"}, Templates: []string{"last-template"}, OutputDir: "last-output"}
	q := jobs.NewQueue(opt, []*jobs.Task{{ID: 1, Phase: jobs.PhaseDone}})
	q.Finished = true
	queuePath := filepath.Join(dir, "queue.json")
	if err := jobs.SaveQueue(queuePath, q); err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	queueBefore, err := os.ReadFile(queuePath)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: store}
	result, err := a.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	got := result["config"].(cfg.Config)
	if !reflect.DeepEqual(got.PatternPaths, opt.Patterns) || !reflect.DeepEqual(got.TemplatePaths, opt.Templates) || got.OutputDir != opt.OutputDir {
		t.Fatal("legacy workspace must prefer the last queue's exact choices over stale defaults")
	}
	configAfter, err := os.ReadFile(store.Path())
	if err != nil || !bytes.Equal(configBefore, configAfter) {
		t.Fatal("reading legacy workspace rewrote the configuration")
	}
	queueAfter, err := os.ReadFile(queuePath)
	if err != nil || !bytes.Equal(queueBefore, queueAfter) {
		t.Fatal("reading legacy workspace rewrote the queue")
	}
	if _, err := a.SaveConfig(got); err != nil {
		t.Fatal(err)
	}
	reloaded, err := cfg.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.Get().PatternPaths, opt.Patterns) || !reflect.DeepEqual(reloaded.Get().TemplatePaths, opt.Templates) {
		t.Fatal("saving settings lost the workspace restored from a legacy queue")
	}
	if _, err := a.SaveWorkspace([]string{}, []string{}, ""); err != nil {
		t.Fatal(err)
	}
	store, err = cfg.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a = &App{store: store}
	result, err = a.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	got = result["config"].(cfg.Config)
	if got.PatternPaths == nil || got.TemplatePaths == nil || len(got.PatternPaths)+len(got.TemplatePaths) != 0 || got.OutputDir != "" {
		t.Fatal("explicitly cleared paths were resurrected from the old queue after restart")
	}
}

func TestWorkspaceChoicesSurviveRestartWithoutReplacingModelSettings(t *testing.T) {
	dir := t.TempDir()
	s, err := cfg.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(func(c *cfg.Config) {
		c.Model = "saved-model"
		c.APIKey = "placeholder"
	}); err != nil {
		t.Fatal(err)
	}
	a := &App{store: s}
	saver, ok := any(a).(workspaceSaver)
	if !ok {
		t.Fatal("App must expose a path-only SaveWorkspace operation")
	}
	patterns := []string{filepath.Join(dir, "图案.png"), filepath.Join(dir, "图案目录")}
	templates := []string{filepath.Join(dir, "模板.tif")}
	output := filepath.Join(dir, "输出")
	stale := s.Get()
	if _, err := saver.SaveWorkspace(patterns, templates, output); err != nil {
		t.Fatal(err)
	}
	// Settings loaded before the path change must not erase the selected lists.
	stale.Model = "edited-model"
	if _, err := a.SaveConfig(stale); err == nil {
		t.Fatal("settings loaded before workspace changes must be rejected")
	}
	current, err := a.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	edited := current["config"].(cfg.Config)
	edited.Model = "edited-model"
	if _, err := a.SaveConfig(edited); err != nil {
		t.Fatal(err)
	}
	patterns[0] = "mutated-caller"
	reloaded, err := cfg.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.Get()
	var paths struct {
		Patterns  []string `json:"pattern_paths"`
		Templates []string `json:"template_paths"`
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &paths); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths.Patterns, []string{filepath.Join(dir, "图案.png"), filepath.Join(dir, "图案目录")}) || !reflect.DeepEqual(paths.Templates, templates) || got.OutputDir != output {
		t.Fatal("reopening lost or replaced the selected workspace paths")
	}
	if got.Model != "edited-model" || got.APIKey != "placeholder" {
		t.Fatal("saving workspace paths replaced model settings")
	}
	if _, err := saver.SaveWorkspace([]string{}, []string{}, ""); err != nil {
		t.Fatal(err)
	}
	reloaded, err = cfg.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(reloaded.Get())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &paths); err != nil {
		t.Fatal(err)
	}
	if paths.Patterns == nil || paths.Templates == nil || len(paths.Patterns)+len(paths.Templates) != 0 || reloaded.Get().OutputDir != "" {
		t.Fatal("explicitly cleared paths must survive restart as empty lists")
	}
}

func TestRunStatusRestoresHistoryWithoutStartingOrRewritingQueue(t *testing.T) {
	for _, finished := range []bool{false, true} {
		t.Run(map[bool]string{false: "unfinished", true: "finished"}[finished], func(t *testing.T) {
			dir := t.TempDir()
			store, err := cfg.NewStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			tasks := []*jobs.Task{{ID: 1, PatternName: "图案", TemplateName: "模板", Phase: jobs.PhaseDone, Files: []string{filepath.Join(dir, "output", "图案", "1.jpg")}, ChosenScale: 1.15, ElapsedMS: 1234, JudgeNote: "completed", TuneRounds: 2}}
			if !finished {
				tasks = append(tasks, &jobs.Task{ID: 2, PatternName: "待执行", Phase: jobs.PhasePending})
			}
			q := jobs.NewQueue(jobs.Options{OutputDir: filepath.Join(dir, "output")}, tasks)
			q.Finished = finished
			path := filepath.Join(dir, "queue.json")
			if err := jobs.SaveQueue(path, q); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			eng := jobs.New(nil, store, nil)
			a := &App{store: store, eng: eng}
			status := a.RunStatus()
			got := status["tasks"].([]*jobs.Task)
			if len(got) != len(tasks) {
				t.Fatalf("reopened history has %d tasks, want %d", len(got), len(tasks))
			}
			if got[0].Phase != jobs.PhaseDone || got[0].Progress != 100 || got[0].ElapsedMS != 1234 || got[0].ChosenScale != 1.15 || got[0].JudgeNote != "completed" || !reflect.DeepEqual(got[0].Files, tasks[0].Files) {
				t.Fatal("restored task lost its completed result")
			}
			if got[0].OutputDir != filepath.Dir(tasks[0].Files[0]) {
				t.Fatal("restored completed task must retain its output directory action")
			}
			if !finished && got[1].Phase != jobs.PhasePending {
				t.Fatal("restoring history changed the unfinished task")
			}
			if status["running"] != false || status["state"] != "idle" || eng.IsRunning() || len(eng.Tasks()) != 0 {
				t.Fatal("viewing history must not start or mutate the engine")
			}
			pending, err := a.PendingQueue()
			if err != nil || (finished && pending != nil) || (!finished && (pending == nil || pending.Pending != 1)) {
				t.Fatalf("pending-queue semantics changed: finished=%v, error=%v", finished, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("viewing history rewrote the queue")
			}
		})
	}
}

func TestRunStatusReportsUnreadableHistoryWithoutDeletingIt(t *testing.T) {
	dir := t.TempDir()
	store, err := cfg.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "queue.json")
	data := []byte("{invalid queue")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	a := &App{store: store, eng: jobs.New(nil, store, nil)}
	status := a.RunStatus()
	if status["error"] == nil || status["error"] == "" {
		t.Fatal("unreadable history must report an error")
	}
	if _, err := a.PendingQueue(); err == nil {
		t.Fatal("startup pending-queue check must report unreadable history")
	}
	config, err := a.GetConfig()
	warning, hasWarning := config["workspaceError"].(string)
	if err != nil || config["config"] == nil || !hasWarning || warning == "" {
		t.Fatal("unreadable legacy workspace must preserve configuration and report its error")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("an unreadable queue must remain available for recovery")
	}
}

func TestWorkspaceSaveFailureIsReportedWithoutOverwritingSavedFile(t *testing.T) {
	s, err := cfg.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: s}
	saved := s.Get()
	before, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.Path()+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveWorkspace([]string{"draft-pattern"}, []string{"draft-template"}, "draft-output"); err == nil {
		t.Fatal("unwritable configuration must return an error")
	}
	after, err := os.ReadFile(s.Path())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed path save overwrote the previously saved configuration")
	}
	if !reflect.DeepEqual(s.Get(), saved) {
		t.Fatal("failed path save must not advance in-memory workspace state")
	}
	if _, err := (&App{}).SaveWorkspace(nil, nil, ""); err == nil {
		t.Fatal("uninitialized configuration must return an error")
	}
}

func TestCurrentSettingsCanEditOutputDirectory(t *testing.T) {
	store, err := cfg.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: store}
	if _, err := a.SaveWorkspace([]string{"pattern"}, []string{"template"}, "first-output"); err != nil {
		t.Fatal(err)
	}
	loaded, err := a.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	edited := loaded["config"].(cfg.Config)
	edited.OutputDir = "explicit-settings-output"
	if _, err := a.SaveConfig(edited); err != nil {
		t.Fatal(err)
	}
	if store.Get().OutputDir != "explicit-settings-output" {
		t.Fatal("current settings must still support an explicit output-directory edit")
	}
	if _, err := a.SaveConfig(edited); err == nil {
		t.Fatal("an output-directory edit must invalidate older settings snapshots")
	}
}

func TestConcurrentOutputEditsRejectOneStaleSnapshot(t *testing.T) {
	store, err := cfg.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: store}
	loaded := store.Get()
	results := make(chan error, 2)
	for _, output := range []string{"first-output", "second-output"} {
		go func(output string) {
			draft := loaded
			draft.OutputDir = output
			_, err := a.SaveConfig(draft)
			results <- err
		}(output)
	}
	succeeded := 0
	for range 2 {
		if <-results == nil {
			succeeded++
		}
	}
	if succeeded != 1 || store.Get().WorkspaceRevision != loaded.WorkspaceRevision+1 {
		t.Fatal("concurrent workspace edits must not both overwrite the same revision")
	}
}
