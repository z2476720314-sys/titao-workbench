package genimg

import (
	"bytes"
	"context"
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
	"strings"
	"testing"
	"time"
)

// ---------- 测试脚手架 ----------

// jpegBytes 造一张纯色小 JPEG。
func jpegBytes(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("编码测试图: %v", err)
	}
	return buf.Bytes()
}

// chatResp 拼 chat/completions 的响应体。
func chatResp(content, reasoning string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{
				"content":           content,
				"reasoning_content": reasoning,
			},
		}},
	})
	return string(b)
}

// visionFor 把 VisionConfig 指向测试服务器。
func visionFor(srv *httptest.Server) VisionConfig {
	return VisionConfig{
		BaseURL:   srv.URL,
		APIKey:    "test-key",
		Model:     "test-vision",
		Endpoint:  srv.URL + "/chat/completions",
		Timeout:   5 * time.Second,
		MaxTokens: 100,
	}
}

// captureBody 起一个假 chat 端点，把请求体记下来供断言。
func captureChat(t *testing.T, status int, body string, captured *[]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("Authorization 头不对: %q", got)
		}
		raw, _ := io.ReadAll(r.Body)
		*captured = raw
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ---------- BuildPrompt 两分支 ----------

func TestBuildPromptHandheld(t *testing.T) {
	var captured []byte
	srv := captureChat(t, 200, chatResp("```\nA hand holds the phone case against a marble desk, soft window light.\n```", ""), &captured)

	got, err := BuildPrompt(context.Background(), visionFor(srv), jpegBytes(t, color.RGBA{200, 120, 60, 255}), ModeHandheld, false)
	if err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}
	want := "A hand holds the phone case against a marble desk, soft window light."
	if got != want {
		t.Errorf("提示词未剥围栏\n got=%q\nwant=%q", got, want)
	}

	// 指令里必须出现手持相关的强约束
	reqText := string(captured)
	for _, kw := range []string{"新的手持图", "完全一致", "摄像头开孔", "不要在画面里出现文字"} {
		if !strings.Contains(reqText, kw) {
			t.Errorf("手持指令缺少关键词 %q", kw)
		}
	}
	// 手持指令不应出现"不要出现手"
	if strings.Contains(reqText, "不要出现手") {
		t.Error("手持指令里不应有「不要出现手」")
	}
	// 参考图必须作为 image_url data URL 送出
	if !strings.Contains(reqText, "data:image/jpeg;base64,") {
		t.Error("请求里缺少参考图 data URL")
	}
	// 关思考时必须显式传 enable_thinking=false
	if !strings.Contains(reqText, `"enable_thinking":false`) {
		t.Error("未显式关闭 enable_thinking")
	}
}

func TestBuildPromptBackground(t *testing.T) {
	var captured []byte
	srv := captureChat(t, 200, chatResp("Prompt: A phone case on a clean wooden table with dried flowers, warm sunset light.", ""), &captured)

	got, err := BuildPrompt(context.Background(), visionFor(srv), jpegBytes(t, color.RGBA{30, 160, 90, 255}), ModeBackground, false)
	if err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}
	want := "A phone case on a clean wooden table with dried flowers, warm sunset light."
	if got != want {
		t.Errorf("提示词未剥 Prompt: 前缀\n got=%q\nwant=%q", got, want)
	}

	reqText := string(captured)
	for _, kw := range []string{"换了背景的新图", "完全一致", "不要出现手", "不要在画面里出现文字"} {
		if !strings.Contains(reqText, kw) {
			t.Errorf("换背景指令缺少关键词 %q", kw)
		}
	}
}

func TestBuildPromptRetryAddsHardConstraint(t *testing.T) {
	var captured []byte
	srv := captureChat(t, 200, chatResp("keep it identical", ""), &captured)

	if _, err := BuildPrompt(context.Background(), visionFor(srv), jpegBytes(t, color.RGBA{10, 10, 10, 255}), ModeHandheld, true); err != nil {
		t.Fatalf("BuildPrompt retry: %v", err)
	}
	if !strings.Contains(string(captured), "pixel-identical to the reference image") {
		t.Error("重试时缺少加强约束")
	}
}

