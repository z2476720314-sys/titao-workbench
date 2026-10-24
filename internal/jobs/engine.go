// Package jobs 是套图任务引擎：把「图案 × 模板」组合排成队列，
// 串行交给 Photoshop 执行，并把进度、日志、结果通过事件推给前端。
//
// Photoshop 是单线程自动化服务器，不能并发驱动，所以任务一律串行。
// 队列存在的意义是：用户可以一次性丢进上百个组合，然后走开。
package jobs

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	"titao/internal/tuner"
)

// Phase 是单个任务的处理阶段。
type Phase string

const (
	PhasePending  Phase = "pending"
	PhasePrecheck Phase = "precheck" // 视觉预检（水印 / 镜头框 / 壳实拍）
	PhasePattern  Phase = "pattern"  // 规范化图案
	PhaseRender   Phase = "render"   // Photoshop 渲染候选
	PhaseJudge    Phase = "judge"    // 视觉模型选档
	PhaseTune     Phase = "tune"     // 多轮评审微调
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
	MaxRounds   int       `json:"maxRounds"`   // 评审微调最多几轮，0 则用配置里的 max_rounds
	NoCrop      bool      `json:"noCrop"`      // 禁止裁切：放大上限压到「完整放下」
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
	TuneRounds   int      `json:"tuneRounds"` // 实际迭代轮数
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
	State   string `json:"state,omitempty"` // idle / running / paused / stopping
}

// State 是引擎的四态：空闲、运行中、已暂停、正在停止。
//
// 前端靠它决定「开始/暂停/继续/停止」四个按钮哪个可点，
// 所以状态迁移必须唯一：同一个时刻只能有一个状态，由 mu 保护。
type State string

const (
	StateIdle     State = "idle"     // 没有任务
	StateRunning  State = "running"  // 正在逐组执行
	StatePaused   State = "paused"   // 已请求暂停，停在组边界
	StateStopping State = "stopping" // 已请求停止，当前组做完就收尾
)

// Engine 管理一次运行的完整生命周期。
type Engine struct {
	ps     *psx.Session
	store  *cfg.Store
	emitFn func(Event)

	mu      sync.Mutex
	tasks   []*Task
	running bool
	state   State
	cancel  context.CancelFunc
	started time.Time
	done    int
	failed  int

	// 闸门：让 run 循环在组边界停住。
	// 单独一把锁 + cond，不能用 mu：暂停等待期间 mu 必须能自由加解锁，
	// 否则 Pause/Resume/Status 全部卡死在 Wait 上。
	gmu    sync.Mutex
	gcond  *sync.Cond
	paused bool
	stop   bool

	// 预检记录库：懒加载，第一次用到时才读盘
	pre      *precheck.Store
	preOnce  sync.Once
	preErr   error
	skipped  int
	skipTags map[string]int // tag -> 次数，用于「水印 3 / 手机壳 5」这类汇总

	// processFn 指向单组执行体。生产代码永远是 processOne；
	// 测试替换成不依赖 Photoshop 的桩，好把「暂停/继续/停止」这些
	// 调度行为单独测干净。
	processFn func(*Engine, context.Context, Options, cfg.Config, judge.Config,
		*Task, string, int, int)
}

// errStopped 是闸门放行后用来区分「用户停的」和「出错的」的哨兵错误。
var errStopped = fmt.Errorf("已停止")

// New 创建引擎。
func New(ps *psx.Session, store *cfg.Store, emit func(Event)) *Engine {
	e := &Engine{ps: ps, store: store, emitFn: emit, state: StateIdle}
	e.gcond = sync.NewCond(&e.gmu)
	e.processFn = (*Engine).processOne
	return e
}

func (e *Engine) emit(ev Event) {
	// 状态由引擎统一盖章：调用点只管业务字段，避免每处都手写 Running/State
	// 而不一致（前端就是靠这两个字段决定按钮可点性）。
	st := e.State()
	ev.State = string(st)
	if st != StateIdle {
		ev.Running = true
	}
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

// State 返回当前状态。
func (e *Engine) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state == "" {
		return StateIdle
	}
	return e.state
}

// Cancel 请求停止当前运行（保留给旧调用方，等价于 Stop）。
func (e *Engine) Cancel() { e.Stop() }

