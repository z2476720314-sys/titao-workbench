// Package jobs 是套图任务引擎：把「图案 × 模板」组合排成队列，
// 串行交给 Photoshop 执行，并把进度、日志、结果通过事件推给前端。
//
// Photoshop 是单线程自动化服务器，不能并发驱动，所以任务一律串行。
// 队列存在的意义是：用户可以一次性丢进上百个组合，然后走开。
package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"titao/internal/cfg"
	"titao/internal/fileutil"
	"titao/internal/imgutil"
	"titao/internal/judge"
	"titao/internal/precheck"
	"titao/internal/psx"
)

// Phase 是单个任务的处理阶段。
type Phase string

const (
	PhasePending  Phase = "pending"
	PhasePrecheck Phase = "precheck" // 视觉预检（水印 / 镜头框 / 壳实拍）
	PhasePattern  Phase = "pattern"  // 规范化图案
	PhaseRender   Phase = "render"   // Photoshop 渲染候选
	PhaseJudge    Phase = "judge"    // 视觉模型选档
	PhaseExport   Phase = "export"   // 切格导出
	PhaseDone     Phase = "done"
	PhaseFailed   Phase = "failed"
	PhaseSkipped  Phase = "skipped"
	PhaseCanceled Phase = "canceled"
)

// Options 是一次套图运行的参数。
type Options struct {
	Patterns    []string  `json:"patterns"`  // 图案文件或目录
	Templates   []string  `json:"templates"` // 模板文件或目录
	OutputDir   string    `json:"outputDir"`
	Group       string    `json:"group"`
	Fit         string    `json:"fit"`
	Scales      []float64 `json:"scales"` // 为空则用配置里的 candidates
	Auto        bool      `json:"auto"`   // 是否调视觉模型
	TrimAlpha   bool      `json:"trimAlpha"`
	OffsetX     float64   `json:"offsetX"`
	OffsetY     float64   `json:"offsetY"`
	CellSize    int       `json:"cellSize"`
	JPGQuality  int       `json:"jpgQuality"`
	PatternName string    `json:"patternName"` // 仅单图案时覆盖名称
}

// Task 是一个「图案 × 模板」组合。
type Task struct {
	ID           int      `json:"id"`
	Pattern      string   `json:"pattern"`
	PatternName  string   `json:"patternName"`
	Template     string   `json:"template"`
	TemplateName string   `json:"templateName"`
	Phase        Phase    `json:"phase"`
	Progress     int      `json:"progress"` // 0-100
	Message      string   `json:"message"`
	OutputDir    string   `json:"outputDir"`
	Files        []string `json:"files"`
	Error        string   `json:"error"`
	ChosenScale  float64  `json:"chosenScale"`
	JudgeNote    string   `json:"judgeNote"`
	ElapsedMS    int64    `json:"elapsedMs"`
}

// Event 是推给前端的事件。
type Event struct {
	Type    string `json:"type"` // log / task / progress / done
	Level   string `json:"level,omitempty"`
	Message string `json:"message,omitempty"`
	Task    *Task  `json:"task,omitempty"`
	Total   int    `json:"total,omitempty"`
	Done    int    `json:"done,omitempty"`
	Failed  int    `json:"failed,omitempty"`
	Skipped int    `json:"skipped,omitempty"`
	Running bool   `json:"running,omitempty"`
}

// Engine 管理一次运行的完整生命周期。
type Engine struct {
	ps     *psx.Session
	store  *cfg.Store
	emitFn func(Event)

	mu      sync.Mutex
	tasks   []*Task
	running bool
	cancel  context.CancelFunc
	started time.Time
	done    int
	failed  int

	// 预检记录库：懒加载，第一次用到时才读盘
	pre      *precheck.Store
	preOnce  sync.Once
	preErr   error
	skipped  int
	skipTags map[string]int // tag -> 次数，用于「水印 3 / 手机壳 5」这类汇总
}

// New 创建引擎。
func New(ps *psx.Session, store *cfg.Store, emit func(Event)) *Engine {
	return &Engine{ps: ps, store: store, emitFn: emit}
}

