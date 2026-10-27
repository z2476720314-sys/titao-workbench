package genimg

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"titao/internal/judge"
)

// VisionConfig 是视觉模型配置（复用 cfg 里已有的视觉模型通道）。
type VisionConfig struct {
	BaseURL     string
	APIKey      string
	Model       string
	MaxTokens   int
	Temperature float64
	Thinking    bool
	Timeout     time.Duration
	// Endpoint 允许测试指向假服务器；为空时用 BaseURL + /chat/completions。
	Endpoint string
}

func (v VisionConfig) normalize() VisionConfig {
	if v.MaxTokens <= 0 {
		v.MaxTokens = 4000
	}
	if v.Timeout <= 0 {
		v.Timeout = 180 * time.Second
	}
	return v
}

// 提示词生成的两套模板。
//
// 强约束部分刻意写死、不让模型自由发挥：用户的原话是「手机壳的任何内容都要和
// 参考图一样，因为必须是同一款手机壳」，而模型最容易犯的错就是把图案重画一遍。
const (
	promptHandheld = `你是手机壳商品图摄影师。下面这张图是一张已经做好的手机壳商品图（手机壳 + 背景）。

请为生图模型写一段**英文**提示词，生成一张**新的手持图**：一个人用手拿着这款手机壳，画面是真实的生活场景。

必须严格遵守：
1. 手机壳本身必须与参考图**完全一致**：图案内容、线条、颜色、比例、位置、边缘细节、摄像头开孔的位置与形状、壳的材质与光泽，一律不得改动、不得重画、不得简化、不得美化。
2. 只改变：持握的手、手指姿态与角度、手机壳的倾斜角度、拍摄视角、背景环境、光线氛围。
3. 新的背景、手势、光线必须与参考图**明显不同**，不能照抄原图的背景和构图。
4. 画面比例 1:1 正方形，商品级质感，清晰锐利。
5. 不要在画面里出现文字、水印、logo、价签。

只输出提示词正文，一行或多行纯文本，不要解释、不要加标题、不要用引号包裹、不要输出 JSON。`

	promptBackground = `你是手机壳商品图摄影师。下面这张图是一张已经做好的手机壳商品图（手机壳 + 背景）。

请为生图模型写一段**英文**提示词，生成一张**换了背景的新图**：手机壳作为静物摆放在一个全新的背景环境里。

必须严格遵守：
1. 手机壳本身必须与参考图**完全一致**：图案内容、线条、颜色、比例、位置、边缘细节、摄像头开孔的位置与形状、壳的材质与光泽，一律不得改动、不得重画、不得简化、不得美化。
2. 只改变：背景环境、道具、台面材质、光线氛围、拍摄角度。不要出现手。
3. 新的背景必须与参考图**明显不同**，不能照抄原图的背景和配色。
4. 画面比例 1:1 正方形，商品级质感，清晰锐利。
5. 不要在画面里出现文字、水印、logo、价签。

只输出提示词正文，一行或多行纯文本，不要解释、不要加标题、不要用引号包裹、不要输出 JSON。`

	// promptRetrySuffix 是一致性校验失败后的加强约束。
	promptRetrySuffix = `

【重要补充】上一次生成的结果被判定为「手机壳与参考图不一致」。这一次请把约束写得更死：
在提示词开头明确写出 "Keep the phone case pixel-identical to the reference image"，并逐项点名参考图上的图案元素、配色、摄像头开孔形状，要求生图模型原样保留，只改变背景/手持。`
)

// PromptFor 返回某模式的基础提示词（不含参考图），供测试与排查使用。
func PromptFor(mode Mode) (string, error) {
	switch mode {
	case ModeHandheld:
		return promptHandheld, nil
	case ModeBackground:
		return promptBackground, nil
	default:
		return "", fmt.Errorf("未知生图模式 %q", mode)
	}
}

// BuildPrompt 让视觉模型看图后生成生图提示词。
//
// refJPEG 是参考图（建议已缩到 1024 内的 JPEG）。retry=true 时追加
// 「上次不一致」的加强约束，用于一致性校验失败后的重试。
func BuildPrompt(ctx context.Context, vc VisionConfig, refJPEG []byte, mode Mode, retry bool) (string, error) {
	base, err := PromptFor(mode)
	if err != nil {
		return "", err
	}
	if retry {
		base += promptRetrySuffix
	}
	if len(refJPEG) == 0 {
		return "", fmt.Errorf("缺少参考图")
	}

	blob, err := chat(ctx, vc, base, [][]byte{refJPEG})
	if err != nil {
		return "", err
	}
	out := cleanPrompt(blob)
	if out == "" {
		return "", fmt.Errorf("视觉模型没有产出提示词（原始输出: %.200s）", blob)
	}
	return out, nil
}

// cleanPrompt 去掉模型爱加的外壳：代码块围栏、包裹引号、前缀标签。
func cleanPrompt(s string) string {
	s = strings.TrimSpace(s)
	// 去掉 ```...``` 围栏
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	// 去掉 "Prompt:" / "提示词：" 这类前缀
	for _, prefix := range []string{"Prompt:", "prompt:", "PROMPT:", "提示词：", "提示词:", "英文提示词：", "英文提示词:"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimSpace(s[len(prefix):])
			break
		}
	}
	// 去掉整体包裹的一对引号（中文引号是多字节，必须按字符串比）
	pairs := [][2]string{{`"`, `"`}, {`'`, `'`}, {`“`, `”`}, {`「`, `」`}}
	for _, p := range pairs {
		if len(s) > len(p[0])+len(p[1]) && strings.HasPrefix(s, p[0]) && strings.HasSuffix(s, p[1]) {
			s = strings.TrimSpace(s[len(p[0]) : len(s)-len(p[1])])
			break
		}
	}
	return s
}

