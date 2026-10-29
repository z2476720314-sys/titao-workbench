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

	"titao/internal/judge"
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
	// Fallback 表示模型判定失败，停止迭代但不视为合格。
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

只检查主要内容是否完整、居中且无明显遮挡；满足就返回 good=true、direction=none，结束微调。不要为了更饱满或细微视觉偏好调整。

评审标准：
1. 对照原图案保护主体、比例、方向、透明区域与细节。透明区域在参考中铺白底，不代表要印白底。独立文字、数字、标志或悬浮贴纸保持居中，不能仅因底部空隙下移；只有原图底部有应衔接壳底的连续背景、地面或被画布截断的实质内容，而且成品出现悬空截边时，才允许小幅 down 贴合壳底，不改变大小、不裁切主体。
2. 默认按实际 fit、scale=1 和零偏移铺放。原画布中的透明区域、半透明像素、主体位置和装饰分布都是原构图，不构成偏小或偏移；主要内容完整且无明显遮挡时直接通过，不能因看到透明而缩小整图。
3. 镜头、边框、手指自然遮挡背景装饰是正常的，不能为躲开整个镜头区而把整幅图缩小。关键人物脸部或文字被遮挡才使用 avoid_cam。
4. 壳体方向倒置时图案也应倒置，不得镜像或非等比拉伸；保持原壳色、几何与光影。
5. 图案必须贴合印刷背板，不能覆盖镜头、壳边或手指。根据实际 scale、fit 和偏移判断下一步是否能执行。

按下面格式只输出一行 JSON，不要任何解释：
{"good": true/false, "score": 0-100, "problem": "主要问题10字内，没有就写无", "direction": "none|bigger|smaller|up|down|left|right|avoid_cam", "step": 0.05~0.3}

字段说明：
- good：这张是否已经够好、可以直接上架。score>=85 时一般填 true。
- direction：只有主要人物、文字或标志明显裁切、遮挡、方向或位置错误才调整；关键主体被镜头遮挡用 avoid_cam，明显裁切用 smaller，主要内容明显偏离中心才用 up/down/left/right。底部确有需要接壳底的内容且悬空时允许小幅 down；正常留白或细微不对称一律 good=true、direction=none。
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
// 网络或解析失败时停止迭代并置 Fallback，结果仍需人工复核。
func Critique(cfg Config, jpeg []byte, placement judge.Placement) Advice {
	if len(jpeg) == 0 {
		return fallback(fmt.Errorf("没有成品图"))
	}

	text := promptTemplate + "\n" + placement.Description()
	if placement.SourceDataURL != "" {
		text += "\n第一张附图是原图案参考，第二张才是本轮套图成品，只给成品评分。"
	}
	content := []any{map[string]any{"type": "text", "text": text}}
	if placement.SourceDataURL != "" {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": placement.SourceDataURL}})
	}
	content = append(content, map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpeg),
		},
	})

	body := map[string]any{
		"model":       cfg.Model,
		"messages":    []any{map[string]any{"role": "user", "content": content}},
		"max_tokens":  cfg.MaxTokens,
		"temperature": cfg.Temperature,
	}
	judge.ApplyThinking(body, cfg.Model, cfg.Thinking)
	judge.ApplyJSONResponse(body, cfg.Model)

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
	resp, err := judge.DoVisionRequest(req, timeout)
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
	if !reGood.MatchString(blob) && adv.Score >= 85 && adv.Direction == DirNone {
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

// fallback 停止迭代，保留未评审状态。
func fallback(err error) Advice {
	return Advice{Direction: DirNone, Fallback: true, Err: err}
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
		// 镜头在智能对象画布顶部，避让必须下移，不能反向推入镜头区。
		nextScale = scale * (1 - step)
		nextOffY = offsetY + step*0.5
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
