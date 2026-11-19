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

	"titao/internal/psx"
)

// Config 是视觉模型配置。
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

// Placement 把实际渲染约束与原图一起交给选档和评审。
type Placement struct {
	DiagnosticViews         bool
	FrameVerified           bool
	ViewPlacements          []psx.AppliedPlacement
	AppliedSummary          string
	FillTop                 bool
	AlignBottom             bool
	FullBleed               bool
	TransparentPadding      bool
	AnchorX, AnchorY        int
	SourceDataURL           string
	SourceAlphaGuide        bool
	DiagnosticFirstView     bool
	Fit                     string
	NoCrop                  bool
	KeepWhole               bool
	Scale, OffsetX, OffsetY float64
}

const PrimaryContentRule = "先按原图的视觉层级区分主体与陪衬。主要内容包括重要文字、人物、动物及明显占画面主要面积的主要物体整体（如主树、主花），不只检查眼睛和头部。镜头遮挡主体显著部分或主物体的关键轮廓时必须判不合格。背景陪衬即使不重复，例如散落的小食品、独立小星形、小花或斑点，也允许自然被镜头遮挡，不能把所有非重复物体都当作主角；但主树顶上的星星等主体附属部分仍随主体一起保护。不得把背景图标的缺口误当成旁边文字笔画被挡。"

