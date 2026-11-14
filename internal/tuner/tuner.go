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
	"titao/internal/psx"
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
	Usage     *judge.TokenUsage `json:"-"`
	Transform *Adjustment       `json:"-"`
	// Good 表示这张已经够好，不需要再迭代。
	Good bool `json:"good"`
	// CameraClear 仅判断本轮文字、主脸及主头部是否避开镜头，不包含贴边质量。
	CameraClear bool `json:"camera_clear"`
	// Score 是 0~100 的综合分（内容完整、主字主脸避镜头、截断边贴边）。
	Score int `json:"score"`
	// Problem 是主要问题的一句话描述。
	Problem string `json:"problem"`
	// Direction 为旧协议方向或新协议派生的 DirTransform。
	Direction string `json:"direction"`
	// Step 仅用于旧协议，0.05~0.3，缺省 0.1。
	Step float64 `json:"step"`
	// Raw 是模型原始输出，便于排查。
	Raw string `json:"-"`
	// Fallback 表示模型判定失败，停止迭代但不视为合格。
	Fallback bool `json:"-"`
	// Err 是失败原因（Fallback 为 true 时非空）。
	Err error `json:"-"`
}

// Adjustment 是相对本轮实际壳面画布的位移增量与实际倍率的乘数。
type Adjustment struct {
	DX, DY, ScaleRatio float64
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
	DirTransform = "transform" // 同轮联合位移与等比缩放
)