func (e *Engine) emit(ev Event) {
	if e.emitFn != nil {
		e.emitFn(ev)
	}
}

func (e *Engine) logf(level, format string, args ...any) {
	e.emit(Event{Type: "log", Level: level, Message: fmt.Sprintf(format, args...)})
}

// IsRunning 报告是否正在运行。
func (e *Engine) IsRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// Tasks 返回当前任务快照。
//
// 必须逐条深拷贝：返回值会被前端（经 Wails 的 JSON 序列化）在另一个
// goroutine 上遍历，而引擎同时还在原地改 *Task 的 Phase/Progress，
// 浅拷贝指针会让序列化读到写一半的结构（数据竞争）。
func (e *Engine) Tasks() []*Task {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*Task, len(e.tasks))
	for i, t := range e.tasks {
		snap := *t
		if t.Files != nil {
			snap.Files = append([]string(nil), t.Files...)
		}
		out[i] = &snap
	}
	return out
}

// Cancel 请求停止当前运行。
func (e *Engine) Cancel() {
	e.mu.Lock()
	cancel := e.cancel
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Plan 把选项展开成任务列表，并做可执行性校验。
func Plan(opt Options, def cfg.Config) ([]*Task, error) {
	patterns, err := expandPaths(opt.Patterns, map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".tif": true, ".tiff": true, ".webp": true,
	})
	if err != nil {
		return nil, err
	}
	templates, err := expandPaths(opt.Templates, map[string]bool{".tif": true, ".tiff": true})
	if err != nil {
		return nil, err
	}
	if len(patterns) == 0 {
		return nil, fmt.Errorf("没有找到可用的图案（支持 png/jpg/tif/webp）")
	}
	if len(templates) == 0 {
		return nil, fmt.Errorf("没有找到可用的模板（.tif / .tiff）")
	}

	var tasks []*Task
	id := 0
	for _, pat := range patterns {
		pname := strings.TrimSuffix(filepath.Base(pat), filepath.Ext(pat))
		if opt.PatternName != "" && len(patterns) == 1 {
			pname = opt.PatternName
		}
		for _, tpl := range templates {
			tname := strings.TrimSuffix(filepath.Base(tpl), filepath.Ext(tpl))
			id++
			tasks = append(tasks, &Task{
				ID:           id,
				Pattern:      pat,
				PatternName:  pname,
				Template:     tpl,
				TemplateName: tname,
				Phase:        PhasePending,
				Progress:     0,
			})
		}
	}
	return tasks, nil
}

// expandPaths 展开文件与目录，去重后排序。
func expandPaths(items []string, exts map[string]bool) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		st, err := os.Stat(it)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("路径不存在: %s", it)
			}
			return nil, err
		}
		if st.IsDir() {
			entries, err := os.ReadDir(it)
			if err != nil {
				return nil, fmt.Errorf("读取目录 %s: %w", it, err)
			}
			for _, en := range entries {
				if en.IsDir() {
					continue
				}
				if !exts[strings.ToLower(filepath.Ext(en.Name()))] {
					continue
				}
				p := filepath.Join(it, en.Name())
				if !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
			continue
		}
		if !exts[strings.ToLower(filepath.Ext(it))] {
			continue
		}
		if !seen[it] {
			seen[it] = true
			out = append(out, it)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Start 启动一次运行。已在进行中时返回错误。
func (e *Engine) Start(opt Options) error {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return fmt.Errorf("已有任务在运行")
	}
	e.mu.Unlock()

	// 配置加载失败时 store 为 nil，这里必须挡住：否则 e.store.Get() 空指针崩溃。
	if e.store == nil {
		return fmt.Errorf("配置未加载，无法开始")
	}
	def := e.store.Get()
	tasks, err := Plan(opt, def)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())

	e.mu.Lock()
	// Plan 期间可能有另一个 Start 抢先，这里再判一次
	if e.running {
		e.mu.Unlock()
		cancel()
		return fmt.Errorf("已有任务在运行")
	}
	e.tasks = tasks
	e.running = true
	e.cancel = cancel
	e.started = time.Now()
	e.done, e.failed = 0, 0
	e.skipped, e.skipTags = 0, map[string]int{}
	e.mu.Unlock()

	go e.run(ctx, opt, def, tasks)
	return nil
}

