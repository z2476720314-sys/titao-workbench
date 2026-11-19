package tuner

import "testing"

// 模型有时把 score 写成 0.96 而不是 96（关闭思考模式时尤其常见）。
// 用 *int 严格解析会整轮报错，让本来合格的成品被当成「评审未完成」。
// 这里钉住兼容行为的边界：小数按百分制换算，非数字仍然拒绝。

func TestScoreAcceptsFractionAsPercent(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{`{"good":true,"camera_clear":true,"score":0.96,"problem":"无","dx":0,"dy":0,"scale_ratio":1}`, 96},
		{`{"good":true,"camera_clear":true,"score":0.9,"problem":"无","dx":0,"dy":0,"scale_ratio":1}`, 90},
		{`{"good":true,"camera_clear":true,"score":1,"problem":"无","dx":0,"dy":0,"scale_ratio":1}`, 100},
		{`{"good":false,"camera_clear":false,"score":0.55,"problem":"主标题被挡","dx":0,"dy":0.1,"scale_ratio":1}`, 55},
	} {
		adv := parseAdvice(tc.raw)
		if adv.Direction == "" {
			t.Errorf("小数 score 被拒绝，应换算为百分制：%s", tc.raw)
			continue
		}
		if adv.Score != tc.want {
			t.Errorf("score 换算错误：%s -> %d，应为 %d", tc.raw, adv.Score, tc.want)
		}
	}
}

func TestScoreKeepsIntegerAsIs(t *testing.T) {
	adv := parseAdvice(`{"good":true,"camera_clear":true,"score":92,"problem":"无","dx":0,"dy":0,"scale_ratio":1}`)
	if adv.Direction == "" || adv.Score != 92 {
		t.Errorf("整数 score 不该被二次换算：%+v", adv)
	}
}

func TestScoreRejectsNonNumeric(t *testing.T) {
	// 宁可判「评审未完成」，也不猜模型的意思。
	for _, raw := range []string{
		`{"good":true,"camera_clear":true,"score":"96","problem":"无","direction":"none"}`,
		`{"good":true,"camera_clear":true,"score":null,"problem":"无","direction":"none"}`,
		`{"good":true,"camera_clear":true,"score":true,"problem":"无","direction":"none"}`,
	} {
		if adv := parseAdvice(raw); adv.Direction != "" {
			t.Errorf("非数字 score 应被拒绝：%s -> %+v", raw, adv)
		}
	}
}

// 小数换算后仍要遵守 85 分合格线，不能因为 0.9 被放大成 90 就绕过其他条件。
func TestFractionScoreStillHonorsApprovalThreshold(t *testing.T) {
	adv := parseAdvice(`{"good":true,"camera_clear":true,"score":0.8,"problem":"无","dx":0,"dy":0,"scale_ratio":1}`)
	if adv.Direction != "" {
		t.Errorf("0.8 换算成 80 分，低于 85 分合格线，不该被接受为合格：%+v", adv)
	}
}
