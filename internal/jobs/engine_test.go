package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"titao/internal/cfg"
	"titao/internal/judge"
	"titao/internal/psx"
)

// ---------- 测试脚手架 ----------

// newTestEngine 造一个不碰 Photoshop 的引擎。
//
// processFn 换成桩：把每次「真正开工」的组 ID 记下来，然后立刻标 done。
// 这样暂停/继续/停止的调度语义就能脱离 COM 单独验证。
func newTestEngine(t *testing.T) (*Engine, *cfg.Store, *recorder) {
	t.Helper()
	dir := t.TempDir()
	store, err := cfg.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.Update(func(c *cfg.Config) { c.OutputDir = filepath.Join(dir, "output"); c.PrecheckOn = false }); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	e := New(nil, store, func(ev Event) { rec.add(ev) })
	return e, store, rec
}

// recorder 收集事件，并发安全。
type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) add(ev Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recorder) snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// doneEvent 取最后一条 done 事件。
func (r *recorder) doneEvent() *Event {
	evs := r.snapshot()
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == "done" {
			return &evs[i]
		}
	}
	return nil
}

// stubProcess 返回一个「立刻完成」的执行体，并统计被真正启动的组。
func stubProcess(started *[]int, mu *sync.Mutex) func(*Engine, context.Context, Options,
	cfg.Config, judge.Config, *Task, string, int, int) {
	return func(e *Engine, ctx context.Context, _ Options, _ cfg.Config, _ judge.Config,
		t *Task, _ string, _, _ int) {
		mu.Lock()
		*started = append(*started, t.ID)
		mu.Unlock()
		e.updateTask(t, func(t *Task) {
			t.Phase = PhaseDone
			t.Progress = 100
			t.Message = "完成"
		})
		e.mu.Lock()
		e.done++
		e.mu.Unlock()
	}
}

// makeTasks 造 n 个待跑任务。
func makeTasks(n int) []*Task {
	out := make([]*Task, n)
	for i := range out {
		out[i] = &Task{
			ID:           i + 1,
			Pattern:      fmt.Sprintf("/p/%d.png", i+1),
			PatternName:  fmt.Sprintf("p%d", i+1),
			Template:     "/t/a.tif",
			TemplateName: "a",
			Phase:        PhasePending,
		}
	}
	return out
}

// waitFor 轮询直到条件成立或超时。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

// ---------- 状态机：合法与非法转移 ----------

func TestStateMachineLegalTransitions(t *testing.T) {
	e, _, rec := newTestEngine(t)
	var mu sync.Mutex
	var started []int
	e.processFn = stubProcess(&started, &mu)

	if got := e.State(); got != StateIdle {
		t.Fatalf("初始状态应为 idle，得到 %s", got)
	}

	if err := e.Start(Options{Patterns: []string{"/p/1.png"}, Templates: []string{"/t/a.tif"}}); err == nil {
		t.Fatal("不存在的路径不该启动成功")
	}

	// idle → running：直接注入任务表绕过 Plan 的文件校验
	if err := e.begin(Options{}, e.store.Get(), makeTasks(4), 0, 0); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if got := e.State(); got != StateRunning {
		t.Fatalf("启动后应为 running，得到 %s", got)
	}

	// running → paused
	if err := e.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if got := e.State(); got != StatePaused {
		t.Fatalf("暂停后应为 paused，得到 %s", got)
	}

	// paused → running
	if err := e.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := e.State(); got != StateRunning {
		t.Fatalf("继续后应为 running，得到 %s", got)
	}

	// running → stopping → idle
	e.Stop()
	if got := e.State(); got != StateStopping && got != StateIdle {
		t.Fatalf("停止请求后应为 stopping 或已完成的 idle，得到 %s", got)
	}
	waitFor(t, "回到 idle", func() bool { return e.State() == StateIdle })

	// 收尾事件必须报「已停止」而不是「完成」
	waitFor(t, "收到 done 事件", func() bool { return rec.doneEvent() != nil })
	ev := rec.doneEvent()
	if ev == nil {
		t.Fatal("没有收到 done 事件")
	}
	if ev.Running {
		t.Fatal("done 事件不该还带 running=true")
	}
}

