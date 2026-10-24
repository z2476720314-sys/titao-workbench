// Package tuner 让视觉模型对「已经套好的成品」做二次评审，
// 决定要不要再迭代一轮、以及下一轮该往哪个方向调参数。
//
// 设计动机（用户要求）：「套图的参数配置全由大模型自行设置微调，
// 怎么适合怎么调，我只看输出的套好的图的效果」。
//
// 与 judge 的分工：
//   - judge 在**多个候选之间**做相对比较，选出最合适的一档；
//   - tuner 在**单张成品上**做绝对评审，指出还差什么、该往哪调。
//
// 实测教训（沿用 judge 的结论）：
//   - 结论可能只出现在 reasoning_content 而 content 为空，两处都要找；
//   - 必须 enable_thinking=false，否则思考链把 token 烧光、content 返回空串；
//   - 送整条长图会看不清，只送单格裁切图。
package tuner

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config 是视觉模型配置（与 judge.Config 同源）。
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

// Advice 是模型对一张成品的评审结论。
type Advice struct {
	// Good 表示这张已经够好，不需要再迭代。
	Good bool `json:"good"`
	// Score 是 0~100 的综合分（饱满度、居中、不压摄像头、不裁切）。
	Score int `json:"score"`
	// Problem 是主要问题的一句话描述。
	Problem string `json:"problem"`
	// Direction 是调整方向，取值见下面常量。
	Direction string `json:"direction"`
	// Step 是调整幅度，0.05~0.3，缺省 0.1。
	Step float64 `json:"step"`
	// Raw 是模型原始输出，便于排查。
	Raw string `json:"-"`
	// Fallback 表示模型判定失败，用的是保守兜底（认为已够好，停止迭代）。
	Fallback bool `json:"-"`
	// Err 是失败原因（Fallback 为 true 时非空）。
	Err error `json:"-"`
}

// 调整方向常量。
const (
	DirNone      = "none"      // 不用调
	DirBigger    = "bigger"    // 图案偏小，放大
	DirSmaller   = "smaller"   // 图案偏大/被裁，缩小
	DirUp        = "up"        // 重心偏下，上移
	DirDown      = "down"      // 重心偏上，下移
	DirLeft      = "left"      // 重心偏右，左移
	DirRight     = "right"     // 重心偏左，右移
	DirAvoidCame = "avoid_cam" // 压到摄像头，需要缩放或位移避让
)

const promptTemplate = `你是手机壳商品图评审。下面这一张是「图案已经套在手机壳上」的成品图（只截取了其中一个格子）。

请判断这张图是否已经达到可直接上架的质量，如果没达到，指出下一轮该往哪个方向调参数。

评审标准：
1. 饱满度：图案要尽量撑满壳面，留白越少越好。留白多会显得空、廉价，这是最常见的扣分项。
2. 不能压摄像头：图案主体不能被摄像头模组遮住。
3. 不能被裁切：图案不能被壳边裁掉可见部分；轻微贴边可接受，明显裁掉不合格。
4. 重心要居中舒服，不要一边倒。
5. 图案是印在壳的背面平面上的，判断比例时不要被拍摄角度误导。

按下面格式只输出一行 JSON，不要任何解释：
{"good": true/false, "score": 0-100, "problem": "主要问题10字内，没有就写无", "direction": "none|bigger|smaller|up|down|left|right|avoid_cam", "step": 0.05~0.3}

字段说明：
- good：这张是否已经够好、可以直接上架。score>=85 时一般填 true。
- direction：下一轮该往哪调。图案偏小用 bigger，偏大或被裁用 smaller，压摄像头用 avoid_cam，重心偏移用 up/down/left/right。
- step：调整幅度，问题明显用 0.2，问题轻微用 0.05。`

var (
	reGood      = regexp.MustCompile(`"good"\s*:\s*(true|false)`)
	reScore     = regexp.MustCompile(`"score"\s*:\s*(\d{1,3})`)
	reProblem   = regexp.MustCompile(`"problem"\s*:\s*"([^"]*)"`)
	reDirection = regexp.MustCompile(`"direction"\s*:\s*"([a-z_]+)"`)
	reStep      = regexp.MustCompile(`"step"\s*:\s*([0-9]*\.?[0-9]+)`)
)

// 合法的 direction 取值集合，模型乱写时用来收敛。
var validDirs = map[string]bool{
	DirNone: true, DirBigger: true, DirSmaller: true,
	DirUp: true, DirDown: true, DirLeft: true, DirRight: true, DirAvoidCame: true,
}

// Critique 让视觉模型评审一张成品图（单格 JPEG），返回调整建议。
//
// 网络或解析失败时返回「已够好」的兜底结论并置 Fallback，
// 这样整批套图不会因为评审挂了而反复空转。
func Critique(cfg Config, jpeg []byte) Advice {
	if len(jpeg) == 0 {
		return fallback(fmt.Errorf("没有成品图"))
	}

	content := []any{
		map[string]any{"type": "text", "text": promptTemplate},
		map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpeg),
			},
		},
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
		return fallback(err)
	}

	url := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fallback(err)
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
		return fallback(err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fallback(fmt.Errorf("HTTP %d: %.300s", resp.StatusCode, raw))
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
		return fallback(fmt.Errorf("解析响应: %w", err))
	}
	if len(parsed.Choices) == 0 {
		return fallback(fmt.Errorf("响应无 choices"))
	}

	msg := parsed.Choices[0].Message
	// 结论可能落在 reasoning_content 里而 content 为空，两处一起找
	blob := msg.Content + "\n" + msg.ReasoningContent

	adv := parseAdvice(blob)
	adv.Raw = strings.TrimSpace(msg.Content)
	if adv.Direction == "" {
		return fallback(fmt.Errorf("模型输出里没有可识别的 direction: %.200s", blob))
	}
	return adv
}

