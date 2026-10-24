// Command probe 是端到端验证工具：不经 GUI，直接驱动 jobs 引擎跑一次真实套图。
// 路径硬编码在源码里，避免 shell 传参破坏西里尔文/中文路径。
package main

import (
	"fmt"
	"os"
	"time"

	"titao/internal/cfg"
	"titao/internal/jobs"
	"titao/internal/psx"
)

const (
	cfgDir   = `E:\套图程序-go\app\build\bin`
	pattern  = `E:\插画\花草\подсолнух 2@WMTAI8395.png`
	template = `E:\套图模板\紫色IMD模版1.tif`
	outDir   = `E:\Temp\out\go-probe`
)

func main() {
	store, err := cfg.NewStore(cfgDir)
	if err != nil {
		panic(err)
	}
	c := store.Get()
	fmt.Printf("cfg: model=%s key_len=%d candidates=%v fit=%s\n",
		c.Model, len(c.APIKey), c.Candidates, c.Fit)

	ps := psx.Open()
	defer ps.Close()

	ver, err := ps.Probe()
	fmt.Printf("PS: version=%q err=%v\n", ver, err)
	if err != nil {
		os.Exit(1)
	}

	done := make(chan struct{})
	eng := jobs.New(ps, store, func(ev jobs.Event) {
		switch ev.Type {
		case "log":
			fmt.Printf("  [%s] %s\n", ev.Level, ev.Message)
		case "task":
			if ev.Task != nil {
				fmt.Printf("  task#%d %s/%s phase=%s p=%d %s\n",
					ev.Task.ID, ev.Task.PatternName, ev.Task.TemplateName,
					ev.Task.Phase, ev.Task.Progress, ev.Task.Message)
			}
		case "done":
			fmt.Printf("  DONE %s (done=%d failed=%d)\n", ev.Message, ev.Done, ev.Failed)
			close(done)
		}
	})

	opt := jobs.Options{
		Patterns:   []string{pattern},
		Templates:  []string{template},
		OutputDir:  outDir,
		Group:      c.Group,
		Fit:        c.Fit,
		Scales:     c.Candidates,
		Auto:       true,
		TrimAlpha:  c.TrimAlpha,
		CellSize:   c.CellSize,
		JPGQuality: c.JPGQuality,
	}
	if err := eng.Start(opt); err != nil {
		panic(err)
	}

	select {
	case <-done:
	case <-time.After(20 * time.Minute):
		fmt.Println("TIMEOUT")
		os.Exit(1)
	}

	for _, t := range eng.Tasks() {
		fmt.Printf("\ntask#%d phase=%s scale=%.2f files=%d elapsed=%dms\n  out=%s\n  note=%s\n  err=%s\n",
			t.ID, t.Phase, t.ChosenScale, len(t.Files), t.ElapsedMS,
			t.OutputDir, t.JudgeNote, t.Error)
		for _, f := range t.Files {
			st, err := os.Stat(f)
			if err != nil {
				fmt.Printf("  MISSING %s\n", f)
				continue
			}
			fmt.Printf("  OK %7d B  %s\n", st.Size(), f)
		}
	}
}
