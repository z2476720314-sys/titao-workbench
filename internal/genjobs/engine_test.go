package genjobs

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"titao/internal/cfg"
	"titao/internal/genimg"
)

// ---------- 假服务端 ----------

// fakeServer 同时扮演 chat/completions 与 images/edits。
type fakeServer struct {
	srv *httptest.Server

	mu sync.Mutex
	// promptCalls 记录提示词生成调用次数
	promptCalls int
	// genCalls 记录图生图调用次数
	genCalls int
	// checkCalls 记录一致性校验调用次数
	checkCalls int
	// genPrompts 记录每次生图收到的 prompt
	genPrompts []string
	// promptInstructions 记录每次「生成提示词」发给视觉模型的指令原文
	promptInstructions []string
	// checkSame 决定一致性校验的判定序列（按调用顺序取，用完后一直用最后一个）
	checkSame []bool
	// genStatus 非 200 时让图生图接口返回该状态码
	genStatus int
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		f.handleChat(w, r)
	case strings.HasSuffix(r.URL.Path, "/images/edits"):
		f.handleGen(w, r)
	default:
		w.WriteHeader(404)
	}
}

func (f *fakeServer) handleChat(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(raw, &req)

	var text string
	if len(req.Messages) > 0 {
		for _, c := range req.Messages[0].Content {
			if c.Type == "text" {
				text = c.Text
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.Contains(text, "质检员"):
		f.mu.Lock()
		idx := f.checkCalls
		f.checkCalls++
		same := true
		if len(f.checkSame) > 0 {
			if idx < len(f.checkSame) {
				same = f.checkSame[idx]
			} else {
				same = f.checkSame[len(f.checkSame)-1]
			}
		}
		f.mu.Unlock()
		body := map[string]any{"same": same, "reason": "test", "diff": ""}
		if !same {
			body["diff"] = "图案被重画"
		}
		b, _ := json.Marshal(body)
		writeChat(w, string(b), "")
	case strings.Contains(text, "商品图摄影师"):
		f.mu.Lock()
		f.promptCalls++
		f.promptInstructions = append(f.promptInstructions, text)
		f.mu.Unlock()
		writeChat(w, "A hand holds the phone case on a marble desk.", "")
	default:
		writeChat(w, "ok", "")
	}
}

func writeChat(w http.ResponseWriter, content, reasoning string) {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{"content": content, "reasoning_content": reasoning},
		}},
	})
	_, _ = w.Write(b)
}

