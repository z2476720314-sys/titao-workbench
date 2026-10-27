// Package precheck 在套图前用视觉模型判断一张图案是否适合直接套图。
//
// 背景（用户实测反馈）：图库里混着带「shuiyin / hituku / Seu Nome」字样的
// 彩色水印图、带镜头框或手机壳边框的图，以及本身就是手机壳实拍/背景图的图。
// 这些套上去只是白费渲染时间与模型调用，所以先拦一道。
//
// 设计取舍：
//   - 整图检查壳结构，局部放大图另查水印残片。
//   - 模型失败保持未确认，由任务引擎按预检跳过开关决定是否继续。
//   - 判定结果落 precheck.jsonl，path+size+mtime 三者全同才算命中；
//     用户替换了同名文件（size 或 mtime 变了）会自动重判。
package precheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"titao/internal/judge"
)

// Config 复用 judge 的视觉模型配置：预检与选档走同一个通道（config.json 的
// base_url / api_key / model），不为预检单独维护一套凭据。
type Config = judge.Config

// Verdict 是一次预检结论。
type Verdict struct {
	FillTop bool `json:"fill_top"`
	// OK 为 true 只表示四项检查均通过，失败时保持 false。
	OK bool `json:"ok"`
	// Reason 是模型描述的可见特征或通过理由。
	Reason string `json:"reason"`
	// Tags 是命中的问题类型，取值见 TagXxx。
	Tags []string `json:"tags"`

	// Cached 表示结论来自记录库，本次没有调用模型。
	Cached bool `json:"-"`
	// Fallback 表示模型调用/解析失败，不能视为已通过。
	Fallback bool `json:"-"`
	// Err 是失败原因（Fallback 为 true 时非空）。
	Err error `json:"-"`
	// Raw 是模型原始输出，便于排查。
	Raw string `json:"-"`
}

// 问题类型标签。
const (
	TagWatermark = "watermark"  // 文字水印 / 店名 / 网址
	TagLens      = "lens"       // 镜头框 / 摄像头模组
	TagCaseEdge  = "case-edge"  // 手机壳边框 / 壳体轮廓
	TagCasePhoto = "case-photo" // 手机壳实拍图 / 商品背景图
	TagUnknown   = "unknown"    // 判不合格但模型没给 tag
)

const promptText = `逐项检查输入图片的真实内容，不要预设它是干净插画；它可能是手机壳成品、带开孔的壳面设计，或水印素材。
这张图将作为一整幅图案再次印到另一张手机壳上，原图已有的镜头孔、镜头框、壳轮廓绝不能再次印进去。

先客观观察几何轮廓，再填写判断，不能因图案内容可爱而忽略边框。
先输出 panel_layout：无独立壳结构为 none；图像上部独立横向大圆角面板/粗边框为 top_panel；上下分离的大圆角面板为 split_case；完整圆角壳身为 case_outline。只判断大面板结构，单个小贴纸、装饰相框不算。
再判断下面四项，四个布尔字段必须全部输出。由程序据此决定是否套图，不要输出总评 ok。
第一张是完整图片；如果还有后续图片，它们是同一素材顶部/底部（或左右两端）的放大细节，不是不同素材。任何一张细节中出现水印或壳结构都要报告，不能只看整体忽略小字。

1. watermark：是否有后加水印。任何位置出现 shuiyin、shui yin、hituku、Seu Nome 及花体/斜写变形，都算；不要因为文字小或与配色一致就放行。网址、店名水印、无关随机字母叠加也算。普通设计文字（Happy、Lucky、Hello Kitty、品牌图案、报纸拼贴）本身不算水印。
2. lens：是否保留了供手机摄像头使用的开孔、镜头保护框或摄像头岛。包括左上角三圆孔、多圆孔；顶部横向大圆角矩形；内部是透明、纯色、波点、卡通小图案都算，不要求空白，也不要求真实镜头照片。单独一块带镜头孔的装饰保护板同样算。
   优先结构规则：图片上方独立横向大圆角矩形面板（接近画布宽，约占上面三分之一），下方另有散落贴纸/主体图案，这是分体式壳面设计，必须 lens=true。框内有星空、波点、小猫、小狗、花纹、完整小插画也不能例外；不能以“星空装饰框”“卡通相框”解释后放行。此结构规则优先于普通装饰相框例外。
3. case_edge：是否保留了手机壳本体形状、四周壳边、长条圆角矩形壳面背景、壳体外轮廓或切割线。即使已抠透明底、没有手持实拍，也仍然算壳体。顶部镜头区加下方壳身的分体设计也算。正常矩形壁纸、花卉纹理、人物自身轮廓、普通装饰花边不算；以是否还带手机壳结构判断，不能只看长宽比例。
4. case_photo：是否是手机/手机壳商品实拍、手持手机壳商品、带场景背景的手机壳图。必须实际看到手机或手机壳商品本体；单纯街景、建筑、风景、动物摄影不算。

重点检查图片顶部、左上角、四个外角与四周边线，再看细小文字。透明区域在本预览中铺白底，不影响上述判定。
例如：顶部大圆角镜头保护区内有可爱小狗、下方还有图案，lens 仍为 true；完整圆角壳身加三颗摄像头孔，lens 和 case_edge 都为 true。
另输出 fill_top：仅当素材是连续重复的满版纹理/几何/花纹背景、不含文字数字、可辨识人物主体、定向标志时为 true，表示允许用背景边缘续铺顶部。人物、标志、文字、单独贴纸和不确定类型均为 false。
只输出一行 JSON，值必须依据图片，不能照抄示例：
{"panel_layout":"none","watermark":false,"lens":false,"case_edge":false,"case_photo":false,"fill_top":false,"reason":"60字内描述具体可见特征"}`

