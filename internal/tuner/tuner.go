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
//   - 优先解析最终 content，兼容只在 reasoning_content 返回完整结论的接口；
//   - 必须 enable_thinking=false，否则思考链把 token 烧光、content 返回空串；
//   - 送整条长图会看不清，只送单格裁切图。
package tuner

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
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

const promptTemplate = `你是手机壳印刷摆放检查员。附图第一张是原图案，后面是同一摆放的多个成品角度。逐张对照，不能只看整体好不好看。

按顺序核对三件事：
1. 原图里的动物/人物头顶、耳朵、脸部轮廓，在成品中是否被镜头吞掉；原图可见的成行文字、字母，在成品中是否藏进镜头下面。头部轮廓和成行文字都是主要内容，不能当作允许遮挡的背景装饰。手指自然遮挡小局部可接受，镜头吞掉头部或文字不可接受。
2. 图案底部若是明显被原画布截断的横边，是否在壳底上方悬空。此类横边应下移接壳底。完整的独立标志或贴纸不强制贴底。
3. 比例、方向、透明区域、壳色、镜头和手部是否保留；不能镜像、拉伸或把透明区域印成白色。
4. 图案若有明显直线截断边，须贴住对应壳边，不能在壳面内部悬空。已有贴边锚点在缩放后仍须保持；自然弧形轮廓不强制贴边。

只要任一角度存在以上主要问题，good=false，problem必须写出具体缺失，例如“猫头被镜头挡住”“上方文字被挡”“底部截边悬空”。不要用“原构图居中”或“自然遮挡”放行。
有透明留白且镜头遮挡时，先用 down 下移；余量不足再用 avoid_cam 等比缩小并下移。明显裁切用 smaller。保留已完整可见的主体。连续满版背景遵守附加实际参数，不为正常背景留白缩小。

只输出完整的一行 JSON：
{"good":true/false,"score":0-100,"problem":"具体问题20字内，没有写无","direction":"none|bigger|smaller|up|down|left|right|avoid_cam","step":0.05~0.3}
good=true 时 direction=none。发现文字或头部被镜头遮挡时不能返回 good=true。step是相对画布的调整幅度，明显问题用0.2，轻微问题用0.05。`

// 合法的 direction 取值集合，模型乱写时用来收敛。
var validDirs = map[string]bool{
	DirNone: true, DirBigger: true, DirSmaller: true,
	DirUp: true, DirDown: true, DirLeft: true, DirRight: true, DirAvoidCame: true,
}

// Critique 让视觉模型评审一张成品图（单格 JPEG），返回调整建议。
//
// 格式不完整时在原时限内重评一次；仍失败则置 Fallback，保留人工复核。
func Critique(cfg Config, jpeg []byte, placement judge.Placement, otherViews ...[]byte) Advice {
	if len(jpeg) == 0 {
		return fallback(fmt.Errorf("没有成品图"))
	}
	text := promptTemplate + "\n" + placement.Description()
	if placement.SourceDataURL != "" {
		text += "\n第一张附图是原图案参考，其余附图才是本轮不同角度的套图成品，只给成品评分。"
	}
	content := []any{map[string]any{"type": "text", "text": text}}
	if placement.SourceDataURL != "" {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": placement.SourceDataURL}})
	}
	content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{
		"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpeg),
	}})
	for _, view := range otherViews {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{
			"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(view),
		}})
	}
	messages := []any{
		map[string]any{"role": "system", "content": "只输出最终评审 JSON 对象，不输出分析、推理或 Markdown。字段必须包含 good(boolean)、score(0至100的整数)、problem(string)、direction(从 none/bigger/smaller/up/down/left/right/avoid_cam 中选一个)、step(number)。必须根据图片评审，不能因输出格式问题推断图片合格。"},
		map[string]any{"role": "user", "content": content},
	}
	body := map[string]any{"model": cfg.Model, "messages": messages, "max_tokens": cfg.MaxTokens, "temperature": cfg.Temperature}
	judge.ApplyThinking(body, cfg.Model, cfg.Thinking)
	judge.ApplyJSONResponse(body, cfg.Model)

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	issue := "模型未返回完整有效的评审 JSON"
	for attempt := 0; attempt < 2; attempt++ {
		payload, err := json.Marshal(body)
		if err != nil {
			return fallback(err)
		}
		url := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return fallback(err)
		}
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := judge.DoVisionRequest(req, timeout)
		if err != nil {
			return fallback(err)
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			return fallback(err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fallback(fmt.Errorf("HTTP %d: %.300s", resp.StatusCode, raw))
		}
		var parsed struct {
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Message      struct {
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
		reply := parsed.Choices[0]
		var adv Advice
		issue = "模型未返回完整有效的评审 JSON"
		if reply.FinishReason == "length" {
			issue = "模型输出达到长度上限，评审结果被截断"
		} else {
			adv = parseAdvice(reply.Message.Content)
			if adv.Direction == "" && strings.TrimSpace(reply.Message.Content) == "" {
				reasoning := strings.TrimSpace(reply.Message.ReasoningContent)
				for _, prefix := range []string{"结论：", "结论:"} {
					reasoning = strings.TrimSpace(strings.TrimPrefix(reasoning, prefix))
				}
				adv = parseAdvice(reasoning)
			}
		}
		if adv.Direction != "" {
			adv.Raw = strings.TrimSpace(reply.Message.Content)
			return adv
		}
		if attempt == 0 {
			// Reassess the same images once; never manufacture advice from partial analysis.
			body["messages"] = append(messages, map[string]any{"role": "user", "content": "上次响应没有完整有效的评审结果。请重新查看所附原图和成品，直接返回最终 JSON，包含全部字段并选择一个合法 direction。不要输出分析、复述要求或从格式错误推断图像质量。"})
		}
	}
	return fallback(fmt.Errorf("%s；格式重试后仍未确认，请人工复核", issue))
}

// parseAdvice 从模型输出里抽出结构化建议。抽不到字段时留零值，由调用方判断。
func parseAdvice(blob string) Advice {
	blob = strings.TrimSpace(blob)
	if strings.HasPrefix(blob, "```") {
		header, content, ok := strings.Cut(blob, "\n")
		if !ok || (header != "```" && !strings.EqualFold(strings.TrimSpace(header), "```json")) || !strings.HasSuffix(content, "```") {
			return Advice{}
		}
		blob = strings.TrimSpace(strings.TrimSuffix(content, "```"))
	}
	var result struct {
		Good      *bool   `json:"good"`
		Score     *int    `json:"score"`
		Problem   string  `json:"problem"`
		Direction string  `json:"direction"`
		Step      float64 `json:"step"`
	}
	if json.Unmarshal([]byte(blob), &result) != nil ||
		result.Score == nil || *result.Score < 0 || *result.Score > 100 ||
		!validDirs[result.Direction] {
		return Advice{}
	}
	adv := Advice{Score: *result.Score, Problem: strings.TrimSpace(result.Problem), Direction: result.Direction, Step: 0.1}
	if result.Step != 0 {
		adv.Step = clampStep(result.Step)
	}
	if result.Good != nil {
		adv.Good = *result.Good
	} else {
		adv.Good = adv.Score >= 85 && adv.Direction == DirNone
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
