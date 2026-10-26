package tuner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"titao/internal/judge"
)

func testCfg(url string) Config {
	return Config{
		BaseURL:      url,
		APIKey:       "dummy",
		Model:        "test-model",
		MaxTokens:    1000,
		Timeout:      5 * time.Second,
		Temperature:  0.1,
		ImageMaxSide: 640,
	}
}

// fakeChat 起一个假的 chat/completions 服务，把模型输出塞进 content。
func fakeChat(t *testing.T, content, reasoning string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("路径错误: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer dummy" {
			t.Errorf("Authorization 错误: %q", got)
		}
		resp := map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"content":           content,
					"reasoning_content": reasoning,
				},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestCritiqueParsesGoodAdvice(t *testing.T) {
	srv := fakeChat(t, `{"good": true, "score": 90, "problem": "无", "direction": "none", "step": 0.1}`, "")
	defer srv.Close()

	adv := Critique(testCfg(srv.URL), []byte("fakejpeg"), judge.Placement{})
	if adv.Fallback {
		t.Fatalf("不该回退: %v", adv.Err)
	}
	if !adv.Good {
		t.Error("good 应为 true")
	}
	if adv.Score != 90 {
		t.Errorf("score = %d, 期望 90", adv.Score)
	}
	if adv.Direction != DirNone {
		t.Errorf("direction = %q, 期望 none", adv.Direction)
	}
}

func TestCritiqueParsesBiggerAdvice(t *testing.T) {
	srv := fakeChat(t, `{"good": false, "score": 60, "problem": "图案偏小", "direction": "bigger", "step": 0.2}`, "")
	defer srv.Close()

	adv := Critique(testCfg(srv.URL), []byte("fakejpeg"), judge.Placement{})
	if adv.Fallback {
		t.Fatalf("不该回退: %v", adv.Err)
	}
	if adv.Good {
		t.Error("good 应为 false")
	}
	if adv.Direction != DirBigger {
		t.Errorf("direction = %q, 期望 bigger", adv.Direction)
	}
	if adv.Step != 0.2 {
		t.Errorf("step = %v, 期望 0.2", adv.Step)
	}
	if adv.Problem != "图案偏小" {
		t.Errorf("problem = %q", adv.Problem)
	}
}

// 结论只出现在 reasoning_content 里而 content 为空（实测会遇到），必须也能解析。
func TestCritiqueFallsBackToReasoningContent(t *testing.T) {
	srv := fakeChat(t, "", `结论：{"good": false, "score": 55, "problem": "压摄像头", "direction": "avoid_cam", "step": 0.15}`)
	defer srv.Close()

	adv := Critique(testCfg(srv.URL), []byte("fakejpeg"), judge.Placement{})
	if adv.Fallback {
		t.Fatalf("不该回退: %v", adv.Err)
	}
	if adv.Direction != DirAvoidCame {
		t.Errorf("direction = %q, 期望 avoid_cam", adv.Direction)
	}
	if adv.Step != 0.15 {
		t.Errorf("step = %v, 期望 0.15", adv.Step)
	}
}

// 模型输出无法识别时结束微调，但不能冒充质量合格。
func TestCritiqueGarbageFallsBack(t *testing.T) {
	srv := fakeChat(t, "我觉得还行吧", "")
	defer srv.Close()

	adv := Critique(testCfg(srv.URL), []byte("fakejpeg"), judge.Placement{})
	if !adv.Fallback {
		t.Error("垃圾输出应该回退")
	}
	if adv.Good {
		t.Error("失败评审不得标记合格")
	}
	if adv.Err == nil {
		t.Error("回退时应带错误原因")
	}
}

func TestCritiqueHTTP500FallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	adv := Critique(testCfg(srv.URL), []byte("fakejpeg"), judge.Placement{})
	if !adv.Fallback {
		t.Error("HTTP 500 应该回退")
	}
	if !strings.Contains(adv.Err.Error(), "500") {
		t.Errorf("错误信息应含状态码: %v", adv.Err)
	}
}

func TestCritiqueEmptyJPEGFallsBack(t *testing.T) {
	adv := Critique(testCfg("http://127.0.0.1:1"), nil, judge.Placement{})
	if !adv.Fallback {
		t.Error("空图应该回退")
	}
}

func TestCritiqueFailureIsUnreviewed(t *testing.T) {
	adv := Critique(testCfg("http://127.0.0.1:1"), nil, judge.Placement{})
	if !adv.Fallback || adv.Good {
		t.Fatalf("failed review must remain unreviewed: %+v", adv)
	}
}