func (p Placement) Description() string {
	scale := "scale见各候选编号的实际倍率"
	if p.Scale > 0 {
		scale = fmt.Sprintf("scale=%.6g", p.Scale)
	}
	text := fmt.Sprintf("实际已应用参数：fit=%s，no_crop=%t，%s，offset_x=%.6g，offset_y=%.6g。零偏移为基准居中；dx/dy是相对本轮实际壳面画布宽/高的位移增量，正x向右、正y向下，scale_ratio乘当前实际倍率。按被遮深度与安全余量计算联合调整，不固定20%%，不按原图留白或成品照片像素直接换算。", p.Fit, p.NoCrop, scale, p.OffsetX, p.OffsetY)
	text += "印刷画布已保护实际内容；这不代表最终照片已经无裁切。逐角对照原图，重要文字、主眼睛和主头部轮廓必须完整且避开镜头，不能因打印域已验证就忽略照片裁切。低对比不等于头部丢失。"
	text += "完整以原图实际存在的像素为准：原图已有的平直截断轮廓、半身或局部形象保持一致就算保留；只判断成品新增的裁切或镜头遮挡，不得推想原图不存在的头顶、身体或文字。"
	text += PrimaryContentRule
	if p.AnchorX != 0 {
		text += "原图左右边缘本来截开的文字或物体属于原始构图；成品保留同样的半个字或局部轮廓就是完整，不能把原图缺少的笔画误报为镜头遮挡。先对照原图左右边界，再确认是否有摄像头实际覆盖原图已有笔画；文字在镜头下方或侧方且仅沿壳边截断时，不得称为镜头遮挡。问题须指出具体哪一字或哪一主体、哪部分新增缺失，避免仅以‘标题左侧’等笼统位置判定。"
	}
	if p.KeepWhole {
		text += "用户已选择保留整幅画面，接受边缘留白，不强制贴壳边或贴底；原图外框作为完整画面保留，不把正常留白判成截边悬空。所有内容仍须留在壳边与画面内，重要文字、主脸和主头部避镜头，不得通过裁背景或拉伸来满铺。"
	}
	text += "模板手指、挂绳和反光不属于镜头遮挡；物理透明壳边框不属于可印刷背板，不应误判为图案留白。"
	if p.SourceAlphaGuide {
		text += "原图参考中左幅Art是原图画布与实际比例，右幅Alpha guide仅增强透明度轮廓，不代表实际印刷颜色，也不是成品；按右幅确认浅色主头部轮廓，再对照各成品检查镜头遮挡。"
	}
	if p.DiagnosticViews {
		text += "成品附图是同一画布版本的评审诊断投影：弱alpha轮廓增强为品红，已确认的模板可临时隐藏手指层；正式导出仍保持原场景与颜色。只判断原图重要内容的照片裁切和镜头遮挡，不评诊断色或诊断图真实性，不因手指/挂绳自然遮挡调整印刷版位。"
	} else if p.DiagnosticFirstView {
		text += "首个成品角度为同一实际摆放的Alpha可见性诊断：只将弱 alpha 轮廓增强为品红，原本不透明的正文、眼睛和底色保留原色；其余角度为原色成品。该诊断只检查镜头遮挡，不评配色，不能将增强色判成素材变色。"
	}
	if p.FullBleed || p.FillTop {
		text += "满铺是初始构图方式；实际内容越界或主字主脸被挡时允许等比缩小并位移。重复背景纹样被镜头自然遮挡不算缺失，保留重复图案连续性、壳色和透明度。"
	}
	if p.TransparentPadding {
		text += "原图有透明留白，安全边界按实际内容计算，透明画布边缘不锁定主体位置；可联合缩放与横纵移动，完整独立贴纸不强制贴底。"
	}
	if p.AlignBottom && !p.KeepWhole {
		text += "当前已按实际内容贴底；若底部是明确直线截断边，缩放后仍须贴壳底，自然曲线不强制贴底。"
	}
	if !p.KeepWhole && (p.AnchorX != 0 || p.AnchorY != 0) {
		text += fmt.Sprintf("明确截断边锚点：anchor_x=%d、anchor_y=%d（负为左/上，正为右/下，x=2为左右同时贴边）；调整后截断边仍须贴对应壳边，不得悬空。若与内容完整或避镜头约束冲突，报告不合格与冲突，不强行越界。", p.AnchorX, p.AnchorY)
	}
	if p.AppliedSummary != "" {
		text += "\n" + p.AppliedSummary
	}
	return text
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
1. 对照原图案保护主体、比例、方向、透明区域与细节。原图透明区域用白底预览，不代表要在壳上印白色矩形。原图已有的平直底边不应悬在壳面中间形成贴纸截边；本款说明若明确用户接受整幅留白，则按该例外，不强制贴壳边。
2. 优先保持原比例、基准大小和居中；主要内容完整且无明显遮挡就合格。独立文字、数字和标志允许正常留白；底部确有需要接壳底的连续场景内容且悬空时可以小幅下移，不要为更饱满而缩放。
3. 摄像头、边框、手指自然遮挡背景装饰是正常实物关系，不要为了躲开整个镜头区域把整幅图缩小。关键人物脸部或文字被镜头遮挡才需避让。
4. 透明孔洞露出壳色、壳体边框、相机开孔与拍摄透视不是图案缺失，不要求图案覆盖这些区域。
5. 原图应跟随壳的方向旋转，不得镜像或拉伸；按印刷背板判断，保留原壳面材质与光影。
6. 若几张都不理想，选择最接近以上要求的一张，在 reason 说明仍有的问题，不能声称全部合格。

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
func Choose(cfg Config, cands []Candidate, patternName string, placement Placement) Result {
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
	text += "\n" + placement.Description()
	if placement.SourceDataURL != "" {
		text += "\n第一张附图是原图案参考，其后的图片依次对应候选编号 1、2……，原图不参与编号。"
	}

	content := make([]any, 0, len(cands)+1)
	content = append(content, map[string]any{"type": "text", "text": text})
	if placement.SourceDataURL != "" {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": placement.SourceDataURL}})
	}
	for _, c := range cands {
		content = append(content, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(c.JPEG),
			},
		})
	}

	body := map[string]any{
		"stream":         true,
		"stream_options": map[string]bool{"include_usage": true},
		"model":          cfg.Model,
		"messages":       []any{map[string]any{"role": "user", "content": content}},
		"max_tokens":     cfg.MaxTokens,
		"temperature":    cfg.Temperature,
	}
	ApplyThinking(body, cfg.Model, cfg.Thinking)
	ApplyJSONResponse(body, cfg.Model)

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
	resp, err := DoVisionRequest(req, timeout, cfg.BeforeRequest)
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