func TestStateMachineIllegalTransitions(t *testing.T) {
	e, _, _ := newTestEngine(t)
	var mu sync.Mutex
	var started []int
	e.processFn = stubProcess(&started, &mu)

	// idle 下不能暂停/继续
	if err := e.Pause(); err == nil {
		t.Fatal("idle 状态不该允许暂停")
	}
	if err := e.Resume(); err == nil {
		t.Fatal("idle 且无队列时不该允许继续")
	}
	// idle 下 Stop 是无操作，不报错也不改状态
	e.Stop()
	if got := e.State(); got != StateIdle {
		t.Fatalf("idle 下 Stop 应保持 idle，得到 %s", got)
	}

	if err := e.begin(Options{}, e.store.Get(), makeTasks(3), 0, 0); err != nil {
		t.Fatalf("begin: %v", err)
	}

	// running 下不能重复继续
	if err := e.Resume(); err == nil {
		t.Fatal("running 状态不该允许继续")
	}
	// running 下不能再次开始
	if err := e.Start(Options{}); err == nil {
		t.Fatal("运行中不该允许再次开始")
	}

	if err := e.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	// paused 下不能重复暂停
	if err := e.Pause(); err == nil {
		t.Fatal("paused 状态不该允许重复暂停")
	}

	e.Stop()
	waitFor(t, "回到 idle", func() bool { return e.State() == StateIdle })
}

// ---------- 暂停后不再启动新组 ----------

func TestPauseStopsStartingNewGroups(t *testing.T) {
	e, _, _ := newTestEngine(t)

	var mu sync.Mutex
	var started []int
	// 每组开工后停 20ms，留出「跑到一半去暂停」的窗口
	e.processFn = func(e *Engine, _ context.Context, _ Options, _ cfg.Config, _ judge.Config,
		t *Task, _ string, _, _ int) {
		mu.Lock()
		started = append(started, t.ID)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		e.updateTask(t, func(t *Task) {
			t.Phase = PhaseDone
			t.Progress = 100
		})
		e.mu.Lock()
		e.done++
		e.mu.Unlock()
	}

	if err := e.begin(Options{}, e.store.Get(), makeTasks(10), 0, 0); err != nil {
		t.Fatalf("begin: %v", err)
	}

	// 等第一组开工，然后立刻暂停
	waitFor(t, "第一组开工", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(started) >= 1
	})
	if err := e.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	// 给足时间：暂停后已启动的组要跑完，但不能再启动新的
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	afterPause := len(started)
	mu.Unlock()

	if afterPause > 2 {
		t.Fatalf("暂停后仍在启动新组：已启动 %d 组", afterPause)
	}
	if got := e.State(); got != StatePaused {
		t.Fatalf("应停在 paused，得到 %s", got)
	}

	// 再等一会儿，确认真的停住了（不是刚好在两组之间）
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	still := len(started)
	mu.Unlock()
	if still != afterPause {
		t.Fatalf("暂停期间又启动了组：%d → %d", afterPause, still)
	}

	// 继续后剩余组必须全部跑完
	if err := e.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitFor(t, "跑完", func() bool { return e.State() == StateIdle })
	mu.Lock()
	total := len(started)
	mu.Unlock()
	if total != 10 {
		t.Fatalf("继续后应跑满 10 组，实际 %d", total)
	}
}

// ---------- 继续时跳过已完成组 ----------

func TestResumeSkipsFinishedGroups(t *testing.T) {
	e, _, _ := newTestEngine(t)

	var mu sync.Mutex
	var started []int
	e.processFn = stubProcess(&started, &mu)

	tasks := makeTasks(6)
	// 预先标好：1/2 完成，3 跳过，4 失败，5/6 待跑
	tasks[0].Phase = PhaseDone
	tasks[1].Phase = PhaseDone
	tasks[2].Phase = PhaseSkipped
	tasks[3].Phase = PhaseFailed

	if err := e.begin(Options{}, e.store.Get(), tasks, 2, 1); err != nil {
		t.Fatalf("begin: %v", err)
	}
	waitFor(t, "跑完", func() bool { return e.State() == StateIdle })

	mu.Lock()
	got := append([]int(nil), started...)
	mu.Unlock()

	if len(got) != 2 || got[0] != 5 || got[1] != 6 {
		t.Fatalf("只应重跑 5/6 两组，实际启动 %v", got)
	}

	// 已完成的组必须原样保留
	snap := e.Tasks()
	for _, i := range []int{0, 1, 2, 3} {
		if snap[i].Phase != tasks[i].Phase {
			t.Fatalf("第 %d 组阶段被改动：%s → %s", i+1, tasks[i].Phase, snap[i].Phase)
		}
	}
}

