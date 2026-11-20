package tuner

import (
	"strings"
	"testing"

	"titao/internal/judge"
)

// 提示词是判定质量的源头，改措辞很容易把规则改坏。这些断言把「必须有」和
// 「必须不能有」的句子钉住：删掉关键约束、把优先级写反、或重新引入互相矛盾的
// 说法，都会在这里失败。

// 反例：曾经同时写「保持尽可能饱满的构图」和「不得为了更饱满放大」，
// 模型在要不要缩小时摇摆。任何鼓励"越满越好"的措辞都不许回来。
var forbiddenPhrases = []string{
	"保证不重复补齐",
	"始终保持饱满铺满",
	"应尽量铺满整个壳面",
	"必须满铺",
	"尽量满铺壳面",
	"越大越好",
	"最大化图案",
}

func TestPromptPrioritizesContentOverComposition(t *testing.T) {
	// 构图必须是最后一位，且明确写出它不能否决内容完整性。
	for _, want := range []string{
		"【判定优先级",
		"1. 内容完整",
		"2. 镜头避让",
		"3. 构图饱满",
		"构图饱满是最后一位",
		"永远不能用来否决内容完整性",
	} {
		if !strings.Contains(promptTemplate, want) {
			t.Errorf("提示词缺少优先级约束：%q", want)
		}
	}
	// 优先级顺序必须真的是内容 > 避让 > 构图，三者的出现位置递增。
	first := strings.Index(promptTemplate, "1. 内容完整")
	second := strings.Index(promptTemplate, "2. 镜头避让")
	third := strings.Index(promptTemplate, "3. 构图饱满")
	if first < 0 || second < 0 || third < 0 || !(first < second && second < third) {
		t.Errorf("优先级顺序错误：内容=%d 避让=%d 构图=%d", first, second, third)
	}
}

func TestPromptForbidsGrowingForFullness(t *testing.T) {
	for _, want := range []string{
		"不得为了\"更饱满\"\"更满铺\"放大",
		"不得为了换高分把整幅缩成底部小标志",
		"正常留白可接受",
	} {
		if !strings.Contains(promptTemplate, want) {
			t.Errorf("提示词缺少构图禁令：%q", want)
		}
	}
	for _, bad := range forbiddenPhrases {
		if strings.Contains(promptTemplate, bad) {
			t.Errorf("提示词出现鼓励铺满的措辞，与构图禁令矛盾：%q", bad)
		}
	}
}

func TestPromptKeepsScaleAndRetryLimits(t *testing.T) {
	for _, want := range []string{
		"单轮 scale_ratio 不低于 0.55",
		"dx/dy 在 [-1,1]，scale_ratio 在 [0.55,2]",
		"score>=85",
		"本轮一次给足修正量",
		"不能重复同一组无效参数",
	} {
		if !strings.Contains(promptTemplate, want) {
			t.Errorf("提示词缺少数值或重试约束：%q", want)
		}
	}
}

func TestPromptExcludesFalsePositives(t *testing.T) {
	for _, want := range []string{
		"壳边透明框、模板手指、挂绳、反光、阴影",
		"低对比不等于内容丢失",
		"原图本身就截断的轮廓",
		"不得臆测成透明渲染错误",
		"不写\"左侧\"\"顶部\"这类笼统位置",
		"贴近不等于遮挡",
		"这个字少了哪一笔",
		"圆环上沿、蜘蛛的腿",
		"新增遮挡就必须同时填 camera_clear=false 和 good=false",
	} {
		if !strings.Contains(promptTemplate, want) {
			t.Errorf("提示词缺少误判排除项：%q", want)
		}
	}
}

