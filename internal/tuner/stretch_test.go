package tuner

import (
	"math"
	"testing"
)

// 「左右和底部都已贴边、图案占比大、只剩顶部差一点」时的收尾补齐：
// 模型可以单独给出 stretch_y，程序只改纵向比例，不动横向与位移。
func TestPlanAppliesTopEdgeStretchOnly(t *testing.T) {
	adv := Advice{Transform: &Adjustment{DX: 0, DY: 0, ScaleRatio: 1, StretchY: 1.06}}
	scale, x, y, stretch, iterate := Plan(adv, 1.0, 0.1, -0.2, 1.0)
	if !iterate {
		t.Fatal("只给 stretch_y 也必须触发重渲染")
	}
	if scale != 1.0 || x != 0.1 || y != -0.2 {
		t.Fatalf("纵向拉伸不能动产横向与位移：scale=%v x=%v y=%v", scale, x, y)
	}
	if math.Abs(stretch-1.06) > 1e-9 {
		t.Fatalf("stretch=%v want 1.06", stretch)
	}
}

// 多轮累积拉伸要有界：连续给 1.12 也不能无限拉长。
func TestPlanStretchAccumulationIsBounded(t *testing.T) {
	stretch := 1.0
	for i := 0; i < 10; i++ {
		_, _, _, next, _ := Plan(Advice{Transform: &Adjustment{ScaleRatio: 1, StretchY: 1.12}}, 1.0, 0, 0, stretch)
		stretch = next
	}
	if stretch > 1.12+1e-9 {
		t.Fatalf("累积拉伸超过上限：%v", stretch)
	}
}

// 越界的 stretch_y 整条建议作废，不能部分采纳。
func TestAdjustmentRejectsOutOfRangeStretch(t *testing.T) {
	for _, v := range []float64{0.5, 0.89, 1.13, 2, math.NaN(), math.Inf(1)} {
		if validAdjustment(Adjustment{ScaleRatio: 1, StretchY: v}) {
			t.Fatalf("stretch_y=%v 应被拒绝", v)
		}
	}
	for _, v := range []float64{0.90, 1.0, 1.05, 1.12} {
		if !validAdjustment(Adjustment{ScaleRatio: 1, StretchY: v}) {
			t.Fatalf("stretch_y=%v 应被接受", v)
		}
	}
}

// 判合格时不允许带任何拉伸；否则等于用拉伸改判通过。
func TestGoodVerdictMustNotCarryStretch(t *testing.T) {
	adv := parseAdvice(`{"good":true,"camera_clear":true,"score":92,"problem":"无","dx":0,"dy":0,"scale_ratio":1,"stretch_y":1.08}`)
	if adv.Transform != nil {
		t.Fatalf("判合格还带拉伸，应整条作废：%+v", adv.Transform)
	}
}
