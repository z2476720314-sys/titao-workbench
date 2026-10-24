// Package genjobs 是生图任务引擎：读参考图 → 生成提示词 → 图生图 →
// 一致性校验 → 落盘，支持暂停/继续/停止。
//
// 设计取向与 internal/jobs 保持一致（同样的 Phase/Task/Event/emit/logf 模式），
// 但生图任务多了一个 internal/jobs 没有的需求：**可暂停**。暂停的语义是
// 「在两个 API 调用之间停下来」，而不是中断正在飞的请求——中断一个已经
// 扣了额度的调用没有意义。
package genjobs

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
	"titao/internal/genimg"
)

// Phase 是任务阶段。
type Phase string

const (
	PhasePending    Phase = "pending"
	PhasePrompt     Phase = "prompt"
	PhaseGenerate   Phase = "generate"
	PhaseCheck      Phase = "check"
	PhaseRetry      Phase = "retry"
	PhaseDone       Phase = "done"
	PhaseFailed     Phase = "failed"
	PhaseCanceled   Phase = "canceled"
	PhaseNoKey      Phase = "nokey"
	phaseRefReadLog       = "读参考图"
)

// State 是引擎状态。
type State string

const (
	StateIdle     State = "idle"
	StateRunning  State = "running"
	StatePaused   State = "paused"
	StateStopping State = "stopping"
)

// GenFile 是一张生成图的结果。
type GenFile struct {
	// Path 是落盘后的绝对路径。
	Path string `json:"path"`
	// Mode 是生图模式。
	Mode string `json:"mode"`
	// Index 是该模式下的第几张（从 1 开始）。
	Index int `json:"index"`
	// Same 表示一致性校验是否通过。
	Same bool `json:"same"`
	// Reason 是校验模型给的理由。
	Reason string `json:"reason"`
	// Diff 是差异描述。
	Diff string `json:"diff"`
	// Retried 表示是否走过一次重试。
	Retried bool `json:"retried"`
}

// Task 是一张参考图对应的生图任务。
type Task struct {
	ID      string `json:"id"`
	RefPath string `json:"refPath"`
	RefName string `json:"refName"`
	// Phase 是当前阶段。
	Phase Phase `json:"phase"`
	// Progress 是 0~100。
	Progress int `json:"progress"`
	// Message 是给人看的一句话。
	Message string `json:"message"`
	// OutputDir 是本任务的产物目录。
	OutputDir string `json:"outputDir"`
	// Files 是成功的产物。
	Files []GenFile `json:"files"`
	// Failed 是失败次数。
	Failed int `json:"failed"`
	// Error 是致命错误。
	Error string `json:"error"`
	// ElapsedMS 是耗时。
	ElapsedMS int64 `json:"elapsedMs"`
}

// clone 深拷贝，避免前端序列化时和引擎抢同一块内存。
func (t *Task) clone() Task {
	cp := *t
	if t.Files != nil {
		cp.Files = make([]GenFile, len(t.Files))
		copy(cp.Files, t.Files)
	}
	return cp
}

// Event 是推给前端的事件，字段与 internal/jobs 的 Event 对齐，前端可复用渲染逻辑。
type Event struct {
	Type    string `json:"type"`
	Level   string `json:"level"`
	Message string `json:"message"`
	Task    *Task  `json:"task,omitempty"`
	Total   int    `json:"total"`
	Done    int    `json:"done"`
	Failed  int    `json:"failed"`
	Running bool   `json:"running"`
	State   string `json:"state"`
}

// Options 是一次生图的入参。
type Options struct {
	// Refs 是参考图路径（每张代表一款手机壳）。
	Refs []string `json:"refs"`
	// Modes 是要生成的模式。
	Modes []genimg.Mode `json:"modes"`
	// Count 是每种模式生成几张（1~10）。
	Count int `json:"count"`
	// OutputRoot 是产物根目录，空则用配置里的 OutputDir。
	OutputRoot string `json:"outputRoot"`
}

// Engine 是生图引擎。
type Engine struct {
	store  *cfg.Store
	emitFn func(Event)

	mu      sync.Mutex
	state   State
	tasks   []*Task
	cancel  context.CancelFunc
	started bool
	done    int
	failed  int

	// 暂停控制
	gmu    sync.Mutex
	gcond  *sync.Cond
	paused bool
	stop   bool

	// 测试可替换：为 nil 时按配置构造
	clientOverride *genimg.Client
	visionOverride *genimg.VisionConfig
}