func TestSystemPromptDoesNotDuplicateDetailedRules(t *testing.T) {
	// 细则只在 user message 里写一份。system 里再抄一遍会两处不同步、互相打架。
	for _, bad := range []string{"【判定优先级", "【修正规则", "【常见误判", "0.55"} {
		if strings.Contains(systemPrompt, bad) {
			t.Errorf("systemPrompt 不应重复 user message 的细则：%q", bad)
		}
	}
	for _, want := range []string{"只输出一行完整 JSON", "good=true 必须同时满足", "score>=85"} {
		if !strings.Contains(systemPrompt, want) {
			t.Errorf("systemPrompt 缺少输出契约：%q", want)
		}
	}
}

// PrimaryContentRule 只能出现在 user message 一处，拼两次会让模型重复权衡同一套规则。
func TestPrimaryContentRuleAppearsOnceInRequest(t *testing.T) {
	placement := judge.Placement{NoCrop: true, Scale: 1}.Description()
	if n := strings.Count(placement, "先按原图的视觉层级区分主体与陪衬"); n != 1 {
		t.Errorf("主要物体规则出现了 %d 次，应恰好一次", n)
	}
}

// 贴边的判据是「有没有笔直的切割线」，不是「边缘有没有内容」。
// 没有笔直切线的自然弧边、手绘收边、完整独立图形都不该贴壳边。
func TestPromptDefinesEdgeAnchoringByStraightCuts(t *testing.T) {
	for _, want := range []string{
		"笔直切线",
		"竖直直线把图案切断",
		"没有笔直切线就没有贴边义务",
		"自然弧形",
		"不要为了\"对齐\"去硬贴",
	} {
		if !strings.Contains(promptTemplate, want) {
			t.Errorf("提示词缺少贴边判据：%q", want)
		}
	}
	if !strings.Contains(promptTemplate, "直切边由程序校验并贴合") || !strings.Contains(promptTemplate, "显式偏移") {
		t.Error("提示词必须描述真实的直切边硬约束和显式偏移")
	}
}

func TestPromptRequiresTranslationOnlyForOcclusionBottomAttempt(t *testing.T) {
	for _, want := range []string{"条件贴底", "上方重要内容被镜头遮挡", "整体向下平移", "保持大小、比例和横向位置不变", "不能向下拉伸", "贴底后仍有遮挡", "停止继续微调", "不得改写为已避镜头"} {
		if !strings.Contains(promptTemplate, want) {
			t.Errorf("missing conditional bottom policy: %q", want)
		}
	}
	if strings.Contains(promptTemplate, "不在渲染阶段强制贴底或贴边") {
		t.Error("prompt contradicts deterministic cut-edge anchoring")
	}
}

// 内容不能越界这条不能被贴边规则盖过。
func TestPromptKeepsContentInsidePrintDomain(t *testing.T) {
	for _, want := range []string{
		"内容不能越界",
		"全部有效内容仍须留在印刷域内",
		"先保不越界",
	} {
		if !strings.Contains(promptTemplate, want) {
			t.Errorf("提示词缺少不越界约束：%q", want)
		}
	}
}

// 顶部收尾补齐：三条边已贴边且占比大、只剩顶部一线时，纵向拉伸补齐。
func TestPromptDefinesTopEdgeFinishStretch(t *testing.T) {
	for _, want := range []string{
		"顶部收尾补齐",
		"左右和底部都已经贴边",
		"把顶部也纵向拉伸到贴住壳边",
		"只改纵向，不改横向",
		"stretch_y",
	} {
		if !strings.Contains(promptTemplate, want) {
			t.Errorf("提示词缺少顶部补齐规则：%q", want)
		}
	}
	// 横向拉伸仍只用于左右贴边，纵向拉伸只用于顶部补齐，两者不能混用
	if !strings.Contains(promptTemplate, "这个拉伸只能用来对齐边缘") {
		t.Error("左右横向拉伸必须仍限定在贴边对齐上")
	}
}

// 合格判定不能靠拉伸改判通过。
func TestPromptRequiresNeutralStretchWhenGood(t *testing.T) {
	if !strings.Contains(promptTemplate, "stretch_y=1") {
		t.Error("good=true 必须要求 stretch_y=1")
	}
}