// ---------- queue.json 序列化往返 ----------

func TestQueueRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.json")

	opt := Options{
		Patterns:   []string{"/p/a.png", "/p/b.png"},
		Templates:  []string{"/t/x.tif"},
		OutputDir:  "/out",
		Group:      "IMD",
		Fit:        "cover",
		Scales:     []float64{0.95, 1.05},
		Auto:       true,
		TrimAlpha:  true,
		CellSize:   1200,
		JPGQuality: 92,
	}
	tasks := makeTasks(3)
	tasks[0].Phase = PhaseDone
	tasks[0].Files = []string{"/out/a.jpg"}
	tasks[0].ChosenScale = 1.05
	tasks[0].ElapsedMS = 4321
	tasks[1].Phase = PhaseFailed
	tasks[1].Error = "渲染失败"

	q := NewQueue(opt, tasks)
	if err := SaveQueue(path, q); err != nil {
		t.Fatalf("SaveQueue: %v", err)
	}

	got, err := LoadQueue(path)
	if err != nil {
		t.Fatalf("LoadQueue: %v", err)
	}
	if got == nil {
		t.Fatal("读回来的队列是 nil")
	}
	if got.Version != QueueVersion {
		t.Fatalf("版本不符：%d", got.Version)
	}
	if len(got.Tasks) != 3 {
		t.Fatalf("任务数不符：%d", len(got.Tasks))
	}
	if got.Pending != 1 {
		t.Fatalf("待跑数应为 1，实际 %d", got.Pending)
	}
	if got.Finished {
		t.Fatal("还有待跑组时不该标 finished")
	}
	if got.Options.OutputDir != "/out" || got.Options.CellSize != 1200 ||
		got.Options.JPGQuality != 92 || !got.Options.Auto || !got.Options.TrimAlpha {
		t.Fatalf("选项往返丢字段：%+v", got.Options)
	}
	if len(got.Options.Scales) != 2 || got.Options.Scales[1] != 1.05 {
		t.Fatalf("档位往返丢字段：%v", got.Options.Scales)
	}
	if got.Tasks[0].ChosenScale != 1.05 || got.Tasks[0].ElapsedMS != 4321 {
		t.Fatalf("任务字段往返丢字段：%+v", got.Tasks[0])
	}
	if len(got.Tasks[0].Files) != 1 || got.Tasks[0].Files[0] != "/out/a.jpg" {
		t.Fatalf("产物列表往返丢字段：%v", got.Tasks[0].Files)
	}
	if got.Tasks[1].Error != "渲染失败" {
		t.Fatalf("错误信息往返丢字段：%q", got.Tasks[1].Error)
	}

	// ToTasks 往返
	back := got.ToTasks()
	if len(back) != 3 || back[0].Phase != PhaseDone || back[0].Progress != 100 {
		t.Fatalf("ToTasks 还原不对：%+v", back[0])
	}
}

// ---------- 损坏的 queue.json 不崩溃 ----------