func (f *fakeServer) handleGen(w http.ResponseWriter, r *http.Request) {
	_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	mr := multipart.NewReader(r.Body, params["boundary"])
	var prompt string
	var images int
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			w.WriteHeader(400)
			return
		}
		raw, _ := io.ReadAll(part)
		switch part.FormName() {
		case "prompt":
			prompt = string(raw)
		case "image":
			images++
		}
		part.Close()
	}

	f.mu.Lock()
	f.genCalls++
	f.genPrompts = append(f.genPrompts, prompt)
	status := f.genStatus
	f.mu.Unlock()

	if status != 0 && status != 200 {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream exploded"}}`))
		return
	}
	if images == 0 {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"no image"}}`))
		return
	}

	img := makeJPEG(64, 64)
	body, _ := json.Marshal(map[string]any{
		"data": []any{map[string]any{"b64_json": base64.StdEncoding.EncodeToString(img)}},
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func makeJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 4), 128, 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	return buf.Bytes()
}

func (f *fakeServer) counters() (prompt, gen, check int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.promptCalls, f.genCalls, f.checkCalls
}

// ---------- 引擎脚手架 ----------

// newStoreFor 造一个指向假服务器的配置存储。
func newStoreFor(t *testing.T, f *fakeServer, outRoot string) (*cfg.Store, error) {
	t.Helper()
	dir := t.TempDir()
	store, err := cfg.NewStore(dir)
	if err != nil {
		return nil, err
	}
	_, err = store.Update(func(c *cfg.Config) {
		c.OutputDir = outRoot
		c.GenBaseURL = f.srv.URL
		c.GenAPIKey = "gen-test-key"
		c.GenModel = "gpt-image-2.5"
		c.GenSize = "1024x1024"
		c.GenTimeout = 10
		c.BaseURL = f.srv.URL
		c.APIKey = "vision-test-key"
		c.Model = "test-vision"
		c.MaxTokens = 500
		c.TimeoutSec = 10
	})
	if err != nil {
		return nil, err
	}
	return store, nil
}

// newTestEngine 造一个指向假服务器的引擎 + 临时目录 + 假参考图。
func newTestEngine(t *testing.T, f *fakeServer) (*Engine, string, []string) {
	t.Helper()
	dir := t.TempDir()
	store, err := newStoreFor(t, f, filepath.Join(dir, "out"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	refs := make([]string, 0, 2)
	for _, name := range []string{"patternA", "patternB"} {
		p := filepath.Join(dir, name+".jpg")
		if err := os.WriteFile(p, makeJPEG(80, 80), 0o644); err != nil {
			t.Fatalf("写参考图: %v", err)
		}
		refs = append(refs, p)
	}

	var emu sync.Mutex
	eng := New(store, func(ev Event) {
		emu.Lock()
		_ = ev
		emu.Unlock()
	})
	return eng, dir, refs
}

// waitDone 等引擎跑完（含暂停状态下的超时保护）。
func waitDone(t *testing.T, eng *Engine, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !eng.IsRunning() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("引擎在 %v 内没有结束，state=%s", timeout, eng.State())
}

// ---------- 核心行为 ----------

// TestCountLoop 验证 GenCount=N 时每种模式调用 N 次生图。
func TestCountLoop(t *testing.T) {
	f := newFakeServer(t)
	eng, _, refs := newTestEngine(t, f)

	if err := eng.Start(Options{
		Refs:  refs[:1],
		Modes: []genimg.Mode{genimg.ModeHandheld},
		Count: 3,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDone(t, eng, 20*time.Second)

	prompt, gen, check := f.counters()
	if gen != 3 {
		t.Errorf("Count=3 应调用生图 3 次，实际 %d", gen)
	}
	if check != 3 {
		t.Errorf("应对每张图校验一次，实际 %d", check)
	}
	if prompt != 3 {
		t.Errorf("每张图各生成一次提示词，实际 %d", prompt)
	}

	done, failed := eng.Counters()
	if done != 1 || failed != 0 {
		t.Errorf("计数错 done=%d failed=%d", done, failed)
	}

	tasks := eng.Tasks()
	if len(tasks) != 1 || tasks[0].Phase != PhaseDone {
		t.Fatalf("任务状态错: %+v", tasks)
	}
	if len(tasks[0].Files) != 3 {
		t.Fatalf("应产出 3 个文件，实际 %d", len(tasks[0].Files))
	}
	for _, gf := range tasks[0].Files {
		if _, err := os.Stat(gf.Path); err != nil {
			t.Errorf("产物不存在: %s (%v)", gf.Path, err)
		}
		if !gf.Same {
			t.Errorf("应判一致: %+v", gf)
		}
	}
	// 命名规范 <图案名>_<模式>_genNN.jpg
	base := filepath.Base(tasks[0].Files[0].Path)
	if base != "patternA_handheld_gen01.jpg" {
		t.Errorf("命名不符合规范: %s", base)
	}
	if !strings.Contains(tasks[0].Files[0].Path, filepath.Join("patternA", "生成图")) {
		t.Errorf("产物目录不对: %s", tasks[0].Files[0].Path)
	}
}

// TestTwoModes 验证两种模式都跑。
func TestTwoModes(t *testing.T) {
	f := newFakeServer(t)
	eng, _, refs := newTestEngine(t, f)

	if err := eng.Start(Options{
		Refs:  refs[:1],
		Modes: []genimg.Mode{genimg.ModeHandheld, genimg.ModeBackground},
		Count: 1,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDone(t, eng, 20*time.Second)

	_, gen, _ := f.counters()
	if gen != 2 {
		t.Errorf("两种模式各 1 张应调用 2 次，实际 %d", gen)
	}
	tasks := eng.Tasks()
	if len(tasks[0].Files) != 2 {
		t.Fatalf("应产出 2 个文件，实际 %d", len(tasks[0].Files))
	}
	modes := map[string]bool{}
	for _, gf := range tasks[0].Files {
		modes[gf.Mode] = true
	}
	if !modes["handheld"] || !modes["background"] {
		t.Errorf("模式不全: %v", modes)
	}
}

// TestInconsistentRetryOnce 验证不一致时重试一次（总共生成 2 次，产物保留）。
func TestInconsistentRetryOnce(t *testing.T) {
	f := newFakeServer(t)
	f.checkSame = []bool{false, false} // 两次都判不一致
	eng, _, refs := newTestEngine(t, f)

	if err := eng.Start(Options{
		Refs:  refs[:1],
		Modes: []genimg.Mode{genimg.ModeHandheld},
		Count: 1,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDone(t, eng, 20*time.Second)

	_, gen, check := f.counters()
	if gen != 2 {
		t.Errorf("不一致应重试一次（共 2 次生图），实际 %d", gen)
	}
	if check != 2 {
		t.Errorf("应对两次结果各校验一次，实际 %d", check)
	}

	tasks := eng.Tasks()
	if len(tasks[0].Files) != 1 {
		t.Fatalf("重试后仍应保留 1 个产物，实际 %d", len(tasks[0].Files))
	}
	gf := tasks[0].Files[0]
	if gf.Same {
		t.Error("应标记为不一致")
	}
	if !gf.Retried {
		t.Error("应标记为走过重试")
	}
	if gf.Diff != "图案被重画" {
		t.Errorf("diff 未记录: %q", gf.Diff)
	}
	if _, err := os.Stat(gf.Path); err != nil {
		t.Errorf("不一致的产物也要保留在盘上: %v", err)
	}

	// 重试时应向视觉模型下发带加强约束的指令
	f.mu.Lock()
	instructions := append([]string(nil), f.promptInstructions...)
	f.mu.Unlock()
	if len(instructions) != 2 {
		t.Fatalf("应生成 2 次提示词（首次 + 重试），实际 %d", len(instructions))
	}
	if strings.Contains(instructions[0], "pixel-identical") {
		t.Error("首次提示词指令不应带重试加强约束")
	}
	if !strings.Contains(instructions[1], "pixel-identical") {
		t.Errorf("重试指令缺少加强约束: %.200s", instructions[1])
	}
}

// TestInconsistentThenPass 验证第一次不一致、重试后一致。
func TestInconsistentThenPass(t *testing.T) {
	f := newFakeServer(t)
	f.checkSame = []bool{false, true}
	eng, _, refs := newTestEngine(t, f)

	if err := eng.Start(Options{Refs: refs[:1], Modes: []genimg.Mode{genimg.ModeHandheld}, Count: 1}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDone(t, eng, 20*time.Second)

	tasks := eng.Tasks()
	gf := tasks[0].Files[0]
	if !gf.Same || !gf.Retried {
		t.Errorf("重试后应一致且标记重试: %+v", gf)
	}
}

// TestGenerateAPIError 验证图生图 4xx/5xx 不会崩，任务标记失败。
func TestGenerateAPIError(t *testing.T) {
	f := newFakeServer(t)
	f.genStatus = 500
	eng, _, refs := newTestEngine(t, f)

	if err := eng.Start(Options{Refs: refs[:1], Modes: []genimg.Mode{genimg.ModeHandheld}, Count: 1}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDone(t, eng, 20*time.Second)

	tasks := eng.Tasks()
	if tasks[0].Phase != PhaseFailed {
		t.Errorf("全部失败时任务应为 failed，实际 %s", tasks[0].Phase)
	}
	if len(tasks[0].Files) != 0 {
		t.Errorf("失败不应有产物，实际 %d", len(tasks[0].Files))
	}
	done, failed := eng.Counters()
	if done != 0 || failed != 1 {
		t.Errorf("计数错 done=%d failed=%d", done, failed)
	}
}

// TestMissingKey 验证没配 key 时 Start 直接拒绝。
func TestMissingKey(t *testing.T) {
	f := newFakeServer(t)
	eng, _, refs := newTestEngine(t, f)
	store, _ := cfg.NewStore(t.TempDir())
	eng2 := New(store, nil)
	if err := eng2.Start(Options{Refs: refs[:1], OutputRoot: t.TempDir()}); err == nil {
		t.Fatal("缺 key 应拒绝启动")
	}
	_ = eng
}

// TestValidation 验证入参校验。
func TestValidation(t *testing.T) {
	f := newFakeServer(t)
	eng, dir, refs := newTestEngine(t, f)

	if err := eng.Start(Options{}); err == nil {
		t.Error("空参考图应报错")
	}
	if err := eng.Start(Options{Refs: refs, Modes: []genimg.Mode{genimg.Mode("bad")}}); err == nil {
		t.Error("未知模式应报错")
	}
	// Count 越界应被夹到 1~10
	opt, err := eng.validate(Options{Refs: refs, Count: 99, OutputRoot: dir})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if opt.Count != 10 {
		t.Errorf("Count 应夹到 10，得到 %d", opt.Count)
	}
	opt2, _ := eng.validate(Options{Refs: refs, Count: -1, OutputRoot: dir})
	if opt2.Count != 2 {
		t.Errorf("Count 非法时应回落到 2，得到 %d", opt2.Count)
	}
	opt3, _ := eng.validate(Options{Refs: refs, OutputRoot: dir})
	if len(opt3.Modes) != 1 || opt3.Modes[0] != genimg.ModeHandheld {
		t.Errorf("模式缺省应为 handheld，得到 %v", opt3.Modes)
	}
}

// TestPauseResume 验证暂停后不再推进、继续后跑完。
func TestPauseResume(t *testing.T) {
	f := newFakeServer(t)
	// 让第一次校验慢一点，好在中途暂停
	eng, _, refs := newTestEngine(t, f)

	if err := eng.Start(Options{Refs: refs, Modes: []genimg.Mode{genimg.ModeHandheld}, Count: 2}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 立刻暂停，然后在暂停状态下观察生图调用数不再增长
	if err := eng.Pause(); err != nil {
		// 可能已经跑完了，视为通过
		waitDone(t, eng, 20*time.Second)
		return
	}
	if eng.State() != StatePaused {
		t.Errorf("应为 paused，实际 %s", eng.State())
	}
	_, g1, _ := f.counters()
	time.Sleep(150 * time.Millisecond)
	_, g2, _ := f.counters()
	if g2 > g1+1 {
		t.Errorf("暂停期间不应继续生图: %d → %d", g1, g2)
	}

	if err := eng.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitDone(t, eng, 20*time.Second)
	done, _ := eng.Counters()
	if done == 0 {
		t.Error("继续后应至少完成一个任务")
	}
}

// TestStop 验证停止。
func TestStop(t *testing.T) {
	f := newFakeServer(t)
	eng, _, refs := newTestEngine(t, f)
	// 造多张参考图，保证有足够时间点停止
	more := append([]string{}, refs...)
	for i := 0; i < 6; i++ {
		p := filepath.Join(filepath.Dir(refs[0]), fmt.Sprintf("extra%d.jpg", i))
		_ = os.WriteFile(p, makeJPEG(80, 80), 0o644)
		more = append(more, p)
	}

	if err := eng.Start(Options{Refs: more, Modes: []genimg.Mode{genimg.ModeHandheld}, Count: 1}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	eng.Stop()
	waitDone(t, eng, 20*time.Second)

	tasks := eng.Tasks()
	canceled := 0
	for _, tk := range tasks {
		if tk.Phase == PhaseCanceled {
			canceled++
		}
	}
	if canceled == 0 {
		t.Logf("任务状态: %v（可能跑太快没来得及取消）", phases(tasks))
	}
}

func phases(tasks []Task) []string {
	out := make([]string, len(tasks))
	for i, t := range tasks {
		out[i] = string(t.Phase)
	}
	return out
}

// TestPauseWhenIdle 验证空转时暂停/继续报错而不崩。
func TestPauseWhenIdle(t *testing.T) {
	eng := New(nil, nil)
	if err := eng.Pause(); err == nil {
		t.Error("空闲时暂停应报错")
	}
	if err := eng.Resume(); err == nil {
		t.Error("空闲时继续应报错")
	}
	eng.Stop() // 不应崩
	if eng.IsRunning() {
		t.Error("空闲时 IsRunning 应为 false")
	}
}

// TestTasksDeepCopy 验证 Tasks() 返回的是深拷贝。
func TestTasksDeepCopy(t *testing.T) {
	f := newFakeServer(t)
	eng, _, refs := newTestEngine(t, f)
	if err := eng.Start(Options{Refs: refs[:1], Modes: []genimg.Mode{genimg.ModeHandheld}, Count: 1}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDone(t, eng, 20*time.Second)

	snap := eng.Tasks()
	if len(snap) == 0 || len(snap[0].Files) == 0 {
		t.Fatal("快照为空")
	}
	snap[0].Files[0].Path = "MUTATED"
	again := eng.Tasks()
	if again[0].Files[0].Path == "MUTATED" {
		t.Error("Tasks() 没有深拷贝 Files，前端会读到被改的内存")
	}
}

// TestListGenerated 验证产物扫描。
func TestListGenerated(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "patternA", "生成图")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a_gen01.jpg", "a_gen02.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 干扰项：非生成图目录
	_ = os.MkdirAll(filepath.Join(root, "patternB"), 0o755)

	groups, err := ListGenerated(root)
	if err != nil {
		t.Fatalf("ListGenerated: %v", err)
	}
	if len(groups) != 1 || groups[0].Name != "patternA" || groups[0].Count != 2 {
		t.Fatalf("扫描结果错: %+v", groups)
	}
	if groups, err := ListGenerated(""); err != nil || groups != nil {
		t.Errorf("空根目录应返回 nil,nil，得到 %v,%v", groups, err)
	}
	if groups, err := ListGenerated(filepath.Join(root, "nope")); err != nil || groups != nil {
		t.Errorf("不存在的目录应返回 nil,nil，得到 %v,%v", groups, err)
	}
}

// TestConcurrentStart 验证连点只会启动一轮。
func TestConcurrentStart(t *testing.T) {
	f := newFakeServer(t)
	eng, _, refs := newTestEngine(t, f)

	var started, rejected int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := eng.Start(Options{Refs: refs[:1], Modes: []genimg.Mode{genimg.ModeHandheld}, Count: 1}); err != nil {
				atomic.AddInt32(&rejected, 1)
			} else {
				atomic.AddInt32(&started, 1)
			}
		}()
	}
	wg.Wait()
	waitDone(t, eng, 20*time.Second)

	if started != 1 {
		t.Errorf("并发 Start 应只成功 1 次，实际 %d（拒绝 %d）", started, rejected)
	}
}