// Pause 请求暂停。
//
// 语义是「当前这组做完就停」——渲染一组要几秒到几十秒，中途硬切会留下
// 半套图产物和一堆没关的 PS 文档。所以这里只翻标志位，真正的等待发生在
// run 循环下一轮的 gate()。
func (e *Engine) Pause() error {
	e.mu.Lock()
	st := e.state
	if st != StateRunning {
		e.mu.Unlock()
		return fmt.Errorf("当前状态 %s，无法暂停", st)
	}
	e.mu.Unlock()

	// 先合闸门再改状态：反过来的话，run 循环可能在这两步之间跑完当前组、
	// 撞上还没关的闸门，多启动一组才发现已暂停。
	e.gmu.Lock()
	e.paused = true
	e.gmu.Unlock()

	e.mu.Lock()
	if e.state != StateRunning {
		// 合闸门期间被 Stop 抢先了，撤掉暂停标志，状态以 Stop 为准
		cur := e.state
		e.mu.Unlock()
		e.gmu.Lock()
		e.paused = false
		e.gmu.Unlock()
		return fmt.Errorf("当前状态 %s，无法暂停", cur)
	}
	e.state = StatePaused
	e.mu.Unlock()

	e.logf("warn", "⏸ 已暂停：当前这组做完就停在组边界，随时可以继续")
	return nil
}

// Stop 请求停止：当前这组做完就收尾，未执行的组标 canceled。
//
// 故意不复用 ctx 取消来「立刻掐断」渲染：PS COM 调用被打断后残留的文档
// 会让下一次运行输出错图。ctx 只用来让后续的组别再启动。
func (e *Engine) Stop() {
	e.mu.Lock()
	if e.state == StateIdle {
		e.mu.Unlock()
		return
	}
	e.state = StateStopping
	cancel := e.cancel
	e.mu.Unlock()

	// 先解闸门再取消：暂停中的 run 正卡在 gcond.Wait 上，
	// 只取消 ctx 是叫不醒它的。
	e.gmu.Lock()
	e.stop = true
	e.paused = false
	e.gcond.Broadcast()
	e.gmu.Unlock()

	if cancel != nil {
		cancel()
	}
	e.logf("warn", "⏹ 正在停止：当前这组做完就收尾")
}

// gate 是组边界的闸门：暂停时阻塞，停止或 ctx 结束时返回错误。
//
// 每个任务开始前调一次，所以暂停/停止的粒度是「一整组」，
// 不会切在半成品状态上。
func (e *Engine) gate(ctx context.Context) error {
	e.gmu.Lock()
	for e.paused && !e.stop {
		e.gcond.Wait()
	}
	stopped := e.stop
	e.gmu.Unlock()

	if stopped {
		return errStopped
	}
	return ctx.Err()
}

// isStopped 判断错误是否来自用户停止（而不是真正的故障）。
func isStopped(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errStopped) || errors.Is(err, context.Canceled) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "已停止") || strings.Contains(msg, "context canceled")
}