// Check 调视觉模型判定一张图案。thumbDataURL 是 imgutil.ThumbDataURL 的产物。
//
// 网络/解析失败时返回 Verdict{Fallback:true, Err:...}，
// 由调用方决定标失败或在显式宽松设置下继续。
func Check(cfg Config, thumbDataURL string, details ...string) Verdict {
	if strings.TrimSpace(thumbDataURL) == "" {
		return fallback(fmt.Errorf("缩略图为空"))
	}
	raw, err := modelText(cfg, promptText, append([]string{thumbDataURL}, details...))
	if err != nil {
		return fallback(err)
	}
	return Parse(raw, raw)
}

func modelText(cfg Config, prompt string, views []string) (string, error) {
	content := []any{
		map[string]any{"type": "text", "text": prompt},
	}
	for _, view := range views {
		if strings.TrimSpace(view) != "" {
			content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": view, "detail": "high"}})
		}
	}

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
		return "", err
	}

	url := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	resp, err := judge.DoVisionRequest(req, timeout)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %.300s", resp.StatusCode, raw)
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
		return "", fmt.Errorf("解析响应: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("响应无 choices")
	}

	msg := parsed.Choices[0].Message
	// 结论可能落在 reasoning_content 里而 content 为空，两处一起找（judge 同款坑）
	if strings.TrimSpace(msg.Content) != "" {
		return strings.TrimSpace(msg.Content), nil
	}
	return strings.TrimSpace(msg.ReasoningContent), nil
}

// Parse 从模型输出里抽出结论。blob 是 content + reasoning_content 的合并文本。
//
// 单独导出是为了让单测不必起 HTTP 服务就能覆盖解析分支。
func Parse(blob, raw string) Verdict {
	text := strings.TrimSpace(raw)
	if text == "" {
		text = strings.TrimSpace(blob)
	}
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return fallback(fmt.Errorf("预检未返回检查清单"))
	}
	var checks struct {
		Watermark   *bool  `json:"watermark"`
		Lens        *bool  `json:"lens"`
		CaseEdge    *bool  `json:"case_edge"`
		CasePhoto   *bool  `json:"case_photo"`
		FillTop     bool   `json:"fill_top"`
		PanelLayout string `json:"panel_layout"`
		Reason      string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &checks); err != nil {
		return fallback(fmt.Errorf("预检检查清单无效: %w", err))
	}
	if checks.Watermark == nil || checks.Lens == nil || checks.CaseEdge == nil || checks.CasePhoto == nil || strings.TrimSpace(checks.Reason) == "" {
		return fallback(fmt.Errorf("预检检查清单缺少字段，未确认合格"))
	}
	v := Verdict{OK: true, Reason: strings.TrimSpace(checks.Reason), Tags: []string{}, Raw: raw}
	switch checks.PanelLayout {
	case "top_panel":
		*checks.Lens = true
	case "split_case":
		*checks.Lens, *checks.CaseEdge = true, true
	case "case_outline":
		*checks.CaseEdge = true
	}
	for _, item := range []struct {
		flag *bool
		tag  string
	}{{checks.Watermark, TagWatermark}, {checks.Lens, TagLens}, {checks.CaseEdge, TagCaseEdge}, {checks.CasePhoto, TagCasePhoto}} {
		if *item.flag {
			v.OK = false
			v.Tags = append(v.Tags, item.tag)
		}
	}
	v.FillTop = v.OK && checks.FillTop
	return v
}

// fallback 保留未确认状态，不伪造合格结论。
func fallback(err error) Verdict {
	return Verdict{Fallback: true, Err: err}
}

// TagLabel 把 tag 翻成中文，用于汇总文案。
func TagLabel(tag string) string {
	switch tag {
	case TagWatermark:
		return "水印"
	case TagLens:
		return "镜头框"
	case TagCaseEdge:
		return "壳边框"
	case TagCasePhoto:
		return "壳实拍"
	case TagUnknown:
		return "其他"
	}
	return tag
}