func (e *Engine) run(ctx context.Context, opt Options, def cfg.Config, tasks []*Task) {
	defer func() {
		e.mu.Lock()
		e.running = false
		e.cancel = nil
		done, failed := e.done, e.failed
		skipped, skipTags := e.skipped, e.skipTags
		e.mu.Unlock()

		// 取消后 done+failed 会小于总数，此时不能报「完成」，否则用户以为跑全了
		canceled := ctx.Err() != nil
		msg := fmt.Sprintf("完成：%d 组成功，%d 组失败", done, failed)
		if skipped > 0 {
			msg = fmt.Sprintf("完成：%d 组成功，%d 组失败，跳过 %d 组（%s）",
				done, failed, skipped, tagSummary(skipTags))
		}
		if canceled {
			msg = fmt.Sprintf("已取消：%d 组成功，%d 组失败，%d 组未执行",
				done, failed, len(tasks)-done-failed)
			if skipped > 0 {
				msg = fmt.Sprintf("已取消：%d 组成功，%d 组失败，%d 组未执行，跳过 %d 组（%s）",
					done, failed, len(tasks)-done-failed-skipped, skipped, tagSummary(skipTags))
			}
		}
		e.emit(Event{Type: "done", Total: len(tasks), Done: done, Failed: failed,
			Skipped: skipped, Running: false, Message: msg})
	}()

	// 参数兜底
	if opt.Group == "" {
		opt.Group = def.Group
	}
	if opt.Fit == "" {
		opt.Fit = def.Fit
	}
	if opt.CellSize <= 0 {
		opt.CellSize = def.CellSize
	}
	if opt.JPGQuality <= 0 {
		opt.JPGQuality = def.JPGQuality
	}
	if len(opt.Scales) == 0 {
		opt.Scales = def.Candidates
	}
	if len(opt.Scales) == 0 {
		// 配置被手工改坏（candidates 为空）时的最后兜底，避免下面 [:1] 越界 panic
		opt.Scales = cfg.DefaultCandidates()
	}
	if !opt.Auto && len(opt.Scales) > 1 {
		// 关掉自动判断时只需要一档；保留用户填的第一档而不是配置里的默认值
		opt.Scales = opt.Scales[:1]
	}
	outRoot := opt.OutputDir
	if outRoot == "" {
		outRoot = def.OutputDir
	}

	jc := judge.Config{
		BaseURL:      def.BaseURL,
		APIKey:       def.APIKey,
		Model:        def.Model,
		MaxTokens:    def.MaxTokens,
		Timeout:      time.Duration(def.TimeoutSec) * time.Second,
		Temperature:  def.Temperature,
		ImageMaxSide: def.ImageMaxSide,
		Thinking:     def.Thinking,
	}

	e.logf("info", "开始：%d 个组合 / 输出到 %s / %s",
		len(tasks), outRoot, autoLabel(opt.Auto, def.Model))

	// 清掉上次残留的 PS 文档，避免互相干扰
	if err := e.ps.CloseAllDocs(); err != nil {
		e.logf("warn", "清理 Photoshop 文档: %v", err)
	}

	for i, t := range tasks {
		if ctx.Err() != nil {
			e.updateTask(t, func(t *Task) {
				t.Phase = PhaseCanceled
				t.Message = "已取消"
			})
			continue
		}
		// 预检：同一个图案只判一次（多模板时后续组直接复用记录库里的结论）
		if skip := e.precheckOne(ctx, def, jc, t, i+1, len(tasks)); skip {
			continue
		}
		e.processOne(ctx, opt, def, jc, t, outRoot, i+1, len(tasks))
	}
}

func autoLabel(auto bool, model string) string {
	if auto {
		return "视觉模型自动判断（" + model + "）"
	}
	return "固定档位"
}