// New 创建引擎。emit 为 nil 时事件被丢弃（便于测试）。
func New(store *cfg.Store, emit func(Event)) *Engine {
	e := &Engine{store: store, emitFn: emit, state: StateIdle}
	e.gcond = sync.NewCond(&e.gmu)
	return e
}

// emit 推事件。
func (e *Engine) emit(ev Event) {
	ev.Running = e.State() == StateRunning || e.State() == StatePaused || e.State() == StateStopping
	ev.State = string(e.State())
	if e.emitFn != nil {
		e.emitFn(ev)
	}
}

// logf 推一条日志。
func (e *Engine) logf(level, format string, args ...any) {
	e.emit(Event{Type: "log", Level: level, Message: fmt.Sprintf(format, args...)})
}

// State 返回当前状态。
func (e *Engine) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

// IsRunning 报告是否有任务在跑（含暂停）。
func (e *Engine) IsRunning() bool {
	s := e.State()
	return s == StateRunning || s == StatePaused || s == StateStopping
}

// Tasks 返回任务快照（逐条深拷贝）。
func (e *Engine) Tasks() []Task {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Task, len(e.tasks))
	for i, t := range e.tasks {
		out[i] = t.clone()
	}
	return out
}

// Counters 返回累计成功/失败数。
func (e *Engine) Counters() (done, failed int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.done, e.failed
}

// Pause 请求暂停。非运行态调用无副作用。
func (e *Engine) Pause() error {
	e.mu.Lock()
	state := e.state
	e.mu.Unlock()
	if state != StateRunning {
		return fmt.Errorf("当前状态 %s，无法暂停", state)
	}
	e.gmu.Lock()
	e.paused = true
	e.gmu.Unlock()
	e.mu.Lock()
	e.state = StatePaused
	e.mu.Unlock()
	e.emit(Event{Type: "log", Level: "warn", Message: "⏸ 已暂停（当前 API 调用结束后停住）"})
	return nil
}

// Resume 继续执行。
func (e *Engine) Resume() error {
	e.mu.Lock()
	state := e.state
	e.mu.Unlock()
	if state != StatePaused {
		return fmt.Errorf("当前状态 %s，无法继续", state)
	}
	e.gmu.Lock()
	e.paused = false
	e.gcond.Broadcast()
	e.gmu.Unlock()
	e.mu.Lock()
	e.state = StateRunning
	e.mu.Unlock()
	e.emit(Event{Type: "log", Level: "info", Message: "▶ 已继续"})
	return nil
}

// Stop 请求停止。
func (e *Engine) Stop() {
	e.mu.Lock()
	state := e.state
	cancel := e.cancel
	if state == StateIdle {
		e.mu.Unlock()
		return
	}
	e.state = StateStopping
	e.mu.Unlock()

	e.gmu.Lock()
	e.stop = true
	e.paused = false
	e.gcond.Broadcast()
	e.gmu.Unlock()

	if cancel != nil {
		cancel()
	}
	e.emit(Event{Type: "log", Level: "warn", Message: "⏹ 正在停止…"})
}

// gate 是每个 API 调用前必须过的闸门：暂停时阻塞，停止时返回错误。
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
	if err := ctx.Err(); err != nil {
		return errStopped
	}
	return nil
}

var errStopped = fmt.Errorf("已停止")

// reset 清空上一轮状态。
func (e *Engine) reset() {
	e.gmu.Lock()
	e.paused = false
	e.stop = false
	e.gmu.Unlock()
	e.mu.Lock()
	e.tasks = nil
	e.done = 0
	e.failed = 0
	e.started = false
	e.mu.Unlock()
}

// releaseClaim 撤销 Start 里的状态占位。
//
// Start 会先占住 StateRunning 再校验参数，校验失败时必须还回去，
// 否则界面会一直显示「运行中」且再也点不动「开始生图」。
func (e *Engine) releaseClaim() {
	e.mu.Lock()
	if e.state == StateRunning {
		e.state = StateIdle
	}
	e.mu.Unlock()
}

