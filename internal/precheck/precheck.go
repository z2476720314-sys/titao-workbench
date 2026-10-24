// Package precheck 在套图前用视觉模型判断一张图案是否适合直接套图。
//
// 背景（用户实测反馈）：图库里混着带「shuiyin / hituku / Seu Nome」字样的
// 彩色水印图、带镜头框或手机壳边框的图，以及本身就是手机壳实拍/背景图的图。
// 这些套上去只是白费渲染时间与模型调用，所以先拦一道。
//
// 设计取舍：
//   - 只送缩略图（imgutil.ThumbDataURL），一次判定约等于一次选档的 token 成本。
//   - 模型失败一律放行：宁可多套一张，也不能因为接口抽风让整批任务不动。
//   - 判定结果落 precheck.jsonl，path+size+mtime 三者全同才算命中；
//     用户替换了同名文件（size 或 mtime 变了）会自动重判。
package precheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"titao/internal/judge"
)

// Config 复用 judge 的视觉模型配置：预检与选档走同一个通道（config.json 的
// base_url / api_key / model），不为预检单独维护一套凭据。
type Config = judge.Config

// Verdict 是一次预检结论。
type Verdict struct {
	// OK 为 true 表示可以套图。模型失败时也为 true（放行）。
	OK bool `json:"ok"`
	// Reason 是模型给的中文短理由（15 字内，可能为空）。
	Reason string `json:"reason"`
	// Tags 是命中的问题类型，取值见 TagXxx。
	Tags []string `json:"tags"`

	// Cached 表示结论来自记录库，本次没有调用模型。
	Cached bool `json:"-"`
	// Fallback 表示模型调用/解析失败，按放行处理。
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

const promptText = `你是手机壳图案质检员。这张图是准备印到手机壳上的**图案素材**（不是手机壳实拍）。

任务：判断它能不能直接拿去套图。只有「干净的一整幅图案」才算合格。

判 ok=false 只有这四种情况：

1. **商家水印**（tag: watermark）：卖家盖在图案上的叠加文字。
   **强规则**：图中任何位置出现 shuiyin、hituku、Seu Nome、shui yin、shuiyin 变形
   这类**拼音/葡语词**，不管字体多手写、多花体、多淡、多小、斜置还是重复，
   一律判 watermark、ok=false。这类词就是水印，不可能是图案设计。
   另外：网址、无意义字母串（如 U8FT3LE7）、与画风无关的突兀叠加文字，也算。
   **不算水印**（图案设计本身，必须放行）：
   - 正常英文装饰词：enlife story、Happy、LUCKY、LOVELY、EVERYTHING IT'S OK、STEP BY STEP；
   - 拼贴图里的报纸/杂志碎字、印章风文字、署名风插图文字、品牌风 logo 排版。
   区分要点：装饰英文与图案同色系、融进画面；拼音水印词突兀、像后贴上去的。

2. **镜头框**（tag: lens）：手机壳背面摄像头位置的**空框**。
   典型特征：位于图的最上方，宽度接近图宽（占 50%-100%），高度约图高的 1/5 到 1/3，
   圆角矩形，**框内基本是空白或只有零散小图案**，框线粗、边缘常直接切到图外，
   有的框内还带 1-3 个正圆孔。
   **不算镜头框**：拼贴图里的装饰圆环、相框、气泡框、心形/星形外框、
   卡通图案里画的相机造型插画、包住主体图案的装饰性边框。

3. **壳边框**（tag: case-edge）：手机壳本体的外轮廓、圆角矩形壳体边、摄像头凸起台阶。

4. **壳实拍**（tag: case-photo）：整张图本身就是手机壳实拍、手机实拍或商品场景图。

合格标准：整张图是一幅图案/插画/纹理，没有商家水印、没有壳体轮廓、没有镜头空框。

**除上面四类之外拿不准，一律判 ok=true**：漏套一张的成本远低于错杀一张。
但第 1 条的拼音水印词是硬规则，看到就判 false。

只输出一行 JSON，不要任何解释：
{"ok": true, "reason": "<15字内>", "tags": []}

tags 只能从 watermark、lens、case-edge、case-photo 里选。合格时 tags 给空数组。`

var (
	reOK     = regexp.MustCompile(`"ok"\s*:\s*(true|false)`)
	reReason = regexp.MustCompile(`"reason"\s*:\s*"([^"]*)"`)
	reTags   = regexp.MustCompile(`"tags"\s*:\s*\[([^\]]*)\]`)
)

// Check 调视觉模型判定一张图案。thumbDataURL 是 imgutil.ThumbDataURL 的产物。
//
// 网络/解析失败时不返回错误，而是 Verdict{OK:true, Fallback:true, Err:...}，
// 由调用方照常套图并记一条警告。
func Check(cfg Config, thumbDataURL string) Verdict {
	if strings.TrimSpace(thumbDataURL) == "" {
		return fallback(fmt.Errorf("缩略图为空"))
	}

	content := []any{
		map[string]any{"type": "text", "text": promptText},
		map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": thumbDataURL},
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
	// 结论可能落在 reasoning_content 里而 content 为空，两处一起找（judge 同款坑）
	blob := msg.Content + "\n" + msg.ReasoningContent

	return Parse(blob, strings.TrimSpace(msg.Content))
}

// Parse 从模型输出里抽出结论。blob 是 content + reasoning_content 的合并文本。
//
// 单独导出是为了让单测不必起 HTTP 服务就能覆盖解析分支。
func Parse(blob, raw string) Verdict {
	v := Verdict{OK: true, Raw: raw}

	m := reOK.FindStringSubmatch(blob)
	if m == nil {
		return fallback(fmt.Errorf("模型输出里没有 ok 字段: %.200s", blob))
	}
	if r := reReason.FindStringSubmatch(blob); r != nil {
		v.Reason = strings.TrimSpace(r[1])
	}
	v.OK = m[1] == "true"

	if t := reTags.FindStringSubmatch(blob); t != nil {
		v.Tags = splitTags(t[1])
	}
	if !v.OK && len(v.Tags) == 0 {
		v.Tags = []string{TagUnknown}
	}
	return v
}

// splitTags 把 `"watermark", "lens"` 拆成干净的小写标签，只保留已知取值。
func splitTags(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		t := strings.ToLower(strings.Trim(strings.TrimSpace(part), `"'`))
		if t == "" || seen[t] {
			continue
		}
		switch t {
		case TagWatermark, TagLens, TagCaseEdge, TagCasePhoto, TagUnknown:
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// fallback 造一个「放行」结论：模型挂了也不能拦住整批套图。
func fallback(err error) Verdict {
	return Verdict{OK: true, Fallback: true, Err: err}
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