// precheckStore 懒加载 precheck.jsonl（与 config.json 同目录）。
// 配置加载失败（store 为 nil）时返回 nil，预检自动降级为不判。
func (e *Engine) precheckStore() *precheck.Store {
	e.preOnce.Do(func() {
		if e.store == nil {
			e.preErr = fmt.Errorf("配置未加载")
			return
		}
		p := filepath.Join(filepath.Dir(e.store.Path()), "precheck.jsonl")
		s, err := precheck.NewStore(p)
		if err != nil {
			e.preErr = err
			return
		}
		e.pre = s
	})
	return e.pre
}

// precheckOne 对 t.Pattern 做一次视觉预检。返回 true 表示该组应被跳过。
//
// 判定顺序：记录库命中 → 模型判定 → 落库。命中时不再调模型，
// 所以「图案 × 多模板」只会为同一个图案花一次判定成本。
//
// 任何异常（读文件失败、缩略图失败、模型失败）都放行：预检只是省时间，
// 不能因为它让整批任务停摆。
func (e *Engine) precheckOne(ctx context.Context, def cfg.Config,
	jc judge.Config, t *Task, seq, total int) bool {

	if !def.PrecheckOn {
		return false
	}
	st, err := os.Stat(t.Pattern)
	if err != nil {
		e.logf("warn", "  预检跳过（读取文件失败）: %v", err)
		return false
	}
	store := e.precheckStore()

	// 1) 记录库命中
	if store != nil {
		if r, ok := store.Lookup(t.Pattern, st.Size(), st.ModTime().Unix()); ok {
			if !r.OK && def.PrecheckSkip {
				e.markSkipped(t, r.Reason, r.Tags, "命中记录")
				return true
			}
			if !r.OK {
				e.logf("info", "  [%d/%d] 预检记录判不合格但未开启跳过：%s（%s）",
					seq, total, t.PatternName, r.Reason)
			}
			return false
		}
	}

	// 2) 调模型
	e.updateTask(t, func(t *Task) {
		t.Phase = PhasePrecheck
		t.Progress = 2
		t.Message = "预检图案"
	})

	dataURL, err := imgutil.ThumbDataURL(t.Pattern, def.ImageMaxSide)
	if err != nil {
		e.logf("warn", "  [%d/%d] 预检缩略图失败，放行: %v", seq, total, err)
		return false
	}

	v := precheck.Check(jc, dataURL)
	if v.Fallback {
		// 模型挂了：放行并标注，不落库（下次还要重试）
		e.logf("warn", "  [%d/%d] 预检失败，默认放行: %v", seq, total, v.Err)
		return false
	}

	// 3) 落库
	if store != nil {
		rec := precheck.Record{
			Path:   t.Pattern,
			Size:   st.Size(),
			MTime:  st.ModTime().Unix(),
			OK:     v.OK,
			Reason: v.Reason,
			Tags:   v.Tags,
		}
		if err := store.Append(rec); err != nil {
			e.logf("warn", "  预检记录落盘失败: %v", err)
		}
	}

	if v.OK {
		e.logf("dim", "  [%d/%d] 预检通过：%s", seq, total, t.PatternName)
		return false
	}
	if !def.PrecheckSkip {
		e.logf("info", "  [%d/%d] 预检判不合格（未开启跳过，照常套图）：%s %s",
			seq, total, t.PatternName, v.Reason)
		return false
	}
	e.markSkipped(t, v.Reason, v.Tags, "模型判定")
	return true
}

// markSkipped 把任务标成 PhaseSkipped 并累计汇总计数。
func (e *Engine) markSkipped(t *Task, reason string, tags []string, via string) {
	if reason == "" {
		reason = "不适合套图"
	}
	if len(tags) == 0 {
		tags = []string{precheck.TagUnknown}
	}
	label := tagSummaryOf(tags)

	e.mu.Lock()
	e.skipped++
	for _, tg := range tags {
		e.skipTags[tg]++
	}
	e.mu.Unlock()

	e.updateTask(t, func(t *Task) {
		t.Phase = PhaseSkipped
		t.Progress = 100
		t.Message = "跳过：" + reason
		t.Error = ""
		t.JudgeNote = reason
	})
	e.logf("warn", "  ⤼ 跳过 %s × %s（%s｜%s｜%s）",
		t.PatternName, t.TemplateName, label, reason, via)
}

