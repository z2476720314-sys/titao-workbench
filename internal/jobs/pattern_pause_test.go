package jobs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"titao/internal/psx"
)

type pausePhotoshop struct {
	reviewPhotoshop
	started chan psx.RenderRequest
	release chan struct{}
	blockAt int32
	count   atomic.Int32
}

func (p *pausePhotoshop) Render(req psx.RenderRequest) (string, error) {
	n := p.count.Add(1)
	p.started <- req
	if n == p.blockAt {
		<-p.release
	}
	return p.reviewPhotoshop.Render(req)
}

func awaitPauseRender(t *testing.T, ch <-chan psx.RenderRequest) psx.RenderRequest {
	t.Helper()
	select {
	case req := <-ch:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("render did not start")
		return psx.RenderRequest{}
	}
}

func assertPatternPaused(t *testing.T, e *Engine, ps *pausePhotoshop) {
	t.Helper()
	select {
	case req := <-ps.started:
		t.Fatalf("pause started another render: %s", req.TemplateCopy)
	case <-time.After(500 * time.Millisecond):
	}
	if e.State() != StatePaused {
		t.Fatalf("pattern did not remain paused: %s", e.State())
	}
	for _, task := range e.Tasks() {
		if task.Phase == PhaseDone || task.Phase == PhaseFailed || len(task.Files) != 0 {
			t.Fatalf("unfinished group was published while paused: %+v", task)
		}
	}
}

