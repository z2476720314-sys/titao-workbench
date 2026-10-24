package precheck

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// serve 起一个假端点，返回固定响应体，并校验请求形态。
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
		MaxTokens: 200, Timeout: 5 * time.Second, Temperature: 0.1,
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

// ---------- 解析 ----------

// TestParseRejectsWatermark 守住不合格判定的完整解析（ok/reason/tags）。
func TestParseRejectsWatermark(t *testing.T) {
	v := Parse(`{"ok": false, "reason": "带 shuiyin 水印", "tags": ["watermark"]}`, "")
	if v.OK {
		t.Fatal("应判不合格")
	}
	if v.Reason != "带 shuiyin 水印" {
		t.Errorf("Reason = %q", v.Reason)
	}
	if len(v.Tags) != 1 || v.Tags[0] != TagWatermark {
		t.Errorf("Tags = %v", v.Tags)
	}
	if v.Fallback {
		t.Error("正常解析不该标 Fallback")
	}
}

// TestParseAcceptsClean 守住合格判定。
func TestParseAcceptsClean(t *testing.T) {
	v := Parse(`{"ok": true, "reason": "纯图案无水印", "tags": []}`, "")
	if !v.OK {
		t.Fatal("应判合格")
	}
	if len(v.Tags) != 0 {
		t.Errorf("合格时 Tags 应为空，得到 %v", v.Tags)
	}
}

// TestParseReadsReasoningContent 守住「content 空、结论只在 reasoning_content」这个坑。
func TestParseReadsReasoningContent(t *testing.T) {
	v := Parse("\n"+`我看了图，{"ok": false, "reason": "有镜头框", "tags": ["lens"]}`, "")
	if v.OK {
		t.Fatal("应判不合格")
	}
	if v.Reason != "有镜头框" {
		t.Errorf("Reason = %q", v.Reason)
	}
	if len(v.Tags) != 1 || v.Tags[0] != TagLens {
		t.Errorf("Tags = %v", v.Tags)
	}
}

// TestParseMultiTags 守住多标签。
func TestParseMultiTags(t *testing.T) {
	v := Parse(`{"ok": false, "reason": "壳边框+水印", "tags": ["case-edge", "watermark"]}`, "")
	if len(v.Tags) != 2 {
		t.Fatalf("Tags = %v，期望 2 个", v.Tags)
	}
	if v.Tags[0] != TagCaseEdge || v.Tags[1] != TagWatermark {
		t.Errorf("Tags = %v", v.Tags)
	}
}

// TestParseFiltersUnknownTags 守住未知 tag 被丢弃、不合格时补 unknown。
func TestParseFiltersUnknownTags(t *testing.T) {
	v := Parse(`{"ok": false, "reason": "怪东西", "tags": ["blurry", "雾"]}`, "")
	if len(v.Tags) != 1 || v.Tags[0] != TagUnknown {
		t.Errorf("Tags = %v，期望 [unknown]", v.Tags)
	}
}

// TestParseFallbackOnGarbage 守住无法解析时默认放行。
func TestParseFallbackOnGarbage(t *testing.T) {
	v := Parse("这张图看起来还行吧。", "")
	if !v.OK {
		t.Error("无法解析时必须默认放行")
	}
	if !v.Fallback {
		t.Error("应标 Fallback")
	}
	if v.Err == nil {
		t.Error("Fallback 时 Err 应非空")
	}
}

// ---------- HTTP 通道 ----------

// TestCheckSendsDisableThinking 守住 enable_thinking=false 必须进请求体。
func TestCheckSendsDisableThinking(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, respWith(`{"ok": true, "reason": "干净", "tags": []}`, ""))
	}))
	defer srv.Close()

	cfg := cfgFor(srv)
	cfg.Thinking = false
	Check(cfg, "data:image/jpeg;base64,AAA")
	if v, ok := got["enable_thinking"]; !ok || v != false {
		t.Errorf("enable_thinking = %v（存在=%v），期望 false", v, ok)
	}
}

// TestCheckSendsOneImage 守住只送一张缩略图（省 token）。
func TestCheckSendsOneImage(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, respWith(`{"ok": true}`, ""))
	}))
	defer srv.Close()

	Check(cfgFor(srv), "data:image/jpeg;base64,AAA")
	msgs := got["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	var imgs int
	for _, c := range content {
		if c.(map[string]any)["type"] == "image_url" {
			imgs++
		}
	}
	if imgs != 1 {
		t.Errorf("送了 %d 张图，期望 1 张", imgs)
	}
}

// TestCheckHTTPErrorPasses 守住模型失败默认放行（不能因为接口挂了就不套图）。
func TestCheckHTTPErrorPasses(t *testing.T) {
	srv := serve(t, 500, `{"error":"boom"}`)
	v := Check(cfgFor(srv), "data:image/jpeg;base64,AAA")
	if !v.OK {
		t.Error("HTTP 500 时必须默认放行")
	}
	if !v.Fallback || v.Err == nil {
		t.Errorf("应标 Fallback 并带 Err，得到 Fallback=%v Err=%v", v.Fallback, v.Err)
	}
}

// TestCheckNetworkErrorPasses 守住连不上也放行。
func TestCheckNetworkErrorPasses(t *testing.T) {
	cfg := Config{BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m",
		MaxTokens: 10, Timeout: 2 * time.Second}
	v := Check(cfg, "data:image/jpeg;base64,AAA")
	if !v.OK || !v.Fallback {
		t.Errorf("连不上时应放行并标 Fallback，得到 OK=%v Fallback=%v", v.OK, v.Fallback)
	}
}

// TestCheckEmptyThumbPasses 守住缩略图为空时放行。
func TestCheckEmptyThumbPasses(t *testing.T) {
	v := Check(Config{}, "   ")
	if !v.OK || !v.Fallback {
		t.Errorf("空缩略图应放行，得到 OK=%v Fallback=%v", v.OK, v.Fallback)
	}
}

// TestCheckRejectPath 守住端到端不合格链路。
func TestCheckRejectPath(t *testing.T) {
	srv := serve(t, 200, respWith(`{"ok": false, "reason": "手机壳实拍", "tags": ["case-photo"]}`, ""))
	v := Check(cfgFor(srv), "data:image/jpeg;base64,AAA")
	if v.OK {
		t.Fatal("应判不合格")
	}
	if len(v.Tags) != 1 || v.Tags[0] != TagCasePhoto {
		t.Errorf("Tags = %v", v.Tags)
	}
}

// ---------- TagLabel ----------

// TestTagLabel 守住汇总文案里的中文标签。
func TestTagLabel(t *testing.T) {
	cases := map[string]string{
		TagWatermark: "水印",
		TagLens:      "镜头框",
		TagCaseEdge:  "壳边框",
		TagCasePhoto: "壳实拍",
		TagUnknown:   "其他",
		"weird":      "weird",
	}
	for in, want := range cases {
		if got := TagLabel(in); got != want {
			t.Errorf("TagLabel(%q) = %q，期望 %q", in, got, want)
		}
	}
}