// tagSummary 把 tag 计数拼成「水印 3 / 壳边框 5」，按次数从多到少。
func tagSummary(counts map[string]int) string {
	if len(counts) == 0 {
		return "无"
	}
	type kv struct {
		tag string
		n   int
	}
	var list []kv
	for t, n := range counts {
		list = append(list, kv{t, n})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].tag < list[j].tag
	})
	parts := make([]string, 0, len(list))
	for _, it := range list {
		parts = append(parts, fmt.Sprintf("%s %d", precheck.TagLabel(it.tag), it.n))
	}
	return strings.Join(parts, " / ")
}

// tagSummaryOf 把一次判定的 tags 拼成短文案（一个任务可能命中多个类型）。
func tagSummaryOf(tags []string) string {
	parts := make([]string, 0, len(tags))
	for _, t := range tags {
		parts = append(parts, precheck.TagLabel(t))
	}
	return strings.Join(parts, "+")
}

// processOne 处理单个「图案 × 模板」组合。
//
// seq / total 是本轮运行内的进度序号，用于日志里的 [3/32]，避免用全局 Task.ID。
func (e *Engine) processOne(ctx context.Context, opt Options, def cfg.Config,
	jc judge.Config, t *Task, outRoot string, seq, total int) {

	t0 := time.Now()
	// 每组的临时目录：图案规范化产物与各档拼合图都放这里，结束即删
	tmp, err := os.MkdirTemp("", "titao_")
	if err != nil {
		e.failTask(t, fmt.Errorf("创建临时目录: %w", err))
		return
	}
	defer os.RemoveAll(tmp)

	e.updateTask(t, func(t *Task) {
		t.Phase = PhasePattern
		t.Progress = 5
		t.Message = "规范化图案"
	})
	e.logf("info", "[%d/%d] %s × %s", seq, total, t.PatternName, t.TemplateName)

	// 1) 图案规范化：去 dpi + 可选裁透明留白
	patPath := filepath.Join(tmp, "pat.png")
	pr, err := imgutil.Normalize(t.Pattern, patPath, opt.TrimAlpha)
	if err != nil {
		e.failTask(t, fmt.Errorf("规范化图案: %w", err))
		return
	}
	if pr.Trimmed {
		e.logf("dim", "  图案 %dx%d（裁掉透明留白 %s）", pr.Width, pr.Height, pr.OrigBBox)
	} else {
		e.logf("dim", "  图案 %dx%d", pr.Width, pr.Height)
	}

	// 2) Photoshop 渲染各档候选
	e.updateTask(t, func(t *Task) {
		t.Phase = PhaseRender
		t.Progress = 20
		t.Message = "Photoshop 渲染候选"
	})

	// 模板复制到临时目录：原模板永不被打开或修改
	tplCopy := filepath.Join(tmp, "tpl.tif")
	if err := copyFile(t.Template, tplCopy); err != nil {
		e.failTask(t, fmt.Errorf("复制模板: %w", err))
		return
	}

	scales := opt.Scales
	req := psx.RenderRequest{
		TemplateCopy: tplCopy,
		Pattern:      patPath,
		Group:        opt.Group,
		Fit:          opt.Fit,
		Scales:       scales,
		OffsetX:      opt.OffsetX,
		OffsetY:      opt.OffsetY,
		OutDir:       tmp,
	}
	log, err := e.ps.Render(req)
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			e.logf("dim", "  %s", line)
		}
	}
	if err != nil {
		e.failTask(t, fmt.Errorf("Photoshop 渲染: %w", err))
		return
	}

	// 收集实际产出的拼合图。
	// flatIdx 记录 flats[i] 对应的档位下标：某档没产出时不能靠位置对齐，
	// 否则 flats[1] 会错配成 scales[1] 而它其实是 scales[2]。
	var flats []string
	var flatIdx []int
	for i := range scales {
		p := req.FlatPath(i)
		if _, err := os.Stat(p); err == nil {
			flats = append(flats, p)
			flatIdx = append(flatIdx, i)
		}
	}
	if len(flats) == 0 {
		e.failTask(t, fmt.Errorf("拼合图未生成"))
		return
	}

	// 3) 视觉模型选档
	e.updateTask(t, func(t *Task) {
		t.Phase = PhaseJudge
		t.Progress = 60
		t.Message = "视觉模型判断摆放"
	})

	// pick 是 flats 的下标（不是 scales 的下标）。
	// 某档拼合图缺失时 flats 比 scales 短，两者下标不能混用。
	pick := 0
	if opt.Auto && len(flats) > 1 {
		cands := make([]judge.Candidate, 0, len(flats))
		var idxFlat []int // cands[i] 对应 flats 的哪个下标
		for i, f := range flats {
			jpg, err := imgutil.FirstCellJPEG(f, opt.CellSize, def.ImageMaxSide, 90)
			if err != nil {
				e.logf("warn", "  预览图生成失败 %s: %v", filepath.Base(f), err)
				continue
			}
			cands = append(cands, judge.Candidate{JPEG: jpg, Scale: scales[flatIdx[i]]})
			idxFlat = append(idxFlat, i)
		}
		if len(cands) > 1 {
			res := judge.Choose(jc, cands, t.PatternName)
			if res.Pick >= 0 && res.Pick < len(idxFlat) {
				pick = idxFlat[res.Pick]
			}
			chosen := scales[flatIdx[pick]]
			if res.Fallback {
				e.logf("warn", "  视觉模型判断失败，回退 x%.2f: %v", chosen, res.Err)
			} else {
				e.logf("info", "  模型选第 %d 档 (x%.2f) %s", flatIdx[pick]+1, chosen, res.Reason)
			}
			e.updateTask(t, func(t *Task) {
				t.JudgeNote = res.Reason
			})
		} else {
			pick = idxFlat[0]
			e.logf("warn", "  可用预览图不足，跳过模型判断，用 x%.2f", scales[flatIdx[pick]])
		}
	} else {
		e.logf("info", "  固定档位 x%.2f", scales[flatIdx[0]])
	}

	// 4) 切格导出
	e.updateTask(t, func(t *Task) {
		t.Phase = PhaseExport
		t.Progress = 80
		t.Message = "切格导出"
	})

	outDir := filepath.Join(outRoot, t.PatternName)
	files, err := imgutil.SplitCells(flats[pick], outDir, t.PatternName, t.TemplateName,
		opt.CellSize, opt.JPGQuality)
	if err != nil {
		e.failTask(t, fmt.Errorf("切格导出: %w", err))
		return
	}

	elapsed := time.Since(t0)
	e.mu.Lock()
	e.done++
	e.mu.Unlock()

	e.updateTask(t, func(t *Task) {
		t.Phase = PhaseDone
		t.Progress = 100
		t.Message = fmt.Sprintf("生成 %d 张", len(files))
		t.Files = files
		t.OutputDir = outDir
		t.ChosenScale = scales[flatIdx[pick]]
		t.ElapsedMS = elapsed.Milliseconds()
	})
	e.logf("ok", "  ✓ %d 张 → %s（%.1fs）", len(files), outDir, elapsed.Seconds())
}

func (e *Engine) failTask(t *Task, err error) {
	e.mu.Lock()
	e.failed++
	e.mu.Unlock()
	e.updateTask(t, func(t *Task) {
		t.Phase = PhaseFailed
		t.Error = err.Error()
		t.Message = "失败"
	})
	e.logf("err", "  ✗ %s × %s: %v", t.PatternName, t.TemplateName, err)
	// 出错后清掉 PS 残留文档，避免影响后续任务
	if e2 := e.ps.CloseAllDocs(); e2 != nil {
		e.logf("warn", "  清理 Photoshop 文档失败: %v", e2)
	}
}

func (e *Engine) updateTask(t *Task, fn func(*Task)) {
	e.mu.Lock()
	fn(t)
	snapshot := *t
	done, failed := e.done, e.failed
	total := len(e.tasks)
	e.mu.Unlock()

	e.emit(Event{
		Type:    "task",
		Task:    &snapshot,
		Total:   total,
		Done:    done,
		Failed:  failed,
		Running: true,
	})
}

func copyFile(src, dst string) error { return fileutil.Copy(src, dst) }