func assertPatternStopped(t *testing.T, e *Engine, output string) {
	t.Helper()
	waitFor(t, "stopped pattern", func() bool { return e.State() == StateIdle })
	for _, task := range e.Tasks() {
		if task.Phase != PhaseCanceled || task.Error != "" || len(task.Files) != 0 {
			t.Fatalf("stopped pattern was failed or published: %+v", task)
		}
	}
	if e.done != 0 || e.failed != 0 {
		t.Fatalf("stop counted as success/failure: done=%d failed=%d", e.done, e.failed)
	}
	completed, err := loadCompletions(e.completionPath())
	if err != nil || len(completed.Records) != 0 {
		t.Fatalf("stopped group wrote completion records: %+v, %v", completed, err)
	}
	entries, err := os.ReadDir(filepath.Join(output, "pattern"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "existing.jpg" {
		t.Fatalf("partial group was published: %+v, %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(output, "pattern", "existing.jpg"))
	if err != nil || string(data) != "existing production output" {
		t.Fatalf("stop changed existing output: %q, %v", data, err)
	}
	work, err := filepath.Glob(filepath.Join(filepath.Dir(output), "titao-pattern-*"))
	if err != nil || len(work) != 0 {
		t.Fatalf("stopped pattern leaked temporary files: %v, %v", work, err)
	}
}

func seedPauseOutput(t *testing.T, output string) {
	t.Helper()
	dir := filepath.Join(output, "pattern")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "existing.jpg"), []byte("existing production output"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPatternPauseAtTemplateBoundary(t *testing.T) {
	for _, action := range []string{"resume", "stop"} {
		t.Run(action, func(t *testing.T) {
			_, store, rec := newTestEngine(t)
			ps := &pausePhotoshop{started: make(chan psx.RenderRequest, 8), release: make(chan struct{}), blockAt: 1}
			e := New(ps, store, rec.add)
			tasks, output := patternGroupInputs(t)
			seedPauseOutput(t, output)
			var release sync.Once
			t.Cleanup(func() {
				e.Stop()
				release.Do(func() { close(ps.release) })
				waitFor(t, "test engine cleanup", func() bool { return e.State() == StateIdle })
			})
			opt := Options{OutputDir: output, NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90}
			if err := e.begin(opt, store.Get(), tasks, 0, 0); err != nil {
				t.Fatal(err)
			}
			first := awaitPauseRender(t, ps.started)
			if err := e.Pause(); err != nil {
				t.Fatal(err)
			}
			release.Do(func() { close(ps.release) })
			assertPatternPaused(t, e, ps)
			if _, err := os.Stat(first.Canvas.Path); err != nil {
				t.Fatalf("pause discarded the active canvas: %v", err)
			}
			if action == "stop" {
				e.Stop()
				assertPatternStopped(t, e, output)
				if ps.count.Load() != 1 {
					t.Fatalf("stop started another template: %d", ps.count.Load())
				}
				return
			}
			if err := e.Resume(); err != nil {
				t.Fatal(err)
			}
			second := awaitPauseRender(t, ps.started)
			waitFor(t, "resumed group", func() bool { return e.State() == StateIdle })
			if ps.count.Load() != 2 || first.Canvas.SHA256 != second.Canvas.SHA256 {
				t.Fatal("resume restarted the group or lost the shared canvas")
			}
			for _, task := range e.Tasks() {
				if task.Phase != PhaseDone || task.CanvasSHA256 != first.Canvas.SHA256 {
					t.Fatalf("resume did not complete the original canvas: %+v", task)
				}
			}
		})
	}
}

func TestPatternPauseAfterReviewPreventsNextRound(t *testing.T) {
	_, store, rec := newTestEngine(t)
	ps := &pausePhotoshop{started: make(chan psx.RenderRequest, 8)}
	e := New(ps, store, rec.add)
	tasks, output := patternGroupInputs(t)
	tasks = tasks[:1]
	arrived := make(chan struct{}, 8)
	release := make(chan struct{})
	var releaseOnce sync.Once
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		arrived <- struct{}{}
		if n <= 1 {
			<-release
		}
		// 第 1 轮不合格（50 分），第 2 轮合格（95 分）。
		// 分数必须递增，否则会触发止损逻辑提前结束，掩盖这里要测的暂停/恢复语义。
		reply := `{"good":true,"camera_clear":true,"score":95,"problem":"无","dx":0,"dy":0,"scale_ratio":1}`
		if n <= 1 {
			reply = `{"good":false,"camera_clear":false,"score":50,"problem":"主脸被挡","dx":0.05,"dy":0.1,"scale_ratio":0.9}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}}})
	}))
	t.Cleanup(func() {
		e.Stop()
		releaseOnce.Do(func() { close(release) })
		waitFor(t, "test engine cleanup", func() bool { return e.State() == StateIdle })
		srv.Close()
	})
	def := store.Get()
	def.BaseURL, def.Model = srv.URL, "fixture"
	opt := Options{OutputDir: output, Auto: true, NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90, MaxRounds: 3}
	if err := e.begin(opt, def, tasks, 0, 0); err != nil {
		t.Fatal(err)
	}
	first := awaitPauseRender(t, ps.started)
	// 每轮只送一张最能代表全组的正面图评审，不再逐格发请求。
	for range 1 {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("first review round did not start")
		}
	}
	if err := e.Pause(); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	assertPatternPaused(t, e, ps)
	// 每轮只发 1 次评审（单模板），暂停时不该有第二轮
	if calls.Load() != 1 {
		t.Fatalf("pause launched another review round: %d", calls.Load())
	}
	if err := e.Resume(); err != nil {
		t.Fatal(err)
	}
	second := awaitPauseRender(t, ps.started)
	waitFor(t, "resumed review", func() bool { return e.State() == StateIdle })
	got := e.Tasks()[0]
	if ps.count.Load() != 2 || calls.Load() != 2 || got.Phase != PhaseDone || got.TuneRounds != 2 || first.Canvas.SHA256 == second.Canvas.SHA256 {
		t.Fatalf("resume lost tuning progress: renders=%d calls=%d task=%+v", ps.count.Load(), calls.Load(), got)
	}
}

func TestPatternPauseStopsQueuedReviews(t *testing.T) {
	for _, action := range []string{"resume", "stop"} {
		t.Run(action, func(t *testing.T) {
			_, store, rec := newTestEngine(t)
			ps := &pausePhotoshop{reviewPhotoshop: reviewPhotoshop{cells: 3}, started: make(chan psx.RenderRequest, 8)}
			e := New(ps, store, rec.add)
			tasks, output := patternGroupInputs(t)
			seedPauseOutput(t, output)
			arrived := make(chan struct{}, 8)
			release := make(chan struct{})
			var releaseOnce sync.Once
			var calls, active, peak atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				current := active.Add(1)
				defer active.Add(-1)
				for previous := peak.Load(); current > previous; previous = peak.Load() {
					if peak.CompareAndSwap(previous, current) {
						break
					}
				}
				arrived <- struct{}{}
				if n <= 1 {
					<-release
				}
				// 每轮只评审第一个模板，所以首轮就是 n=1。
				// 首轮判不合格、恢复后第二轮才合格：
				// 这样才存在「排队的评审」和「恢复后继续跑」，能测出暂停是否漏放请求。
				reply := `{"good":true,"camera_clear":true,"score":95,"problem":"无","dx":0,"dy":0,"scale_ratio":1}`
				if n <= 1 {
					reply = `{"good":false,"camera_clear":false,"score":50,"problem":"主脸被挡","dx":0.05,"dy":0.1,"scale_ratio":0.9}`
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}}})
			}))
			t.Cleanup(func() {
				e.Stop()
				releaseOnce.Do(func() { close(release) })
				waitFor(t, "test engine cleanup", func() bool { return e.State() == StateIdle })
				srv.Close()
			})
			def := store.Get()
			def.BaseURL, def.Model = srv.URL, "fixture"
			opt := Options{OutputDir: output, Auto: true, NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90, MaxRounds: 2}
			if err := e.begin(opt, def, tasks, 0, 0); err != nil {
				t.Fatal(err)
			}
			awaitPauseRender(t, ps.started)
			awaitPauseRender(t, ps.started)
			// 只用第一个模板评审，每轮 1 次
			for range 1 {
				select {
				case <-arrived:
				case <-time.After(5 * time.Second):
					t.Fatal("initial review batch did not start")
				}
			}
			if err := e.Pause(); err != nil {
				t.Fatal(err)
			}
			releaseOnce.Do(func() { close(release) })
			assertPatternPaused(t, e, ps)
			if calls.Load() != 1 || peak.Load() > 2 {
				t.Fatalf("pause started queued reviews: calls=%d peak=%d", calls.Load(), peak.Load())
			}
			if action == "stop" {
				e.Stop()
				assertPatternStopped(t, e, output)
				if calls.Load() != 1 {
					t.Fatalf("stop started queued reviews: %d", calls.Load())
				}
				if event := rec.doneEvent(); event == nil || !strings.Contains(event.Message, "已停止") {
					t.Fatalf("stop reported success: %+v", event)
				}
				return
			}
			if err := e.Resume(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "resumed queued reviews", func() bool { return e.State() == StateIdle })
			// 2 轮，每轮只评审第一个模板 = 2 次；每个模板每轮都渲染 = 4 次
			if calls.Load() != 2 || ps.count.Load() != 4 || peak.Load() > 2 {
				t.Fatalf("resume repeated/skipped work: calls=%d renders=%d peak=%d", calls.Load(), ps.count.Load(), peak.Load())
			}
			for _, task := range e.Tasks() {
				if task.Phase != PhaseDone {
					t.Fatalf("resume failed: %+v", task)
				}
			}
		})
	}
}

func TestPatternPauseStopsReviewRepairRequests(t *testing.T) {
	for _, repair := range []string{"format", "noop", "http"} {
		for _, action := range []string{"resume", "stop"} {
			t.Run(repair+"/"+action, func(t *testing.T) {
				_, store, rec := newTestEngine(t)
				ps := &pausePhotoshop{reviewPhotoshop: reviewPhotoshop{cells: 1}, started: make(chan psx.RenderRequest, 8)}
				e := New(ps, store, rec.add)
				tasks, output := patternGroupInputs(t)
				tasks = tasks[:1]
				seedPauseOutput(t, output)
				arrived := make(chan struct{}, 4)
				release := make(chan struct{})
				var releaseOnce sync.Once
				var calls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					n := calls.Add(1)
					arrived <- struct{}{}
					reply := `{"good":true,"camera_clear":true,"score":95,"problem":"无","dx":0,"dy":0,"scale_ratio":1}`
					if n == 1 {
						<-release
						if repair == "http" {
							w.WriteHeader(http.StatusBadGateway)
							return
						}
						reply = `invalid JSON`
						if repair == "noop" {
							reply = `{"good":false,"camera_clear":false,"score":50,"problem":"主脸被挡","dx":0,"dy":0,"scale_ratio":1}`
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}}})
				}))
				t.Cleanup(func() {
					e.Stop()
					releaseOnce.Do(func() { close(release) })
					waitFor(t, "test engine cleanup", func() bool { return e.State() == StateIdle })
					srv.Close()
				})
				def := store.Get()
				def.BaseURL, def.Model, def.TimeoutSec = srv.URL, "fixture", 1
				opt := Options{OutputDir: output, Auto: true, NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90, MaxRounds: 2}
				if err := e.begin(opt, def, tasks, 0, 0); err != nil {
					t.Fatal(err)
				}
				awaitPauseRender(t, ps.started)
				select {
				case <-arrived:
				case <-time.After(5 * time.Second):
					t.Fatal("initial review did not start")
				}
				if err := e.Pause(); err != nil {
					t.Fatal(err)
				}
				releaseOnce.Do(func() { close(release) })
				select {
				case <-arrived:
					t.Fatalf("pause started a %s repair HTTP request", repair)
				case <-time.After(1100 * time.Millisecond):
				}
				assertPatternPaused(t, e, ps)
				if calls.Load() != 1 {
					t.Fatalf("pause sent more than the in-flight request: %d", calls.Load())
				}
				if action == "stop" {
					e.Stop()
					assertPatternStopped(t, e, output)
					if calls.Load() != 1 {
						t.Fatalf("stop sent a repair request: %d", calls.Load())
					}
					return
				}
				if err := e.Resume(); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "resumed repair", func() bool { return e.State() == StateIdle })
				got := e.Tasks()[0]
				if calls.Load() != 2 || ps.count.Load() != 1 || got.Phase != PhaseDone {
					t.Fatalf("pause exhausted network budget or repeated work: calls=%d renders=%d task=%+v", calls.Load(), ps.count.Load(), got)
				}
			})
		}
	}
}

func TestPatternPauseStopsPrecheckNetworkRetry(t *testing.T) {
	for _, action := range []string{"resume", "stop"} {
		t.Run(action, func(t *testing.T) {
			_, store, rec := newTestEngine(t)
			ps := &pausePhotoshop{started: make(chan psx.RenderRequest, 8)}
			e := New(ps, store, rec.add)
			tasks, output := patternGroupInputs(t)
			tasks = tasks[:1]
			seedPauseOutput(t, output)
			arrived := make(chan struct{}, 16)
			release := make(chan struct{})
			var releaseOnce sync.Once
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				arrived <- struct{}{}
				if n == 1 {
					<-release
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				reply := `{"watermark":false,"lens":false,"case_edge":false,"case_photo":false,"reason":"无"}`
				if n == 3 {
					reply = `{"rounded_panels":false,"layout":"无"}`
				} else if n > 3 {
					reply = `{"watermark":false,"uncertain":false,"evidence":"无"}`
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}}})
			}))
			t.Cleanup(func() {
				e.Stop()
				releaseOnce.Do(func() { close(release) })
				waitFor(t, "test engine cleanup", func() bool { return e.State() == StateIdle })
				if e.pre != nil {
					_ = e.pre.Close()
				}
				srv.Close()
			})
			def := store.Get()
			def.BaseURL, def.Model, def.TimeoutSec, def.PrecheckOn, def.PrecheckSkip = srv.URL, "fixture", 1, true, true
			opt := Options{OutputDir: output, NoCrop: true, Fit: "auto", Scales: []float64{1}, CellSize: 80, JPGQuality: 90}
			if err := e.begin(opt, def, tasks, 0, 0); err != nil {
				t.Fatal(err)
			}
			select {
			case <-arrived:
			case <-time.After(5 * time.Second):
				t.Fatal("precheck did not start")
			}
			if err := e.Pause(); err != nil {
				t.Fatal(err)
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case <-arrived:
				t.Fatal("pause started a precheck retry HTTP request")
			case <-time.After(1100 * time.Millisecond):
			}
			assertPatternPaused(t, e, ps)
			if action == "stop" {
				e.Stop()
				assertPatternStopped(t, e, output)
				if calls.Load() != 1 {
					t.Fatalf("stop sent precheck retry: %d", calls.Load())
				}
				return
			}
			if err := e.Resume(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "resumed precheck", func() bool { return e.State() == StateIdle })
			if got := e.Tasks()[0]; calls.Load() != 7 || ps.count.Load() != 1 || got.Phase != PhaseDone {
				t.Fatalf("precheck pause exhausted budget or repeated work: calls=%d renders=%d task=%+v", calls.Load(), ps.count.Load(), got)
			}
		})
	}
}
