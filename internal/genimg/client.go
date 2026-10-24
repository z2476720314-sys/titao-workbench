// Package genimg 接入生图大模型（Junli Studio / OpenAI 兼容服务）。
//
// 职责边界：
//   - Client.Generate 负责一次图生图调用（POST /v1/images/edits，multipart）
//   - BuildPrompt / CheckConsistency 走视觉模型（POST /v1/chat/completions）
//
// 关键事实（已从官方文档核实，不要凭直觉改）：
//   - n 每次只能生成 1 张，JSON 请求不接受其他数量；要 N 张就循环调用 N 次
//   - image 字段名重复可传多张参考图，最多 15 张
//   - response_format=b64_json 时图片在 data[0].b64_json
//
// 解析教训沿用 judge 包：结论可能只出现在 message.reasoning_content 而
// content 为空，两处必须一起找，否则会误判成"模型没回答"。
package genimg

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

// MaxRefImages 是接口允许的单次参考图上限。
const MaxRefImages = 15

// Mode 是生图模式。
type Mode string

const (
	// ModeHandheld 新手持图：换手持姿态、角度、场景，壳不变。
	ModeHandheld Mode = "handheld"
	// ModeBackground 换背景图：换背景环境，壳不变。
	ModeBackground Mode = "background"
)

// Valid 报告模式是否合法。
func (m Mode) Valid() bool { return m == ModeHandheld || m == ModeBackground }

// Label 返回中文名。
func (m Mode) Label() string {
	switch m {
	case ModeHandheld:
		return "新手持图"
	case ModeBackground:
		return "换背景图"
	default:
		return string(m)
	}
}

// ParseMode 把字符串解析成 Mode，空串默认 handheld。
func ParseMode(s string) (Mode, error) {
	switch Mode(strings.TrimSpace(strings.ToLower(s))) {
	case "", ModeHandheld:
		return ModeHandheld, nil
	case ModeBackground:
		return ModeBackground, nil
	default:
		return "", fmt.Errorf("未知生图模式 %q（可选 handheld / background）", s)
	}
}

// Config 是生图模型配置。字段与 cfg.Config 的 Gen* 一一对应。
type Config struct {
	BaseURL  string
	APIKey   string
	Model    string
	Size     string
	Thinking bool
	Timeout  time.Duration
}

// HasKey 报告是否配置了可用的 API Key。
func (c Config) HasKey() bool { return strings.TrimSpace(c.APIKey) != "" }

// normalize 兜住空值，避免拼出 "//images/edits" 这种 URL。
func (c Config) normalize() Config {
	if c.BaseURL == "" {
		c.BaseURL = "https://junli1.junlistudio.com/v1"
	}
	if c.Model == "" {
		c.Model = "gpt-image-2.5"
	}
	if c.Size == "" {
		c.Size = "1024x1024"
	}
	if c.Timeout <= 0 {
		c.Timeout = 300 * time.Second
	}
	return c
}

// GenerateRequest 是一次图生图的入参。
type GenerateRequest struct {
	// Refs 是参考图（JPEG/PNG 原始字节），至少 1 张，最多 MaxRefImages 张。
	Refs [][]byte
	// Prompt 是生图提示词。
	Prompt string
}

// GenerateResult 是一次图生图的结果。
type GenerateResult struct {
	// Raw 是接口返回的图片原始字节（PNG 或 JPEG）。
	Raw []byte
	// MIME 是按魔数识别出的类型。
	MIME string
	// RevisedPrompt 是服务端改写后的提示词（可能为空）。
	RevisedPrompt string
	// Usage 是服务端返回的用量信息（可能为空）。
	Usage map[string]any
}

// Client 调用生图接口。
type Client struct {
	cfg  Config
	http *http.Client
	// Endpoint 允许测试指向假服务器；为空时用 cfg.BaseURL + /images/edits。
	Endpoint string
}

// NewClient 创建客户端。
func NewClient(cfg Config) *Client {
	c := cfg.normalize()
	return &Client{cfg: c, http: &http.Client{Timeout: c.Timeout}}
}

// Config 返回生效的配置（已补默认值）。
func (c *Client) Config() Config { return c.cfg }

