// Command precheck-sample 用真实图库抽样验收预检，不碰 GUI、不驱动 PS。
//
// 用法（在 E:\套图程序-go\app 下）：
//
//	go run ./cmd/precheck-sample -dir "Z:\美国" -n 40 -stride auto
//
// 只读：绝不写回图库目录。判定结果写进 build/bin/precheck.jsonl（与程序一致）。
package main

import (
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

const cfgDir = `E:\套图程序-go\app\build\bin`

func main() {
	dir := flag.String("dir", `Z:\美国`, "抽样目录")
	n := flag.Int("n", 40, "抽样张数")
	start := flag.Int("start", 0, "从第几张开始（0-based，配合 stride 复现）")
	stride := flag.Int("stride", 0, "抽样步长，0=自动（总数/张数）")
	flag.Parse()

	all, err := listImages(*dir)
	if err != nil {
		fmt.Println("列目录失败:", err)
		os.Exit(1)
	}
	if len(all) == 0 {
		fmt.Println("目录里没有图片")
		os.Exit(1)
	}
	st := *stride
	if st <= 0 {
		st = len(all) / *n
		if st < 1 {
			st = 1
		}
	}
	var pick []string
	for i := *start; i < len(all) && len(pick) < *n; i += st {
		pick = append(pick, all[i])
	}

	store, err := cfg.NewStore(cfgDir)
	if err != nil {
		fmt.Println("加载配置失败:", err)
		os.Exit(1)
	}
	c := store.Get()
	jc := judge.Config{
		BaseURL: c.BaseURL, APIKey: c.APIKey, Model: c.Model,
		MaxTokens: c.MaxTokens, Timeout: time.Duration(c.TimeoutSec) * time.Second,
		Temperature: c.Temperature, ImageMaxSide: c.ImageMaxSide, Thinking: c.Thinking,
	}

	// 抽样专用记录库：不污染正式 precheck.jsonl，跑完可删
	pjPath := filepath.Join(cfgDir, "precheck.sample.jsonl")
	_ = os.Remove(pjPath)
	ps, err := precheck.NewStore(pjPath)
	if err != nil {
		fmt.Println("打开抽样记录库失败:", err)
		os.Exit(1)
	}
	defer ps.Close()

	fmt.Printf("模型=%s 目录=%s 总数=%d 步长=%d 抽样=%d\n\n", c.Model, *dir, len(all), st, len(pick))
	fmt.Printf("%-4s %-6s %-9s %-9s %-26s %s\n", "#", "ok", "elapsed", "tags", "reason", "file")
	fmt.Println(strings.Repeat("-", 130))

	var okCnt, skipCnt, fbCnt int
	var skipList []string
	for i, p := range pick {
		fi, err := os.Stat(p)
		if err != nil {
			fmt.Printf("%-4d STAT-ERR %v\n", i+1, err)
			continue
		}
		dataURL, err := imgutil.ThumbDataURL(p, c.ImageMaxSide)
		if err != nil {
			fmt.Printf("%-4d THUMB-ERR %v\n", i+1, err)
			continue
		}
		t0 := time.Now()
		v := precheck.Check(jc, dataURL)
		el := time.Since(t0)

		if v.Fallback {
			fbCnt++
		}
		if v.OK {
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
			_ = ps.Append(precheck.Record{
				Path: p, Size: fi.Size(), MTime: fi.ModTime().Unix(),
				OK: v.OK, Reason: v.Reason, Tags: v.Tags,
			})
		}
	}

	fmt.Println(strings.Repeat("-", 130))
	fmt.Printf("\n合格 %d / 跳过 %d / 模型失败放行 %d（共 %d）\n", okCnt, skipCnt, fbCnt, len(pick))
	if len(skipList) > 0 {
		fmt.Println("\n被跳过的文件：")
		for _, s := range skipList {
			fmt.Println("  " + s)
		}
	}
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
