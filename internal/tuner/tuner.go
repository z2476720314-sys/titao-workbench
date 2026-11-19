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
	BaseURL       string
	APIKey        string
	Model         string
	MaxTokens     int
	Timeout       time.Duration
	Temperature   float64
	ImageMaxSide  int
	Thinking      bool
	BeforeRequest func() error `json:"-"`
}

// Advice 是模型对一张成品的评审结论。
type Advice struct {
	Usage     *judge.TokenUsage `json:"-"`
	Transform *Adjustment       `json:"-"`
	// Good 表示这张已经够好，不需要再迭代。
	Good bool `json:"good"`
	// CameraClear 判断本轮主要内容是否避开镜头，不包含贴边质量。
	CameraClear bool `json:"camera_clear"`
	// Score 是 0~100 的主要内容完整与镜头避让分；直边由程序校验。
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
	// StretchY 是纵向拉伸倍数（1 = 不拉伸）。
	// 用于「左右和底部都已贴边、只差顶部一线」时把顶部补齐：
	// 只改纵向比例，不动横向，避免整体重排。
	StretchY float64
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

// minScale 是缩放硬下限：低于此值成品会缩成壳面上的小贴纸，无论评审给多少分都不接受。
const minScale = 0.55

// maxSingleStepShrink 是单轮允许的最大缩小幅度，避免一轮就把图案缩到无法挽回。
const maxSingleStepShrink = 0.75

// systemPrompt 只放角色、输出契约和硬门槛；判定细则在 user message 的 promptTemplate 里，
// 避免同一套规则在两条消息里各写一遍、彼此矛盾。
const systemPrompt = `你是手机壳套图成品的质检员。只输出一行完整 JSON，不分析、不加 Markdown、不写解释。
字段固定八个：good(boolean)、camera_clear(boolean)、score(0至100整数)、problem(string)、dx(number)、dy(number)、scale_ratio(number)、stretch_y(number)。
判定顺序：先看原图重要文字、主脸、主头部轮廓和主要物体整体是否完整可见，再看这些内容是否被镜头孔覆盖，最后才看构图。
壳边透明框、留白、阴影、模板手指、挂绳、反光都不可作为判不合格的依据。
good=true 必须同时满足：score>=85、camera_clear=true、dx=0、dy=0、scale_ratio=1、stretch_y=1。字段缺失、格式错误、截断输出都不代表合格。
按本轮已渲染的成品判断，不得填写调整后的预期结果。`

const promptTemplate = `【任务】审查本轮成品，判断原图重要内容是否完整、是否被镜头孔切断、是否被照片边缘裁掉，并给出一次可执行的修正。

【判定优先级，从高到低】
1. 内容完整：重要文字（含原图自带的小字、气泡字和已有残字）、主脸、主头部轮廓、占画面主要面积的主体整体（主树、主花等）必须完整可见。上述内容被镜头覆盖或被照片边缘裁掉，即 camera_clear=false 或 good=false。
2. 镜头避让：只有第 1 条的内容被镜头覆盖时才要求避让。镜头孔切掉的是重复背景纹样、连续纹样的边缘延伸、非主体的点缀，不算遮挡，不因此要求缩小或位移。
3. 构图饱满：内容完整且避开镜头后不要再动。不得为了"更饱满""更满铺"放大，也不得为了换高分把整幅缩成底部小标志；但也不必追求铺满，正常留白可接受。

构图饱满是最后一位，永远不能用来否决内容完整性；反过来，内容完整也不能成为放行镜头切断主标题的理由。

【修正规则】
- 初始摆放按原图大小居中出图，不在渲染阶段强制贴底或贴边；贴底、贴边都是你在评审这一轮时给出的修正，不是程序预置的。
- 默认保持当前倍率直接出图；只有第 1 条的内容被遮或被裁，或下面提到的贴边没做到时才调整，且本轮一次给足修正量，不要小步试探拖到下一轮。
- 底部优先：图案底部若是被笔直切线截断（能看到一条水平直线把图案切断），这条切线必须贴住壳底，不得悬空，用纵向位移 dy 往下压。此时不要顺手缩小——缩小会让底部重新离开壳边。
- 左右竖直切线：图案某一侧若有一条竖直直线把图案切断，成品该侧必须贴住壳边，不得留缝；左右两侧都有竖直切线时，横向拉伸到同时贴住两边（只改横向，不动纵向比例）。这个拉伸只能用来对齐边缘，不能把内容推出壳面。
- 没有笔直切线就没有贴边义务：图案边缘是自然弧形、随手绘制的收边、或是完整独立的图形轮廓时，不需要贴壳边，正常留白即可，不要为了"对齐"去硬贴。
- 内容不能越界：任何位移、缩放、拉伸之后，全部有效内容仍须留在印刷域内。贴边与不越界冲突时，先保不越界，报告冲突。
- 顶部收尾补齐：当左右和底部都已经贴边、图案在壳面上占比明显较大，只剩下顶部还差一点点没贴住时，把顶部也纵向拉伸到贴住壳边（只改纵向，不改横向）。这是收尾动作，前提是三条边确实都已贴边且占比够大；只差一线就补齐，不要为了一点缝隙整体重排。
- 优先横移 dx 绕开镜头；横移被边界夹回时改纵移 dy，或小幅缩小。
- 单轮 scale_ratio 不低于 0.55。已是 0.55 仍无法露出重要内容的，判 false、返回 0/0/1 并写明具体冲突，不得改判合格。
- dx、dy、scale_ratio、stretch_y 四项独立给出，不设固定步长、不用预设方向，可同时调整。stretch_y 是纵向拉伸倍数，只有「收尾补齐」时才不等于 1，其余情况一律填 1。
- 比较本轮请求与实际位置：若被印刷域或直边约束夹回（画布未改变），本轮必须换方向或组合调整，不能重复同一组无效参数。

【常见误判，不要犯】
- 壳边透明框、模板手指、挂绳、反光、阴影、照片中印刷留白：都不属于图案问题，不得据此判不合格。
- **贴近不等于遮挡**：只有当镜头孔的实体边缘确实压住笔画或主脸、且能指出哪一笔被切掉时才算遮挡。元素紧挨镜头孔边缘、被镜头外圆环的反光或阴影蹭到、因拍摄角度看起来拥挤，都属于正常排版，判合格。判遮挡前先自问：这个字少了哪一笔？这个脸少了哪一部位？说不出来就不是遮挡。
- 低对比不等于内容丢失，浅色头部先确认真实轮廓再判断。
- 原图本身就截断的轮廓、半身形象、半个字：成品保持一致就算完整，不得推想原图不存在的头顶、身体或笔画。
- 成品黑白区域与原图一致时，不得臆测成透明渲染错误。
- 问题描述必须点名具体哪一字、哪一主体、哪一部位新增缺失，不写"左侧""顶部"这类笼统位置。

【输出】只输出一行完整 JSON，八字段齐全，不分析、不加 Markdown：
{"good":false,"camera_clear":false,"score":0,"problem":"具体问题20字内，无问题写无","dx":0,"dy":0,"scale_ratio":1,"stretch_y":1}
score 为 0 至 100 整数，只按第 1 条的内容完整与镜头避让打分，不按壳框、留白、阴影、手指或模板自带文字打分。dx/dy 在 [-1,1]，scale_ratio 在 [0.55,2]，stretch_y 在 [0.90,1.12]。good=true 必须同时满足 score>=85、camera_clear=true、dx=dy=0、scale_ratio=1、stretch_y=1。`

// 合法的 direction 取值集合，模型乱写时用来收敛。
var validDirs = map[string]bool{
	DirNone: true, DirBigger: true, DirSmaller: true,
	DirUp: true, DirDown: true, DirLeft: true, DirRight: true, DirAvoidCame: true,
}

// Critique 让视觉模型评审一张成品图（单格 JPEG），返回调整建议。
//
// 格式不完整时在原时限内重评一次；仍失败则置 Fallback，保留人工复核。
//
// 每轮只评审一张成品图（调用方用 jobs.pickReviewView 挑最能代表全组的正面图）。
// 不再接受多个角度：全组共用同一套摆放参数，逐格送审等于把同一个问题问多遍。
func Critique(cfg Config, jpeg []byte, placement judge.Placement) Advice {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	return critique(cfg, jpeg, placement, &timeout, false)
}

func critique(cfg Config, jpeg []byte, placement judge.Placement, remaining *time.Duration, cameraOnly bool) Advice {
	if len(jpeg) == 0 {
		return fallback(fmt.Errorf("没有成品图"))
	}
	text := promptTemplate + "\n" + placement.Description()
	if placement.SourceDataURL != "" {
		text += "\n第一张附图是原图案参考，其余附图才是本轮不同角度的套图成品，只给成品评分。"
	}
	content := []any{map[string]any{"type": "text", "text": text}}
	if placement.SourceDataURL != "" {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": placement.SourceDataURL, "detail": "high"}})
	}
	content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{
		"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpeg), "detail": "high",
	}})
	messages := []any{
		map[string]any{"role": "system", "content": systemPrompt},
		map[string]any{"role": "user", "content": content},
	}
	maxTokens := cfg.MaxTokens
	if !cfg.Thinking && (maxTokens <= 0 || maxTokens > 512) {
		maxTokens = 512
	}
	body := map[string]any{"stream": true, "stream_options": map[string]bool{"include_usage": true}, "model": cfg.Model, "messages": messages, "max_tokens": maxTokens, "temperature": cfg.Temperature}
	judge.ApplyThinking(body, cfg.Model, cfg.Thinking)
	judge.ApplyJSONResponse(body, cfg.Model)

	issue := "模型未返回完整有效的评审 JSON"
	for attempt := 0; attempt < 2; attempt++ {
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
		if *remaining <= 0 {
			return fallback(context.DeadlineExceeded)
		}
		var waited time.Duration
		beforeRequest := cfg.BeforeRequest
		if beforeRequest != nil {
			beforeRequest = func() error {
				started := time.Now()
				err := cfg.BeforeRequest()
				waited += time.Since(started)
				return err
			}
		}
		started := time.Now()
		resp, err := judge.DoVisionRequest(req, *remaining, beforeRequest)
		*remaining -= time.Since(started) - waited
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
			_, _, _, _, iterate := Plan(adv, 1, 0, 0, 1)
			if attempt == 0 && !adv.Good && !iterate && !(cameraOnly && adv.CameraClear) {
				body["messages"] = append(messages,
					map[string]any{"role": "assistant", "content": adv.Raw},
					map[string]any{"role": "user", "content": "本轮不合格但你未给出任何调整。重新对照原图和成品，按实际壳面参数给出一次可执行的联合缩放与位移：优先解决主字主脸的镜头遮挡；遮挡已避开时，若图案有笔直切线（横向或竖直的直线切断）却没贴住对应壳边，就用位移把它贴上去，左右两侧都有竖直切线时横向拉伸到同时贴边，同时保证内容不越界。确实没有安全解才返回0/0/1，并说明具体冲突；不要把没有尝试的方向当作无解，也不要改判合格来回避调整。只输出八字段最终JSON。"})
				continue
			}
			// 逐格复核已废弃：现在每轮只送一张最能代表全组的正面图（见 jobs.pickReviewView），
			// 没有 otherViews。留着旧分支只会让后续维护者以为还在逐格复核。
			return adv
		}
		if attempt == 0 {
			// Reassess the same images once; never manufacture advice from partial analysis.
			body["messages"] = append(messages, map[string]any{"role": "user", "content": "重新核对原图与本轮成品，只返回完整最终JSON：good、camera_clear、score、problem、dx、dy、scale_ratio、stretch_y。camera_clear判断本轮主要内容是否已避镜头，不含贴边或调整预期。字段齐全且数值合法；合格必须score>=85、camera_clear=true及0/0/1/1。不分析、不借格式错误判断合格。"})
		}
	}
	return fallback(fmt.Errorf("%s；格式重试后仍未确认，请人工复核", issue))
}