// validate 校验入参。
func (e *Engine) validate(opt Options) (Options, error) {
	if len(opt.Refs) == 0 {
		return opt, fmt.Errorf("请先选择参考图")
	}
	if len(opt.Modes) == 0 {
		opt.Modes = []genimg.Mode{genimg.ModeHandheld}
	}
	for _, m := range opt.Modes {
		if !m.Valid() {
			return opt, fmt.Errorf("未知生图模式 %q", m)
		}
	}
	if opt.Count <= 0 {
		opt.Count = 2
	}
	if opt.Count > 10 {
		opt.Count = 10
	}
	if opt.OutputRoot == "" && e.store != nil {
		opt.OutputRoot = e.store.Get().OutputDir
	}
	if opt.OutputRoot == "" {
		return opt, fmt.Errorf("请先设置输出目录")
	}
	return opt, nil
}

// Start 启动一轮生图。已在运行时直接报错（双检，避免连点）。
func (e *Engine) Start(opt Options) error {
	// 先持锁占位再校验：validate 要读文件、读配置，比较慢。
	// 如果先检查再解锁去校验，8 个并发 Start 会同时通过检查、各自启动一轮
	// （实测能稳定复现，任务被跑两遍、产物互相覆盖）。
	// 占位后任何后来者都会看到 StateRunning 而被拒。
	e.mu.Lock()
	if e.state == StateRunning || e.state == StatePaused || e.state == StateStopping {
		e.mu.Unlock()
		return fmt.Errorf("生图任务正在运行")
	}
	e.state = StateRunning
	e.mu.Unlock()

	// 校验失败要把占位还回去，否则引擎会永久卡在「运行中」再也起不来。
	opt, err := e.validate(opt)
	if err != nil {
		e.releaseClaim()
		return err
	}
	if e.store != nil && strings.TrimSpace(e.store.Get().GenAPIKey) == "" {
		e.releaseClaim()
		return fmt.Errorf("未配置生图 Key（设置 → 生图模型 → API Key）")
	}

	e.reset()

	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.cancel = cancel
	e.state = StateRunning
	e.tasks = make([]*Task, len(opt.Refs))
	for i, ref := range opt.Refs {
		name := strings.TrimSuffix(filepath.Base(ref), filepath.Ext(ref))
		e.tasks[i] = &Task{
			ID:      fmt.Sprintf("gen-%02d", i+1),
			RefPath: ref,
			RefName: name,
			Phase:   PhasePending,
			Message: "等待中",
		}
	}
	e.mu.Unlock()

	go e.run(ctx, opt)
	return nil
}

// run 是主循环。
func (e *Engine) run(ctx context.Context, opt Options) {
	defer func() {
		e.mu.Lock()
		state := e.state
		e.state = StateIdle
		done, failed := e.done, e.failed
		total := len(e.tasks)
		e.mu.Unlock()

		msg := fmt.Sprintf("生图结束：%d 张参考图，成功 %d，失败 %d", total, done, failed)
		level := "ok"
		if state == StateStopping {
			msg = fmt.Sprintf("生图已停止：成功 %d，失败 %d", done, failed)
			level = "warn"
		}
		e.emit(Event{Type: "done", Level: level, Message: msg, Total: total, Done: done, Failed: failed})
	}()

	cfgNow := e.genCfg()
	e.logf("info", "生图开始：%d 张参考图 × %d 种模式 × 每模式 %d 张", len(opt.Refs), len(opt.Modes), opt.Count)
	e.logf("info", "模型 %s，尺寸 %s，超时 %ds", cfgNow.Model, cfgNow.Size, int(cfgNow.Timeout.Seconds()))

	total := len(opt.Refs)
	for i, t := range e.snapshotTasks() {
		if err := e.gate(ctx); err != nil {
			e.markCanceled(t.ID)
			e.skipRest(i)
			return
		}
		e.logf("info", "[%d/%d] %s", i+1, total, t.RefName)
		e.processOne(ctx, t.ID, opt)
	}
}

// snapshotTasks 返回当前任务指针切片（内部用，不深拷贝）。
func (e *Engine) snapshotTasks() []*Task {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*Task, len(e.tasks))
	copy(out, e.tasks)
	return out
}