func TestBuildPromptEmptyRef(t *testing.T) {
	if _, err := BuildPrompt(context.Background(), VisionConfig{}, nil, ModeHandheld, false); err == nil {
		t.Fatal("空参考图应当报错")
	}
}

func TestBuildPromptNoAPIKey(t *testing.T) {
	vc := VisionConfig{BaseURL: "http://127.0.0.1:1", Model: "m"}
	if _, err := BuildPrompt(context.Background(), vc, jpegBytes(t, color.RGBA{1, 2, 3, 255}), ModeHandheld, false); err == nil {
		t.Fatal("缺 API Key 应当报错")
	}
}

func TestPromptForUnknownMode(t *testing.T) {
	if _, err := PromptFor(Mode("nope")); err == nil {
		t.Fatal("未知模式应当报错")
	}
}

func TestCleanPrompt(t *testing.T) {
	cases := map[string]string{
		"```\nabc\n```":          "abc",
		"Prompt: abc":            "abc",
		"提示词：abc":                "abc",
		`"abc"`:                  "abc",
		"“abc”":                  "abc",
		"  plain  ":              "plain",
		"```text\nabc\ndef\n```": "abc\ndef",
	}
	for in, want := range cases {
		if got := cleanPrompt(in); got != want {
			t.Errorf("cleanPrompt(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---------- 一致性校验解析 ----------

func TestCheckConsistencyJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(chatResp(`{"same": true, "reason": "图案完全一致", "diff": ""}`, "")))
	}))
	defer srv.Close()

	res := CheckConsistency(context.Background(), visionFor(srv), jpegBytes(t, color.RGBA{9, 9, 9, 255}), jpegBytes(t, color.RGBA{9, 9, 9, 255}))
	if !res.Parsed || !res.Same {
		t.Fatalf("应判一致，得到 %+v", res)
	}
	if res.Reason != "图案完全一致" {
		t.Errorf("reason 解析错: %q", res.Reason)
	}
}

func TestCheckConsistencyReasoningFallback(t *testing.T) {
	// content 为空，结论只在 reasoning_content 里 —— judge 包踩过的坑
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(chatResp("", `判断完成：{"same": false, "reason": "图案被重画", "diff": "花瓣数量变少、颜色偏紫"}`)))
	}))
	defer srv.Close()

	res := CheckConsistency(context.Background(), visionFor(srv), jpegBytes(t, color.RGBA{9, 9, 9, 255}), jpegBytes(t, color.RGBA{8, 8, 8, 255}))
	if !res.Parsed {
		t.Fatalf("reasoning_content 回退失败: %+v", res)
	}
	if res.Same {
		t.Fatal("应判不一致")
	}
	if res.Reason != "图案被重画" {
		t.Errorf("reason 解析错: %q", res.Reason)
	}
	if !strings.Contains(res.Diff, "花瓣") {
		t.Errorf("diff 解析错: %q", res.Diff)
	}
}

func TestCheckConsistencyChineseFallback(t *testing.T) {
	// 模型没吐 JSON，只写自然语言
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(chatResp("两张图的手机壳图案不一致，花瓣被简化了。", "")))
	}))
	defer srv.Close()
	res := CheckConsistency(context.Background(), visionFor(srv), jpegBytes(t, color.RGBA{9, 9, 9, 255}), jpegBytes(t, color.RGBA{8, 8, 8, 255}))
	if !res.Parsed || res.Same {
		t.Fatalf("中文兜底应判不一致，得到 %+v", res)
	}
}

func TestCheckConsistencyChineseSameFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(chatResp("手机壳图案完全一致，是同一款。", "")))
	}))
	defer srv.Close()
	res := CheckConsistency(context.Background(), visionFor(srv), jpegBytes(t, color.RGBA{9, 9, 9, 255}), jpegBytes(t, color.RGBA{8, 8, 8, 255}))
	if !res.Parsed || !res.Same {
		t.Fatalf("中文兜底应判一致，得到 %+v", res)
	}
}

func TestCheckConsistencyGarbageFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(chatResp("我看不出来", "")))
	}))
	defer srv.Close()
	res := CheckConsistency(context.Background(), visionFor(srv), jpegBytes(t, color.RGBA{9, 9, 9, 255}), jpegBytes(t, color.RGBA{8, 8, 8, 255}))
	if res.Same {
		t.Fatal("解析失败必须当作不一致，绝不能静默通过")
	}
	if res.Err == nil {
		t.Fatal("解析失败必须带 Err")
	}
}

func TestCheckConsistencyHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer srv.Close()
	res := CheckConsistency(context.Background(), visionFor(srv), jpegBytes(t, color.RGBA{9, 9, 9, 255}), jpegBytes(t, color.RGBA{8, 8, 8, 255}))
	if res.Same || res.Err == nil {
		t.Fatalf("HTTP 500 应失败，得到 %+v", res)
	}
	if !strings.Contains(res.Err.Error(), "boom") {
		t.Errorf("错误信息未带上服务端 message: %v", res.Err)
	}
}

func TestCheckConsistencyEmptyInput(t *testing.T) {
	res := CheckConsistency(context.Background(), VisionConfig{}, nil, nil)
	if res.Same || res.Err == nil {
		t.Fatal("空输入必须失败")
	}
}

// ---------- 生图 multipart 字段名 ----------