// parseAdvice 从模型输出里抽出结构化建议。抽不到字段时留零值，由调用方判断。
// flexibleScore 兼容模型把 score 写成小数的情况。
// 有的模型（尤其在关闭思考模式时）会返回 0.96 而不是 96，
// 用 *int 严格解析会直接报错，整轮评审被当成「未完成」而白白失败。
// 规则：0~1 之间的小数按百分制换算，其余按数值本身取整。
type flexibleScore float64

func (s *flexibleScore) UnmarshalJSON(b []byte) error {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	v, ok := raw.(float64)
	if !ok {
		// 字符串、布尔、null 都不接受：宁可判「未完成」，也不猜模型的意思。
		return fmt.Errorf("score 必须是数字，实际 %T", raw)
	}
	if v > 0 && v <= 1 {
		v *= 100
	}
	*s = flexibleScore(math.Round(v))
	return nil
}

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
		Score       *flexibleScore `json:"score"`
		Problem     *string       `json:"problem"`
		DX          *float64      `json:"dx"`
		DY          *float64      `json:"dy"`
		ScaleRatio  *float64      `json:"scale_ratio"`
		StretchY    *float64      `json:"stretch_y"`
		Direction   string        `json:"direction"`
		Step        float64       `json:"step"`
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(blob), &result) != nil || json.Unmarshal([]byte(blob), &fields) != nil ||
		result.Score == nil || *result.Score < 0 || *result.Score > 100 {
		return Advice{}
	}
	if result.Good != nil && *result.Good && *result.Score < 85 {
		return Advice{}
	}
	if fields["dx"] != nil || fields["dy"] != nil || fields["scale_ratio"] != nil || fields["stretch_y"] != nil {
		if result.Good == nil || result.CameraClear == nil || result.Problem == nil || result.DX == nil || result.DY == nil || result.ScaleRatio == nil || fields["direction"] != nil || fields["step"] != nil {
			return Advice{}
		}
		stretch := 1.0
		if result.StretchY != nil {
			stretch = *result.StretchY
		}
		adjustment := Adjustment{DX: *result.DX, DY: *result.DY, ScaleRatio: *result.ScaleRatio, StretchY: stretch}
		if !validAdjustment(adjustment) || (*result.Good && (!*result.CameraClear || adjustment.DX != 0 || adjustment.DY != 0 || adjustment.ScaleRatio != 1 || adjustment.StretchY != 1)) {
			return Advice{}
		}
		return Advice{Good: *result.Good, CameraClear: *result.CameraClear, Score: int(*result.Score), Problem: strings.TrimSpace(*result.Problem), Direction: DirTransform, Transform: &adjustment}
	}
	if !validDirs[result.Direction] || (result.Good != nil && *result.Good && result.Direction != DirNone) {
		return Advice{}
	}
	adv := Advice{Score: int(*result.Score), Direction: result.Direction, Step: 0.1}
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
	return finite(a.DX, a.DY, a.ScaleRatio, a.StretchY) && math.Abs(a.DX) <= 1 && math.Abs(a.DY) <= 1 &&
		a.ScaleRatio >= 0.05 && a.ScaleRatio <= 2 &&
		// 纵向拉伸只用于补齐顶部一线，区间与 psx 的 ValidStretch 保持一致：
		// 两处各写一个魔数曾在 v0.3.22 造成「模型给了 1.05、画布直接构建失败」。
		a.StretchY >= psx.MinVerticalStretch && a.StretchY <= psx.MaxVerticalStretch
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
// Plan 根据评审建议算出下一轮要用的参数。
//
// 入参是上一轮选中的实际参数（scale 为相对铺满的倍数，
// offsetX/offsetY 为相对画布宽高的比例偏移，stretchY 为本轮纵向比例）。
// 返回下一轮的参数、下一轮的纵向比例，以及「是否有必要再迭代」。
func Plan(adv Advice, scale, offsetX, offsetY, stretchY float64) (nextScale float64, nextOffX, nextOffY, nextStretchY float64, iterate bool) {
	if !finite(stretchY) || stretchY <= 0 {
		stretchY = 1
	}
	if adv.Good || adv.Fallback || !finite(scale, offsetX, offsetY) || scale <= 0 {
		return scale, offsetX, offsetY, stretchY, false
	}
	if adv.Transform != nil {
		if !validAdjustment(*adv.Transform) {
			return scale, offsetX, offsetY, stretchY, false
		}
		nextScale = scale * adv.Transform.ScaleRatio
		if nextScale < scale*maxSingleStepShrink {
			nextScale = scale * maxSingleStepShrink
		}
		nextScale = clampScaleFloor(nextScale, scale)
		nextOffX, nextOffY = offsetX+adv.Transform.DX, offsetY+adv.Transform.DY
		nextStretchY = stretchY
		if adv.Transform.StretchY > 0 && adv.Transform.StretchY != 1 {
			nextStretchY = stretchY * adv.Transform.StretchY
			// 累积拉伸也要有界，避免多轮叠加把主体拉变形。
			nextStretchY = math.Max(psx.MinVerticalStretch, math.Min(psx.MaxVerticalStretch, nextStretchY))
		}
		if !finite(nextScale, nextOffX, nextOffY, nextStretchY) || nextScale <= 0 {
			return scale, offsetX, offsetY, stretchY, false
		}
		return nextScale, nextOffX, nextOffY, nextStretchY,
			nextScale != scale || nextOffX != offsetX || nextOffY != offsetY || nextStretchY != stretchY
	}
	if adv.Good || adv.Direction == DirNone || adv.Direction == "" {
		return scale, offsetX, offsetY, stretchY, false
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
		nextOffY = offsetY + step*0.35
	case DirUp:
		nextOffY = offsetY - step
	case DirDown:
		nextOffY = offsetY + step
	case DirLeft:
		nextOffX = offsetX - step
	case DirRight:
		nextOffX = offsetX + step
	default:
		return scale, offsetX, offsetY, stretchY, false
	}

	nextScale = clampScale(nextScale)
	nextOffX = clampOffset(nextOffX)
	nextOffY = clampOffset(nextOffY)

	// 参数已经顶到边界还让继续调，说明调不动了，直接收工
	if nextScale == scale && nextOffX == offsetX && nextOffY == offsetY {
		return scale, offsetX, offsetY, stretchY, false
	}
	return nextScale, nextOffX, nextOffY, stretchY, true
}

// clampScale 限制填充倍数：小于 0.3 太空、大于 2.0 必然裁坏。
func clampScale(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 1.0
	}
	if v < minScale {
		return minScale
	}
	if v > 2.0 {
		return 2.0
	}
	return v
}

// clampScaleFloor 在保持基准缩放的相对下限的前提下夹回倍率：
// 基准已经小于 minScale 时不再把它放大，只禁止继续缩小。
func clampScaleFloor(v, base float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return base
	}
	floor := math.Min(base, minScale)
	if v < floor {
		return floor
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
