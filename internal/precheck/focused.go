package precheck

import (
	"encoding/json"
	"fmt"
	"strings"
)

const watermarkPrompt = `此图仅做水印痕迹检查。第一张是完整素材参考，第二张是本轮放大局部；只有一张时它就是完整素材。
必须对照整图：局部中被截断的大字、主体字体的破损笔画、笔刷肌理、印刷颗粒和装饰线条本身都不算后加水印。不能把主体文字的刷痕当成另一层小字。
先查找与主体画风不同的小字笔画；绿色/彩色斜写、被遮挡而读不出的残缺字母也必须记录。读取不完整的小字不等于没有水印。
返回JSON {"watermark":true或false,"uncertain":true或false,"evidence":"60字内说明字母颜色、位置或正常设计文字"}。
与主体无关的斜写小字为watermark=true；看到了残余小字但读不清或分不清用途为uncertain=true。正常标题/标语/品牌插画文字、球衣数字是设计本身，两个字段都为false，不能仅因有文字判水印。`

const panelPrompt = `只做几何轮廓检测，不描述卡通或水果内容，也不解释用途。
检查图中是否有独立的大圆角矩形面板/粗边框：接近画布宽度的顶部横向面板，或上下分开的圆角壳面。内部有图案仍算。不要把这种独立面板解释为标注框而忽略。
小贴纸、普通花卉边饰和图案中的小相框不算。只输出JSON {"rounded_panels":true或false,"layout":"轮廓与位置，没有则写无"}。`

// Inspect separates structural inspection from the small-text check so details cannot be waived by the overall verdict.
func Inspect(cfg Config, views []string) Verdict {
	if len(views) == 0 {
		return fallback(fmt.Errorf("预检图片为空"))
	}
	v := Check(cfg, views[0], views[1:]...)
	if v.Fallback || !v.OK {
		return v
	}
	panelView := views[0]
	if len(views) == 7 {
		panelView = views[1]
	}
	raw, err := modelText(cfg, panelPrompt, []string{panelView})
	if err != nil {
		return fallback(err)
	}
	var panel struct {
		Rounded *bool  `json:"rounded_panels"`
		Layout  string `json:"layout"`
	}
	if err := parseFocused(raw, &panel); err != nil || panel.Rounded == nil || (*panel.Rounded && strings.TrimSpace(panel.Layout) == "") {
		return fallback(fmt.Errorf("壳面轮廓细查结果无效"))
	}
	if *panel.Rounded {
		v.OK, v.FillTop, v.Tags, v.Reason = false, false, []string{TagLens}, "独立圆角壳面："+panel.Layout
		return v
	}
	details := views
	if len(views) == 7 {
		details = views[3:]
	} else if len(views) == 5 {
		details = views[1:]
	}
	for i := len(details) - 1; i >= 0; i-- {
		images := []string{views[0]}
		if details[i] != views[0] {
			images = append(images, details[i])
		}
		raw, err = modelText(cfg, watermarkPrompt, images)
		if err != nil {
			return fallback(err)
		}
		var check struct {
			Watermark *bool  `json:"watermark"`
			Uncertain *bool  `json:"uncertain"`
			Evidence  string `json:"evidence"`
		}
		if err := parseFocused(raw, &check); err != nil || check.Watermark == nil || check.Uncertain == nil || ((*check.Watermark || *check.Uncertain) && strings.TrimSpace(check.Evidence) == "") {
			return fallback(fmt.Errorf("水印细查结果无效"))
		}
		if *check.Watermark {
			v.OK, v.FillTop, v.Tags = false, false, []string{TagWatermark}
			v.Reason = "水印细查：" + check.Evidence
			return v
		}
		if *check.Uncertain {
			return fallback(fmt.Errorf("可疑小字需人工复核：%s", check.Evidence))
		}
		v.Raw += "\n" + raw
	}
	return v
}

func parseFocused(raw string, result any) error {
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		return fmt.Errorf("未返回JSON")
	}
	return json.Unmarshal([]byte(raw[start:end+1]), result)
}