// skipRest 把剩下的任务标为已取消。
func (e *Engine) skipRest(from int) {
	e.mu.Lock()
	tasks := e.tasks
	e.mu.Unlock()
	for i := from + 1; i < len(tasks); i++ {
		e.updateTask(tasks[i].ID, func(t *Task) {
			if t.Phase == PhasePending {
				t.Phase = PhaseCanceled
				t.Message = "已取消"
			}
		})
	}
}

// markCanceled 把某个任务标为取消。
func (e *Engine) markCanceled(id string) {
	e.updateTask(id, func(t *Task) {
		if t.Phase == PhaseDone {
			return
		}
		t.Phase = PhaseCanceled
		t.Message = "已取消"
	})
}

// genCfg 从配置里取生图参数。
func (e *Engine) genCfg() genimg.Config {
	c := genimg.Config{
		BaseURL: "https://junli1.junlistudio.com/v1",
		Model:   "gpt-image-2.5",
		Size:    "1024x1024",
		Timeout: 300 * time.Second,
	}
	if e.store == nil {
		return c
	}
	s := e.store.Get()
	c.BaseURL = firstNonEmpty(s.GenBaseURL, c.BaseURL)
	c.APIKey = s.GenAPIKey
	c.Model = firstNonEmpty(s.GenModel, c.Model)
	c.Size = firstNonEmpty(s.GenSize, c.Size)
	c.Thinking = s.GenThinking
	if s.GenTimeout > 0 {
		c.Timeout = time.Duration(s.GenTimeout) * time.Second
	}
	return c
}

// visionCfg 从配置里取视觉模型参数。
func (e *Engine) visionCfg() genimg.VisionConfig {
	v := genimg.VisionConfig{}
	if e.store == nil {
		return v
	}
	s := e.store.Get()
	v.BaseURL = s.BaseURL
	v.APIKey = s.APIKey
	v.Model = s.Model
	v.MaxTokens = s.MaxTokens
	v.Temperature = s.Temperature
	v.Thinking = s.Thinking
	if s.TimeoutSec > 0 {
		v.Timeout = time.Duration(s.TimeoutSec) * time.Second
	}
	return v
}

// client 返回生图客户端（测试可替换）。
func (e *Engine) client() *genimg.Client {
	if e.clientOverride != nil {
		return e.clientOverride
	}
	return genimg.NewClient(e.genCfg())
}

// vision 返回视觉配置（测试可替换）。
func (e *Engine) vision() genimg.VisionConfig {
	if e.visionOverride != nil {
		return *e.visionOverride
	}
	return e.visionCfg()
}