const promptTemplate = `检查同一摆放的各角度成品；附原图时对照其实际内容，不给原图评分。
只可裁掉全透明空白；浅色、半透明的实际内容也必须留在壳边与画面内，不能镜像、拉伸或填白透明区域。原图自带的文字（含小字、气泡文字及已有残字）、主脸及主头部轮廓避开镜头，不能只检查最大标题。先确认浅色头部真实轮廓，别把低对比误判成缺失；成品黑白区域若与原图一致，不得猜测成透明渲染错误。重复纹样的背景元素可被镜头自然遮挡，不把每个小纹样当主角。明显直线截断边须贴对应壳边，独立完整贴纸不强制贴底；本款说明若明确用户接受整幅留白，则按该例外，不强制贴壳边。
只因镜头区域遮挡主字主脸而避让；模板里的手指、挂绳与反光是原场景，不因这些遮挡反复缩小。保全内容并避开镜头后，保持尽可能饱满的构图，不能将整幅缩成底部小标志来换高分；物理透明壳边框不算印刷背板留白。
按本轮实际已应用参数、被遮深度和安全余量计算一次联合修正：dx/dy是壳面画布宽/高比例的增量，正右/下；scale_ratio乘本轮实际倍率。主字或主头部被遮时，移到镜头最下沿以下并留余量，别只修正一小段仍受遮挡。比较本轮请求与实际位置，若移动被夹回，必须同时充分缩小释放所需位移，不能反复只移动。可同时横移、纵移、等比缩放，不固定20%，不用预设方向或最小步长；实际内容越界时可缩小，贴边锚点仍须保留。
camera_clear只判断本轮原图文字、主脸及主头部是否已避开镜头，不包含截边贴合；不能填调整后的预期结果。
只输出一行完整JSON，七字段齐全，不分析：
{"good":false,"camera_clear":false,"score":0,"problem":"具体问题20字内，无问题写无","dx":0,"dy":0,"scale_ratio":1}
score为0至100整数；dx/dy在[-1,1]，scale_ratio在[0.05,2]。good=true必须camera_clear=true、dx=dy=0且scale_ratio=1；false可联合调整三项。若安全约束冲突无法修正，false并返回0/0/1及简短原因，不得当作合格。`

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
		map[string]any{"role": "system", "content": "只输出最终JSON，不分析或Markdown。七字段齐全：good(boolean)、camera_clear(boolean)、score(0至100整数)、problem(string)、dx(number)、dy(number)、scale_ratio(number)。camera_clear仅判断本轮文字、主脸主头部是否避镜头，不包含贴边。dx/dy在[-1,1]，scale_ratio在[0.05,2]；good=true必须camera_clear=true及0/0/1。根据本轮成品判断，不填调整预期，格式错误不代表合格。"},
		map[string]any{"role": "user", "content": content},
	}
	maxTokens := cfg.MaxTokens
	if !cfg.Thinking && (maxTokens <= 0 || maxTokens > 512) {
		maxTokens = 512
	}
	body := map[string]any{"stream": true, "stream_options": map[string]bool{"include_usage": true}, "model": cfg.Model, "messages": messages, "max_tokens": maxTokens, "temperature": cfg.Temperature}
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
			Usage   *judge.TokenUsage `json:"usage"`
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
			adv.Usage = parsed.Usage
			adv.Raw = strings.TrimSpace(reply.Message.Content)
			return adv
		}
		if attempt == 0 {
			// Reassess the same images once; never manufacture advice from partial analysis.
			body["messages"] = append(messages, map[string]any{"role": "user", "content": "重新核对原图与本轮成品，只返回完整最终JSON：good、camera_clear、score、problem、dx、dy、scale_ratio。camera_clear只判断本轮文字、主脸主头部是否已避镜头，不含贴边或调整预期。字段齐全且数值合法；合格必须camera_clear=true及0/0/1。不分析、不借格式错误判断合格。"})
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
		Good        *bool    `json:"good"`
		CameraClear *bool    `json:"camera_clear"`
		Score       *int     `json:"score"`
		Problem     *string  `json:"problem"`
		DX          *float64 `json:"dx"`
		DY          *float64 `json:"dy"`
		ScaleRatio  *float64 `json:"scale_ratio"`
		Direction   string   `json:"direction"`
		Step        float64  `json:"step"`
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(blob), &result) != nil || json.Unmarshal([]byte(blob), &fields) != nil ||
		result.Score == nil || *result.Score < 0 || *result.Score > 100 {
		return Advice{}
	}
	if fields["dx"] != nil || fields["dy"] != nil || fields["scale_ratio"] != nil {
		if result.Good == nil || result.CameraClear == nil || result.Problem == nil || result.DX == nil || result.DY == nil || result.ScaleRatio == nil || fields["direction"] != nil || fields["step"] != nil {
			return Advice{}
		}
		adjustment := Adjustment{DX: *result.DX, DY: *result.DY, ScaleRatio: *result.ScaleRatio}
		if !validAdjustment(adjustment) || (*result.Good && (!*result.CameraClear || adjustment.DX != 0 || adjustment.DY != 0 || adjustment.ScaleRatio != 1)) {
			return Advice{}
		}
		return Advice{Good: *result.Good, CameraClear: *result.CameraClear, Score: *result.Score, Problem: strings.TrimSpace(*result.Problem), Direction: DirTransform, Transform: &adjustment}
	}
	if !validDirs[result.Direction] || (result.Good != nil && *result.Good && result.Direction != DirNone) {
		return Advice{}
	}
	adv := Advice{Score: *result.Score, Direction: result.Direction, Step: 0.1}
	if result.Problem != nil {
		adv.Problem = strings.TrimSpace(*result.Problem)
	}
	if result.Step != 0 {
		adv.Step = clampStep(result.Step)
	}
	if result.Good != nil {
		adv.Good = *result.Good
	} else {
		adv.Good = adv.Score >= 85 && adv.Direction == DirNone
	}
	adv.CameraClear = adv.Good
	if result.CameraClear != nil {
		adv.CameraClear = *result.CameraClear
	} else if fields["camera_clear"] != nil {
		return Advice{}
	}
	if adv.Good && !adv.CameraClear {
		return Advice{}
	}
	return adv
}

func finite(values ...float64) bool {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func validAdjustment(a Adjustment) bool {
	return finite(a.DX, a.DY, a.ScaleRatio) && math.Abs(a.DX) <= 1 && math.Abs(a.DY) <= 1 && a.ScaleRatio >= 0.05 && a.ScaleRatio <= 2
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
	if adv.Good || adv.Fallback || !finite(scale, offsetX, offsetY) || scale <= 0 {
		return scale, offsetX, offsetY, false
	}
	if adv.Transform != nil {
		if !validAdjustment(*adv.Transform) {
			return scale, offsetX, offsetY, false
		}
		nextScale = scale * adv.Transform.ScaleRatio
		nextOffX, nextOffY = offsetX+adv.Transform.DX, offsetY+adv.Transform.DY
		if !finite(nextScale, nextOffX, nextOffY) || nextScale <= 0 {
			return scale, offsetX, offsetY, false
		}
		return nextScale, nextOffX, nextOffY, nextScale != scale || nextOffX != offsetX || nextOffY != offsetY
	}
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
	Applied []psx.AppliedPlacement `json:"applied,omitempty"`
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
	Good        bool
	CameraClear bool `json:"camera_clear"`
}