// Generate 调一次图生图，返回 1 张图的字节。
//
// 注意：接口的 n 只支持 1，多张由调用方循环。这里刻意不做重试——
// 重试策略属于任务层（genjobs），客户端保持单次调用的确定性。
func (c *Client) Generate(ctx context.Context, req GenerateRequest) (*GenerateResult, error) {
	if !c.cfg.HasKey() {
		return nil, fmt.Errorf("未配置生图 API Key（config.json 的 gen_api_key）")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("生图提示词为空")
	}
	if len(req.Refs) == 0 {
		return nil, fmt.Errorf("缺少参考图")
	}
	if len(req.Refs) > MaxRefImages {
		return nil, fmt.Errorf("参考图 %d 张，超过上限 %d 张", len(req.Refs), MaxRefImages)
	}

	body, contentType, err := buildMultipart(c.cfg, req)
	if err != nil {
		return nil, err
	}

	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = strings.TrimRight(c.cfg.BaseURL, "/") + "/images/edits"
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	hreq.Header.Set("Content-Type", contentType)

	resp, err := c.http.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("请求生图接口: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("生图接口 HTTP %d: %s", resp.StatusCode, errorText(raw))
	}

	var parsed struct {
		Data []struct {
			B64JSON       string `json:"b64_json"`
			URL           string `json:"url"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
		Usage map[string]any `json:"usage"`
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("解析生图响应: %w（前 200 字节: %.200s）", err, raw)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return nil, fmt.Errorf("生图接口报错: %s", parsed.Error.Message)
	}
	if len(parsed.Data) == 0 {
		return nil, fmt.Errorf("生图响应没有 data 数组: %.300s", raw)
	}
	item := parsed.Data[0]
	if item.B64JSON == "" {
		if item.URL != "" {
			return nil, fmt.Errorf("生图接口只返回了 url（%s），请确认 response_format=b64_json", item.URL)
		}
		return nil, fmt.Errorf("生图响应 data[0] 既无 b64_json 也无 url")
	}

	img, err := base64.StdEncoding.DecodeString(item.B64JSON)
	if err != nil {
		return nil, fmt.Errorf("解码 b64_json: %w", err)
	}
	if len(img) == 0 {
		return nil, fmt.Errorf("生图响应 b64_json 解码后为空")
	}
	return &GenerateResult{
		Raw:           img,
		MIME:          DetectMIME(img),
		RevisedPrompt: item.RevisedPrompt,
		Usage:         parsed.Usage,
	}, nil
}

// buildMultipart 拼装 multipart/form-data 请求体。
//
// 字段名必须精确：model / prompt / image / size / response_format / thinking。
// image 重复出现表示多张参考图；n 一律固定 1（接口只支持 1）。
func buildMultipart(cfg Config, req GenerateRequest) (io.Reader, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	if err := w.WriteField("model", cfg.Model); err != nil {
		return nil, "", err
	}
	if err := w.WriteField("prompt", req.Prompt); err != nil {
		return nil, "", err
	}
	if err := w.WriteField("size", cfg.Size); err != nil {
		return nil, "", err
	}
	if err := w.WriteField("response_format", "b64_json"); err != nil {
		return nil, "", err
	}
	if err := w.WriteField("n", "1"); err != nil {
		return nil, "", err
	}
	thinking := "false"
	if cfg.Thinking {
		thinking = "true"
	}
	if err := w.WriteField("thinking", thinking); err != nil {
		return nil, "", err
	}

	for i, ref := range req.Refs {
		mime := DetectMIME(ref)
		ext := ".png"
		switch mime {
		case "image/jpeg":
			ext = ".jpg"
		case "image/webp":
			ext = ".webp"
		case "image/png":
		default:
			// 魔数认不出时按 PNG 送，别把 octet-stream 塞给接口
			mime = "image/png"
		}
		name := fmt.Sprintf("ref%02d%s", i+1, ext)

		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition",
			fmt.Sprintf(`form-data; name="image"; filename="%s"`, name))
		h.Set("Content-Type", mime)
		part, err := w.CreatePart(h)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(ref); err != nil {
			return nil, "", err
		}
	}

	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, w.FormDataContentType(), nil
}

// errorText 从错误响应里挖出可读信息。
func errorText(raw []byte) string {
	var parsed struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &parsed); err == nil {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return parsed.Error.Message
		}
		if parsed.Message != "" {
			return parsed.Message
		}
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 300 {
		s = s[:300]
	}
	if s == "" {
		s = "(空响应)"
	}
	return s
}
