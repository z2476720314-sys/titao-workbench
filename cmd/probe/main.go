// Command probe 是端到端验证工具：不经 GUI，直接驱动 jobs 引擎跑一次真实套图。
//
// 所有路径都由命令行给出，源码里不留任何本机路径：
//
//	go run ./cmd/probe -config <配置目录> -pattern <图案> -template <模板> -out <输出目录>
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"titao/internal/cfg"
	"titao/internal/jobs"
	"titao/internal/psx"
)

func main() {
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("probe", flag.ContinueOnError)
	cfgDir := flags.String("config", "", "配置目录（含 config.json），通常是 exe 所在目录")
	pattern := flags.String("pattern", "", "要套的图案图片路径")
	template := flags.String("template", "", "套图模板 tif 路径")
	outDir := flags.String("out", "", "输出目录")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("多余参数：%v", flags.Args())
	}

	missing := []string{}
	if *cfgDir == "" {
		missing = append(missing, "-config")
	}
	if *pattern == "" {
		missing = append(missing, "-pattern")
	}
	if *template == "" {
		missing = append(missing, "-template")
	}
	if *outDir == "" {
		missing = append(missing, "-out")
	}
	if len(missing) > 0 {
		return fmt.Errorf("缺少参数：%v\n用法：go run ./cmd/probe -config DIR -pattern PNG -template TIF -out DIR", missing)
	}
	for _, input := range []struct{ name, path string }{
		{"图案", *pattern}, {"模板", *template}, {"config.json", filepath.Join(*cfgDir, "config.json")},
	} {
		st, err := os.Stat(input.path)
		if err != nil {
			return fmt.Errorf("读取%s失败：%w", input.name, err)
		}
		if !st.Mode().IsRegular() {
			return fmt.Errorf("%s必须是文件：%s", input.name, input.path)
		}
	}
	if st, err := os.Stat(*outDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("读取输出目录失败：%w", err)
	} else if err == nil && !st.IsDir() {
		return fmt.Errorf("输出目录不能是文件：%s", *outDir)
	}

	store, err := cfg.NewStore(*cfgDir)
	if err != nil {
		return err
	}
	c := store.Get()
	fmt.Printf("cfg: model=%s candidates=%v fit=%s\n", c.Model, c.Candidates, c.Fit)

	ps := psx.Open()
	defer ps.Close()

	ver, err := ps.Probe()
	fmt.Printf("PS: version=%q err=%v\n", ver, err)
	if err != nil {
		return err
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
		Patterns:   []string{*pattern},
		Templates:  []string{*template},
		OutputDir:  *outDir,
		Group:      c.Group,
		Fit:        c.Fit,
		Scales:     c.Candidates,
		Auto:       true,
		TrimAlpha:  c.TrimAlpha,
		NoCrop:     c.NoCropOn(),
		CellSize:   c.CellSize,
		JPGQuality: c.JPGQuality,
	}
	if err := eng.Start(opt); err != nil {
		return err
	}

	select {
	case <-done:
	case <-time.After(20 * time.Minute):
		eng.Stop()
		return errors.New("套图验证超时")
	}

	return checkResults(eng.Tasks())
}

func checkResults(tasks []*jobs.Task) error {
	if len(tasks) == 0 {
		return errors.New("未执行任何套图任务")
	}
	var resultErr error
	exported := 0
	for _, t := range tasks {
		fmt.Printf("\ntask#%d phase=%s scale=%.2f files=%d elapsed=%dms\n  out=%s\n  note=%s\n  err=%s\n",
			t.ID, t.Phase, t.ChosenScale, len(t.Files), t.ElapsedMS,
			t.OutputDir, t.JudgeNote, t.Error)
		if t.Phase == jobs.PhaseSkipped {
			continue
		}
		if t.Phase != jobs.PhaseDone || len(t.Files) == 0 {
			resultErr = errors.Join(resultErr, fmt.Errorf("任务 %d 未完成有效导出：%s", t.ID, t.Phase))
		}
		for _, f := range t.Files {
			st, err := os.Stat(f)
			if err != nil {
				fmt.Printf("  MISSING %s\n", f)
				resultErr = errors.Join(resultErr, err)
				continue
			}
			if !st.Mode().IsRegular() || st.Size() == 0 {
				resultErr = errors.Join(resultErr, fmt.Errorf("无效导出文件：%s", f))
				continue
			}
			fmt.Printf("  OK %7d B  %s\n", st.Size(), f)
			if t.Phase == jobs.PhaseDone {
				exported++
			}
		}
	}
	if exported == 0 {
		resultErr = errors.Join(resultErr, errors.New("未导出任何有效成品，套图验证未通过"))
	}
	return resultErr
}
