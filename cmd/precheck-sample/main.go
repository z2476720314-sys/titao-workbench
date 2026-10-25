// Command precheck-sample 用真实图库抽样验收预检，不碰 GUI、不驱动 PS。
//
// 用法（在仓库根目录下）：
//
//	go run ./cmd/precheck-sample -config <配置目录> -dir <图库目录> -n 40
//
// 路径一律由 flag 给出，源码里不留本机目录——这是公开仓库。
// 只读：绝不写回图库目录。判定结果追加到 <配置目录>/precheck.sample.jsonl。
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"titao/internal/cfg"
	"titao/internal/imgutil"
	"titao/internal/judge"
	"titao/internal/precheck"
)

func main() {
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) (retErr error) {
	flags := flag.NewFlagSet("precheck-sample", flag.ContinueOnError)
	cfgDir := flags.String("config", "", "配置目录（含 config.json），通常是 exe 所在目录")
	dir := flags.String("dir", "", "抽样目录")
	n := flags.Int("n", 40, "抽样张数")
	start := flags.Int("start", 0, "从第几张开始（0-based，配合 stride 复现）")
	stride := flags.Int("stride", 0, "抽样步长，0=自动（总数/张数）")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("多余参数：%v", flags.Args())
	}

	if *cfgDir == "" || *dir == "" {
		return errors.New("缺少参数：用法 go run ./cmd/precheck-sample -config DIR -dir LIBRARY_DIR [-n 40]")
	}
	if *n <= 0 {
		return errors.New("-n 必须大于 0")
	}
	if *start < 0 {
		return errors.New("-start 不能为负数")
	}
	if *stride < 0 {
		return errors.New("-stride 不能为负数，0 表示自动")
	}

	all, err := listImages(*dir)
	if err != nil {
		return fmt.Errorf("列目录失败：%w", err)
	}
	if len(all) == 0 {
		return errors.New("目录里没有图片")
	}
	if *start >= len(all) {
		return fmt.Errorf("-start 超出图片范围：共 %d 张", len(all))
	}
	st := *stride
	if st == 0 {
		st = len(all) / *n
		if st < 1 {
			st = 1
		}
	}
	var pick []string
	for i := *start; i < len(all) && len(pick) < *n; {
		pick = append(pick, all[i])
		if st >= len(all)-i {
			break
		}
		i += st
	}
	configInfo, err := os.Stat(filepath.Join(*cfgDir, "config.json"))
	if err != nil {
		return fmt.Errorf("读取 config.json 失败：%w", err)
	}
	if !configInfo.Mode().IsRegular() {
		return errors.New("config.json 必须是文件")
	}

	store, err := cfg.NewStore(*cfgDir)
	if err != nil {
		return fmt.Errorf("加载配置失败：%w", err)
	}
	settings := store.Get()
	jc := judge.Config{
		BaseURL: settings.BaseURL, APIKey: settings.APIKey, Model: settings.Model,
		MaxTokens: settings.MaxTokens, Timeout: time.Duration(settings.TimeoutSec) * time.Second,
		Temperature: settings.Temperature, ImageMaxSide: settings.ImageMaxSide, Thinking: settings.Thinking,
	}

	// 抽样专用记录库：不污染正式 precheck.jsonl，跑完可删
	pjPath := filepath.Join(*cfgDir, "precheck.sample.jsonl")
	ps, err := precheck.NewStore(pjPath)
	if err != nil {
		return fmt.Errorf("打开抽样记录库失败：%w", err)
	}
	defer func() { retErr = errors.Join(retErr, ps.Close()) }()

	fmt.Printf("模型=%s 目录=%s 总数=%d 步长=%d 抽样=%d\n\n", settings.Model, *dir, len(all), st, len(pick))
	fmt.Printf("%-4s %-6s %-9s %-9s %-26s %s\n", "#", "ok", "elapsed", "tags", "reason", "file")
	fmt.Println(strings.Repeat("-", 130))

	var okCnt, skipCnt, fbCnt, errCnt int
	var skipList []string
	for i, p := range pick {
		fi, err := os.Stat(p)
		if err != nil {
			fmt.Printf("%-4d STAT-ERR %v\n", i+1, err)
			errCnt++
			continue
		}
		dataURL, err := imgutil.ThumbDataURL(p, settings.ImageMaxSide)
		if err != nil {
			fmt.Printf("%-4d THUMB-ERR %v\n", i+1, err)
			errCnt++
			continue
		}
		t0 := time.Now()
		v := precheck.Check(jc, dataURL)
		el := time.Since(t0)

		if v.Fallback {
			fbCnt++
		} else if v.OK {
			okCnt++
		} else {
			skipCnt++
			skipList = append(skipList, filepath.Base(p))
		}
		tags := "-"
		if len(v.Tags) > 0 {
			tags = strings.Join(v.Tags, ",")
		}
		reason := v.Reason
		if reason == "" {
			reason = "-"
		}
		if v.Fallback {
			reason = "FALLBACK: " + trunc(v.Err.Error(), 40)
		}
		fmt.Printf("%-4d %-6v %-9s %-9s %-26s %s\n",
			i+1, v.OK, el.Round(time.Millisecond), tags, trunc(reason, 26), filepath.Base(p))

		if !v.Fallback {
			if err := ps.Append(precheck.Record{
				Path: p, Size: fi.Size(), MTime: fi.ModTime().Unix(),
				OK: v.OK, Reason: v.Reason, Tags: v.Tags,
			}); err != nil {
				return fmt.Errorf("保存抽样记录失败：%w", err)
			}
		}
	}

	fmt.Println(strings.Repeat("-", 130))
	fmt.Printf("\n合格 %d / 跳过 %d / 模型失败 %d / 图片失败 %d（共 %d）\n", okCnt, skipCnt, fbCnt, errCnt, len(pick))
	if len(skipList) > 0 {
		fmt.Println("\n被跳过的文件：")
		for _, s := range skipList {
			fmt.Println("  " + s)
		}
	}
	if fbCnt+errCnt > 0 {
		return fmt.Errorf("抽样未完成：模型失败 %d，图片失败 %d", fbCnt, errCnt)
	}
	return nil
}

func listImages(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	exts := map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".tif": true, ".tiff": true, ".webp": true}
	var out []string
	for _, e := range ents {
		if e.IsDir() || !exts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