// genServer 起假生图端点：解析 multipart，校验字段名，返回 b64_json。
func genServer(t *testing.T, status int, wantFields map[string]string, wantImages int, captured *map[string][]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/edits" {
			t.Errorf("路径不对: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("Authorization 头不对: %q", got)
		}
		mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || !strings.HasPrefix(mt, "multipart/form-data") {
			t.Fatalf("Content-Type 不是 multipart: %q (%v)", r.Header.Get("Content-Type"), err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		fields := map[string][]string{}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("读 multipart: %v", err)
			}
			raw, _ := io.ReadAll(part)
			fields[part.FormName()] = append(fields[part.FormName()], string(raw))
			if part.FormName() == "image" {
				if fn := part.FileName(); fn == "" {
					t.Error("image 部分缺少 filename")
				}
				if ct := part.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
					t.Errorf("image 部分 Content-Type 不对: %q", ct)
				}
			}
			part.Close()
		}
		*captured = fields

		if status != 200 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream exploded"}}`))
			return
		}
		// 校验必填字段
		for k, v := range wantFields {
			if len(fields[k]) == 0 {
				t.Errorf("缺字段 %q", k)
				continue
			}
			if fields[k][0] != v {
				t.Errorf("字段 %q = %q, want %q", k, fields[k][0], v)
			}
		}
		if n := len(fields["image"]); n != wantImages {
			t.Errorf("image 部分 %d 个，want %d", n, wantImages)
		}

		img := jpegBytes(t, color.RGBA{50, 100, 150, 255})
		body, _ := json.Marshal(map[string]any{
			"data": []any{map[string]any{
				"b64_json":       base64.StdEncoding.EncodeToString(img),
				"revised_prompt": "revised!",
			}},
			"usage": map[string]any{"total_tokens": 42},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func genClientFor(srv *httptest.Server) *Client {
	c := NewClient(Config{BaseURL: srv.URL + "/v1", APIKey: "test-key", Model: "gpt-image-2.5", Size: "1024x1024"})
	return c
}

func TestGenerateMultipartFields(t *testing.T) {
	want := map[string]string{
		"model":           "gpt-image-2.5",
		"prompt":          "a hand holding the case",
		"size":            "1024x1024",
		"response_format": "b64_json",
		"n":               "1",
		"thinking":        "false",
	}
	var fields map[string][]string
	srv := genServer(t, 200, want, 2, &fields)

	c := genClientFor(srv)
	res, err := c.Generate(context.Background(), GenerateRequest{
		Refs:   [][]byte{jpegBytes(t, color.RGBA{1, 1, 1, 255}), jpegBytes(t, color.RGBA{2, 2, 2, 255})},
		Prompt: "a hand holding the case",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(res.Raw) == 0 || res.MIME != "image/jpeg" {
		t.Errorf("结果不对: len=%d mime=%q", len(res.Raw), res.MIME)
	}
	if res.RevisedPrompt != "revised!" {
		t.Errorf("revised_prompt 未解析: %q", res.RevisedPrompt)
	}
	if res.Usage["total_tokens"] != float64(42) {
		t.Errorf("usage 未解析: %v", res.Usage)
	}
	if got := fields["image"]; len(got) != 2 {
		t.Errorf("image 字段应重复 2 次，得到 %d", len(got))
	}
}

func TestGenerateThinkingTrue(t *testing.T) {
	var fields map[string][]string
	srv := genServer(t, 200, map[string]string{"thinking": "true"}, 1, &fields)
	c := NewClient(Config{BaseURL: srv.URL + "/v1", APIKey: "k", Thinking: true})
	if _, err := c.Generate(context.Background(), GenerateRequest{
		Refs: [][]byte{jpegBytes(t, color.RGBA{1, 1, 1, 255})}, Prompt: "p",
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if fields["thinking"][0] != "true" {
		t.Errorf("thinking 应为 true，得到 %q", fields["thinking"][0])
	}
}

func TestGenerateHTTP4xx(t *testing.T) {
	var fields map[string][]string
	srv := genServer(t, 429, nil, 1, &fields)
	_, err := genClientFor(srv).Generate(context.Background(), GenerateRequest{
		Refs: [][]byte{jpegBytes(t, color.RGBA{1, 1, 1, 255})}, Prompt: "p",
	})
	if err == nil {
		t.Fatal("429 应当报错")
	}
	if !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "upstream exploded") {
		t.Errorf("错误信息应含状态码与服务端 message: %v", err)
	}
}

func TestGenerateHTTP5xx(t *testing.T) {
	var fields map[string][]string
	srv := genServer(t, 503, nil, 1, &fields)
	_, err := genClientFor(srv).Generate(context.Background(), GenerateRequest{
		Refs: [][]byte{jpegBytes(t, color.RGBA{1, 1, 1, 255})}, Prompt: "p",
	})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("503 应报错且带状态码，得到 %v", err)
	}
}

func TestGenerateEmptyData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	_, err := genClientFor(srv).Generate(context.Background(), GenerateRequest{
		Refs: [][]byte{jpegBytes(t, color.RGBA{1, 1, 1, 255})}, Prompt: "p",
	})
	if err == nil || !strings.Contains(err.Error(), "data") {
		t.Fatalf("空 data 应报错，得到 %v", err)
	}
}

func TestGenerateValidations(t *testing.T) {
	c := NewClient(Config{APIKey: ""})
	if _, err := c.Generate(context.Background(), GenerateRequest{Prompt: "p"}); err == nil {
		t.Error("缺 key 应报错")
	}

	c2 := NewClient(Config{APIKey: "k"})
	if _, err := c2.Generate(context.Background(), GenerateRequest{Prompt: "p"}); err == nil {
		t.Error("缺参考图应报错")
	}
	if _, err := c2.Generate(context.Background(), GenerateRequest{Refs: [][]byte{{1}}}); err == nil {
		t.Error("空提示词应报错")
	}
	tooMany := make([][]byte, MaxRefImages+1)
	for i := range tooMany {
		tooMany[i] = []byte{1}
	}
	if _, err := c2.Generate(context.Background(), GenerateRequest{Refs: tooMany, Prompt: "p"}); err == nil {
		t.Error("超过 15 张应报错")
	}
}

func TestConfigDefaults(t *testing.T) {
	c := NewClient(Config{})
	got := c.Config()
	if got.BaseURL != "https://junli1.junlistudio.com/v1" {
		t.Errorf("BaseURL 默认值错: %q", got.BaseURL)
	}
	if got.Model != "gpt-image-2.5" {
		t.Errorf("Model 默认值错: %q", got.Model)
	}
	if got.Size != "1024x1024" {
		t.Errorf("Size 默认值错: %q", got.Size)
	}
	if got.Timeout != 300*time.Second {
		t.Errorf("Timeout 默认值错: %v", got.Timeout)
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": ModeHandheld, "handheld": ModeHandheld, "Background": ModeBackground, " background ": ModeBackground} {
		got, err := ParseMode(in)
		if err != nil || got != want {
			t.Errorf("ParseMode(%q) = %v,%v want %v", in, got, err, want)
		}
	}
	if _, err := ParseMode("weird"); err == nil {
		t.Error("未知模式应报错")
	}
	if ModeHandheld.Label() == "" || ModeBackground.Label() == "" {
		t.Error("Label 不应为空")
	}
	if !ModeHandheld.Valid() || Mode("x").Valid() {
		t.Error("Valid 判断错")
	}
}

func TestDetectMIME(t *testing.T) {
	if DetectMIME(jpegBytes(t, color.RGBA{1, 1, 1, 255})) != "image/jpeg" {
		t.Error("JPEG 识别错")
	}
	if DetectMIME([]byte("\x89PNG\r\n\x1a\nxxxx")) != "image/png" {
		t.Error("PNG 识别错")
	}
	if DetectMIME([]byte("RIFF\x00\x00\x00\x00WEBPVP8 ")) != "image/webp" {
		t.Error("WebP 识别错")
	}
	if DetectMIME([]byte("abc")) != "application/octet-stream" {
		t.Error("未知类型应返回 application/octet-stream")
	}
}

func TestSaveAndToJPEG(t *testing.T) {
	raw := jpegBytes(t, color.RGBA{20, 30, 40, 255})
	j, err := ToJPEG(raw, 64, 88)
	if err != nil || len(j) == 0 {
		t.Fatalf("ToJPEG: %v", err)
	}
	dir := t.TempDir()
	w, h, err := SaveJPEG(raw, dir+"/out.jpg", 88)
	if err != nil {
		t.Fatalf("SaveJPEG: %v", err)
	}
	if w <= 0 || h <= 0 {
		t.Errorf("宽高异常: %dx%d", w, h)
	}
	if _, err := FileJPEG(dir+"/missing.jpg", 64, 88); err == nil {
		t.Error("不存在的文件应报错")
	}
	if _, err := ToJPEG([]byte("not an image"), 64, 88); err == nil {
		t.Error("非图片字节应报错")
	}
}

func TestErrorTextFallbacks(t *testing.T) {
	if got := errorText([]byte(`{"error":{"message":"m1"}}`)); got != "m1" {
		t.Errorf("error.message 未提取: %q", got)
	}
	if got := errorText([]byte(`{"message":"m2"}`)); got != "m2" {
		t.Errorf("message 未提取: %q", got)
	}
	if got := errorText([]byte("  plain text  ")); got != "plain text" {
		t.Errorf("纯文本未提取: %q", got)
	}
	if got := errorText(nil); got != "(空响应)" {
		t.Errorf("空响应兜底错: %q", got)
	}
	long := strings.Repeat("x", 400)
	if got := errorText([]byte(long)); len(got) != 300 {
		t.Errorf("长文本应截到 300，得到 %d", len(got))
	}
}

func TestVisionChatReasoningOnly(t *testing.T) {
	// content 空、reasoning_content 有内容时必须能拿到
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(chatResp("", "只有思考链里的结论")))
	}))
	defer srv.Close()
	got, err := chat(context.Background(), visionFor(srv), "hi", nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if !strings.Contains(got, "只有思考链里的结论") {
		t.Errorf("reasoning_content 未回退: %q", got)
	}
}

func TestVisionChatNoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()
	if _, err := chat(context.Background(), visionFor(srv), "hi", nil); err == nil {
		t.Fatal("无 choices 应报错")
	}
}

func TestVisionChatEmptyBlob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(chatResp("", "")))
	}))
	defer srv.Close()
	if _, err := chat(context.Background(), visionFor(srv), "hi", nil); err == nil {
		t.Fatal("空内容应报错")
	}
}

func TestVisionConfigNormalize(t *testing.T) {
	v := VisionConfig{}.normalize()
	if v.MaxTokens != 4000 || v.Timeout != 180*time.Second {
		t.Errorf("默认值错: %+v", v)
	}
}

func TestErrorMessageIncludesMode(t *testing.T) {
	_, err := PromptFor(Mode("bad"))
	if err == nil || !strings.Contains(fmt.Sprint(err), "bad") {
		t.Errorf("错误信息应含模式名: %v", err)
	}
}
