package genjobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"titao/internal/genimg"
)

// TestEndToEnd 是验收用的全链路测试：
// 读参考图 → BuildPrompt → Generate → 一致性校验 → 落盘。
//
// 全程只打 httptest 假服务器，不消耗真实额度。日志用 t.Log 打出来，
// 便于把真实产物路径与事件流水贴进回报。
func TestEndToEnd(t *testing.T) {
	f := newFakeServer(t)

	root := t.TempDir()
	refDir := filepath.Join(root, "refs")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		t.Fatal(err)
	}
	refPath := filepath.Join(refDir, "繁花手机壳.jpg")
	if err := os.WriteFile(refPath, makeJPEG(120, 120), 0o644); err != nil {
		t.Fatal(err)
	}
	outRoot := filepath.Join(root, "out")

	var emu sync.Mutex
	var logs []string
	eng, err := newEngineWithStore(t, f, outRoot, func(ev Event) {
		if ev.Type == "log" {
			emu.Lock()
			logs = append(logs, fmt.Sprintf("[%s] %s", ev.Level, ev.Message))
			emu.Unlock()
		}
	})
	if err != nil {
		t.Fatalf("构造引擎: %v", err)
	}

	if err := eng.Start(Options{
		Refs:       []string{refPath},
		Modes:      []genimg.Mode{genimg.ModeHandheld, genimg.ModeBackground},
		Count:      2,
		OutputRoot: outRoot,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDone(t, eng, 30*time.Second)

	emu.Lock()
	for _, line := range logs {
		t.Log(line)
	}
	emu.Unlock()

	tasks := eng.Tasks()
	if len(tasks) != 1 {
		t.Fatalf("任务数错: %d", len(tasks))
	}
	tk := tasks[0]
	if tk.Phase != PhaseDone {
		t.Fatalf("任务未完成: %s / %s", tk.Phase, tk.Error)
	}
	if len(tk.Files) != 4 {
		t.Fatalf("2 模式 × 2 张应产出 4 个文件，实际 %d", len(tk.Files))
	}

	t.Logf("产物目录: %s", tk.OutputDir)
	for _, gf := range tk.Files {
		st, err := os.Stat(gf.Path)
		if err != nil {
			t.Errorf("产物缺失 %s: %v", gf.Path, err)
			continue
		}
		if st.Size() == 0 {
			t.Errorf("产物为空: %s", gf.Path)
		}
		t.Logf("  ✓ %-40s same=%v mode=%s #%d  %d bytes",
			filepath.Base(gf.Path), gf.Same, gf.Mode, gf.Index, st.Size())
	}

	// 磁盘上的分组扫描应能读到这些产物
	groups, err := ListGenerated(outRoot)
	if err != nil {
		t.Fatalf("ListGenerated: %v", err)
	}
	if len(groups) != 1 || groups[0].Count != 4 {
		t.Fatalf("扫描结果错: %+v", groups)
	}
	t.Logf("ListGenerated: %s (%d 张) → %s", groups[0].Name, groups[0].Count, groups[0].Dir)

	// 命名规范：<图案名>_<模式>_genNN.jpg
	want := map[string]bool{
		"繁花手机壳_handheld_gen01.jpg":   false,
		"繁花手机壳_handheld_gen02.jpg":   false,
		"繁花手机壳_background_gen01.jpg": false,
		"繁花手机壳_background_gen02.jpg": false,
	}
	for _, gf := range tk.Files {
		b := filepath.Base(gf.Path)
		if _, ok := want[b]; !ok {
			t.Errorf("命名不符合规范: %s", b)
			continue
		}
		want[b] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("缺少产物: %s", name)
		}
	}

	// 假服务器调用次数：2 模式 × 2 张 = 4 次生图、4 次校验、4 次提示词
	prompt, gen, check := f.counters()
	if gen != 4 || check != 4 || prompt != 4 {
		t.Errorf("调用次数错 prompt=%d gen=%d check=%d（期望各 4）", prompt, gen, check)
	}
	if !strings.Contains(tk.Message, "完成 4 张") {
		t.Errorf("任务消息异常: %q", tk.Message)
	}
}

// newEngineWithStore 与 newTestEngine 同构，但允许指定输出根目录与自定义 emit。
func newEngineWithStore(t *testing.T, f *fakeServer, outRoot string, emit func(Event)) (*Engine, error) {
	t.Helper()
	store, err := newStoreFor(t, f, outRoot)
	if err != nil {
		return nil, err
	}
	return New(store, emit), nil
}