// processOne 处理一张参考图。
func (e *Engine) processOne(ctx context.Context, id string, opt Options) {
	start := time.Now()
	t := e.taskByID(id)
	if t == nil {
		return
	}

	// 1. 读参考图并统一转成 JPEG（视觉模型与生图接口都吃 JPEG 更稳）
	e.updateTask(id, func(t *Task) {
		t.Phase = PhasePrompt
		t.Progress = 2
		t.Message = phaseRefReadLog
	})
	refJPEG, err := genimg.FileJPEG(t.RefPath, 1024, 92)
	if err != nil {
		e.failTask(id, fmt.Sprintf("读参考图失败：%v", err))
		return
	}

	outDir := filepath.Join(opt.OutputRoot, t.RefName, "生成图")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		e.failTask(id, fmt.Sprintf("创建输出目录失败：%v", err))
		return
	}
	e.updateTask(id, func(t *Task) { t.OutputDir = outDir })

	cl := e.client()
	vc := e.vision()
	units := len(opt.Modes) * opt.Count
	idx := 0
	okCount := 0

	for _, mode := range opt.Modes {
		for n := 1; n <= opt.Count; n++ {
			if err := e.gate(ctx); err != nil {
				e.markCanceled(id)
				return
			}
			idx++
			base := (idx - 1) * 100 / units

			e.updateTask(id, func(t *Task) {
				t.Phase = PhasePrompt
				t.Progress = base + 2
				t.Message = fmt.Sprintf("%s %d/%d：生成提示词", mode.Label(), n, opt.Count)
			})
			prompt, err := genimg.BuildPrompt(ctx, vc, refJPEG, mode, false)
			if err != nil {
				if isStopped(err) {
					e.markCanceled(id)
					return
				}
				e.updateTask(id, func(t *Task) { t.Failed++ })
				e.logf("err", "  ✗ %s 提示词生成失败：%v", mode.Label(), err)
				continue
			}
			e.logf("info", "  · %s 提示词：%s", mode.Label(), truncate(prompt, 120))

			file, ferr := e.genAndCheck(ctx, id, mode, n, prompt, refJPEG, cl, vc, outDir, base, idx, units)
			if ferr != nil {
				if isStopped(ferr) {
					e.markCanceled(id)
					return
				}
				e.updateTask(id, func(t *Task) { t.Failed++ })
				e.logf("err", "  ✗ %s %d/%d 失败：%v", mode.Label(), n, opt.Count, ferr)
				continue
			}
			okCount++
			e.mu.Lock()
			t := e.taskByIDLocked(id)
			if t != nil {
				t.Files = append(t.Files, *file)
			}
			e.mu.Unlock()
			// 立刻推一次任务快照，前端能看到新图
			e.pushTask(id)

			if file.Same {
				e.logf("ok", "  ✓ %s %d/%d → %s", mode.Label(), n, opt.Count, filepath.Base(file.Path))
			} else {
				e.logf("warn", "  ⚠ %s %d/%d 已重试仍不一致（%s）→ %s", mode.Label(), n, opt.Count, file.Reason, filepath.Base(file.Path))
			}
		}
	}

	e.mu.Lock()
	elapsed := time.Since(start).Milliseconds()
	e.mu.Unlock()

	e.updateTask(id, func(t *Task) {
		t.Phase = PhaseDone
		t.Progress = 100
		t.ElapsedMS = elapsed
		if okCount == 0 {
			t.Phase = PhaseFailed
			if t.Error == "" {
				t.Error = "全部生成失败"
			}
			t.Message = "失败"
		} else {
			t.Message = fmt.Sprintf("完成 %d 张（%.1fs）", okCount, float64(elapsed)/1000)
		}
	})
	e.mu.Lock()
	if okCount > 0 {
		e.done++
	} else {
		e.failed++
	}
	e.mu.Unlock()
	e.pushTask(id)
	if okCount > 0 {
		e.logf("ok", "  ✓ %d 张 → %s（%.1fs）", okCount, outDir, float64(elapsed)/1000)
	}
}

// genAndCheck 生成一张并做一致性校验；不一致则重试一次。
func (e *Engine) genAndCheck(
	ctx context.Context, id string, mode genimg.Mode, n int, prompt string,
	refJPEG []byte, cl *genimg.Client, vc genimg.VisionConfig,
	outDir string, base, idx, units int,
) (*GenFile, error) {
	dst := filepath.Join(outDir, fmt.Sprintf("%s_%s_gen%02d.jpg", e.refNameOf(id), mode, n))
	file := &GenFile{Path: dst, Mode: string(mode), Index: n}

	for attempt := 1; attempt <= 2; attempt++ {
		if attempt == 2 {
			// 重试：让视觉模型带着「上次不一致」的加强约束重写提示词
			e.updateTask(id, func(t *Task) {
				t.Phase = PhaseRetry
				t.Message = fmt.Sprintf("%s %d/%d：不一致，重试", mode.Label(), n, 1)
			})
			if err := e.gate(ctx); err != nil {
				return nil, err
			}
			p2, perr := genimg.BuildPrompt(ctx, vc, refJPEG, mode, true)
			if perr == nil && strings.TrimSpace(p2) != "" {
				prompt = p2
			}
			file.Retried = true
		}

		if err := e.gate(ctx); err != nil {
			return nil, err
		}
		e.updateTask(id, func(t *Task) {
			t.Phase = PhaseGenerate
			t.Progress = base + 100/units/3
			t.Message = fmt.Sprintf("%s %d/%d：生成中", mode.Label(), n, 1)
		})

		res, err := cl.Generate(ctx, genimg.GenerateRequest{Refs: [][]byte{refJPEG}, Prompt: prompt})
		if err != nil {
			if ctx.Err() != nil {
				return nil, errStopped
			}
			// 生成失败不重试（省额度），直接算一次失败
			return nil, err
		}

		w, h, err := genimg.SaveJPEG(res.Raw, dst, 92)
		if err != nil {
			return nil, fmt.Errorf("保存生成图: %w", err)
		}
		e.logf("info", "  · 生成图 %dx%d → %s", w, h, filepath.Base(dst))

		if err := e.gate(ctx); err != nil {
			return nil, err
		}
		e.updateTask(id, func(t *Task) {
			t.Phase = PhaseCheck
			t.Progress = base + 100/units*2/3
			t.Message = fmt.Sprintf("%s %d/%d：一致性校验", mode.Label(), n, 1)
		})

		genJPEG, err := genimg.FileJPEG(dst, 1024, 92)
		if err != nil {
			return nil, fmt.Errorf("读回生成图: %w", err)
		}
		cr := genimg.CheckConsistency(ctx, vc, refJPEG, genJPEG)
		file.Same = cr.Same
		file.Reason = cr.Reason
		file.Diff = cr.Diff
		if cr.Err != nil {
			e.logf("warn", "  · 一致性校验异常（按不一致处理）：%v", cr.Err)
		}
		if cr.Same {
			return file, nil
		}
	}
	return file, nil
}

