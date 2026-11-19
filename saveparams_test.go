package main

import (
	"path/filepath"
	"testing"

	"titao/internal/cfg"
)

// 套图页参数即时保存走的就是 SaveConfig。
// 它带 workspace_revision 校验：如果界面里的 conf 快照落后于磁盘，
// 保存会被拒。这里确认「只改参数、不动路径」能正常存下去。
func TestSaveConfigAcceptsParamOnlyEdit(t *testing.T) {
	dir := t.TempDir()
	store, err := cfg.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(func(c *cfg.Config) {
		c.OutputDir = filepath.Join(dir, "out")
		c.PatternPaths = []string{filepath.Join(dir, "p")}
	}); err != nil {
		t.Fatal(err)
	}
	app := &App{store: store}
	snap, err := app.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	cur := snap["config"].(cfg.Config)
	// revision 初始为 0，只有 SaveWorkspace 改路径时才递增；
	// 这里验证的是「快照与磁盘一致时参数保存能通过」。
	if cur.WorkspaceRevision != store.Get().WorkspaceRevision {
		t.Fatal("快照 revision 与磁盘不一致")
	}

	// 模拟 saveFormParams：基于快照只改参数
	next := cur
	next.MaxRounds = 7
	next.Group = "插画"
	saved, err := app.SaveConfig(next)
	if err != nil {
		t.Fatalf("参数保存被拒：%v", err)
	}
	if saved.MaxRounds != 7 {
		t.Errorf("MaxRounds 应为 7，实际 %d", saved.MaxRounds)
	}
	// 路径不能被参数保存覆盖
	if got := store.Get().PatternPaths; len(got) != 1 {
		t.Errorf("路径被覆盖：%v", got)
	}
}
