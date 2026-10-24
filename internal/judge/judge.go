// Package judge 用视觉模型从多个候选摆放里挑出最合适的一张。
//
// 设计要点（实测教训）：
//   - 必须只送第一格裁切图。整条 4800 长图缩到 640 后每格只剩约 107px，模型看不清、判断趋同。
//   - 结论可能只出现在 reasoning_content 里而 content 为空，两处都要找。
//   - 关思考（enable_thinking=false）否则 token 被思考链烧光、content 返回空串。
package judge

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config 是视觉模型配置。
type Config struct {
	BaseURL      string
	APIKey       string
	Model        string
	MaxTokens    int
	Timeout      time.Duration
	Temperature  float64
	ImageMaxSide int
	Thinking     bool
}

// Candidate 是一个候选。
type Candidate struct {
	// JPEG 是第一格的预览图字节。
	JPEG []byte
	// Scale 是该候选对应的填充倍数，仅用于向模型说明。
	Scale float64
}

// Result 是判定结果。
type Result struct {
	// Pick 是 0-based 下标。
	Pick int
	// Reason 是模型给的理由（可能为空）。
	Reason string
	// Raw 是模型原始输出，便于排查。
	Raw string
	// Fallback 表示模型判定失败、用了兜底档。
	Fallback bool
	// Err 是失败原因（Fallback 为 true 时非空）。
	Err error
}

const promptTemplate = `你是手机壳商品图评审。这是同一个图案套在同一款手机壳上的 %d 张候选图，编号 1 到 %d，各图的图案大小不同。

%s

按下面的标准挑出最好的一张：
1. 优先看「饱满度」：图案要尽量撑满壳面，留白越少越好。留白多会显得空、廉价，这是最常见的扣分项。
2. 图案主体不能被摄像头模组压住。
3. 只有在图案已经明显超出壳边、被裁掉可见部分时才算不合格；轻微贴边是可以接受的，宁可贴边也不要小。
4. 图案在壳面上的重心要居中舒服，不要一边倒。
5. 注意：图案是印在壳的背面平面上的，判断时看「图案 vs 整个壳面」的比例，不要被拍摄角度误导。
6. 若几张都不理想，选最接近「刚好撑满且未被裁切」的那张。

只输出一行 JSON，不要任何解释：
{"pick": <编号>, "reason": "<15字内>"}`

var (
	rePickJSON = regexp.MustCompile(`"pick"\s*:\s*(\d+)`)
	rePickCN   = regexp.MustCompile(`(?:选|第)\s*([1-9]\d?)\s*(?:张|档|个)`)
	reReason   = regexp.MustCompile(`"reason"\s*:\s*"([^"]*)"`)
)

// Choose 让视觉模型从候选中挑一张，返回 0-based 下标。
//
// 网络或解析失败时不返回错误，而是回退到中间偏小的档位并置 Fallback，
// 保证整批套图不中断。
func Choose(cfg Config, cands []Candidate, patternName string) Result {
	if len(cands) == 0 {
		return Result{Pick: 0, Fallback: true, Err: fmt.Errorf("没有候选")}
	}
	if len(cands) == 1 {
		return Result{Pick: 0}
	}

	legend := make([]string, len(cands))
	for i, c := range cands {
		legend[i] = fmt.Sprintf("  第 %d 张：基准的 %.0f%%", i+1, c.Scale*100)
	}
	text := fmt.Sprintf(promptTemplate, len(cands), len(cands), strings.Join(legend, "\n"))

	content := make([]any, 0, len(cands)+1)
	content = append(content, map[string]any{"type": "text", "text": text})
	for _, c := range cands {
		content = append(content, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(c.JPEG),
			},
		})
	}

	body := map[string]any{
		"model":       cfg.Model,
		"messages":    []any{map[string]any{"role": "user", "content": content}},
		"max_tokens":  cfg.MaxTokens,
		"temperature": cfg.Temperature,
	}
	if !cfg.Thinking {
		body["enable_thinking"] = false
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fallback(len(cands), err)
	}

	url := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fallback(len(cands), err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fallback(len(cands), err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fallback(len(cands), fmt.Errorf("HTTP %d: %.300s", resp.StatusCode, raw))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fallback(len(cands), fmt.Errorf("解析响应: %w", err))
	}
	if len(parsed.Choices) == 0 {
		return fallback(len(cands), fmt.Errorf("响应无 choices"))
	}

	msg := parsed.Choices[0].Message
	// 结论可能落在 reasoning_content 里而 content 为空，两处一起找
	blob := msg.Content + "\n" + msg.ReasoningContent

	res := Result{Raw: strings.TrimSpace(msg.Content)}
	if m := reReason.FindStringSubmatch(blob); m != nil {
		res.Reason = m[1]
	}

	if m := rePickJSON.FindStringSubmatch(blob); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 1 && n <= len(cands) {
			res.Pick = n - 1
			return res
		}
	}
	if m := rePickCN.FindStringSubmatch(blob); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 1 && n <= len(cands) {
			res.Pick = n - 1
			return res
		}
	}
	return fallback(len(cands), fmt.Errorf("模型输出里没有可识别的选择: %.200s", blob))
}

// fallback 选择中间偏小的档位：宁可略小，也不要把图案裁坏。
func fallback(n int, err error) Result {
	return Result{Pick: min(1, n-1), Fallback: true, Err: err}
}