// resetGate 在每次新运行开始时把闸门归零。
func (e *Engine) resetGate() {
	e.gmu.Lock()
	e.paused = false
	e.stop = false
	e.gcond.Broadcast()
	e.gmu.Unlock()
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

// Start 启动一次全新的运行。已在进行中时返回错误。
//
// 会覆盖上一份 queue.json：用户点「开始」就是要按当前选择重新排队，
// 不该被上次没跑完的残局干扰。
func (e *Engine) Start(opt Options) error {
	// 先做完全部校验再占状态位：占位后才发现参数不对，会把引擎永久卡在
	// 「运行中」——用户既点不了开始也点不了停止。
	if e.store == nil {
		return fmt.Errorf("配置未加载，无法开始")
	}
	def := e.store.Get()
	tasks, err := Plan(opt, def)
	if err != nil {
		return err
	}
	return e.begin(opt, def, tasks, 0, 0)
}

// Resume 继续上一次运行。
//
// 两种情形共用同一个入口，前端只需要一个「继续」按钮：
//   - 状态是 paused：解闸门，接着跑；
//   - 状态是 idle 但 queue.json 里还有没结论的组（程序被关掉/强杀过）：
//     从队列恢复任务列表和参数，只补跑没做完的组。
func (e *Engine) Resume() error {
	e.mu.Lock()
	st := e.state
	e.mu.Unlock()

	switch st {
	case StatePaused:
		e.mu.Lock()
		if e.state != StatePaused {
			cur := e.state
			e.mu.Unlock()
			return fmt.Errorf("当前状态 %s，无法继续", cur)
		}
		e.state = StateRunning
		e.mu.Unlock()

		e.gmu.Lock()
		e.paused = false
		e.gcond.Broadcast()
		e.gmu.Unlock()

		e.logf("info", "▶ 已继续")
		return nil

	case StateIdle:
		return e.resumeFromQueue()

	case StateRunning:
		return fmt.Errorf("当前状态 running，无需继续")

	default:
		return fmt.Errorf("当前状态 %s，无法继续", st)
	}
}

// resumeFromQueue 从 queue.json 恢复一次运行。
func (e *Engine) resumeFromQueue() error {
	path := e.queuePath()
	q, err := LoadQueue(path)
	if err != nil {
		// 队列坏了不该挡住用户：清掉残局，让「开始」可用
		e.logf("warn", "队列文件损坏，已忽略：%v", err)
		_ = RemoveQueue(path)
		return fmt.Errorf("队列文件损坏，请点「开始套图」重新排队")
	}
	if q == nil || !q.HasPending() {
		return fmt.Errorf("没有未完成的任务")
	}
	if e.store == nil {
		return fmt.Errorf("配置未加载，无法继续")
	}

	def := e.store.Get()
	tasks := q.ToTasks()

	// 恢复计数：已完成的组要算进 done，否则「完成 0/N」会让人以为全白跑了
	done, failed := 0, 0
	for _, t := range tasks {
		switch t.Phase {
		case PhaseDone:
			done++
		case PhaseFailed:
			failed++
		}
	}
	e.logf("info", "↻ 从队列恢复：共 %d 组，其中 %d 组已完成，待跑 %d 组",
		len(tasks), done+failed, q.CountPending())
	return e.begin(q.Options, def, tasks, done, failed)
}

// tryLockIdle 抢占「空闲 → 运行中」这一次状态迁移，返回是否抢到。
func (e *Engine) tryLockIdle() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running || (e.state != StateIdle && e.state != "") {
		return false
	}
	e.running = true
	e.state = StateRunning
	return true
}

// begin 装好运行状态并起 goroutine。
//
// 状态位在这里抢占，而不是在 Start/Resume 各自抢：两个入口共用一个
// 「检查并占用」的点，才能保证同一时刻只有一次运行。
func (e *Engine) begin(opt Options, def cfg.Config, tasks []*Task, done, failed int) error {
	if len(tasks) == 0 {
		return fmt.Errorf("没有可执行的任务")
	}
	if !e.tryLockIdle() {
		return fmt.Errorf("已有任务在运行")
	}

	ctx, cancel := context.WithCancel(context.Background())

	e.resetGate()

	e.mu.Lock()
	e.tasks = tasks
	e.cancel = cancel
	e.started = time.Now()
	e.done, e.failed = done, failed
	e.skipped, e.skipTags = 0, map[string]int{}
	e.mu.Unlock()

	// 立刻落一份队列：程序在这之后被强杀也能恢复
	e.saveQueue(opt)

	go e.run(ctx, opt, def, tasks)
	return nil
}

// queuePath 是 queue.json 的位置：跟 config.json 同目录。
func (e *Engine) queuePath() string {
	if e.store == nil {
		return ""
	}
	p := e.store.Path()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "queue.json")
}

// PendingQueue 返回磁盘上还没跑完的队列；没有则返回 nil。
//
// 给前端在启动时问一次：「上次还有 N 组没做完，要继续吗？」
// 队列文件损坏时返回 nil 并顺手清掉，不让残局永久卡住界面。
func (e *Engine) PendingQueue() (*Queue, error) {
	path := e.queuePath()
	if path == "" {
		return nil, nil
	}
	q, err := LoadQueue(path)
	if err != nil {
		e.logf("warn", "队列文件损坏，已清理：%v", err)
		_ = RemoveQueue(path)
		return nil, nil
	}
	if q == nil || !q.HasPending() {
		return nil, nil
	}
	return q, nil
}

