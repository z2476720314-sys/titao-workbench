package judge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// cands 造 n 个候选，JPEG 内容只需非空。
func cands(n int) []Candidate {
	out := make([]Candidate, n)
	for i := range out {
		out[i] = Candidate{JPEG: []byte{0xFF, 0xD8, 0xFF, byte(i)}, Scale: 0.85 + float64(i)*0.1}
	}
	return out
}

// serve 起一个假端点，返回固定响应体。
func serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("缺少 Authorization 头: %q", got)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("路径 = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func cfgFor(srv *httptest.Server) Config {
	return Config{
		BaseURL: srv.URL, APIKey: "k", Model: "m",
		MaxTokens: 100, Timeout: 5 * time.Second, Temperature: 0.1,
		ImageMaxSide: 640, Thinking: false,
	}
}

func respWith(content, reasoning string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{"content": content, "reasoning_content": reasoning},
		}},
	})
	return string(b)
}

// TestChooseParsesJSONPick 守住标准 JSON 输出路径。
func TestChooseParsesJSONPick(t *testing.T) {
	srv := serve(t, 200, respWith(`{"pick": 3, "reason": "饱满不压摄像头"}`, ""))
	r := Choose(cfgFor(srv), cands(5), "图案A")
	if r.Fallback {
		t.Fatalf("不该回退: %v", r.Err)
	}
	if r.Pick != 2 {
		t.Errorf("Pick = %d，期望 2（模型给的是 1-based 3）", r.Pick)
	}
	if r.Reason != "饱满不压摄像头" {
		t.Errorf("Reason = %q", r.Reason)
	}
}

// TestChooseReadsReasoningContent 守住「content 为空、结论只在 reasoning_content 里」这个坑。
func TestChooseReadsReasoningContent(t *testing.T) {
	srv := serve(t, 200, respWith("", `我比较了五张，{"pick": 4, "reason": "留白少"}`))
	r := Choose(cfgFor(srv), cands(5), "图案A")
	if r.Fallback {
		t.Fatalf("不该回退: %v", r.Err)
	}
	if r.Pick != 3 {
		t.Errorf("Pick = %d，期望 3", r.Pick)
	}
}

// TestChooseParsesChinesePick 守住中文兜底解析。
func TestChooseParsesChinesePick(t *testing.T) {
	srv := serve(t, 200, respWith("我选第 2 张，因为留白合适。", ""))
	r := Choose(cfgFor(srv), cands(5), "图案A")
	if r.Fallback {
		t.Fatalf("不该回退: %v", r.Err)
	}
	if r.Pick != 1 {
		t.Errorf("Pick = %d，期望 1", r.Pick)
	}
}

// TestChooseFallbackOnOutOfRange 守住越界下标不会让上层 panic。
func TestChooseFallbackOnOutOfRange(t *testing.T) {
	srv := serve(t, 200, respWith(`{"pick": 99}`, ""))
	r := Choose(cfgFor(srv), cands(3), "图案A")
	if !r.Fallback {
		t.Fatal("越界下标应回退")
	}
	if r.Pick < 0 || r.Pick >= 3 {
		t.Errorf("回退下标越界: %d", r.Pick)
	}
}

// TestChooseFallbackOnHTTPError 守住 HTTP 失败时回退而不中断流程。
func TestChooseFallbackOnHTTPError(t *testing.T) {
	srv := serve(t, 500, `{"error":"boom"}`)
	r := Choose(cfgFor(srv), cands(5), "图案A")
	if !r.Fallback {
		t.Fatal("HTTP 500 应回退")
	}
	if r.Err == nil {
		t.Error("回退时 Err 应非空")
	}
	// 兜底取中间偏小：宁可略小也不要裁坏
	if r.Pick != 1 {
		t.Errorf("兜底 Pick = %d，期望 1（中间偏小）", r.Pick)
	}
}

// TestChooseFallbackOnGarbage 守住无法解析的输出回退。
func TestChooseFallbackOnGarbage(t *testing.T) {
	srv := serve(t, 200, respWith("我不知道该选哪个。", ""))
	r := Choose(cfgFor(srv), cands(4), "图案A")
	if !r.Fallback {
		t.Fatal("无法解析应回退")
	}
	if r.Pick != 1 {
		t.Errorf("兜底 Pick = %d，期望 1", r.Pick)
	}
}

// TestChooseSendsDisableThinking 守住 enable_thinking=false 必须进请求体
// （否则思考链烧光 token，content 返回空串）。
func TestChooseSendsDisableThinking(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, respWith(`{"pick": 1}`, ""))
	}))
	defer srv.Close()

	cfg := cfgFor(srv)
	cfg.Thinking = false
	Choose(cfg, cands(2), "图案A")
	v, ok := got["enable_thinking"]
	if !ok {
		t.Fatal("请求体缺少 enable_thinking")
	}
	if v != false {
		t.Errorf("enable_thinking = %v，期望 false", v)
	}
}

// TestChooseSendsAllImages 守住多档候选一起送，模型才能做相对比较。
func TestChooseSendsAllImages(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, respWith(`{"pick": 2}`, ""))
	}))
	defer srv.Close()

	Choose(cfgFor(srv), cands(5), "图案A")

	msgs := got["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	var imgs int
	for _, c := range content {
		if c.(map[string]any)["type"] == "image_url" {
			imgs++
		}
	}
	if imgs != 5 {
		t.Errorf("送了 %d 张图，期望 5 张", imgs)
	}
}