// 关思考时请求体必须带 enable_thinking=false，否则 token 被思考链烧光。
func TestCritiqueSendsEnableThinkingFalse(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"direction\":\"none\",\"good\":true,\"score\":90}"}}]}`))
	}))
	defer srv.Close()

	cfg := testCfg(srv.URL)
	cfg.Thinking = false
	Critique(cfg, []byte("fakejpeg"), judge.Placement{})

	if v, ok := got["enable_thinking"]; !ok || v != false {
		t.Errorf("enable_thinking 应为 false，实际 %v", v)
	}
}

// ── Plan 的参数推导 ──────────────────────────────────────────

func TestPlanGoodStops(t *testing.T) {
	_, _, _, iterate := Plan(Advice{Good: true, Direction: DirNone}, 1.0, 0, 0)
	if iterate {
		t.Error("已达标不该再迭代")
	}
}

func TestPlanNoneStops(t *testing.T) {
	_, _, _, iterate := Plan(Advice{Direction: DirNone}, 1.0, 0, 0)
	if iterate {
		t.Error("direction=none 不该再迭代")
	}
}

func TestPlanBigger(t *testing.T) {
	sc, ox, oy, iterate := Plan(Advice{Direction: DirBigger, Step: 0.2}, 1.0, 0, 0)
	if !iterate {
		t.Fatal("应该继续迭代")
	}
	if sc != 1.2 {
		t.Errorf("scale = %v, 期望 1.2", sc)
	}
	if ox != 0 || oy != 0 {
		t.Errorf("偏移不该变: %v %v", ox, oy)
	}
}

func TestPlanSmaller(t *testing.T) {
	sc, _, _, iterate := Plan(Advice{Direction: DirSmaller, Step: 0.1}, 1.0, 0, 0)
	if !iterate {
		t.Fatal("应该继续迭代")
	}
	if sc < 0.89 || sc > 0.91 {
		t.Errorf("scale = %v, 期望约 0.9", sc)
	}
}

func TestPlanOffsets(t *testing.T) {
	cases := []struct {
		dir    string
		wantX  float64
		wantY  float64
		reason string
	}{
		{DirUp, 0, -0.1, "上移"},
		{DirDown, 0, 0.1, "下移"},
		{DirLeft, -0.1, 0, "左移"},
		{DirRight, 0.1, 0, "右移"},
	}
	for _, c := range cases {
		_, ox, oy, iterate := Plan(Advice{Direction: c.dir, Step: 0.1}, 1.0, 0, 0)
		if !iterate {
			t.Errorf("%s 应该继续迭代", c.reason)
			continue
		}
		if ox != c.wantX || oy != c.wantY {
			t.Errorf("%s: offset = (%v,%v), 期望 (%v,%v)", c.reason, ox, oy, c.wantX, c.wantY)
		}
	}
}

func TestPlanAvoidCam(t *testing.T) {
	sc, _, oy, iterate := Plan(Advice{Direction: DirAvoidCame, Step: 0.2}, 1.0, 0, 0)
	if !iterate {
		t.Fatal("应该继续迭代")
	}
	if sc >= 1.0 {
		t.Errorf("压摄像头应该缩小，实际 scale=%v", sc)
	}
	if oy <= 0 {
		t.Errorf("避让画布顶部镜头应下移，实际 offsetY=%v", oy)
	}
}

// 参数已经顶到边界时不该继续空转。
func TestPlanAtBoundaryStops(t *testing.T) {
	_, _, _, iterate := Plan(Advice{Direction: DirBigger, Step: 0.2}, 2.0, 0, 0)
	if iterate {
		t.Error("scale 已在 2.0 上限，不该继续迭代")
	}
	_, _, _, iterate = Plan(Advice{Direction: DirRight, Step: 0.1}, 1.0, 0.3, 0)
	if iterate {
		t.Error("offsetX 已在 0.3 上限，不该继续迭代")
	}
}

func TestClampScale(t *testing.T) {
	if v := clampScale(0.1); v != 0.3 {
		t.Errorf("下限应为 0.3，实际 %v", v)
	}
	if v := clampScale(5); v != 2.0 {
		t.Errorf("上限应为 2.0，实际 %v", v)
	}
	if v := clampScale(1.1); v != 1.1 {
		t.Errorf("正常值不该被改，实际 %v", v)
	}
}

func TestClampStep(t *testing.T) {
	if v := clampStep(0.01); v != 0.05 {
		t.Errorf("下限应为 0.05，实际 %v", v)
	}
	if v := clampStep(1.0); v != 0.3 {
		t.Errorf("上限应为 0.3，实际 %v", v)
	}
}

func TestClampOffset(t *testing.T) {
	if v := clampOffset(-1); v != -0.3 {
		t.Errorf("下限应为 -0.3，实际 %v", v)
	}
	if v := clampOffset(1); v != 0.3 {
		t.Errorf("上限应为 0.3，实际 %v", v)
	}
}

// 只给分数没给 good 时，score>=85 且 direction=none 应视为达标。
func TestParseAdviceInfersGoodFromScore(t *testing.T) {
	adv := parseAdvice(`{"score": 88, "direction": "none"}`)
	if !adv.Good {
		t.Error("score=88 且 direction=none 应推断为 good")
	}
}

func TestParseAdvicePreservesExplicitFailure(t *testing.T) {
	adv := parseAdvice(`{"good":false,"score":88,"problem":"关键文字被遮挡","direction":"none"}`)
	if adv.Good {
		t.Fatal("explicit good=false must not be overridden by score")
	}
}

// 非法 direction 应被丢弃，走回退。
func TestParseAdviceRejectsUnknownDirection(t *testing.T) {
	adv := parseAdvice(`{"direction": "explode", "good": false}`)
	if adv.Direction != "" {
		t.Errorf("非法 direction 应被清空，实际 %q", adv.Direction)
	}
}