// refNameOf 取任务名。
func (e *Engine) refNameOf(id string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t := e.taskByIDLocked(id); t != nil {
		return t.RefName
	}
	return "gen"
}

// taskByID 取任务快照。
func (e *Engine) taskByID(id string) *Task {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.taskByIDLocked(id)
	if t == nil {
		return nil
	}
	cp := t.clone()
	return &cp
}

// taskByIDLocked 取任务指针（调用方已持锁）。
func (e *Engine) taskByIDLocked(id string) *Task {
	for _, t := range e.tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// updateTask 在锁内改任务并推快照。
func (e *Engine) updateTask(id string, fn func(*Task)) {
	e.mu.Lock()
	t := e.taskByIDLocked(id)
	if t == nil {
		e.mu.Unlock()
		return
	}
	fn(t)
	cp := t.clone()
	done, failed := e.done, e.failed
	total := len(e.tasks)
	e.mu.Unlock()
	e.emit(Event{Type: "task", Task: &cp, Total: total, Done: done, Failed: failed})
}

// pushTask 推一次任务快照。
func (e *Engine) pushTask(id string) {
	e.mu.Lock()
	t := e.taskByIDLocked(id)
	if t == nil {
		e.mu.Unlock()
		return
	}
	cp := t.clone()
	done, failed := e.done, e.failed
	total := len(e.tasks)
	e.mu.Unlock()
	e.emit(Event{Type: "task", Task: &cp, Total: total, Done: done, Failed: failed})
}

// failTask 标记任务失败。
func (e *Engine) failTask(id, msg string) {
	e.updateTask(id, func(t *Task) {
		t.Phase = PhaseFailed
		t.Error = msg
		t.Message = "失败"
		t.Failed++
	})
	e.mu.Lock()
	e.failed++
	e.mu.Unlock()
	e.logf("err", "  ✗ %s", msg)
}

func isStopped(err error) bool {
	if err == nil {
		return false
	}
	if err == errStopped {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "已停止") || strings.Contains(s, "context canceled")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// GeneratedGroup 是一组生成图（按参考图名分组）。
type GeneratedGroup struct {
	Name  string   `json:"name"`
	Dir   string   `json:"dir"`
	Count int      `json:"count"`
	Files []string `json:"files"`
}

// ListGenerated 扫描输出根目录，列出所有「生成图」子目录里的 jpg。
func ListGenerated(root string) ([]GeneratedGroup, error) {
	if strings.TrimSpace(root) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var groups []GeneratedGroup
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		dir := filepath.Join(root, ent.Name(), "生成图")
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var jpgs []string
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(f.Name()))
			if ext == ".jpg" || ext == ".jpeg" || ext == ".png" {
				jpgs = append(jpgs, filepath.Join(dir, f.Name()))
			}
		}
		if len(jpgs) == 0 {
			continue
		}
		sort.Strings(jpgs)
		groups = append(groups, GeneratedGroup{Name: ent.Name(), Dir: dir, Count: len(jpgs), Files: jpgs})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	return groups, nil
}