// ClearQueue 丢弃上次没跑完的队列。运行中不允许清，否则正在跑的任务
// 一落盘又把文件写回来了，用户会以为按钮没生效。
func (e *Engine) ClearQueue() error {
	if e.IsRunning() {
		return fmt.Errorf("运行中无法清理队列")
	}
	return RemoveQueue(e.queuePath())
}

// saveQueue 把当前进度落盘。写失败只记日志：恢复能力是加分项，
// 不能因为磁盘只读之类的原因让整批任务停下。
func (e *Engine) saveQueue(opt Options) {
	path := e.queuePath()
	if path == "" {
		return
	}
	if err := SaveQueue(path, NewQueue(opt, e.Tasks())); err != nil {
		e.logf("warn", "队列落盘失败（不影响本次运行）: %v", err)
	}
}

func (e *Engine) run(ctx context.Context, opt Options, def cfg.Config, tasks []*Task) {
	stopped := false

	defer func() {
		e.mu.Lock()
		e.running = false
		e.state = StateIdle
		e.cancel = nil
		done, failed := e.done, e.failed
		skipped, skipTags := e.skipped, e.skipTags
		e.mu.Unlock()

		// 停止后 done+failed 会小于总数，此时不能报「完成」，否则用户以为跑全了
		pending := 0
		for _, t := range e.Tasks() {
			if !settledPhase(t.Phase) {
				pending++
			}
		}
		msg := fmt.Sprintf("完成：%d 组成功，%d 组失败", done, failed)
		if skipped > 0 {
			msg = fmt.Sprintf("完成：%d 组成功，%d 组失败，跳过 %d 组（%s）",
				done, failed, skipped, tagSummary(skipTags))
		}
		if stopped || pending > 0 {
			msg = fmt.Sprintf("已停止：%d 组成功，%d 组失败，%d 组未执行（可点继续）",
				done, failed, pending)
			if skipped > 0 {
				msg = fmt.Sprintf("已停止：%d 组成功，%d 组失败，%d 组未执行，跳过 %d 组（%s）",
					done, failed, pending, skipped, tagSummary(skipTags))
			}
		}
		// 收尾再落一次：把 done/finished 刷新到磁盘
		e.saveQueue(opt)
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
	if err := e.closeDocs(); err != nil {
		e.logf("warn", "清理 Photoshop 文档: %v", err)
	}

	for i, t := range tasks {
		// 组边界闸门：暂停时在这里阻塞，停止时在这里返回
		if err := e.gate(ctx); err != nil {
			stopped = true
			e.cancelRest(tasks[i:])
			break
		}
		// 继续跑时跳过已经有结论的组（done / skipped / failed）
		if settledPhase(t.Phase) {
			e.logf("dim", "  ⤼ 跳过已完成 [%d/%d]：%s × %s",
				i+1, len(tasks), t.PatternName, t.TemplateName)
			continue
		}

		// 组开始前落盘：这时若被强杀，这一组会重跑（比漏跑安全）
		e.markRunning(t)
		e.saveQueue(opt)

		// 预检：同一个图案只判一次（多模板时后续组直接复用记录库里的结论）
		if skip := e.precheckOne(ctx, def, jc, t, i+1, len(tasks)); skip {
			e.saveQueue(opt)
			continue
		}
		e.processFn(e, ctx, opt, def, jc, t, outRoot, i+1, len(tasks))
		// 组结束后落盘：强杀恢复时能准确知道这组已完成
		e.saveQueue(opt)
	}
}

// markRunning 把即将执行的组标成「运行中」，让前端立刻看到进度归属。
func (e *Engine) markRunning(t *Task) {
	e.updateTask(t, func(t *Task) {
		if settledPhase(t.Phase) {
			return
		}
		t.Phase = PhasePattern
		t.Progress = 1
		t.Message = "开始处理"
	})
}

// cancelRest 把还没跑到的组标成已取消，并保留已完成的图。
func (e *Engine) cancelRest(rest []*Task) {
	for _, t := range rest {
		if settledPhase(t.Phase) {
			continue
		}
		e.updateTask(t, func(t *Task) {
			t.Phase = PhaseCanceled
			t.Message = "已取消"
		})
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

	// 2) 多轮参数微调
	//    第一轮用候选档位让视觉模型挑，之后每轮按评审建议用单档重渲，
	//    直到模型说「够了」、调不动、或到达 max_rounds 上限。
	//    关闭自动判断或 auto_tune_on 时退化为原来的单轮行为。
	tc := tuner.Config{
		BaseURL:      def.BaseURL,
		APIKey:       def.APIKey,
		Model:        def.Model,
		MaxTokens:    def.MaxTokens,
		Timeout:      time.Duration(def.TimeoutSec) * time.Second,
		Temperature:  def.Temperature,
		ImageMaxSide: def.ImageMaxSide,
		Thinking:     def.Thinking,
	}
	// 轮数优先级：请求里显式给的 maxRounds > 配置里的 max_rounds。
	// 只有「调模型」才谈得上微调，所以 Auto 关闭时恒为 1 轮。
	wantRounds := opt.MaxRounds
	if wantRounds <= 0 {
		wantRounds = def.MaxRounds
	}
	rounds := 1
	if opt.Auto && def.AutoTuneOn && wantRounds > 1 {
		rounds = wantRounds
		if rounds > 3 {
			rounds = 3
		}
	}

	scales := opt.Scales

	// 禁止裁切：auto/contain 语义下 1.0 就是「完整放下」的极限，
	// 任何 >1 的档位都会把图案推出壳面被裁掉。宽扁图案（如 945x574）
	// 尤其明显——1:1 时宽度已顶满，放大就横向丢掉设计元素。
	if opt.NoCrop && fitIsContainLike(opt.Fit) {
		if kept := squashScales(scales); len(kept) != len(scales) {
			e.logf("dim", "  禁止裁切：候选档位等比压入 (0,1] → %v", kept)
			scales = kept
		}
	}
	offX, offY := opt.OffsetX, opt.OffsetY
	var (
		flatPick    string        // 最终选中的拼合图
		chosenScale float64       // 最终填充倍数
		chosenNote  string        // 最终选档理由
		hist        []tuner.Round // 各轮评审记录，供界面展示
	)

	for round := 1; ; round++ {
		e.updateTask(t, func(t *Task) {
			t.Phase = PhaseRender
			t.Progress = 20
			if round > 1 {
				t.Message = fmt.Sprintf("第 %d 轮渲染", round)
			} else {
				t.Message = "Photoshop 渲染候选"
			}
		})

		// 模板复制到临时目录：原模板永不被打开或修改
		tplCopy := filepath.Join(tmp, "tpl.tif")
		if err := copyFile(t.Template, tplCopy); err != nil {
			e.failTask(t, fmt.Errorf("复制模板: %w", err))
			return
		}

		req := psx.RenderRequest{
			TemplateCopy: tplCopy,
			Pattern:      patPath,
			Group:        opt.Group,
			Fit:          opt.Fit,
			Scales:       scales,
			OffsetX:      offX,
			OffsetY:      offY,
			OutDir:       tmp,
			PSVersion:    def.PSVersion,
			NoCrop:       opt.NoCrop,
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
				if res.Fallback {
					e.logf("warn", "  视觉模型判断失败，回退 x%.2f: %v", scales[flatIdx[pick]], res.Err)
				} else {
					chosenNote = res.Reason
					e.logf("info", "  模型选第 %d 档 (x%.2f) %s", flatIdx[pick]+1, scales[flatIdx[pick]], res.Reason)
				}
			} else {
				pick = idxFlat[0]
				e.logf("warn", "  可用预览图不足，跳过模型判断，用 x%.2f", scales[flatIdx[pick]])
			}
		} else {
			e.logf("info", "  固定档位 x%.2f", scales[flatIdx[0]])
		}

		flatPick = flats[pick]
		chosenScale = scales[flatIdx[pick]]

		cur := tuner.Round{Index: round, Scale: chosenScale, OffsetX: offX, OffsetY: offY}

		// 最后一轮不再评审，直接出图
		if round >= rounds {
			hist = append(hist, cur)
			break
		}

		// 4) 评审这一轮的成品，决定是否继续微调
		e.updateTask(t, func(t *Task) {
			t.Phase = PhaseTune
			t.Progress = 72
			t.Message = fmt.Sprintf("第 %d 轮评审微调", round)
		})

		jpg, err := imgutil.FirstCellJPEG(flatPick, opt.CellSize, def.ImageMaxSide, 90)
		if err != nil {
			e.logf("warn", "  评审图生成失败，结束微调: %v", err)
			hist = append(hist, cur)
			break
		}
		adv := tuner.Critique(tc, jpg)
		cur.Score = adv.Score
		cur.Problem = adv.Problem
		cur.Good = adv.Good
		hist = append(hist, cur)

		if adv.Fallback {
			e.logf("warn", "  评审模型调用失败，结束微调: %v", adv.Err)
			break
		}
		nextScale, nx, ny, iterate := tuner.Plan(adv, chosenScale, offX, offY)
		if !iterate {
			e.logf("info", "  第 %d 轮评审 %d 分：%s → 不再调整", round, adv.Score, adv.Problem)
			break
		}
		e.logf("info", "  第 %d 轮评审 %d 分：%s → %s x%.2f 偏移(%+.2f,%+.2f)",
			round, adv.Score, adv.Problem, adv.Direction, nextScale, nx, ny)

		// 禁止裁切时，微调不能把图案放大到超出壳面。
		// 只压回上限、不放弃这一轮：模型可能同时给了位移建议，位移仍要生效。
		if opt.NoCrop && fitIsContainLike(opt.Fit) && nextScale > 1 {
			e.logf("info", "  禁止裁切：x%.2f 超出完整放下上限，压回 x1.00", nextScale)
			nextScale = 1
		}

		// 下一轮只渲这一档，省 PS 与模型额度
		scales = []float64{nextScale}
		offX, offY = nx, ny
	}

	// 5) 切格导出
	e.updateTask(t, func(t *Task) {
		t.Phase = PhaseExport
		t.Progress = 80
		t.Message = "切格导出"
	})

	outDir := filepath.Join(outRoot, t.PatternName)
	files, err := imgutil.SplitCells(flatPick, outDir, t.PatternName, t.TemplateName,
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
		if chosenNote != "" {
			t.JudgeNote = chosenNote
		}
		t.ChosenScale = chosenScale
		t.TuneRounds = len(hist)
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
	if e2 := e.closeDocs(); e2 != nil {
		e.logf("warn", "  清理 Photoshop 文档失败: %v", e2)
	}
}

// closeDocs 清掉 Photoshop 残留文档。
//
// 单独包一层是为了让单测能在没有 Photoshop 的情况下跑调度逻辑：
// ps 为 nil 时直接跳过，而不是空指针崩溃。
func (e *Engine) closeDocs() error {
	if e.ps == nil {
		return nil
	}
	return e.ps.CloseAllDocs()
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

// fitIsContainLike 判断该摆放方式是否以「完整放下」为基准。
// auto 与 contain 同义；留空按 auto 处理。cover / stretch / none 不受禁止裁切约束。
func fitIsContainLike(fit string) bool {
	return fit == "" || fit == psx.FitAuto || fit == psx.FitContain
}

// squashScales 在「禁止裁切」时把候选档位等比压进 (0,1]。
//
// 不能简单把 >1 的档位截断成 1：那样 [0.85 0.95 1.05 1.15 1.3] 会退化成
// [0.85 0.95 1.00] 三档且全挤在一侧，模型只能做相对比较、选出的档位随区间漂移。
// 等比缩放保留档位之间的相对间距，仍是完整的「明显偏小 → 刚好铺满」区间。
//
// 若最大档本就 <=1（说明调用方已经给好区间），原样返回。
func squashScales(scales []float64) []float64 {
	maxS := 0.0
	for _, s := range scales {
		if s > maxS {
			maxS = s
		}
	}
	if maxS <= 1 || len(scales) == 0 {
		return scales
	}
	k := 1 / maxS
	out := make([]float64, 0, len(scales))
	for _, s := range scales {
		// 保留三位小数，避免 0.8500000001 这类浮点噪声影响去重与显示
		v := math.Round(s*k*1000) / 1000
		if v > 0 {
			out = append(out, v)
		}
	}
	return out
}