// parseAdvice 从模型输出里抽出结构化建议。抽不到字段时留零值，由调用方判断。
func parseAdvice(blob string) Advice {
	var adv Advice

	if m := reGood.FindStringSubmatch(blob); m != nil {
		adv.Good = m[1] == "true"
	}
	if m := reScore.FindStringSubmatch(blob); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 0 && n <= 100 {
			adv.Score = n
		}
	}
	if m := reProblem.FindStringSubmatch(blob); m != nil {
		adv.Problem = strings.TrimSpace(m[1])
	}
	if m := reDirection.FindStringSubmatch(blob); m != nil {
		if validDirs[m[1]] {
			adv.Direction = m[1]
		}
	}
	if m := reStep.FindStringSubmatch(blob); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			adv.Step = clampStep(v)
		}
	}
	if adv.Step == 0 {
		adv.Step = 0.1
	}
	// 只给了分数没给 good 时，用分数兜底
	if adv.Score > 0 && adv.Score >= 85 && adv.Direction == DirNone {
		adv.Good = true
	}
	return adv
}

// clampStep 把模型给的幅度限制在 0.05~0.3，避免一步跳太远。
func clampStep(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0.1
	}
	if v < 0.05 {
		return 0.05
	}
	if v > 0.3 {
		return 0.3
	}
	return v
}

// fallback 认为「已够好」并停止迭代：宁可少迭代一轮，也不要在评审挂了的时候空转烧钱。
func fallback(err error) Advice {
	return Advice{Good: true, Direction: DirNone, Fallback: true, Err: err}
}

// Plan 根据评审建议算出下一轮要用的参数。
//
// 入参是上一轮选中的实际参数（scale 为相对铺满的倍数，
// offsetX/offsetY 为相对画布宽高的比例偏移）。
// 返回下一轮的参数与「是否有必要再迭代」。
//
// 规则：
//   - Good 或 Direction==none → 不再迭代；
//   - bigger/smaller → 以当前 scale 为中心按 step 展开 3 档；
//   - up/down/left/right → scale 不变，只调 offset；
//   - avoid_cam → 先缩小 step，再按当前重心偏移的反方向微移。
func Plan(adv Advice, scale, offsetX, offsetY float64) (nextScale float64, nextOffX, nextOffY float64, iterate bool) {
	if adv.Good || adv.Direction == DirNone || adv.Direction == "" {
		return scale, offsetX, offsetY, false
	}

	step := clampStep(adv.Step)
	nextScale, nextOffX, nextOffY = scale, offsetX, offsetY

	switch adv.Direction {
	case DirBigger:
		nextScale = scale * (1 + step)
	case DirSmaller:
		nextScale = scale * (1 - step)
	case DirAvoidCame:
		// 压摄像头：先缩一点腾出空间，同时把图案往当前空的一侧推
		nextScale = scale * (1 - step)
		if offsetY >= 0 {
			nextOffY = offsetY - step*0.5
		} else {
			nextOffY = offsetY + step*0.5
		}
	case DirUp:
		nextOffY = offsetY - step
	case DirDown:
		nextOffY = offsetY + step
	case DirLeft:
		nextOffX = offsetX - step
	case DirRight:
		nextOffX = offsetX + step
	default:
		return scale, offsetX, offsetY, false
	}

	nextScale = clampScale(nextScale)
	nextOffX = clampOffset(nextOffX)
	nextOffY = clampOffset(nextOffY)

	// 参数已经顶到边界还让继续调，说明调不动了，直接收工
	if nextScale == scale && nextOffX == offsetX && nextOffY == offsetY {
		return scale, offsetX, offsetY, false
	}
	return nextScale, nextOffX, nextOffY, true
}

// clampScale 限制填充倍数：小于 0.3 太空、大于 2.0 必然裁坏。
func clampScale(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 1.0
	}
	if v < 0.3 {
		return 0.3
	}
	if v > 2.0 {
		return 2.0
	}
	return v
}

// clampOffset 限制偏移在 ±0.3 画布内，再大图案就飞出壳面了。
func clampOffset(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v < -0.3 {
		return -0.3
	}
	if v > 0.3 {
		return 0.3
	}
	return v
}

// Round 描述一轮迭代的结果，供引擎记录日志与界面展示。
type Round struct {
	// Index 是 1-based 轮次。
	Index int
	// Scale / OffsetX / OffsetY 是本轮实际使用的参数。
	Scale   float64
	OffsetX float64
	OffsetY float64
	// Score 是本轮成品的评审分（0 表示没评审）。
	Score int
	// Problem 是本轮评审指出的问题。
	Problem string
	// Good 表示本轮已达标。
	Good bool
}