// ConsistencyResult 是一致性校验结论。
type ConsistencyResult struct {
	// Same 表示手机壳是否与参考图同一款。
	Same bool
	// Reason 是模型给的短理由。
	Reason string
	// Diff 是差异描述（Same=false 时有意义）。
	Diff string
	// Raw 是模型原始输出，便于排查。
	Raw string
	// Parsed 表示是否成功解析出 JSON。解析失败时调用方必须当作不一致处理。
	Parsed bool
	// Err 是解析或网络失败原因。
	Err error
}

const consistencyPrompt = `你是手机壳商品图质检员。第一张图是**参考图**（正确的手机壳），第二张图是**待检图**（AI 生成的新图）。

判断待检图里的手机壳与参考图是不是**同一款**。判据：
1. 图案内容、线条、颜色、比例、在壳面上的位置是否一致。
2. 摄像头开孔的位置、数量、形状是否一致。
3. 壳的材质、光泽、边缘处理是否一致。
4. 背景、手持姿态、光线、拍摄角度**不算差异**，不要因此判不一致。
5. 只要图案被重画、变形、简化、换色、裁掉一部分，就判不一致。

只输出一行 JSON，不要任何解释：
{"same": true 或 false, "reason": "<15字内>", "diff": "<不一致时描述差异，一致时留空>"}`

var (
	reSameJSON = regexp.MustCompile(`"same"\s*:\s*(true|false)`)
	reSameCN   = regexp.MustCompile(`(一致|相同|同一款)`)
	reDiffJSON = regexp.MustCompile(`"diff"\s*:\s*"([^"]*)"`)
	reReasonJS = regexp.MustCompile(`"reason"\s*:\s*"([^"]*)"`)
)

// CheckConsistency 让视觉模型对比参考图与生成图。
//
// 解析失败一律按「不一致」处理（Same=false + Err 非空），绝不静默通过：
// 用户要的是「必须是同一款手机壳」，误放行比误拦代价高得多。
func CheckConsistency(ctx context.Context, vc VisionConfig, refJPEG, genJPEG []byte) ConsistencyResult {
	if len(refJPEG) == 0 || len(genJPEG) == 0 {
		return ConsistencyResult{Err: fmt.Errorf("参考图或生成图为空"), Raw: ""}
	}
	blob, err := chat(ctx, vc, consistencyPrompt, [][]byte{refJPEG, genJPEG})
	if err != nil {
		return ConsistencyResult{Err: err}
	}

	res := ConsistencyResult{Raw: strings.TrimSpace(blob)}
	if m := reReasonJS.FindStringSubmatch(blob); m != nil {
		res.Reason = strings.TrimSpace(m[1])
	}
	if m := reDiffJSON.FindStringSubmatch(blob); m != nil {
		res.Diff = strings.TrimSpace(m[1])
	}

	if m := reSameJSON.FindStringSubmatch(blob); m != nil {
		res.Parsed = true
		res.Same = m[1] == "true"
		return res
	}
	// 兜底：模型可能漏掉 JSON 只写自然语言
	if reSameCN.MatchString(blob) {
		res.Parsed = true
		// 出现「不一致」时 reSameCN 也会命中「一致」，必须先排除否定词
		if strings.Contains(blob, "不一致") || strings.Contains(blob, "不同") {
			res.Same = false
		} else {
			res.Same = true
		}
		return res
	}
	res.Err = fmt.Errorf("模型输出里没有可识别的 same 结论: %.200s", blob)
	return res
}

// chat 发一次多图 + 文本的对话请求，返回 content 与 reasoning_content 合并后的文本。
//
// 与 judge 包同一套做法：
//   - 关思考时显式传 enable_thinking=false，否则 token 被思考链烧光
//   - 结论可能只落在 reasoning_content 而 content 为空，两处一起找
func chat(ctx context.Context, vc VisionConfig, text string, images [][]byte) (string, error) {
	vc = vc.normalize()
	if strings.TrimSpace(vc.APIKey) == "" {
		return "", fmt.Errorf("未配置视觉模型 API Key（config.json 的 api_key）")
	}
	if vc.Model == "" {
		return "", fmt.Errorf("未配置视觉模型（config.json 的 model）")
	}

	content := make([]any, 0, len(images)+1)
	content = append(content, map[string]any{"type": "text", "text": text})
	for _, img := range images {
		content = append(content, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(img),
			},
		})
	}

	body := map[string]any{
		"model":       vc.Model,
		"messages":    []any{map[string]any{"role": "user", "content": content}},
		"max_tokens":  vc.MaxTokens,
		"temperature": vc.Temperature,
	}
	judge.ApplyThinking(body, vc.Model, vc.Thinking)

	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	endpoint := vc.Endpoint
	if endpoint == "" {
		endpoint = strings.TrimRight(vc.BaseURL, "/") + "/chat/completions"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+vc.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := judge.DoVisionRequest(req, vc.Timeout)
	if err != nil {
		return "", fmt.Errorf("请求视觉模型: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("视觉模型 HTTP %d: %s", resp.StatusCode, errorText(raw))
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
		return "", fmt.Errorf("解析视觉模型响应: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("视觉模型响应无 choices: %.200s", raw)
	}
	msg := parsed.Choices[0].Message
	// 结论可能落在 reasoning_content 里而 content 为空，两处一起找
	blob := strings.TrimSpace(msg.Content + "\n" + msg.ReasoningContent)
	if blob == "" {
		return "", fmt.Errorf("视觉模型返回空内容")
	}
	return blob, nil
}