func TestLoadCorruptQueue(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name string
		body string
	}{
		{"空文件", ""},
		{"截断的 JSON", `{"version":1,"tasks":[`},
		{"纯垃圾", "\x00\x01\x02 not json at all"},
		{"类型不符", `{"version":"一","tasks":{}}`},
		{"版本过新", `{"version":999,"tasks":[]}`},
		{"版本为零", `{"version":0,"tasks":[]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, "bad.json")
			if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
				t.Fatalf("写文件: %v", err)
			}
			q, err := LoadQueue(path)
			if err == nil {
				t.Fatalf("损坏内容应返回错误，得到 %+v", q)
			}
			if q != nil {
				t.Fatalf("出错时应返回 nil，得到 %+v", q)
			}
		})
	}

	// 文件不存在是正常情况，不是错误
	q, err := LoadQueue(filepath.Join(dir, "nope.json"))
	if err != nil || q != nil {
		t.Fatalf("文件不存在应返回 (nil,nil)，得到 (%+v,%v)", q, err)
	}

	// 引擎层面：坏队列不能让 Resume 崩，也不能卡住
	e, store, _ := newTestEngine(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(store.Path()), "queue.json"),
		[]byte("garbage"), 0o644); err != nil {
		t.Fatalf("写坏队列: %v", err)
	}
	pending, err := e.PendingQueue()
	if err != nil || pending != nil {
		t.Fatalf("坏队列时 PendingQueue 应返回 (nil,nil)，得到 (%+v,%v)", pending, err)
	}
	if err := e.Resume(); err == nil {
		t.Fatal("坏队列时 Resume 应报错提示重新排队")
	}
	// 报错之后状态必须回到 idle，用户可以正常点「开始」
	if got := e.State(); got != StateIdle {
		t.Fatalf("坏队列后应停在 idle，得到 %s", got)
	}
}

// ---------- 并发 Pause/Resume 不 panic ----------

func TestConcurrentPauseResume(t *testing.T) {
	e, _, _ := newTestEngine(t)

	var mu sync.Mutex
	var started []int
	e.processFn = func(e *Engine, _ context.Context, _ Options, _ cfg.Config, _ judge.Config,
		t *Task, _ string, _, _ int) {
		mu.Lock()
		started = append(started, t.ID)
		mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		e.updateTask(t, func(t *Task) {
			t.Phase = PhaseDone
			t.Progress = 100
		})
		e.mu.Lock()
		e.done++
		e.mu.Unlock()
	}

	if err := e.begin(Options{}, e.store.Get(), makeTasks(30), 0, 0); err != nil {
		t.Fatalf("begin: %v", err)
	}

	var wg sync.WaitGroup
	var errors atomic.Int64
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				// 状态竞争下大部分调用会返回「状态不对」的错误，这是预期行为；
				// 只要不 panic、不卡死就算通过。
				if err := e.Pause(); err != nil {
					errors.Add(1)
				}
				if err := e.Resume(); err != nil {
					errors.Add(1)
				}
			}
		}()
	}

	// 同时开一堆只读观察者，验证状态读取不会和写入撞车
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = e.State()
				_ = e.IsRunning()
				_ = e.Tasks()
			}
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("并发 Pause/Resume 卡死了")
	}

	// 收尾：确保能停下来
	e.Stop()
	waitFor(t, "回到 idle", func() bool { return e.State() == StateIdle })
}

// ---------- 从队列恢复 ----------

func TestResumeFromQueueAfterCrash(t *testing.T) {
	dir := t.TempDir()
	store, err := cfg.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.Update(func(c *cfg.Config) { c.PrecheckOn = false }); err != nil {
		t.Fatal(err)
	}

	// 模拟「跑到一半被强杀」：磁盘上有一份队列，前两组 done，第三组卡在 running
	tasks := makeTasks(6)
	tasks[0].Phase = PhaseDone
	tasks[0].Files = []string{"/out/1.jpg"}
	tasks[1].Phase = PhaseDone
	tasks[1].Files = []string{"/out/2.jpg"}
	tasks[2].Phase = PhaseRender // 强杀时正卡在渲染
	q := NewQueue(Options{OutputDir: "/out"}, tasks)
	qPath := filepath.Join(dir, "queue.json")
	if err := SaveQueue(qPath, q); err != nil {
		t.Fatalf("SaveQueue: %v", err)
	}

	// 新进程：全新引擎
	e := New(nil, store, func(Event) {})
	var mu sync.Mutex
	var started []int
	e.processFn = stubProcess(&started, &mu)

	pending, err := e.PendingQueue()
	if err != nil {
		t.Fatalf("PendingQueue: %v", err)
	}
	if pending == nil {
		t.Fatal("应该读到未完成的队列")
	}
	if pending.CountPending() != 4 {
		t.Fatalf("待跑组数应为 4（含卡在 running 的那组），实际 %d", pending.CountPending())
	}

	if err := e.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitFor(t, "跑完", func() bool { return e.State() == StateIdle })

	mu.Lock()
	got := append([]int(nil), started...)
	mu.Unlock()
	// 3(running 残留)/4/5/6 都要重跑，1/2 跳过
	if len(got) != 4 || got[0] != 3 || got[3] != 6 {
		t.Fatalf("恢复后应重跑 3/4/5/6，实际 %v", got)
	}

	// 恢复后的队列文件应该没有待跑项了
	final, err := LoadQueue(qPath)
	if err != nil {
		t.Fatalf("LoadQueue: %v", err)
	}
	if final.HasPending() {
		t.Fatalf("跑完后仍有待跑项：%d", final.CountPending())
	}
	if !final.Finished {
		t.Fatal("跑完后应标 finished")
	}
	// 已完成的图必须保留在队列里
	if len(final.Tasks[0].Files) != 1 || final.Tasks[0].Files[0] != "/out/1.jpg" {
		t.Fatalf("已完成组的产物被清掉了：%v", final.Tasks[0].Files)
	}
}

// ---------- 停止后未执行组标 canceled ----------

func TestStopMarksRestCanceled(t *testing.T) {
	e, _, _ := newTestEngine(t)

	var mu sync.Mutex
	var started []int
	e.processFn = func(e *Engine, _ context.Context, _ Options, _ cfg.Config, _ judge.Config,
		t *Task, _ string, _, _ int) {
		mu.Lock()
		started = append(started, t.ID)
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		e.updateTask(t, func(t *Task) {
			t.Phase = PhaseDone
			t.Progress = 100
			t.Files = []string{fmt.Sprintf("/out/%d.jpg", t.ID)}
		})
		e.mu.Lock()
		e.done++
		e.mu.Unlock()
	}

	if err := e.begin(Options{}, e.store.Get(), makeTasks(8), 0, 0); err != nil {
		t.Fatalf("begin: %v", err)
	}
	waitFor(t, "第一组开工", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(started) >= 1
	})
	e.Stop()
	waitFor(t, "回到 idle", func() bool { return e.State() == StateIdle })

	snap := e.Tasks()
	var doneN, canceledN int
	for _, task := range snap {
		switch task.Phase {
		case PhaseDone:
			doneN++
			if len(task.Files) == 0 {
				t.Fatalf("已完成组 %d 的产物丢失", task.ID)
			}
		case PhaseCanceled:
			canceledN++
		default:
			t.Fatalf("第 %d 组停在意外阶段 %s（应已标 canceled）", task.ID, task.Phase)
		}
	}
	if doneN == 0 {
		t.Fatal("停止后至少要有一组已完成，用来验证产物保留")
	}
	if doneN+canceledN != 8 {
		t.Fatalf("组数对不上：done=%d canceled=%d", doneN, canceledN)
	}
}

// ---------- 停止后继续，只补跑没做的 ----------

func TestStopThenResumeCompletes(t *testing.T) {
	e, store, _ := newTestEngine(t)

	var mu sync.Mutex
	var started []int
	e.processFn = func(e *Engine, _ context.Context, _ Options, _ cfg.Config, _ judge.Config,
		t *Task, _ string, _, _ int) {
		mu.Lock()
		started = append(started, t.ID)
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		e.updateTask(t, func(t *Task) {
			t.Phase = PhaseDone
			t.Progress = 100
		})
		e.mu.Lock()
		e.done++
		e.mu.Unlock()
	}

	if err := e.begin(Options{}, store.Get(), makeTasks(6), 0, 0); err != nil {
		t.Fatalf("begin: %v", err)
	}
	waitFor(t, "第一组开工", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(started) >= 1
	})
	e.Stop()
	waitFor(t, "停止完成", func() bool { return e.State() == StateIdle })

	mu.Lock()
	firstRun := append([]int(nil), started...)
	mu.Unlock()

	// 继续：应只跑 canceled 的那些
	if err := e.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitFor(t, "补跑完成", func() bool { return e.State() == StateIdle })

	mu.Lock()
	secondRun := append([]int(nil), started[len(firstRun):]...)
	mu.Unlock()

	if len(secondRun) != 6-len(firstRun) {
		t.Fatalf("补跑组数不符：首轮 %d 组，补跑 %d 组", len(firstRun), len(secondRun))
	}
	for _, task := range e.Tasks() {
		if task.Phase != PhaseDone {
			t.Fatalf("第 %d 组最终应为 done，实际 %s", task.ID, task.Phase)
		}
	}
}

// ---------- 队列落盘时机 ----------

func TestQueueWrittenDuringRun(t *testing.T) {
	e, store, _ := newTestEngine(t)

	var mu sync.Mutex
	var started []int
	e.processFn = func(e *Engine, _ context.Context, _ Options, _ cfg.Config, _ judge.Config,
		t *Task, _ string, _, _ int) {
		mu.Lock()
		started = append(started, t.ID)
		mu.Unlock()
		e.updateTask(t, func(t *Task) {
			t.Phase = PhaseDone
			t.Progress = 100
		})
		e.mu.Lock()
		e.done++
		e.mu.Unlock()
	}

	qPath := filepath.Join(filepath.Dir(store.Path()), "queue.json")
	if err := e.begin(Options{OutputDir: "/out"}, store.Get(), makeTasks(3), 0, 0); err != nil {
		t.Fatalf("begin: %v", err)
	}
	// begin 之后立刻就有一份队列，这样强杀也能恢复
	if _, err := os.Stat(qPath); err != nil {
		t.Fatalf("启动后应立即有 queue.json：%v", err)
	}
	waitFor(t, "跑完", func() bool { return e.State() == StateIdle })

	final, err := LoadQueue(qPath)
	if err != nil {
		t.Fatalf("LoadQueue: %v", err)
	}
	if !final.Finished || final.Pending != 0 {
		t.Fatalf("跑完后队列应 finished 且无待跑：finished=%v pending=%d",
			final.Finished, final.Pending)
	}
	if final.Options.OutputDir != "/out" {
		t.Fatalf("选项没落盘：%+v", final.Options)
	}
}

// ---------- ClearQueue ----------

func TestClearQueue(t *testing.T) {
	e, store, _ := newTestEngine(t)
	qPath := filepath.Join(filepath.Dir(store.Path()), "queue.json")

	if err := SaveQueue(qPath, NewQueue(Options{}, makeTasks(2))); err != nil {
		t.Fatalf("SaveQueue: %v", err)
	}
	if q, _ := e.PendingQueue(); q == nil {
		t.Fatal("清理前应能读到队列")
	}
	if err := e.ClearQueue(); err != nil {
		t.Fatalf("ClearQueue: %v", err)
	}
	if q, _ := e.PendingQueue(); q != nil {
		t.Fatal("清理后不该再读到队列")
	}
	// 重复清理不算错
	if err := e.ClearQueue(); err != nil {
		t.Fatalf("重复清理不该报错：%v", err)
	}
}

// ---------- settledPhase 语义 ----------

func TestSettledPhase(t *testing.T) {
	settled := []Phase{PhaseDone, PhaseSkipped, PhaseFailed}
	unsettled := []Phase{PhasePending, PhasePrecheck, PhasePattern, PhaseRender,
		PhaseJudge, PhaseExport, PhaseCanceled}

	for _, p := range settled {
		if !settledPhase(p) {
			t.Fatalf("%s 应算已有结论", p)
		}
	}
	for _, p := range unsettled {
		if settledPhase(p) {
			t.Fatalf("%s 应算还没结论", p)
		}
	}
}

// ---------- 事件里的状态字段 ----------

func TestEventCarriesState(t *testing.T) {
	e, store, rec := newTestEngine(t)
	var mu sync.Mutex
	var started []int
	e.processFn = stubProcess(&started, &mu)

	if err := e.begin(Options{}, store.Get(), makeTasks(2), 0, 0); err != nil {
		t.Fatalf("begin: %v", err)
	}
	// 等 done 事件而不是等 State()==StateIdle：run 的 defer 先把状态置 idle、
	// 再 emit done，等状态会抢在 emit 之前返回，doneEvent() 拿到 nil 而误判失败。
	waitFor(t, "跑完并发出 done 事件", func() bool { return rec.doneEvent() != nil })

	var sawState bool
	for _, ev := range rec.snapshot() {
		if ev.Type == "task" && ev.State != "" {
			sawState = true
		}
	}
	if !sawState {
		t.Fatal("task 事件应带 state 字段，供前端决定按钮可点性")
	}

	// done 事件必须明确 running=false，前端靠它复位界面
	ev := rec.doneEvent()
	if ev == nil || ev.Running {
		t.Fatalf("done 事件应带 running=false，实际 %+v", ev)
	}
}

// ---------- queue.json 结构可读性 ----------

func TestQueueJSONShape(t *testing.T) {
	q := NewQueue(Options{OutputDir: "/out"}, makeTasks(2))
	data, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, k := range []string{"version", "created_at", "finished", "pending", "options", "tasks"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("queue.json 缺少字段 %q", k)
		}
	}
	tasks, _ := raw["tasks"].([]any)
	if len(tasks) != 2 {
		t.Fatalf("任务数不符：%d", len(tasks))
	}
	first, _ := tasks[0].(map[string]any)
	for _, k := range []string{"id", "pattern", "template", "pattern_name", "template_name", "phase"} {
		if _, ok := first[k]; !ok {
			t.Fatalf("queue task 缺少字段 %q", k)
		}
	}
}

// TestSquashScalesKeepsSpacing 守住「禁止裁切」不会把候选档位挤成一堆。
//
// 曾经的写法是把 >1 的档位直接截断成 1，[0.85 0.95 1.05 1.15 1.3] 会变成
// [0.85 0.95 1.00]——只剩三档且全在左半边，视觉模型只能做相对比较，
// 选出的档位随区间漂移（已实测）。等比缩放后仍是 5 档、间距保留。
func TestSquashScalesKeepsSpacing(t *testing.T) {
	in := []float64{0.85, 0.95, 1.05, 1.15, 1.3}
	got := squashScales(in)

	if len(got) != len(in) {
		t.Fatalf("档位数量 = %d，期望 %d（不能因为压入 (0,1] 就丢档）", len(got), len(in))
	}
	if got[len(got)-1] != 1 {
		t.Errorf("最大档 = %v，期望 1（刚好铺满）", got[len(got)-1])
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Errorf("档位未严格递增：%v", got)
			break
		}
	}
	// 相对间距应保持：0.85/1.3 = 0.6538...
	wantFirst := 0.85 / 1.3
	if d := got[0] - wantFirst; d > 0.002 || d < -0.002 {
		t.Errorf("首档 = %v，期望约 %.4f（等比缩放应保留相对间距）", got[0], wantFirst)
	}
}

// TestSquashScalesLeavesGoodRange 已经 <=1 的区间必须原样返回，不能动。
func TestSquashScalesLeavesGoodRange(t *testing.T) {
	in := []float64{0.6, 0.75, 0.9}
	got := squashScales(in)
	if len(got) != len(in) {
		t.Fatalf("档位数量被改动：%v", got)
	}
	for i := range in {
		if got[i] != in[i] {
			t.Errorf("第 %d 档 = %v，期望 %v（不该改动已合规的区间）", i, got[i], in[i])
		}
	}
}

// TestFitIsContainLike 守住「禁止裁切」只作用于 contain 语义的摆放方式。
func TestFitIsContainLike(t *testing.T) {
	yes := []string{"", psx.FitAuto, psx.FitContain}
	for _, f := range yes {
		if !fitIsContainLike(f) {
			t.Errorf("fitIsContainLike(%q) = false，期望 true", f)
		}
	}
	no := []string{psx.FitCover, psx.FitStretch, psx.FitNone}
	for _, f := range no {
		if fitIsContainLike(f) {
			t.Errorf("fitIsContainLike(%q) = true，期望 false（cover/stretch/none 本就允许裁切）", f)
		}
	}
}
